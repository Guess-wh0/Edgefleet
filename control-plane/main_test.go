package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withTempWorkingDir(t *testing.T) {
	t.Helper()

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}

	t.Cleanup(func() {
		if db != nil {
			_ = db.Close()
			db = nil
		}
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restore workdir: %v", err)
		}
	})
}

func testMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/register", registrationHandler)
	mux.HandleFunc("/heartbeat", heartbeatHandler)
	mux.HandleFunc("/desired-state/{nodeID}", getDesiredState)
	mux.HandleFunc("/health", getHealthDetail)
	mux.HandleFunc("/nodes", listNodes)
	mux.HandleFunc("/debug/set-desired", setDesiredState)
	return mux
}

func performRequest(t *testing.T, mux *http.ServeMux, method, path string, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestControlPlaneRestartPreservesDesiredStateAndHeartbeat(t *testing.T) {
	withTempWorkingDir(t)

	if err := initDB(); err != nil {
		t.Fatalf("init db first start: %v", err)
	}
	mux := testMux()

	registerResp := performRequest(t, mux, http.MethodPost, "/register", "", map[string]string{
		"X-Node-Hostname": "restart-drill-node",
		"X-Node-Arch":     "amd64",
	})
	if registerResp.Code != http.StatusOK {
		t.Fatalf("register status = %d, want %d", registerResp.Code, http.StatusOK)
	}
	var registration RegistrationResponse
	if err := json.Unmarshal(registerResp.Body.Bytes(), &registration); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if registration.NodeID == "" || registration.NodeSecret == "" {
		t.Fatal("register returned incomplete node identity")
	}
	nodeID := registration.NodeID

	desiredPayload := `{"version":3,"payload":"restart-drill"}`
	setDesiredResp := performRequest(
		t,
		mux,
		http.MethodPost,
		"/debug/set-desired?nodeID="+nodeID+"&version=3",
		desiredPayload,
		nil,
	)
	if setDesiredResp.Code != http.StatusOK {
		t.Fatalf("set desired status = %d, want %d", setDesiredResp.Code, http.StatusOK)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close db before restart: %v", err)
	}
	db = nil

	if err := initDB(); err != nil {
		t.Fatalf("init db after restart: %v", err)
	}
	mux = testMux()

	heartbeatResp := performRequest(t, mux, http.MethodPost, "/heartbeat", "", map[string]string{
		"X-Node-ID":    nodeID,
		"X-Node-Token": registration.NodeSecret,
	})
	if heartbeatResp.Code != http.StatusOK {
		t.Fatalf("heartbeat after restart status = %d, want %d", heartbeatResp.Code, http.StatusOK)
	}
	if strings.TrimSpace(heartbeatResp.Body.String()) != "ack" {
		t.Fatalf("heartbeat after restart body = %q, want %q", heartbeatResp.Body.String(), "ack")
	}

	desiredResp := performRequest(t, mux, http.MethodGet, "/desired-state/"+nodeID, "", map[string]string{
		"X-Node-ID":    nodeID,
		"X-Node-Token": registration.NodeSecret,
	})
	if desiredResp.Code != http.StatusOK {
		t.Fatalf("desired state fetch status = %d, want %d", desiredResp.Code, http.StatusOK)
	}
	if strings.TrimSpace(desiredResp.Body.String()) != desiredPayload {
		t.Fatalf("desired state after restart = %q, want %q", desiredResp.Body.String(), desiredPayload)
	}

	var status string
	err := db.QueryRow(`SELECT status FROM nodes WHERE node_id = ?`, nodeID).Scan(&status)
	if err != nil {
		t.Fatalf("query node after restart: %v", err)
	}
	if status != "active" {
		t.Fatalf("node status after restart = %q, want %q", status, "active")
	}

	if _, err := os.Stat(filepath.Join(".", "edgefleet.db")); err != nil {
		t.Fatalf("expected sqlite db file to exist after restart drill: %v", err)
	}
}

func TestDesiredStateRemainsAvailableAcrossDBReopen(t *testing.T) {
	withTempWorkingDir(t)

	if err := initDB(); err != nil {
		t.Fatalf("init db first open: %v", err)
	}

	const nodeID = "node-reopen"
	const desiredPayload = `{"version":9,"payload":"persisted"}`

	if _, err := db.Exec(
		`INSERT INTO nodes (node_id, last_heartbeat, status, hostname, arch)
		VALUES (?, CURRENT_TIMESTAMP, 'registered', 'node-reopen', 'amd64')`,
		nodeID,
	); err != nil {
		t.Fatalf("insert node: %v", err)
	}

	if err := upsertDesiredState(nodeID, 9, desiredPayload); err != nil {
		t.Fatalf("upsert desired state: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	db = nil

	if err := initDB(); err != nil {
		t.Fatalf("reopen db: %v", err)
	}

	var version int
	var payload string
	err := db.QueryRow(`SELECT version, payload FROM desired_state WHERE node_id = ?`, nodeID).Scan(&version, &payload)
	if err != nil {
		t.Fatalf("query desired state after reopen: %v", err)
	}

	if version != 9 {
		t.Fatalf("version after reopen = %d, want %d", version, 9)
	}
	if payload != desiredPayload {
		t.Fatalf("payload after reopen = %q, want %q", payload, desiredPayload)
	}
}

func TestGetDesiredStateAfterRestartReturnsBody(t *testing.T) {
	withTempWorkingDir(t)

	if err := initDB(); err != nil {
		t.Fatalf("init db first open: %v", err)
	}

	const nodeID = "node-handler"
	const desiredPayload = `{"version":4,"payload":"handler-check"}`

	if err := upsertDesiredState(nodeID, 4, desiredPayload); err != nil {
		t.Fatalf("upsert desired state: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	db = nil

	if err := initDB(); err != nil {
		t.Fatalf("reopen db: %v", err)
	}

	mux := testMux()
	resp := performRequest(t, mux, http.MethodGet, "/desired-state/"+nodeID, "", map[string]string{
		"X-Node-ID":    nodeID,
		"X-Node-Token": "wrong-token",
	})
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("desired state status = %d, want %d", resp.Code, http.StatusUnauthorized)
	}
}

func TestHeartbeatRejectsMissingNodeToken(t *testing.T) {
	withTempWorkingDir(t)

	if err := initDB(); err != nil {
		t.Fatalf("init db: %v", err)
	}
	mux := testMux()

	registerResp := performRequest(t, mux, http.MethodPost, "/register", "", map[string]string{
		"X-Node-Hostname": "auth-node",
		"X-Node-Arch":     "amd64",
	})
	if registerResp.Code != http.StatusOK {
		t.Fatalf("register status = %d, want %d", registerResp.Code, http.StatusOK)
	}

	var registration RegistrationResponse
	if err := json.Unmarshal(registerResp.Body.Bytes(), &registration); err != nil {
		t.Fatalf("decode register response: %v", err)
	}

	resp := performRequest(t, mux, http.MethodPost, "/heartbeat", "", map[string]string{
		"X-Node-ID": registration.NodeID,
	})
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("heartbeat status = %d, want %d", resp.Code, http.StatusUnauthorized)
	}
}

func TestDesiredStateAcceptsValidNodeToken(t *testing.T) {
	withTempWorkingDir(t)

	if err := initDB(); err != nil {
		t.Fatalf("init db first open: %v", err)
	}

	registerMux := testMux()
	registerResp := performRequest(t, registerMux, http.MethodPost, "/register", "", map[string]string{
		"X-Node-Hostname": "node-handler",
		"X-Node-Arch":     "amd64",
	})
	if registerResp.Code != http.StatusOK {
		t.Fatalf("register status = %d, want %d", registerResp.Code, http.StatusOK)
	}

	var registration RegistrationResponse
	if err := json.Unmarshal(registerResp.Body.Bytes(), &registration); err != nil {
		t.Fatalf("decode register response: %v", err)
	}

	const desiredPayload = `{"version":4,"payload":"handler-check"}`

	if err := upsertDesiredState(registration.NodeID, 4, desiredPayload); err != nil {
		t.Fatalf("upsert desired state: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	db = nil

	if err := initDB(); err != nil {
		t.Fatalf("reopen db: %v", err)
	}

	mux := testMux()
	resp := performRequest(t, mux, http.MethodGet, "/desired-state/"+registration.NodeID, "", map[string]string{
		"X-Node-ID":    registration.NodeID,
		"X-Node-Token": registration.NodeSecret,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("desired state status = %d, want %d", resp.Code, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Result().Body)
	if err != nil {
		t.Fatalf("read handler body: %v", err)
	}
	if strings.TrimSpace(string(body)) != desiredPayload {
		t.Fatalf("desired state body = %q, want %q", string(body), desiredPayload)
	}
}
