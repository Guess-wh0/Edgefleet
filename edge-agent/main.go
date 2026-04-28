package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const controlPlaneBase = "http://localhost:8080"
const stateFileName = "state.json"

var stateFile = ""

type DesiredState struct {
	Version int    `json:"version"`
	Payload string `json:"payload"`
}

type PersistentState struct {
	NodeID             string `json:"node_id"`
	LastAppliedVersion int    `json:"last_applied_desired_state_version"`
}

func loadPersistentState() PersistentState {
	data, err := os.ReadFile(stateFile)
	if err == nil {
		var state PersistentState
		if err := json.Unmarshal(data, &state); err != nil {
			log.Printf("state file unreadable: %v", err)
			return PersistentState{}
		}
		return state
	}

	if !errors.Is(err, os.ErrNotExist) {
		log.Printf("state file read error: %v", err)
		return PersistentState{}
	}

	return migrateLegacyState()
}

func savePersistentState(state PersistentState) {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("state marshal error: %v", err)
		return
	}

	tempFile := stateFile + ".tmp"
	data = append(data, '\n')

	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		log.Printf("state write error: %v", err)
		return
	}

	if err := os.Rename(tempFile, stateFile); err != nil {
		_ = os.Remove(stateFile)
		if err := os.Rename(tempFile, stateFile); err != nil {
			log.Printf("state replace error: %v", err)
			_ = os.Remove(tempFile)
		}
	}
}

func migrateLegacyState() PersistentState {
	state := PersistentState{
		NodeID:             loadLegacyNodeID(),
		LastAppliedVersion: loadLegacyAppliedVersion(),
	}

	if state.NodeID == "" && state.LastAppliedVersion == 0 {
		return PersistentState{}
	}

	savePersistentState(state)
	log.Printf("[STATE] migrated legacy local state into %s", stateFileName)

	return state
}

func loadLegacyNodeID() string {
	data, err := os.ReadFile(filepath.Join(filepath.Dir(stateFile), "node_id.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func loadLegacyAppliedVersion() int {
	candidates := []string{
		filepath.Join(filepath.Dir(stateFile), "applied_version.txt"),
		"applied_version.txt",
	}

	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}

		v, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil {
			return v
		}
	}

	return 0
}

func registerNode() string {
	req, _ := http.NewRequest(
		"POST",
		controlPlaneBase+"/register",
		nil,
	)

	req.Header.Set("X-Node-Hostname", getenv("EDGE_HOSTNAME", "edge-sim"))
	req.Header.Set("X-Node-Arch", getenv("EDGE_ARCH", "amd64"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal("registration failed:", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	nodeID := strings.TrimSpace(string(body))

	savePersistentState(PersistentState{NodeID: nodeID})
	log.Println("Registered node:", nodeID)

	return nodeID
}

func sendHeartbeat(nodeID string) {
	req, _ := http.NewRequest(
		"POST",
		controlPlaneBase+"/heartbeat",
		nil,
	)
	req.Header.Set("X-Node-ID", nodeID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Println("heartbeat error:", err)
		return
	}
	resp.Body.Close()

	log.Println("heartbeat sent")
}

func getenv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func getenvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

func fetchDesiredState(nodeID string) (*DesiredState, error) {
	url := controlPlaneBase + "/desired-state/" + nodeID

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if len(body) == 0 {
		return nil, nil
	}

	var ds DesiredState
	err = json.Unmarshal(body, &ds)
	if err != nil {
		return nil, err
	}
	log.Printf("[fetched_state] version=%d",
		ds.Version)
	return &ds, nil
}

func reconcile(state *PersistentState) {
	ds, err := fetchDesiredState(state.NodeID)
	if err != nil {
		log.Println("fetch error:", err)
		return
	}

	if ds == nil {
		return
	}

	if ds.Version <= state.LastAppliedVersion {
		log.Printf("[RECONCILE] stale/replay version=%d (last=%d)",
			ds.Version, state.LastAppliedVersion)
		return
	}

	// Persist only after a newer desired state is accepted locally.
	log.Printf("[RECONCILE] applying version=%d payload=%s",
		ds.Version, ds.Payload)

	// TODO: actual execution later

	state.LastAppliedVersion = ds.Version
	savePersistentState(*state)
}

func main() {
	// connectWiFi() // platform-specific: implemented on Pico (TinyGo)
	nodeDir := getenv("EDGE_NODE_DIR", ".")
	_ = os.MkdirAll(nodeDir, 0755)
	stateFile = filepath.Join(nodeDir, stateFileName)

	state := loadPersistentState()
	if state.NodeID == "" {
		state.NodeID = registerNode()
	}

	for {
		heartbeatEvery := time.Duration(getenvInt("EDGE_HEARTBEAT_SEC", 10)) * time.Second
		sendHeartbeat(state.NodeID)
		reconcile(&state)
		time.Sleep(heartbeatEvery)
	}
}
