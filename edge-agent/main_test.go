package main

import (
	"os"
	"path/filepath"
	"testing"
)

func withStateFile(t *testing.T, path string) {
	t.Helper()

	previous := stateFile
	stateFile = path
	t.Cleanup(func() {
		stateFile = previous
	})
}

func TestSaveAndLoadPersistentState(t *testing.T) {
	tempDir := t.TempDir()
	withStateFile(t, filepath.Join(tempDir, stateFileName))

	expected := PersistentState{
		NodeID:             "node-123",
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
