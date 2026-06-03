package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

var db *sql.DB
var controlPlaneUser = getenv("CONTROL_PLANE_USER", "admin")
var controlPlanePassword = getenv("CONTROL_PLANE_PASSWORD", "edgefleet")

const (
	errMethodNotAllowed = "method not allowed"
	errMissingNodeID    = "missing node id"
	errMissingNodeToken = "missing node token"
)

const (
	heartbeatExpiry = 30 * time.Second
	sweepInterval   = 10 * time.Second
)

type Node struct {
	NodeId        string `json:"node_id"`
	LastHeartbeat string `json:"last_heartbeat"`
	Status        string `json:"status"`
}

type RegistrationResponse struct {
	NodeID     string `json:"node_id"`
	NodeSecret string `json:"node_secret"`
}

type DesiredStateEnvelope struct {
	Version    int             `json:"version"`
	WorkloadID string          `json:"workload_id,omitempty"`
	Type       string          `json:"type,omitempty"`
	Spec       json.RawMessage `json:"spec,omitempty"`
	Payload    string          `json:"payload,omitempty"`
	Signature  string          `json:"signature"`
}

type desiredStateSignatureBody struct {
	Version    int             `json:"version"`
	WorkloadID string          `json:"workload_id"`
	Type       string          `json:"type"`
	Spec       json.RawMessage `json:"spec"`
}

func (ds DesiredStateEnvelope) hasTypedWorkload() bool {
	return ds.WorkloadID != "" || ds.Type != "" || len(ds.Spec) > 0
}

func (ds DesiredStateEnvelope) signingPayload() string {
	if !ds.hasTypedWorkload() {
		return ds.Payload
	}

	body := desiredStateSignatureBody{
		Version:    ds.Version,
		WorkloadID: ds.WorkloadID,
		Type:       ds.Type,
		Spec:       ds.Spec,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return ""
	}
	return string(data)
}

func desiredStateEnvelopeFromPayload(version int, payload string, nodeSecret string, nodeID string) DesiredStateEnvelope {
	var envelope DesiredStateEnvelope
	if err := json.Unmarshal([]byte(payload), &envelope); err == nil && envelope.hasTypedWorkload() {
		envelope.Version = version
		envelope.Payload = ""
		envelope.Signature = signDesiredState(nodeID, envelope.Version, envelope.signingPayload(), nodeSecret)
		return envelope
	}

	envelope = DesiredStateEnvelope{
		Version: version,
		Payload: payload,
	}
	envelope.Signature = signDesiredState(nodeID, envelope.Version, envelope.signingPayload(), nodeSecret)
	return envelope
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return false
	}

	if r.Method != method {
		http.Error(w, errMethodNotAllowed, http.StatusMethodNotAllowed)
		return false
	}
	return true
}

func nodeIdMissing(w http.ResponseWriter, nodeID string) bool {
	if nodeID == "" {
		http.Error(w, errMissingNodeID, http.StatusBadRequest)
		return true
	}
	return false
}

func nodeTokenMissing(w http.ResponseWriter, nodeToken string) bool {
	if nodeToken == "" {
		http.Error(w, errMissingNodeToken, http.StatusUnauthorized)
		return true
	}
	return false
}

func logNodeAuthReject(r *http.Request, reason, presentedNodeID, expectedNodeID string) {
	logSystem("control-plane.auth", "node_auth_rejected", "rejected", map[string]any{
		"path":           r.URL.Path,
		"reason":         reason,
		"presented_node": presentedNodeID,
		"expected_node":  expectedNodeID,
		"retryable":      false,
	}, fmt.Sprintf("[AUTH][REJECT] path=%s reason=%s presented_node=%s expected_node=%s", r.URL.Path, reason, presentedNodeID, expectedNodeID))
}

func logUserAuthReject(r *http.Request, reason string) {
	logSystem("control-plane.auth", "user_auth_rejected", "rejected", map[string]any{
		"path":      r.URL.Path,
		"reason":    reason,
		"retryable": false,
	}, fmt.Sprintf("[USER_AUTH][REJECT] path=%s reason=%s", r.URL.Path, reason))
}

func generateNodeSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func authenticateNodeRequest(w http.ResponseWriter, r *http.Request, expectedNodeID string) bool {
	nodeID := r.Header.Get("X-Node-ID")
	if nodeIdMissing(w, nodeID) {
		logNodeAuthReject(r, "missing-node-id", nodeID, expectedNodeID)
		return false
	}
	if expectedNodeID != "" && nodeID != expectedNodeID {
		logNodeAuthReject(r, "node-id-mismatch", nodeID, expectedNodeID)
		http.Error(w, "node id mismatch", http.StatusUnauthorized)
		return false
	}

	nodeToken := r.Header.Get("X-Node-Token")
	if nodeTokenMissing(w, nodeToken) {
		logNodeAuthReject(r, "missing-node-token", nodeID, expectedNodeID)
		return false
	}

	var storedToken string
	err := db.QueryRow(`SELECT node_secret FROM nodes WHERE node_id = ?`, nodeID).Scan(&storedToken)
	if err == sql.ErrNoRows {
		logNodeAuthReject(r, "unknown-node", nodeID, expectedNodeID)
		http.Error(w, "unknown node", http.StatusUnauthorized)
		return false
	}
	if err != nil {
		logSystem("control-plane.auth", "node_auth_failed", "error", map[string]any{
			"path":      r.URL.Path,
			"node_id":   nodeID,
			"reason":    err.Error(),
			"retryable": false,
		}, fmt.Sprintf("[AUTH][ERROR] path=%s node=%s err=%v", r.URL.Path, nodeID, err))
		http.Error(w, "db error", http.StatusInternalServerError)
		return false
	}
	if storedToken == "" || subtle.ConstantTimeCompare([]byte(storedToken), []byte(nodeToken)) != 1 {
		logNodeAuthReject(r, "invalid-node-token", nodeID, expectedNodeID)
		http.Error(w, "invalid node token", http.StatusUnauthorized)
		return false
	}

	return true
}

func signDesiredState(nodeID string, version int, payload, nodeSecret string) string {
	mac := hmac.New(sha256.New, []byte(nodeSecret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%s\n%d\n%s", nodeID, version, payload)))
	return hex.EncodeToString(mac.Sum(nil))
}

func getenv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func authenticateUserRequest(w http.ResponseWriter, r *http.Request) bool {
	username, password, ok := r.BasicAuth()
	if !ok {
		logUserAuthReject(r, "missing-basic-auth")
		w.Header().Set("WWW-Authenticate", `Basic realm="edgefleet-control-plane"`)
		http.Error(w, "basic auth required", http.StatusUnauthorized)
		return false
	}

	userMatch := subtle.ConstantTimeCompare([]byte(username), []byte(controlPlaneUser)) == 1
	passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(controlPlanePassword)) == 1
	if !userMatch || !passwordMatch {
		logUserAuthReject(r, "invalid-basic-auth")
		w.Header().Set("WWW-Authenticate", `Basic realm="edgefleet-control-plane"`)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return false
	}

	return true
}

func heartbeatHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	nodeID := r.Header.Get("X-Node-ID")
	if !authenticateNodeRequest(w, r, nodeID) {
		return
	}

	res, err := db.Exec(
		`UPDATE nodes
		SET last_heartbeat = ?, status = 'active'
		WHERE node_id = ?`,
		time.Now().UTC(),
		nodeID,
	)
	if err != nil {
		logSystem("control-plane.heartbeat", "heartbeat_failed", "error", map[string]any{
			"node_id":   nodeID,
			"reason":    err.Error(),
			"retryable": false,
		}, err.Error())
		http.Error(w, "db error", http.StatusBadRequest)
		return
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		http.Error(w, "unknown node", http.StatusBadRequest)
		return
	}

	logSystem("control-plane.heartbeat", "heartbeat_received", "success", map[string]any{
		"node_id": nodeID,
		"rows":    rows,
	}, fmt.Sprintf("[Heartbeat][%s] marked ACTIVE at %s", nodeID, time.Now().Format(time.RFC3339)))

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ack"))
}

func registrationHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	nodeID := uuid.New().String()
	nodeSecret, err := generateNodeSecret()
	if err != nil {
		logSystem("control-plane.registration", "node_registration", "error", map[string]any{
			"reason":    err.Error(),
			"retryable": false,
		}, err.Error())
		http.Error(w, "failed to generate node secret", http.StatusInternalServerError)
		return
	}

	hostname := r.Header.Get("X-Node-Hostname")
	arch := r.Header.Get("X-Node-Arch")

	_, err = db.Exec(
		`INSERT INTO nodes (node_id, node_secret, last_heartbeat, status, hostname, arch)
		VALUES (?, ?, ?, ?, ?, ?)`,
		nodeID,
		nodeSecret,
		time.Now().UTC(),
		"registered",
		hostname,
		arch,
	)
	if err != nil {
		logSystem("control-plane.registration", "node_registration", "error", map[string]any{
			"node_id":   nodeID,
			"hostname":  hostname,
			"arch":      arch,
			"reason":    err.Error(),
			"retryable": false,
		}, err.Error())
		http.Error(w, "failed to register node", http.StatusInternalServerError)
		return
	}

	logSystem("control-plane.registration", "node_registered", "success", map[string]any{
		"node_id":  nodeID,
		"hostname": hostname,
		"arch":     arch,
	}, fmt.Sprintf("[REGISTER] node=%s hostname=%s arch=%s time=%s", nodeID, hostname, arch, time.Now().Format(time.RFC3339)))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(RegistrationResponse{
		NodeID:     nodeID,
		NodeSecret: nodeSecret,
	})
}

func getDesiredState(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	nodeID := r.PathValue("nodeID")
	if nodeIdMissing(w, nodeID) {
		return
	}
	if !authenticateNodeRequest(w, r, nodeID) {
		return
	}

	var version int
	var payload string
	var nodeSecret string

	err := db.QueryRow(`
		SELECT d.version, d.payload, n.node_secret
		FROM desired_state d
		JOIN nodes n ON n.node_id = d.node_id
		WHERE d.node_id = ?
	`, nodeID).Scan(&version, &payload, &nodeSecret)

	if err == sql.ErrNoRows {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(""))
		return
	}
	if err != nil {
		logSystem("control-plane.desired-state", "desired_state_serve_failed", "error", map[string]any{
			"node_id":   nodeID,
			"reason":    err.Error(),
			"retryable": false,
		}, err.Error())
		http.Error(w, "error fetching desired state", http.StatusInternalServerError)
		return
	}

	logReconciliation("control-plane.desired-state", "desired_state_served", "success", map[string]any{
		"node_id": nodeID,
		"version": version,
	}, fmt.Sprintf("[DESIRED_STATE_FETCH] node=%s version=%d time=%s", nodeID, version, time.Now().Format(time.RFC3339)))

	envelope := desiredStateEnvelopeFromPayload(version, payload, nodeSecret, nodeID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(envelope)
}

func getHealthDetail(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if !authenticateUserRequest(w, r) {
		return
	}

	rows, err := db.Query(`SELECT node_id, status FROM nodes`)
	if err != nil {
		logSystem("control-plane.health", "health_detail_failed", "error", map[string]any{
			"reason":    err.Error(),
			"retryable": false,
		}, err.Error())
		http.Error(w, "health unavailable", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf("nodes=%d", count)))
}

func initDB() error {
	var err error
	db, err = sql.Open("sqlite", "./edgefleet.db")
	if err != nil {
		return err
	}

	_, _ = db.Exec(`PRAGMA journal_mode=WAL;`)

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS nodes (
		node_id TEXT PRIMARY KEY,
		node_secret TEXT,
		last_heartbeat TIMESTAMP,
		status TEXT,
		hostname TEXT,
		arch TEXT
	)
	`)
	if err != nil {
		return err
	}
	if err := ensureNodeSecretColumn(); err != nil {
		return err
	}

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS desired_state (
		node_id TEXT PRIMARY KEY,
		version INTEGER,
		payload TEXT
	)
	`)
	if err != nil {
		return err
	}
	return err
}

func ensureNodeSecretColumn() error {
	rows, err := db.Query(`PRAGMA table_info(nodes)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var dataType string
		var notNull int
		var defaultValue any
		var pk int

		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == "node_secret" {
			return nil
		}
	}

	_, err = db.Exec(`ALTER TABLE nodes ADD COLUMN node_secret TEXT`)
	return err
}

func upsertDesiredState(nodeID string, version int, payload string) error {
	_, err := db.Exec(`
		INSERT INTO desired_state (node_id, version, payload)
		VALUES (?, ?, ?)
		ON CONFLICT(node_id)
		DO UPDATE SET
			version = excluded.version,
			payload = excluded.payload
		`, nodeID, version, payload)

	return err
}

func livenessSweep(stop <-chan struct{}) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		cutoff := time.Now().UTC().Add(-heartbeatExpiry)

		rows, err := db.Query(`
			SELECT node_id, status
			FROM nodes
			WHERE last_heartbeat < ?
			  AND status != 'unknown'
		`, cutoff)
		if err != nil {
			logSystem("control-plane.liveness-sweep", "liveness_sweep_failed", "error", map[string]any{
				"reason":       err.Error(),
				"retryable":    true,
				"retry_in_sec": int(sweepInterval / time.Second),
			}, fmt.Sprintf("[SWEEP][ERROR] %v", err))
			continue
		}

		var affected []string

		for rows.Next() {
			var nodeID, status string
			rows.Scan(&nodeID, &status)

			_, _ = db.Exec(`
				UPDATE nodes
				SET status = 'unknown'
				WHERE node_id = ?
			`, nodeID)

			affected = append(affected, nodeID)
		}

		rows.Close()

		for _, id := range affected {
			logReconciliation("control-plane.liveness-sweep", "node_marked_unknown", "success", map[string]any{
				"node_id":    id,
				"from":       "active",
				"to":         "unknown",
				"expired_at": cutoff.Format(time.RFC3339),
			}, fmt.Sprintf("[STATE][%s] ACTIVE -> UNKNOWN", id))
		}
	}
}

func listNodes(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if !authenticateUserRequest(w, r) {
		return
	}

	rows, err := db.Query(`
		SELECT node_id, status, last_heartbeat
		FROM nodes
	`)
	if err != nil {
		logSystem("control-plane.nodes", "node_list_failed", "error", map[string]any{
			"reason":    err.Error(),
			"retryable": false,
		}, err.Error())
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, status string
		var ts time.Time
		rows.Scan(&id, &status, &ts)

		fmt.Fprintf(w, "%s | %s | %s\n",
			id,
			status,
			ts.Format(time.RFC3339),
		)
	}
}

func setDesiredState(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !authenticateUserRequest(w, r) {
		return
	}

	nodeID := r.URL.Query().Get("nodeID")
	if nodeID == "" {
		http.Error(w, "missing nodeID", http.StatusBadRequest)
		return
	}

	versionStr := r.URL.Query().Get("version")
	payloadBytes, _ := io.ReadAll(r.Body)

	version, _ := strconv.Atoi(versionStr)

	err := upsertDesiredState(nodeID, version, string(payloadBytes))
	if err != nil {
		logReconciliation("control-plane.desired-state", "desired_state_update_failed", "error", map[string]any{
			"node_id":   nodeID,
			"version":   version,
			"reason":    err.Error(),
			"retryable": false,
		}, err.Error())
		http.Error(w, "failed to set desired state", http.StatusInternalServerError)
		return
	}

	logReconciliation("control-plane.desired-state", "desired_state_updated", "success", map[string]any{
		"node_id": nodeID,
		"version": version,
	}, fmt.Sprintf("[DESIRED_STATE_SET][%s] version=%d", nodeID, version))

	w.WriteHeader(http.StatusOK)
}

func main() {
	addr := ":8080"
	if err := initDB(); err != nil {
		logSystem("control-plane", "startup", "error", map[string]any{
			"addr":      addr,
			"reason":    err.Error(),
			"retryable": false,
		}, err.Error())
		log.Fatal(err)
	}

	sweepStop := make(chan struct{})
	go livenessSweep(sweepStop)

	mux := http.NewServeMux()
	mux.HandleFunc("/register", registrationHandler)
	mux.HandleFunc("/heartbeat", heartbeatHandler)
	mux.HandleFunc("/desired-state/{nodeID}", getDesiredState)
	mux.HandleFunc("/health", getHealthDetail)
	mux.HandleFunc("/nodes", listNodes)
	mux.HandleFunc("/debug/set-desired", setDesiredState)

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(shutdownSignals)

	go func() {
		sig := <-shutdownSignals
		logSystem("control-plane", "shutdown", "started", map[string]any{
			"signal": sig.String(),
		}, "")
		close(sweepStop)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			logSystem("control-plane", "shutdown", "error", map[string]any{
				"signal":    sig.String(),
				"reason":    err.Error(),
				"retryable": false,
			}, err.Error())
		}
	}()

	logSystem("control-plane", "startup", "success", map[string]any{
		"addr": addr,
	}, "Control Plane starting on "+addr)

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logSystem("control-plane", "serve_http_failed", "error", map[string]any{
			"addr":      addr,
			"reason":    err.Error(),
			"retryable": false,
		}, err.Error())
		log.Fatal(err)
	}

	logSystem("control-plane", "shutdown", "success", map[string]any{
		"addr": addr,
	}, "")
}
