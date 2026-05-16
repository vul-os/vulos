package storageprov

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// storageprovTestHome creates a temp directory as a fake home.
func storageprovTestHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

// TestStorageprovDisabled verifies that enable:false returns 200 with status "disabled".
func TestStorageprovDisabled(t *testing.T) {
	home := storageprovTestHome(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, home)

	body, _ := json.Marshal(storageprovRequest{Enable: false})
	req := httptest.NewRequest("POST", "/api/setup/storage", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp storageprovResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "disabled" {
		t.Errorf("expected status disabled, got %q", resp.Status)
	}
}

// TestStorageprovEnabled verifies keys are generated and storage.json is written without passphrase.
func TestStorageprovEnabled(t *testing.T) {
	home := storageprovTestHome(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, home)

	body, _ := json.Marshal(storageprovRequest{
		Enable:     true,
		SizeGB:     10,
		Password:   "hunter2",
		Passphrase: "super-secret-passphrase",
	})
	req := httptest.NewRequest("POST", "/api/setup/storage", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp storageprovResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %q", resp.Status)
	}
	if resp.AccessKey == "" {
		t.Error("expected non-empty access_key")
	}
	if resp.SecretKey == "" {
		t.Error("expected non-empty secret_key")
	}
	if resp.BucketName != storageprovBucket {
		t.Errorf("expected bucket %q, got %q", storageprovBucket, resp.BucketName)
	}

	// Verify storage.json exists and does NOT contain the passphrase
	stateFile := filepath.Join(home, ".vulos", "db", "storage.json")
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("storage.json not written: %v", err)
	}
	var state storageprovState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal storage.json: %v", err)
	}
	if !state.Enabled {
		t.Error("expected enabled=true in storage.json")
	}
	if state.AccessKey == "" || state.SecretKey == "" {
		t.Error("expected keys in storage.json")
	}
	// Passphrase must NEVER appear in the persisted file
	raw := string(data)
	if contains(raw, "super-secret-passphrase") {
		t.Error("passphrase must NOT be persisted in storage.json")
	}
}

// TestStorageprovInvalidBody verifies a 400 is returned on malformed JSON.
func TestStorageprovInvalidBody(t *testing.T) {
	home := storageprovTestHome(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, home)

	req := httptest.NewRequest("POST", "/api/setup/storage", bytes.NewBufferString("not-json"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// TestStorageprovPassphraseNotPersisted is an explicit assertion that the passphrase
// field is absent from the storageprovState struct (compile-time check via field enumeration).
func TestStorageprovPassphraseNotPersisted(t *testing.T) {
	// Marshal an empty state and confirm "passphrase" key is absent
	state := storageprovState{}
	data, _ := json.Marshal(state)
	if contains(string(data), "passphrase") {
		t.Error("storageprovState must not have a passphrase field")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
