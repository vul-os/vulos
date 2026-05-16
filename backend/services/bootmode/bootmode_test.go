package bootmode

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// setupHome creates a temporary home directory for testing.
func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	return home
}

// makeDBDir creates ~/.vulos/db under home and returns the db path.
func makeDBDir(t *testing.T, home string) string {
	t.Helper()
	dbDir := filepath.Join(home, ".vulos", "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return dbDir
}

// writeJSON writes v as JSON to path.
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// TestBootmodeResultPrefix verifies the exported type names start with "bootmode" / "Result".
// The types are in package bootmode so this compiles only if Result is exported.
func TestBootmodeResultPrefix(t *testing.T) {
	var r Result
	r.Mode = "normal"
	r.SyncState = ""
	if r.Mode != "normal" {
		t.Fatalf("expected normal, got %s", r.Mode)
	}
}

// AC: fresh (no db/instance.json) → setup
func TestDetect_FreshNoDBDir(t *testing.T) {
	home := setupHome(t)
	// db dir does not exist
	result, err := Detect(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Mode != "setup" {
		t.Errorf("expected setup, got %q", result.Mode)
	}
}

// AC: fresh (db dir exists but no instance.json) → setup
func TestDetect_DBDirNoInstanceJSON(t *testing.T) {
	home := setupHome(t)
	makeDBDir(t, home)
	// no instance.json

	result, err := Detect(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Mode != "setup" {
		t.Errorf("expected setup, got %q", result.Mode)
	}
}

// AC: sync-state.json syncing → sync
func TestDetect_SyncStateSyncing(t *testing.T) {
	home := setupHome(t)
	dbDir := makeDBDir(t, home)

	// write instance.json (present)
	writeJSON(t, filepath.Join(dbDir, instanceFile), map[string]string{"id": "test-id"})

	// write sync-state.json with status=syncing
	writeJSON(t, filepath.Join(dbDir, syncStateFile), map[string]string{"status": "syncing"})

	result, err := Detect(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Mode != "sync" {
		t.Errorf("expected sync, got %q", result.Mode)
	}
	if result.SyncState != "syncing" {
		t.Errorf("expected SyncState=syncing, got %q", result.SyncState)
	}
}

// AC: db+instance.json, no sync-state → normal
func TestDetect_Normal(t *testing.T) {
	home := setupHome(t)
	dbDir := makeDBDir(t, home)

	// write instance.json
	writeJSON(t, filepath.Join(dbDir, instanceFile), map[string]string{"id": "test-id"})
	// no sync-state.json

	result, err := Detect(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Mode != "normal" {
		t.Errorf("expected normal, got %q", result.Mode)
	}
}

// Extra: sync-state.json with status != "syncing" → normal
func TestDetect_SyncStateComplete(t *testing.T) {
	home := setupHome(t)
	dbDir := makeDBDir(t, home)

	writeJSON(t, filepath.Join(dbDir, instanceFile), map[string]string{"id": "test-id"})
	writeJSON(t, filepath.Join(dbDir, syncStateFile), map[string]string{"status": "complete"})

	result, err := Detect(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Mode != "normal" {
		t.Errorf("expected normal (complete sync = done), got %q", result.Mode)
	}
}

// Extra: RegisterHandlers smoke test — ensure it does not panic
func TestRegisterHandlers_NoPanic(t *testing.T) {
	home := setupHome(t)
	mux := &http.ServeMux{}
	// should not panic
	RegisterHandlers(mux, home)
}
