package storageprov

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"vulos/backend/services/auth"
)

const testAdminID = "admin-test-user"

// storageprovTestHome creates a temp directory as a fake home.
func storageprovTestHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// storageprovTestAuth creates an auth.Store in a temp directory with a single
// admin user pre-registered (H2 fix: RegisterHandlers now requires authStore).
func storageprovTestAuth(t *testing.T) *auth.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := auth.NewStore(dir)
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	// Seed an admin profile — no need to register a full user for header-gated
	// auth; SetProfile is enough for GetProfile to return the role.
	store.SetProfile(&auth.Profile{
		UserID: testAdminID,
		Role:   auth.RoleAdmin,
	})
	return store
}

// adminRequest wraps httptest.NewRequest and pre-sets the admin user header.
func adminRequest(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testAdminID)
	return req
}

// TestStorageprovForbiddenWithoutAdmin verifies the H2 admin gate.
func TestStorageprovForbiddenWithoutAdmin(t *testing.T) {
	home := storageprovTestHome(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, home, storageprovTestAuth(t))

	body, _ := json.Marshal(storageprovRequest{Enable: false})
	req := httptest.NewRequest("POST", "/api/setup/storage", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No X-User-ID → not admin
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", w.Code)
	}
}

// TestStorageprovDisabled verifies that enable:false returns 200 with status "disabled".
func TestStorageprovDisabled(t *testing.T) {
	home := storageprovTestHome(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, home, storageprovTestAuth(t))

	body, _ := json.Marshal(storageprovRequest{Enable: false})
	req := adminRequest("POST", "/api/setup/storage", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
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
	RegisterHandlers(mux, home, storageprovTestAuth(t))

	body, _ := json.Marshal(storageprovRequest{
		Enable:     true,
		SizeGB:     10,
		Password:   "hunter2",
		Passphrase: "super-secret-passphrase",
	})
	req := adminRequest("POST", "/api/setup/storage", body)
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

// TestStorageprovInvalidBody verifies a 400 is returned on malformed JSON
// when an admin user is authenticated (auth passes, body parsing fails).
func TestStorageprovInvalidBody(t *testing.T) {
	home := storageprovTestHome(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, home, storageprovTestAuth(t))

	req := httptest.NewRequest("POST", "/api/setup/storage", bytes.NewBufferString("not-json"))
	req.Header.Set("X-User-ID", testAdminID)
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
