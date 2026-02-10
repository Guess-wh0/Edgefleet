package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
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

// Phase 1: heartbeat updates liveness metadata only.
// No scheduling or reconciliation is triggered.
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
		SET last_heartbeat = ?, status = ?
		WHERE node_id = ?`,
		time.Now().UTC(),
		"available",
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
	log.Printf("[Heartbeat] node=%s time=%s",
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

	log.Printf("[DESIRED_STATE_FETCH] node=%s time=%s",
		nodeID,
		time.Now().Format(time.RFC3339),
	)

	//WIP: Add code to fetch node data from SQLite using nodeID
	// then compute the desired state in later phase. Currently we are returning empty string

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(""))
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

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS nodes (
		node_id TEXT PRIMARY KEY,
		last_heartbeat TIMESTAMP,
		status TEXT,
		hostname TEXT,
		arch TEXT
	)
	`)
	return err
}

func livenessSweep() {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for range ticker.C {
		cutoff := time.Now().UTC().Add(-heartbeatExpiry)

		res, err := db.Exec(`
			UPDATE nodes
			SET status = 'unavailable'
			WHERE last_heartbeat < ?
			  AND status != 'unavailable'
		`, cutoff)

		if err != nil {
			log.Println("[LIVENESS_SWEEP] error:", err)
			continue
		}

		affected, _ := res.RowsAffected()
		if affected > 0 {
			log.Printf("[LIVENESS_SWEEP] marked %d node(s) unavailable", affected)
		}
	}
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

	addr := ":8080"
	log.Println("Control Plane starting on", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
