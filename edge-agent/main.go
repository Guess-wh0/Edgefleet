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
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var controlPlaneBase = "http://localhost:8080"

const stateFileName = "state.json"
const workloadsFileName = "workloads.json"

var stateFile = ""

type DesiredState struct {
	Version    int             `json:"version"`
	WorkloadID string          `json:"workload_id,omitempty"`
	Type       string          `json:"type,omitempty"`
	Spec       json.RawMessage `json:"spec,omitempty"`
	Payload    string          `json:"payload,omitempty"`
	Signature  string          `json:"signature"`
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

type workloadCommand struct {
	Action     string `json:"action"`
	WorkloadID string `json:"workload_id"`
	Payload    string `json:"payload"`
	Fail       bool   `json:"fail"`
}

type workloadState struct {
	Active []string `json:"active"`
}

type desiredStateSignatureBody struct {
	Version    int             `json:"version"`
	WorkloadID string          `json:"workload_id"`
	Type       string          `json:"type"`
	Spec       json.RawMessage `json:"spec"`
}

func (ds DesiredState) hasTypedWorkload() bool {
	return ds.WorkloadID != "" || ds.Type != "" || len(ds.Spec) > 0
}

func (ds DesiredState) signingPayload() string {
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

func isSecurityFailureStatus(statusCode int) bool {
	return statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden
}

func signDesiredState(nodeID string, version int, payload, nodeSecret string) string {
	mac := hmac.New(sha256.New, []byte(nodeSecret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%s\n%d\n%s", nodeID, version, payload)))
	return hex.EncodeToString(mac.Sum(nil))
}

func verifyDesiredStateSignature(state PersistentState, ds DesiredState) bool {
	expected := signDesiredState(state.NodeID, ds.Version, ds.signingPayload(), state.NodeSecret)
	return hmac.Equal([]byte(expected), []byte(ds.Signature))
}

func workloadStatePath() string {
	return filepath.Join(filepath.Dir(stateFile), workloadsFileName)
}

func currentLoopRetrySec() int {
	return getenvInt("EDGE_HEARTBEAT_SEC", 10)
}

func loadWorkloadState() (workloadState, error) {
	path := workloadStatePath()
	data, err := os.ReadFile(path)
	if err == nil {
		var state workloadState
		if err := json.Unmarshal(data, &state); err != nil {
			return workloadState{}, err
		}
		sort.Strings(state.Active)
		return state, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return workloadState{}, nil
	}
	return workloadState{}, err
}

func saveWorkloadState(state workloadState) error {
	sort.Strings(state.Active)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(workloadStatePath(), data, 0644)
}

func hasWorkload(state workloadState, workloadID string) bool {
	for _, active := range state.Active {
		if active == workloadID {
			return true
		}
	}
	return false
}

func addWorkload(state workloadState, workloadID string) workloadState {
	if hasWorkload(state, workloadID) {
		return state
	}
	state.Active = append(state.Active, workloadID)
	sort.Strings(state.Active)
	return state
}

func removeWorkload(state workloadState, workloadID string) workloadState {
	var active []string
	for _, workload := range state.Active {
		if workload != workloadID {
			active = append(active, workload)
		}
	}
	state.Active = active
	return state
}

func parseWorkloadCommand(payload string) (workloadCommand, error) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return workloadCommand{}, errors.New("empty desired state payload")
	}

	var command workloadCommand
	if err := json.Unmarshal([]byte(trimmed), &command); err == nil {
		if command.WorkloadID == "" && command.Payload != "" {
			command.WorkloadID = command.Payload
		}
		if command.Action == "" {
			command.Action = "start"
		}
		if strings.TrimSpace(command.WorkloadID) == "" {
			return workloadCommand{}, errors.New("missing workload_id")
		}
		command.Action = strings.ToLower(strings.TrimSpace(command.Action))
		command.WorkloadID = strings.TrimSpace(command.WorkloadID)
		return command, nil
	}

	return workloadCommand{
		Action:     "start",
		WorkloadID: trimmed,
		Payload:    trimmed,
	}, nil
}

func loadPersistentState() PersistentState {
	data, err := os.ReadFile(stateFile)
	if err == nil {
		var state PersistentState
		if err := json.Unmarshal(data, &state); err != nil {
			logSystem("edge-agent.state", "state_load_failed", "error", map[string]any{
				"state_file": stateFile,
				"reason":     err.Error(),
				"retryable":  false,
			}, fmt.Sprintf("state file unreadable: %v", err))
			return PersistentState{}
		}
		return state
	}

	if !errors.Is(err, os.ErrNotExist) {
		logSystem("edge-agent.state", "state_load_failed", "error", map[string]any{
			"state_file": stateFile,
			"reason":     err.Error(),
			"retryable":  false,
		}, fmt.Sprintf("state file read error: %v", err))
		return PersistentState{}
	}

	return migrateLegacyState()
}

func savePersistentState(state PersistentState) {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		logSystem("edge-agent.state", "state_save_failed", "error", map[string]any{
			"state_file": stateFile,
			"reason":     err.Error(),
			"retryable":  false,
		}, fmt.Sprintf("state marshal error: %v", err))
		return
	}

	tempFile := stateFile + ".tmp"
	data = append(data, '\n')

	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		logSystem("edge-agent.state", "state_save_failed", "error", map[string]any{
			"state_file": tempFile,
			"reason":     err.Error(),
			"retryable":  false,
		}, fmt.Sprintf("state write error: %v", err))
		return
	}

	if err := os.Rename(tempFile, stateFile); err != nil {
		_ = os.Remove(stateFile)
		if err := os.Rename(tempFile, stateFile); err != nil {
			logSystem("edge-agent.state", "state_save_failed", "error", map[string]any{
				"state_file": stateFile,
				"reason":     err.Error(),
				"retryable":  false,
			}, fmt.Sprintf("state replace error: %v", err))
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
		logSystem("edge-agent.bootstrap", "node_registration", "error", map[string]any{
			"control_plane": controlPlaneBase,
			"reason":        err.Error(),
			"retryable":     false,
		}, fmt.Sprintf("registration failed: %v", err))
		log.Fatal("registration failed:", err)
	}
	defer resp.Body.Close()

	var registration RegistrationResponse
	if err := json.NewDecoder(resp.Body).Decode(&registration); err != nil {
		logSystem("edge-agent.bootstrap", "node_registration", "error", map[string]any{
			"control_plane": controlPlaneBase,
			"reason":        err.Error(),
			"retryable":     false,
		}, fmt.Sprintf("registration decode failed: %v", err))
		log.Fatal("registration decode failed:", err)
	}
	if registration.NodeID == "" || registration.NodeSecret == "" {
		logSystem("edge-agent.bootstrap", "node_registration", "error", map[string]any{
			"control_plane": controlPlaneBase,
			"reason":        "incomplete node identity",
			"retryable":     false,
		}, "registration returned incomplete node identity")
		log.Fatal("registration returned incomplete node identity")
	}

	state := PersistentState{
		NodeID:     registration.NodeID,
		NodeSecret: registration.NodeSecret,
	}
	savePersistentState(state)
	logSystem("edge-agent.bootstrap", "node_registration", "success", map[string]any{
		"node_id": registration.NodeID,
	}, fmt.Sprintf("[REGISTER] node=%s", registration.NodeID))

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
		logSystem("edge-agent.heartbeat", "heartbeat_failed", "error", map[string]any{
			"node_id":      state.NodeID,
			"reason":       err.Error(),
			"retryable":    true,
			"retry_in_sec": currentLoopRetrySec(),
		}, fmt.Sprintf("heartbeat error: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if isSecurityFailureStatus(resp.StatusCode) {
			logSystem("edge-agent.heartbeat", "heartbeat_failed", "rejected", map[string]any{
				"node_id":      state.NodeID,
				"http_status":  resp.StatusCode,
				"reason":       strings.TrimSpace(string(body)),
				"retryable":    true,
				"retry_in_sec": currentLoopRetrySec(),
			}, fmt.Sprintf("[SECURITY][REJECT] heartbeat status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body))))
			return
		}
		logSystem("edge-agent.heartbeat", "heartbeat_failed", "rejected", map[string]any{
			"node_id":      state.NodeID,
			"http_status":  resp.StatusCode,
			"reason":       strings.TrimSpace(string(body)),
			"retryable":    true,
			"retry_in_sec": currentLoopRetrySec(),
		}, fmt.Sprintf("heartbeat rejected: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body))))
		return
	}

	logSystem("edge-agent.heartbeat", "heartbeat_sent", "success", map[string]any{
		"node_id": state.NodeID,
	}, fmt.Sprintf("[HEARTBEAT] sent node=%s", state.NodeID))
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
		if isSecurityFailureStatus(resp.StatusCode) {
			return nil, fmt.Errorf("security rejection fetching desired state: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
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
	logReconciliation("edge-agent.reconcile", "desired_state_fetched", "success", map[string]any{
		"node_id":     state.NodeID,
		"version":     ds.Version,
		"workload_id": ds.WorkloadID,
		"type":        ds.Type,
	}, fmt.Sprintf("[fetched_state] version=%d", ds.Version))
	return &ds, nil
}

func applyDesiredState(state *PersistentState, ds DesiredState) error {
	if ds.hasTypedWorkload() {
		return reconcileTypedDesiredState(state, ds, defaultHandlers())
	}

	command, err := parseWorkloadCommand(ds.Payload)
	if err != nil {
		logExecution("edge-agent.execution", "workload_failure", "error", map[string]any{
			"node_id":      state.NodeID,
			"version":      ds.Version,
			"reason":       err.Error(),
			"retryable":    true,
			"retry_in_sec": currentLoopRetrySec(),
		}, fmt.Sprintf("[RECONCILE] apply failed version=%d reason=%s", ds.Version, err.Error()))
		return err
	}

	workloads, err := loadWorkloadState()
	if err != nil {
		logExecution("edge-agent.execution", "workload_failure", "error", map[string]any{
			"node_id":      state.NodeID,
			"workload_id":  command.WorkloadID,
			"action":       command.Action,
			"version":      ds.Version,
			"reason":       err.Error(),
			"retryable":    true,
			"retry_in_sec": currentLoopRetrySec(),
		}, fmt.Sprintf("[RECONCILE] apply failed version=%d reason=%s", ds.Version, err.Error()))
		return err
	}

	switch command.Action {
	case "start", "run", "apply":
		logExecution("edge-agent.execution", "workload_start", "started", map[string]any{
			"node_id":     state.NodeID,
			"workload_id": command.WorkloadID,
			"version":     ds.Version,
		}, fmt.Sprintf("[RECONCILE] applying version=%d payload=%s", ds.Version, ds.Payload))

		if command.Fail {
			err := errors.New("forced failure")
			logExecution("edge-agent.execution", "workload_failure", "error", map[string]any{
				"node_id":      state.NodeID,
				"workload_id":  command.WorkloadID,
				"action":       "start",
				"version":      ds.Version,
				"reason":       err.Error(),
				"retryable":    true,
				"retry_in_sec": currentLoopRetrySec(),
			}, fmt.Sprintf("[RECONCILE] apply failed version=%d reason=%s", ds.Version, err.Error()))
			return err
		}

		workloads = addWorkload(workloads, command.WorkloadID)
		if err := saveWorkloadState(workloads); err != nil {
			logExecution("edge-agent.execution", "workload_failure", "error", map[string]any{
				"node_id":      state.NodeID,
				"workload_id":  command.WorkloadID,
				"action":       "start",
				"version":      ds.Version,
				"reason":       err.Error(),
				"retryable":    true,
				"retry_in_sec": currentLoopRetrySec(),
			}, fmt.Sprintf("[RECONCILE] apply failed version=%d reason=%s", ds.Version, err.Error()))
			return err
		}

		logExecution("edge-agent.execution", "workload_start", "success", map[string]any{
			"node_id":     state.NodeID,
			"workload_id": command.WorkloadID,
			"version":     ds.Version,
		}, fmt.Sprintf("[RECONCILE] success version=%d", ds.Version))
	case "stop", "remove", "delete":
		logExecution("edge-agent.execution", "workload_stop", "started", map[string]any{
			"node_id":     state.NodeID,
			"workload_id": command.WorkloadID,
			"version":     ds.Version,
		}, fmt.Sprintf("[RECONCILE] applying version=%d payload=%s", ds.Version, ds.Payload))

		if !hasWorkload(workloads, command.WorkloadID) {
			err := errors.New("workload_not_found")
			logExecution("edge-agent.execution", "workload_failure", "error", map[string]any{
				"node_id":      state.NodeID,
				"workload_id":  command.WorkloadID,
				"action":       command.Action,
				"version":      ds.Version,
				"reason":       err.Error(),
				"retryable":    true,
				"retry_in_sec": currentLoopRetrySec(),
			}, fmt.Sprintf("[RECONCILE] apply failed version=%d reason=%s", ds.Version, err.Error()))
			return err
		}
		if command.Fail {
			err := errors.New("forced failure")
			logExecution("edge-agent.execution", "workload_failure", "error", map[string]any{
				"node_id":      state.NodeID,
				"workload_id":  command.WorkloadID,
				"action":       command.Action,
				"version":      ds.Version,
				"reason":       err.Error(),
				"retryable":    true,
				"retry_in_sec": currentLoopRetrySec(),
			}, fmt.Sprintf("[RECONCILE] apply failed version=%d reason=%s", ds.Version, err.Error()))
			return err
		}

		workloads = removeWorkload(workloads, command.WorkloadID)
		if err := saveWorkloadState(workloads); err != nil {
			logExecution("edge-agent.execution", "workload_failure", "error", map[string]any{
				"node_id":      state.NodeID,
				"workload_id":  command.WorkloadID,
				"action":       command.Action,
				"version":      ds.Version,
				"reason":       err.Error(),
				"retryable":    true,
				"retry_in_sec": currentLoopRetrySec(),
			}, fmt.Sprintf("[RECONCILE] apply failed version=%d reason=%s", ds.Version, err.Error()))
			return err
		}

		logExecution("edge-agent.execution", "workload_stop", "success", map[string]any{
			"node_id":     state.NodeID,
			"workload_id": command.WorkloadID,
			"version":     ds.Version,
		}, fmt.Sprintf("[RECONCILE] success version=%d", ds.Version))
	default:
		err := fmt.Errorf("unknown action %q", command.Action)
		logExecution("edge-agent.execution", "workload_failure", "error", map[string]any{
			"node_id":      state.NodeID,
			"workload_id":  command.WorkloadID,
			"action":       command.Action,
			"version":      ds.Version,
			"reason":       err.Error(),
			"retryable":    true,
			"retry_in_sec": currentLoopRetrySec(),
		}, fmt.Sprintf("[RECONCILE] apply failed version=%d reason=%s", ds.Version, err.Error()))
		return err
	}

	state.LastAppliedVersion = ds.Version
	savePersistentState(*state)
	return nil
}

func reconcile(state *PersistentState) {
	ds, err := fetchDesiredState(*state)
	if err != nil {
		logSystem("edge-agent.reconcile", "desired_state_fetch_failed", "error", map[string]any{
			"node_id":      state.NodeID,
			"reason":       err.Error(),
			"retryable":    true,
			"retry_in_sec": currentLoopRetrySec(),
		}, fmt.Sprintf("fetch error: %v", err))
		return
	}

	if ds == nil {
		stopObservedWorkloads(*state, defaultHandlers())
		return
	}
	if !verifyDesiredStateSignature(*state, *ds) {
		logSystem("edge-agent.security", "desired_state_signature_rejected", "rejected", map[string]any{
			"node_id":      state.NodeID,
			"version":      ds.Version,
			"reason":       "invalid signature",
			"retryable":    true,
			"retry_in_sec": currentLoopRetrySec(),
		}, fmt.Sprintf("[SECURITY][REJECT] desired-state invalid signature version=%d", ds.Version))
		logReconciliation("edge-agent.reconcile", "reconciliation_decision", "invalid-signature", map[string]any{
			"node_id": state.NodeID,
			"version": ds.Version,
		}, fmt.Sprintf("[RECONCILE] invalid signature version=%d", ds.Version))
		return
	}

	if ds.hasTypedWorkload() {
		if err := reconcileTypedDesiredState(state, *ds, defaultHandlers()); err != nil {
			logReconciliation("edge-agent.reconcile", "reconciliation_decision", "retry-needed", map[string]any{
				"node_id":      state.NodeID,
				"version":      ds.Version,
				"workload_id":  ds.WorkloadID,
				"type":         ds.Type,
				"reason":       err.Error(),
				"retryable":    true,
				"retry_in_sec": currentLoopRetrySec(),
			}, fmt.Sprintf("[RECONCILE] typed apply failed version=%d workload=%s reason=%s", ds.Version, ds.WorkloadID, err.Error()))
		}
		return
	}

	if ds.Version < state.LastAppliedVersion {
		logReconciliation("edge-agent.reconcile", "reconciliation_decision", "stale", map[string]any{
			"node_id": state.NodeID,
			"remote":  ds.Version,
			"local":   state.LastAppliedVersion,
		}, fmt.Sprintf("[RECONCILE] compare remote=%d local=%d result=stale", ds.Version, state.LastAppliedVersion))
		return
	}
	if ds.Version == state.LastAppliedVersion {
		logReconciliation("edge-agent.reconcile", "reconciliation_decision", "in-sync", map[string]any{
			"node_id": state.NodeID,
			"remote":  ds.Version,
			"local":   state.LastAppliedVersion,
		}, fmt.Sprintf("[RECONCILE] compare remote=%d local=%d result=in-sync", ds.Version, state.LastAppliedVersion))
		return
	}

	logReconciliation("edge-agent.reconcile", "reconciliation_decision", "drift", map[string]any{
		"node_id": state.NodeID,
		"remote":  ds.Version,
		"local":   state.LastAppliedVersion,
	}, fmt.Sprintf("[RECONCILE] compare remote=%d local=%d result=drift", ds.Version, state.LastAppliedVersion))

	_ = applyDesiredState(state, *ds)
}

func initializeLocalState(nodeDir string) PersistentState {
	_ = os.MkdirAll(nodeDir, 0755)
	stateFile = filepath.Join(nodeDir, stateFileName)

	state := loadPersistentState()
	if state.NodeID == "" {
		return registerNode()
	}
	if state.NodeSecret == "" {
		logSystem("edge-agent.state", "node_identity_restore_failed", "missing-secret", map[string]any{
			"node_id": state.NodeID,
		}, fmt.Sprintf("[STATE] missing node secret for node=%s; registering again", state.NodeID))
		return registerNode()
	}
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
	logSystem("edge-agent", "startup", "success", map[string]any{
		"node_id":       state.NodeID,
		"control_plane": controlPlaneBase,
		"node_dir":      nodeDir,
	}, "")

	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(shutdownSignals)

	runOnce(&state)
	heartbeatEvery := time.Duration(getenvInt("EDGE_HEARTBEAT_SEC", 10)) * time.Second
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()

	for {
		select {
		case sig := <-shutdownSignals:
			logSystem("edge-agent", "shutdown", "success", map[string]any{
				"node_id": state.NodeID,
				"signal":  sig.String(),
			}, "")
			return
		case <-ticker.C:
			runOnce(&state)
		}
	}
}
