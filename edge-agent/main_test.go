package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fakeControlPlane struct {
	mu             sync.Mutex
	nodeID         string
	nodeSecret     string
	signingSecret  string
	signingPayload string
	registerCount  int
	heartbeatNodes []string
	desiredVersion int
	desiredPayload string
}

func newFakeControlPlane(t *testing.T, desiredVersion int, desiredPayload string) (*fakeControlPlane, *httptest.Server) {
	t.Helper()

	cp := &fakeControlPlane{
		nodeID:         "node-1",
		nodeSecret:     "secret-1",
		signingSecret:  "secret-1",
		desiredVersion: desiredVersion,
		desiredPayload: desiredPayload,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		cp.mu.Lock()
		defer cp.mu.Unlock()

		cp.registerCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"node_id":"` + cp.nodeID + `","node_secret":"` + cp.nodeSecret + `"}`))
	})

	mux.HandleFunc("/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		cp.mu.Lock()
		defer cp.mu.Unlock()

		nodeID := r.Header.Get("X-Node-ID")
		cp.heartbeatNodes = append(cp.heartbeatNodes, nodeID)
		nodeToken := r.Header.Get("X-Node-Token")
		if nodeID != cp.nodeID || nodeToken != cp.nodeSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ack"))
	})

	mux.HandleFunc("/desired-state/", func(w http.ResponseWriter, r *http.Request) {
		cp.mu.Lock()
		defer cp.mu.Unlock()

		nodeID := strings.TrimPrefix(r.URL.Path, "/desired-state/")
		nodeToken := r.Header.Get("X-Node-Token")
		headerNodeID := r.Header.Get("X-Node-ID")
		if nodeID != cp.nodeID || headerNodeID != cp.nodeID || nodeToken != cp.nodeSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		signingPayload := cp.desiredPayload
		if cp.signingPayload != "" {
			signingPayload = cp.signingPayload
		}

		envelope := DesiredState{
			Version:   cp.desiredVersion,
			Payload:   cp.desiredPayload,
			Signature: signDesiredState(cp.nodeID, cp.desiredVersion, signingPayload, cp.signingSecret),
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(envelope)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return cp, server
}

func withStateFile(t *testing.T, path string) {
	t.Helper()

	previous := stateFile
	stateFile = path
	t.Cleanup(func() {
		stateFile = previous
	})
}

func withControlPlaneBase(t *testing.T, base string) {
	t.Helper()

	previous := controlPlaneBase
	controlPlaneBase = base
	t.Cleanup(func() {
		controlPlaneBase = previous
	})
}

func withLogBuffer(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buffer bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()

	log.SetOutput(&buffer)
	log.SetFlags(0)

	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	return &buffer
}

func countOccurrences(haystack, needle string) int {
	return strings.Count(haystack, needle)
}

func TestSaveAndLoadPersistentState(t *testing.T) {
	tempDir := t.TempDir()
	withStateFile(t, filepath.Join(tempDir, stateFileName))

	expected := PersistentState{
		NodeID:             "node-123",
		NodeSecret:         "secret-123",
		LastAppliedVersion: 7,
	}

	savePersistentState(expected)

	actual := loadPersistentState()
	if actual != expected {
		t.Fatalf("expected %+v, got %+v", expected, actual)
	}
}

func TestLoadPersistentStateMigratesLegacyFiles(t *testing.T) {
	tempDir := t.TempDir()
	withStateFile(t, filepath.Join(tempDir, stateFileName))

	if err := os.WriteFile(filepath.Join(tempDir, "node_id.txt"), []byte("legacy-node\n"), 0644); err != nil {
		t.Fatalf("write node id: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "applied_version.txt"), []byte("11\n"), 0644); err != nil {
		t.Fatalf("write applied version: %v", err)
	}

	state := loadPersistentState()
	if state.NodeID != "legacy-node" {
		t.Fatalf("expected migrated node id, got %q", state.NodeID)
	}
	if state.LastAppliedVersion != 11 {
		t.Fatalf("expected migrated version 11, got %d", state.LastAppliedVersion)
	}

	if _, err := os.Stat(filepath.Join(tempDir, stateFileName)); err != nil {
		t.Fatalf("expected state file to be created: %v", err)
	}
}

func TestLoadPersistentStatePrefersJSONState(t *testing.T) {
	tempDir := t.TempDir()
	withStateFile(t, filepath.Join(tempDir, stateFileName))

	savePersistentState(PersistentState{
		NodeID:             "json-node",
		NodeSecret:         "secret-json",
		LastAppliedVersion: 21,
	})

	if err := os.WriteFile(filepath.Join(tempDir, "node_id.txt"), []byte("legacy-node\n"), 0644); err != nil {
		t.Fatalf("write node id: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "applied_version.txt"), []byte("3\n"), 0644); err != nil {
		t.Fatalf("write applied version: %v", err)
	}

	state := loadPersistentState()
	if state.NodeID != "json-node" {
		t.Fatalf("expected JSON node id, got %q", state.NodeID)
	}
	if state.LastAppliedVersion != 21 {
		t.Fatalf("expected JSON version 21, got %d", state.LastAppliedVersion)
	}
}

func TestInitializeLocalStateReregistersWhenSecretMissing(t *testing.T) {
	tempDir := t.TempDir()
	logBuffer := withLogBuffer(t)

	cp, server := newFakeControlPlane(t, 1, "secure-bootstrap")
	withControlPlaneBase(t, server.URL)

	withStateFile(t, filepath.Join(tempDir, stateFileName))
	savePersistentState(PersistentState{
		NodeID:             "legacy-node",
		LastAppliedVersion: 2,
	})

	state := initializeLocalState(tempDir)
	if state.NodeID != "node-1" {
		t.Fatalf("reregistered node id = %q, want %q", state.NodeID, "node-1")
	}
	if state.NodeSecret != "secret-1" {
		t.Fatalf("reregistered node secret = %q, want %q", state.NodeSecret, "secret-1")
	}

	cp.mu.Lock()
	registerCount := cp.registerCount
	cp.mu.Unlock()

	if registerCount != 1 {
		t.Fatalf("register count = %d, want %d", registerCount, 1)
	}
	if !strings.Contains(logBuffer.String(), "[STATE] missing node secret for node=legacy-node; registering again") {
		t.Fatalf("expected missing-secret log, got %q", logBuffer.String())
	}
}

func TestEdgeRestartReusesNodeIDAndReconcilesIdempotently(t *testing.T) {
	tempDir := t.TempDir()
	logBuffer := withLogBuffer(t)

	cp, server := newFakeControlPlane(t, 4, "restart-drill")
	withControlPlaneBase(t, server.URL)

	firstStart := initializeLocalState(tempDir)
	if firstStart.NodeID != "node-1" {
		t.Fatalf("first start node id = %q, want %q", firstStart.NodeID, "node-1")
	}

	runOnce(&firstStart)

	persistedAfterFirstStart := loadPersistentState()
	if persistedAfterFirstStart.NodeID != "node-1" {
		t.Fatalf("persisted node id after first start = %q, want %q", persistedAfterFirstStart.NodeID, "node-1")
	}
	if persistedAfterFirstStart.NodeSecret != "secret-1" {
		t.Fatalf("persisted node secret after first start = %q, want %q", persistedAfterFirstStart.NodeSecret, "secret-1")
	}
	if persistedAfterFirstStart.LastAppliedVersion != 4 {
		t.Fatalf("persisted version after first start = %d, want %d", persistedAfterFirstStart.LastAppliedVersion, 4)
	}

	firstLogs := logBuffer.String()
	if !strings.Contains(firstLogs, "[REGISTER] node=node-1") {
		t.Fatalf("first start logs should include registration, got %q", firstLogs)
	}
	if !strings.Contains(firstLogs, "[RECONCILE] applying version=4 payload=restart-drill") {
		t.Fatalf("first start logs should include apply, got %q", firstLogs)
	}

	logBuffer.Reset()

	secondStart := initializeLocalState(tempDir)
	if secondStart.NodeID != firstStart.NodeID {
		t.Fatalf("restart node id = %q, want %q", secondStart.NodeID, firstStart.NodeID)
	}
	if secondStart.NodeSecret != firstStart.NodeSecret {
		t.Fatalf("restart node secret changed")
	}

	runOnce(&secondStart)

	cp.mu.Lock()
	registerCount := cp.registerCount
	heartbeatNodes := append([]string(nil), cp.heartbeatNodes...)
	cp.mu.Unlock()

	if registerCount != 1 {
		t.Fatalf("register count = %d, want %d", registerCount, 1)
	}
	if len(heartbeatNodes) != 2 {
		t.Fatalf("heartbeat count = %d, want %d", len(heartbeatNodes), 2)
	}
	for _, nodeID := range heartbeatNodes {
		if nodeID != "node-1" {
			t.Fatalf("heartbeat used node id %q, want %q", nodeID, "node-1")
		}
	}

	persistedAfterRestart := loadPersistentState()
	if persistedAfterRestart.NodeID != "node-1" {
		t.Fatalf("persisted node id after restart = %q, want %q", persistedAfterRestart.NodeID, "node-1")
	}
	if persistedAfterRestart.NodeSecret != "secret-1" {
		t.Fatalf("persisted node secret after restart = %q, want %q", persistedAfterRestart.NodeSecret, "secret-1")
	}
	if persistedAfterRestart.LastAppliedVersion != 4 {
		t.Fatalf("persisted version after restart = %d, want %d", persistedAfterRestart.LastAppliedVersion, 4)
	}

	secondLogs := logBuffer.String()
	if !strings.Contains(secondLogs, "[STATE] restored node=node-1 last_applied=4") {
		t.Fatalf("restart logs should include restored state, got %q", secondLogs)
	}
	if strings.Contains(secondLogs, "[REGISTER]") {
		t.Fatalf("restart logs should not register again, got %q", secondLogs)
	}
	if strings.Contains(secondLogs, "stale/replay") {
		t.Fatalf("restart logs should not report replay for same version, got %q", secondLogs)
	}
	if strings.Contains(secondLogs, "[RECONCILE] applying") {
		t.Fatalf("restart logs should not reapply same version, got %q", secondLogs)
	}
	if !strings.Contains(secondLogs, "[RECONCILE] compare remote=4 local=4 result=in-sync") {
		t.Fatalf("restart logs should show in-sync comparison, got %q", secondLogs)
	}
}

func TestEdgeRestartDetectsDriftAndAppliesUpdatedDesiredStateOnce(t *testing.T) {
	tempDir := t.TempDir()
	logBuffer := withLogBuffer(t)

	cp, server := newFakeControlPlane(t, 4, "before-offline")
	withControlPlaneBase(t, server.URL)

	firstStart := initializeLocalState(tempDir)
	runOnce(&firstStart)

	firstLogs := logBuffer.String()
	if !strings.Contains(firstLogs, "[RECONCILE] compare remote=4 local=0 result=drift") {
		t.Fatalf("first start should detect initial drift, got %q", firstLogs)
	}
	if !strings.Contains(firstLogs, "[RECONCILE] success version=4") {
		t.Fatalf("first start should report successful apply, got %q", firstLogs)
	}

	cp.mu.Lock()
	cp.desiredVersion = 5
	cp.desiredPayload = "after-offline"
	cp.mu.Unlock()

	logBuffer.Reset()

	secondStart := initializeLocalState(tempDir)
	if secondStart.NodeID != "node-1" {
		t.Fatalf("restart node id = %q, want %q", secondStart.NodeID, "node-1")
	}
	if secondStart.NodeSecret != "secret-1" {
		t.Fatalf("restart node secret = %q, want %q", secondStart.NodeSecret, "secret-1")
	}
	if secondStart.LastAppliedVersion != 4 {
		t.Fatalf("restart local version = %d, want %d", secondStart.LastAppliedVersion, 4)
	}

	runOnce(&secondStart)

	cp.mu.Lock()
	registerCount := cp.registerCount
	heartbeatNodes := append([]string(nil), cp.heartbeatNodes...)
	cp.mu.Unlock()

	if registerCount != 1 {
		t.Fatalf("register count after drift restart = %d, want %d", registerCount, 1)
	}
	if len(heartbeatNodes) != 2 {
		t.Fatalf("heartbeat count after drift restart = %d, want %d", len(heartbeatNodes), 2)
	}

	persisted := loadPersistentState()
	if persisted.NodeID != "node-1" {
		t.Fatalf("persisted node id after drift restart = %q, want %q", persisted.NodeID, "node-1")
	}
	if persisted.NodeSecret != "secret-1" {
		t.Fatalf("persisted node secret after drift restart = %q, want %q", persisted.NodeSecret, "secret-1")
	}
	if persisted.LastAppliedVersion != 5 {
		t.Fatalf("persisted version after drift restart = %d, want %d", persisted.LastAppliedVersion, 5)
	}

	secondLogs := logBuffer.String()
	if !strings.Contains(secondLogs, "[STATE] restored node=node-1 last_applied=4") {
		t.Fatalf("restart logs should include restored state, got %q", secondLogs)
	}
	if !strings.Contains(secondLogs, "[RECONCILE] compare remote=5 local=4 result=drift") {
		t.Fatalf("restart logs should detect drift, got %q", secondLogs)
	}
	if countOccurrences(secondLogs, "[RECONCILE] applying version=5 payload=after-offline") != 1 {
		t.Fatalf("restart logs should apply updated version exactly once, got %q", secondLogs)
	}
	if !strings.Contains(secondLogs, "[RECONCILE] success version=5") {
		t.Fatalf("restart logs should report successful apply, got %q", secondLogs)
	}
	if strings.Contains(secondLogs, "[REGISTER]") {
		t.Fatalf("restart logs should not register again, got %q", secondLogs)
	}
}

func TestEdgeRejectsInvalidDesiredStateSignature(t *testing.T) {
	tempDir := t.TempDir()
	logBuffer := withLogBuffer(t)

	cp, server := newFakeControlPlane(t, 7, "tampered")
	withControlPlaneBase(t, server.URL)

	cp.mu.Lock()
	cp.signingSecret = "control-plane-secret"
	cp.mu.Unlock()

	state := PersistentState{
		NodeID:     "node-1",
		NodeSecret: "secret-1",
	}
	withStateFile(t, filepath.Join(tempDir, stateFileName))
	savePersistentState(state)

	runOnce(&state)

	persisted := loadPersistentState()
	if persisted.LastAppliedVersion != 0 {
		t.Fatalf("persisted version after invalid signature = %d, want %d", persisted.LastAppliedVersion, 0)
	}

	logs := logBuffer.String()
	if !strings.Contains(logs, "[RECONCILE] invalid signature version=7") {
		t.Fatalf("expected invalid signature log, got %q", logs)
	}
	if strings.Contains(logs, "[RECONCILE] applying") {
		t.Fatalf("should not apply tampered desired state, got %q", logs)
	}
}

func TestEdgeRejectsStaleDesiredStateReplay(t *testing.T) {
	tempDir := t.TempDir()
	logBuffer := withLogBuffer(t)

	_, server := newFakeControlPlane(t, 4, "old-command")
	withControlPlaneBase(t, server.URL)

	state := PersistentState{
		NodeID:             "node-1",
		NodeSecret:         "secret-1",
		LastAppliedVersion: 6,
	}
	withStateFile(t, filepath.Join(tempDir, stateFileName))
	savePersistentState(state)

	runOnce(&state)

	if state.LastAppliedVersion != 6 {
		t.Fatalf("in-memory version after stale replay = %d, want %d", state.LastAppliedVersion, 6)
	}

	persisted := loadPersistentState()
	if persisted.LastAppliedVersion != 6 {
		t.Fatalf("persisted version after stale replay = %d, want %d", persisted.LastAppliedVersion, 6)
	}

	logs := logBuffer.String()
	if !strings.Contains(logs, "[RECONCILE] compare remote=4 local=6 result=stale") {
		t.Fatalf("expected stale replay log, got %q", logs)
	}
	if strings.Contains(logs, "[RECONCILE] applying") {
		t.Fatalf("should not apply stale desired state, got %q", logs)
	}
}

func TestAttackSimulationRejectsTamperedDesiredStatePayload(t *testing.T) {
	tempDir := t.TempDir()
	logBuffer := withLogBuffer(t)

	cp, server := newFakeControlPlane(t, 9, "tampered-payload")
	withControlPlaneBase(t, server.URL)

	cp.mu.Lock()
	cp.signingPayload = "trusted-payload"
	cp.mu.Unlock()

	state := PersistentState{
		NodeID:     "node-1",
		NodeSecret: "secret-1",
	}
	withStateFile(t, filepath.Join(tempDir, stateFileName))
	savePersistentState(state)

	runOnce(&state)

	if state.LastAppliedVersion != 0 {
		t.Fatalf("version after tampered payload = %d, want %d", state.LastAppliedVersion, 0)
	}

	persisted := loadPersistentState()
	if persisted.LastAppliedVersion != 0 {
		t.Fatalf("persisted version after tampered payload = %d, want %d", persisted.LastAppliedVersion, 0)
	}

	logs := logBuffer.String()
	if !strings.Contains(logs, "[SECURITY][REJECT] desired-state invalid signature version=9") {
		t.Fatalf("expected tampered payload reject log, got %q", logs)
	}
	if strings.Contains(logs, "[RECONCILE] applying") {
		t.Fatalf("should not apply tampered payload, got %q", logs)
	}
}

func TestAttackSimulationRejectsReplayOldDesiredState(t *testing.T) {
	tempDir := t.TempDir()
	logBuffer := withLogBuffer(t)

	_, server := newFakeControlPlane(t, 3, "replayed-command")
	withControlPlaneBase(t, server.URL)

	state := PersistentState{
		NodeID:             "node-1",
		NodeSecret:         "secret-1",
		LastAppliedVersion: 5,
	}
	withStateFile(t, filepath.Join(tempDir, stateFileName))
	savePersistentState(state)

	runOnce(&state)

	if state.LastAppliedVersion != 5 {
		t.Fatalf("version after replay attack = %d, want %d", state.LastAppliedVersion, 5)
	}

	persisted := loadPersistentState()
	if persisted.LastAppliedVersion != 5 {
		t.Fatalf("persisted version after replay attack = %d, want %d", persisted.LastAppliedVersion, 5)
	}

	logs := logBuffer.String()
	if !strings.Contains(logs, "[RECONCILE] compare remote=3 local=5 result=stale") {
		t.Fatalf("expected stale replay log, got %q", logs)
	}
	if strings.Contains(logs, "[RECONCILE] applying") {
		t.Fatalf("should not apply replayed desired state, got %q", logs)
	}
}

func TestEdgeInvalidSignatureDoesNotBreakLaterValidReconcile(t *testing.T) {
	tempDir := t.TempDir()
	logBuffer := withLogBuffer(t)

	cp, server := newFakeControlPlane(t, 7, "tampered-first")
	withControlPlaneBase(t, server.URL)

	cp.mu.Lock()
	cp.signingSecret = "wrong-secret"
	cp.mu.Unlock()

	state := PersistentState{
		NodeID:     "node-1",
		NodeSecret: "secret-1",
	}
	withStateFile(t, filepath.Join(tempDir, stateFileName))
	savePersistentState(state)

	runOnce(&state)

	if state.LastAppliedVersion != 0 {
		t.Fatalf("version after invalid signature = %d, want %d", state.LastAppliedVersion, 0)
	}

	firstLogs := logBuffer.String()
	if !strings.Contains(firstLogs, "[SECURITY][REJECT] desired-state invalid signature version=7") {
		t.Fatalf("expected security reject log, got %q", firstLogs)
	}
	if strings.Contains(firstLogs, "[RECONCILE] success version=7") {
		t.Fatalf("should not report success for invalid signature, got %q", firstLogs)
	}

	cp.mu.Lock()
	cp.signingSecret = cp.nodeSecret
	cp.desiredVersion = 8
	cp.desiredPayload = "trusted-now"
	registerCount := cp.registerCount
	cp.mu.Unlock()

	if registerCount != 0 {
		t.Fatalf("unexpected register count before recovery = %d, want %d", registerCount, 0)
	}

	logBuffer.Reset()
	runOnce(&state)

	if state.LastAppliedVersion != 8 {
		t.Fatalf("version after recovery reconcile = %d, want %d", state.LastAppliedVersion, 8)
	}

	persisted := loadPersistentState()
	if persisted.LastAppliedVersion != 8 {
		t.Fatalf("persisted version after recovery reconcile = %d, want %d", persisted.LastAppliedVersion, 8)
	}

	cp.mu.Lock()
	heartbeatCount := len(cp.heartbeatNodes)
	cp.mu.Unlock()

	if heartbeatCount != 2 {
		t.Fatalf("heartbeat count across reject and recovery = %d, want %d", heartbeatCount, 2)
	}

	secondLogs := logBuffer.String()
	if !strings.Contains(secondLogs, "[RECONCILE] compare remote=8 local=0 result=drift") {
		t.Fatalf("expected drift log on recovery, got %q", secondLogs)
	}
	if !strings.Contains(secondLogs, "[RECONCILE] success version=8") {
		t.Fatalf("expected successful reconcile after recovery, got %q", secondLogs)
	}
}
