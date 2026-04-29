package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Global DB variable
var db *sql.DB

// constant to Standardize error messages
const (
	errMethodNotAllowed = "method not allowed"
	errMissingNodeID    = "missing node id"
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

// heartbeat updates liveness metadata only.
func heartbeatHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	nodeID := r.Header.Get("X-Node-ID")
	if nodeIdMissing(w, nodeID) {
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

	hostname := r.Header.Get("X-Node-Hostname")
	arch := r.Header.Get("X-Node-Arch")

	_, err := db.Exec(
		`INSERT INTO nodes (node_id, last_heartbeat, status, hostname, arch)
		VALUES (?, ?, ?, ?, ?)`,
		nodeID,
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

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(nodeID))
}

func getDesiredState(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	nodeID := r.PathValue("nodeID")
	if nodeIdMissing(w, nodeID) {
		return
	}

	var version int
	var payload string

	err := db.QueryRow(`
		SELECT version, payload
		FROM desired_state
		WHERE node_id = ?
	`, nodeID).Scan(&version, &payload)

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

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(payload))
}

func getHealthDetail(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
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
		last_heartbeat TIMESTAMP,
		status TEXT,
		hostname TEXT,
		arch TEXT
	)
	`)

	if err != nil {
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
