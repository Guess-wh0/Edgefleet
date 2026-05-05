package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var controlPlaneBase = "http://localhost:8080"

const stateFileName = "state.json"

var stateFile = ""

type DesiredState struct {
	Version   int    `json:"version"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type RegistrationResponse struct {
	NodeID     string `json:"node_id"`
	NodeSecret string `json:"node_secret"`
}

type PersistentState struct {
	NodeID             string `json:"node_id"`
	NodeSecret         string `json:"node_secret"`
	LastAppliedVersion int    `json:"last_applied_desired_state_version"`
}

func signDesiredState(nodeID string, version int, payload, nodeSecret string) string {
	mac := hmac.New(sha256.New, []byte(nodeSecret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%s\n%d\n%s", nodeID, version, payload)))
	return hex.EncodeToString(mac.Sum(nil))
}

func verifyDesiredStateSignature(state PersistentState, ds DesiredState) bool {
	expected := signDesiredState(state.NodeID, ds.Version, ds.Payload, state.NodeSecret)
	return hmac.Equal([]byte(expected), []byte(ds.Signature))
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

func registerNode() PersistentState {
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

	var registration RegistrationResponse
	if err := json.NewDecoder(resp.Body).Decode(&registration); err != nil {
		log.Fatal("registration decode failed:", err)
	}
	if registration.NodeID == "" || registration.NodeSecret == "" {
		log.Fatal("registration returned incomplete node identity")
	}

	state := PersistentState{
		NodeID:     registration.NodeID,
		NodeSecret: registration.NodeSecret,
	}
	savePersistentState(state)
	log.Printf("[REGISTER] node=%s", registration.NodeID)

	return state
}

func sendHeartbeat(state PersistentState) {
	req, _ := http.NewRequest(
		"POST",
		controlPlaneBase+"/heartbeat",
		nil,
	)
	req.Header.Set("X-Node-ID", state.NodeID)
	req.Header.Set("X-Node-Token", state.NodeSecret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Println("heartbeat error:", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("heartbeat rejected: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		return
	}

	log.Printf("[HEARTBEAT] sent node=%s", state.NodeID)
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

func fetchDesiredState(state PersistentState) (*DesiredState, error) {
	url := controlPlaneBase + "/desired-state/" + state.NodeID

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("X-Node-ID", state.NodeID)
	req.Header.Set("X-Node-Token", state.NodeSecret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("desired state fetch failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

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
	ds, err := fetchDesiredState(*state)
	if err != nil {
		log.Println("fetch error:", err)
		return
	}

	if ds == nil {
		return
	}
	if !verifyDesiredStateSignature(*state, *ds) {
		log.Printf("[RECONCILE] invalid signature version=%d", ds.Version)
		return
	}

	if ds.Version < state.LastAppliedVersion {
		log.Printf("[RECONCILE] compare remote=%d local=%d result=stale",
			ds.Version, state.LastAppliedVersion)
		return
	}
	if ds.Version == state.LastAppliedVersion {
		log.Printf("[RECONCILE] compare remote=%d local=%d result=in-sync",
			ds.Version, state.LastAppliedVersion)
		return
	}

	log.Printf("[RECONCILE] compare remote=%d local=%d result=drift",
		ds.Version, state.LastAppliedVersion)
	log.Printf("[RECONCILE] applying version=%d payload=%s",
		ds.Version, ds.Payload)

	// TODO: actual execution later

	state.LastAppliedVersion = ds.Version
	savePersistentState(*state)
	log.Printf("[RECONCILE] success version=%d", ds.Version)
}

func initializeLocalState(nodeDir string) PersistentState {
	_ = os.MkdirAll(nodeDir, 0755)
	stateFile = filepath.Join(nodeDir, stateFileName)

	state := loadPersistentState()
	if state.NodeID == "" {
		return registerNode()
	}
	if state.NodeSecret == "" {
		log.Printf("[STATE] missing node secret for node=%s; registering again", state.NodeID)
		return registerNode()
	}

	log.Printf("[STATE] restored node=%s last_applied=%d",
		state.NodeID,
		state.LastAppliedVersion,
	)
	return state
}

func runOnce(state *PersistentState) {
	sendHeartbeat(*state)
	reconcile(state)
}

func main() {
	// connectWiFi() // platform-specific: implemented on Pico (TinyGo)
	nodeDir := getenv("EDGE_NODE_DIR", ".")
	state := initializeLocalState(nodeDir)

	for {
		heartbeatEvery := time.Duration(getenvInt("EDGE_HEARTBEAT_SEC", 10)) * time.Second
		runOnce(&state)
		time.Sleep(heartbeatEvery)
	}
}
