package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Global DB variable
var db *sql.DB
var controlPlaneUser = getenv("CONTROL_PLANE_USER", "admin")
var controlPlanePassword = getenv("CONTROL_PLANE_PASSWORD", "edgefleet")

// constant to Standardize error messages
const (
	errMethodNotAllowed = "method not allowed"
	errMissingNodeID    = "missing node id"
	errMissingNodeToken = "missing node token"
)

const (
	heartbeatExpiry = 30 * time.Second
	sweepInterval   = 10 * time.Second
)

// Node Struct
// use this Node Struct to return value to GET request
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
	Version   int    `json:"version"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// Helper method to validate request method
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

// Helper method to validate NodeId presence in header
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
	log.Printf(
		"[AUTH][REJECT] path=%s reason=%s presented_node=%s expected_node=%s",
		r.URL.Path,
		reason,
		presentedNodeID,
		expectedNodeID,
	)
}

func logUserAuthReject(r *http.Request, reason string) {
	log.Printf("[USER_AUTH][REJECT] path=%s reason=%s", r.URL.Path, reason)
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
		log.Printf("[AUTH][ERROR] path=%s node=%s err=%v", r.URL.Path, nodeID, err)
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

// heartbeat updates liveness metadata only.
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
		http.Error(w, "db error", http.StatusBadRequest)
		return
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		http.Error(w, "unknown node", http.StatusBadRequest)
		return
	}
	log.Printf("[Heartbeat][%s] marked ACTIVE at %s",
		nodeID,
		time.Now().Format(time.RFC3339),
	)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ack"))
}

func registrationHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	nodeID := uuid.New().String()
	nodeSecret, err := generateNodeSecret()
	if err != nil {
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
		http.Error(w, "failed to register node", http.StatusInternalServerError)
		return
	}

	log.Printf("[REGISTER] node=%s hostname=%s arch=%s time=%s",
		nodeID,
		hostname,
		arch,
		time.Now().Format(time.RFC3339),
	)

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
		w.Write([]byte(""))
		return
	}

	if err != nil {
		http.Error(w, "error fetching desired state", http.StatusInternalServerError)
		return
	}

	log.Printf("[DESIRED_STATE_FETCH] node=%s version=%d time=%s",
		nodeID,
		version,
		time.Now().Format(time.RFC3339),
	)

	envelope := DesiredStateEnvelope{
		Version:   version,
		Payload:   payload,
		Signature: signDesiredState(nodeID, version, payload, nodeSecret),
	}

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
		http.Error(w, "health unavailable", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}

	log.Printf("[HEALTH_DETAIL] time=%s", time.Now().Format(time.RFC3339))

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(
		fmt.Sprintf("nodes=%d", count),
	))
}

// Initialize DB server
func initDB() error {
	var err error
	db, err = sql.Open("sqlite", "./edgefleet.db")
	if err != nil {
		return err
	}

	// Enable WAL mode for better concurrency
	_, _ = db.Exec(`PRAGMA journal_mode=WAL;`)

	// Nodes table
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

	// Desired state table
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

func livenessSweep() {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for range ticker.C {
		cutoff := time.Now().UTC().Add(-heartbeatExpiry)

		rows, err := db.Query(`
			SELECT node_id, status
			FROM nodes
			WHERE last_heartbeat < ?
			  AND status != 'unknown'
		`, cutoff)

		if err != nil {
			log.Println("[SWEEP][ERROR]", err)
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
			log.Printf("[STATE][%s] ACTIVE → UNKNOWN", id)
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

// for development stress testing only
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
		http.Error(w, "failed to set desired state", http.StatusInternalServerError)
		return
	}

	log.Printf("[DESIRED_STATE_SET][%s] version=%d",
		nodeID,
		version,
	)

	w.WriteHeader(http.StatusOK)
}

func main() {

	if err := initDB(); err != nil {
		log.Fatal(err)
	}

	go livenessSweep()

	mux := http.NewServeMux()
	mux.HandleFunc("/register", registrationHandler)
	mux.HandleFunc("/heartbeat", heartbeatHandler)
	mux.HandleFunc("/desired-state/{nodeID}", getDesiredState)
	mux.HandleFunc("/health", getHealthDetail)
	mux.HandleFunc("/nodes", listNodes)
	mux.HandleFunc("/debug/set-desired", setDesiredState)

	addr := ":8080"
	log.Println("Control Plane starting on", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
