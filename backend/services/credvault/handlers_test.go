package credvault

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// newTestHandler returns a Handler wired into a fresh ServeMux.
func newTestHandler(t *testing.T) (*Handler, *http.ServeMux) {
	t.Helper()
	base := t.TempDir()
	h := NewHandler(func(uid string) string {
		return filepath.Join(base, uid)
	})
	mux := http.NewServeMux()
	h.RegisterHandlers(mux)
	return h, mux
}

// do is a helper that fires a request against mux and returns the response.
func do(t *testing.T, mux http.Handler, method, path, userID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

const (
	testUser = "user-abc"
	testPass = "my-master-password"
)

// ---- 401 when no X-User-ID -----------------------------------------------

func TestHandlerRequiresUserID(t *testing.T) {
	_, mux := newTestHandler(t)
	paths := []struct{ method, path string }{
		{"POST", "/api/auth/vault/unlock"},
		{"POST", "/api/auth/vault/lock"},
		{"GET", "/api/auth/vault/entries"},
		{"POST", "/api/auth/vault/entry"},
		{"POST", "/api/auth/vault/generate"},
	}
	for _, tc := range paths {
		rr := do(t, mux, tc.method, tc.path, "", map[string]string{"password": "x"})
		if rr.Code != 401 {
			t.Errorf("%s %s: expected 401, got %d", tc.method, tc.path, rr.Code)
		}
	}
}

// ---- unlock / lock -------------------------------------------------------

func TestUnlockLock(t *testing.T) {
	_, mux := newTestHandler(t)

	// Unlock.
	rr := do(t, mux, "POST", "/api/auth/vault/unlock", testUser, map[string]string{"password": testPass})
	if rr.Code != 200 {
		t.Fatalf("unlock: expected 200, got %d — %s", rr.Code, rr.Body.String())
	}

	// Lock.
	rr = do(t, mux, "POST", "/api/auth/vault/lock", testUser, nil)
	if rr.Code != 200 {
		t.Fatalf("lock: expected 200, got %d — %s", rr.Code, rr.Body.String())
	}
}

func TestUnlockWrongPassword(t *testing.T) {
	_, mux := newTestHandler(t)

	// First unlock to initialise the vault.
	do(t, mux, "POST", "/api/auth/vault/unlock", testUser, map[string]string{"password": testPass})
	// Create an entry so vault.enc exists.
	do(t, mux, "POST", "/api/auth/vault/entry", testUser, map[string]string{
		"url": "https://x.example", "username": "u", "password": "p",
	})
	// Lock.
	do(t, mux, "POST", "/api/auth/vault/lock", testUser, nil)

	// Try wrong password.
	rr := do(t, mux, "POST", "/api/auth/vault/unlock", testUser, map[string]string{"password": "wrong"})
	if rr.Code != 401 {
		t.Fatalf("expected 401 for wrong password, got %d", rr.Code)
	}
}

// ---- 423 when locked -----------------------------------------------------

func TestLockedEndpointsReturn423(t *testing.T) {
	_, mux := newTestHandler(t)
	// Vault starts locked (never unlocked).

	cases := []struct{ method, path string }{
		{"GET", "/api/auth/vault/entries"},
		{"GET", "/api/auth/vault/entry/nonexistent"},
		{"POST", "/api/auth/vault/entry"},
		{"PUT", "/api/auth/vault/entry/nonexistent"},
		{"DELETE", "/api/auth/vault/entry/nonexistent"},
	}
	for _, tc := range cases {
		rr := do(t, mux, tc.method, tc.path, testUser, map[string]string{"url": "x"})
		if rr.Code != 423 {
			t.Errorf("%s %s: expected 423 (locked), got %d — %s", tc.method, tc.path, rr.Code, rr.Body.String())
		}
	}
}

// ---- list entries returns metadata only (no passwords) -------------------

func TestListEntriesMetadataOnly(t *testing.T) {
	_, mux := newTestHandler(t)

	do(t, mux, "POST", "/api/auth/vault/unlock", testUser, map[string]string{"password": testPass})
	do(t, mux, "POST", "/api/auth/vault/entry", testUser, map[string]any{
		"url": "https://bank.example", "username": "alice", "password": "s3cr3t",
	})

	rr := do(t, mux, "GET", "/api/auth/vault/entries", testUser, nil)
	if rr.Code != 200 {
		t.Fatalf("list: expected 200, got %d — %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if strings.Contains(body, "s3cr3t") {
		t.Fatalf("password leaked in list response: %s", body)
	}

	var metas []entryMeta
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&metas); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(metas))
	}
	if metas[0].Username != "alice" {
		t.Fatalf("username mismatch: %q", metas[0].Username)
	}
}

// ---- full detail requires vault unlocked ---------------------------------

func TestGetEntryRequiresUnlock(t *testing.T) {
	_, mux := newTestHandler(t)

	// Unlock, add entry, note ID.
	do(t, mux, "POST", "/api/auth/vault/unlock", testUser, map[string]string{"password": testPass})
	rr := do(t, mux, "POST", "/api/auth/vault/entry", testUser, map[string]any{
		"url": "https://secret.example", "username": "bob", "password": "topsecret",
	})
	var created Entry
	json.NewDecoder(rr.Body).Decode(&created)

	// Lock vault.
	do(t, mux, "POST", "/api/auth/vault/lock", testUser, nil)

	// Entry detail must return 423.
	rr2 := do(t, mux, "GET", "/api/auth/vault/entry/"+created.ID, testUser, nil)
	if rr2.Code != 423 {
		t.Fatalf("expected 423 for locked get-entry, got %d — %s", rr2.Code, rr2.Body.String())
	}

	// Unlock and verify we get the full entry with password.
	do(t, mux, "POST", "/api/auth/vault/unlock", testUser, map[string]string{"password": testPass})
	rr3 := do(t, mux, "GET", "/api/auth/vault/entry/"+created.ID, testUser, nil)
	if rr3.Code != 200 {
		t.Fatalf("expected 200 after unlock, got %d", rr3.Code)
	}
	var full Entry
	json.NewDecoder(rr3.Body).Decode(&full)
	if full.Password != "topsecret" {
		t.Fatalf("password not returned in detail: %q", full.Password)
	}
}

// ---- CRUD via HTTP -------------------------------------------------------

func TestHTTPCRUD(t *testing.T) {
	_, mux := newTestHandler(t)
	do(t, mux, "POST", "/api/auth/vault/unlock", testUser, map[string]string{"password": testPass})

	// Create.
	rr := do(t, mux, "POST", "/api/auth/vault/entry", testUser, map[string]any{
		"url": "https://crud.example", "username": "user1", "password": "pass1",
	})
	if rr.Code != 201 {
		t.Fatalf("create: expected 201, got %d — %s", rr.Code, rr.Body.String())
	}
	var created Entry
	json.NewDecoder(rr.Body).Decode(&created)
	if created.ID == "" {
		t.Fatal("create: returned empty ID")
	}

	// Update.
	rr = do(t, mux, "PUT", "/api/auth/vault/entry/"+created.ID, testUser, map[string]any{
		"url": "https://crud.example", "username": "user1", "password": "newpass",
	})
	if rr.Code != 200 {
		t.Fatalf("update: expected 200, got %d — %s", rr.Code, rr.Body.String())
	}

	// Get full entry — password should be updated.
	rr = do(t, mux, "GET", "/api/auth/vault/entry/"+created.ID, testUser, nil)
	var updated Entry
	json.NewDecoder(rr.Body).Decode(&updated)
	if updated.Password != "newpass" {
		t.Fatalf("update not reflected: %q", updated.Password)
	}

	// Delete.
	rr = do(t, mux, "DELETE", "/api/auth/vault/entry/"+created.ID, testUser, nil)
	if rr.Code != 200 {
		t.Fatalf("delete: expected 200, got %d — %s", rr.Code, rr.Body.String())
	}

	// Get after delete = 404.
	rr = do(t, mux, "GET", "/api/auth/vault/entry/"+created.ID, testUser, nil)
	if rr.Code != 404 {
		t.Fatalf("after delete: expected 404, got %d", rr.Code)
	}
}

// ---- generate ------------------------------------------------------------

func TestHandlerGenerate(t *testing.T) {
	_, mux := newTestHandler(t)

	// Generate does not require vault unlock.
	rr := do(t, mux, "POST", "/api/auth/vault/generate", testUser, map[string]any{
		"mode": "random",
		"random": map[string]any{
			"length": 16,
			"upper":  true,
			"lower":  true,
			"digits": true,
		},
	})
	if rr.Code != 200 {
		t.Fatalf("generate: expected 200, got %d — %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if len(resp["password"]) != 16 {
		t.Fatalf("expected 16-char password, got %q", resp["password"])
	}
}

func TestHandlerGeneratePassphrase(t *testing.T) {
	_, mux := newTestHandler(t)

	rr := do(t, mux, "POST", "/api/auth/vault/generate", testUser, map[string]any{
		"mode": "passphrase",
		"passphrase": map[string]any{
			"word_count": 4,
			"separator":  "-",
		},
	})
	if rr.Code != 200 {
		t.Fatalf("generate passphrase: expected 200, got %d — %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	parts := strings.Split(resp["password"], "-")
	if len(parts) != 4 {
		t.Fatalf("expected 4 words, got %d in %q", len(parts), resp["password"])
	}
}

// ---- per-user isolation --------------------------------------------------

func TestPerUserIsolation(t *testing.T) {
	_, mux := newTestHandler(t)
	userA := "user-a"
	userB := "user-b"

	// Unlock both users with different passwords.
	do(t, mux, "POST", "/api/auth/vault/unlock", userA, map[string]string{"password": "passA"})
	do(t, mux, "POST", "/api/auth/vault/unlock", userB, map[string]string{"password": "passB"})

	// Add entry as user A.
	do(t, mux, "POST", "/api/auth/vault/entry", userA, map[string]any{
		"url": "https://only-a.example", "username": "alice", "password": "secretA",
	})

	// User B should see no entries.
	rr := do(t, mux, "GET", "/api/auth/vault/entries", userB, nil)
	var metas []entryMeta
	json.NewDecoder(rr.Body).Decode(&metas)
	if len(metas) != 0 {
		t.Fatalf("user B sees user A entries: %+v", metas)
	}
}
