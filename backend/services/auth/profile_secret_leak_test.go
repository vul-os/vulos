package auth

// profile_secret_leak_test.go — regression for the profile secret-leak fix.
//
// A profile carries two secrets that must NEVER appear in an HTTP response:
//   - PinHash: an unstretched SHA-256 over a short numeric lock-screen PIN with
//     the salt disclosed inline, so leaking it effectively leaks the PIN.
//   - AIAPIKey: the user's model API key (masked, never returned cleartext).
//
// These tests assert every profile-returning handler scrubs both, and that
// scrubbing a response never mutates the live in-memory profile.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func seedProfileStore(t *testing.T) (*Store, *User, *Session, string) {
	t.Helper()
	store := &Store{
		users:    make(map[string]*User),
		sessions: make(map[string]*Session),
		profiles: make(map[string]*Profile),
		secret:   []byte("test-secret-leak"),
	}
	u := &User{ID: "uid-leak", Username: "alice", Email: "alice@test.local"}
	store.users[u.ID] = u
	p := DefaultProfile(u.ID, "Alice")
	p.Role = RoleAdmin // so admin-gated listing is reachable
	p.AIAPIKey = "sk-super-secret-key-value"
	store.profiles[u.ID] = p
	if err := store.SetPIN(u.ID, "1234"); err != nil {
		t.Fatalf("SetPIN: %v", err)
	}
	pinHash := store.profiles[u.ID].PinHash
	if pinHash == "" {
		t.Fatal("expected a PinHash to be set")
	}
	sess := store.CreateSession(u, "test-device")
	return store, u, sess, pinHash
}

// assertNoSecrets fails if the response body exposes the PIN hash, the
// "pin_hash" key, or the cleartext API key.
func assertNoSecrets(t *testing.T, where, body, pinHash string) {
	t.Helper()
	if strings.Contains(body, pinHash) {
		t.Fatalf("%s leaked the PIN hash: %s", where, body)
	}
	if strings.Contains(body, "pin_hash") {
		t.Fatalf("%s exposed the pin_hash field: %s", where, body)
	}
	if strings.Contains(body, "sk-super-secret-key-value") {
		t.Fatalf("%s leaked the cleartext AI API key: %s", where, body)
	}
}

func TestHandleMe_ScrubsPinHashAndKey(t *testing.T) {
	store, _, sess, pinHash := seedProfileStore(t)
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: sess.Token})
	w := httptest.NewRecorder()
	h.handleMe(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("handleMe status = %d", w.Code)
	}
	assertNoSecrets(t, "handleMe", w.Body.String(), pinHash)
}

func TestHandleGetProfile_ScrubsPinHash(t *testing.T) {
	store, u, _, pinHash := seedProfileStore(t)
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/profiles/"+u.ID, nil)
	req.Header.Set("X-User-ID", u.ID)
	req.SetPathValue("userId", u.ID)
	w := httptest.NewRecorder()
	h.handleGetProfile(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("handleGetProfile status = %d: %s", w.Code, w.Body.String())
	}
	assertNoSecrets(t, "handleGetProfile", w.Body.String(), pinHash)

	// The live stored profile must be untouched (scrubbing works on a copy).
	if store.profiles[u.ID].PinHash != pinHash {
		t.Fatal("handleGetProfile mutated the stored PinHash")
	}
	if store.profiles[u.ID].AIAPIKey != "sk-super-secret-key-value" {
		t.Fatal("handleGetProfile mutated the stored AIAPIKey")
	}
}

func TestHandleListProfiles_ScrubsPinHashForAdmin(t *testing.T) {
	store, u, _, pinHash := seedProfileStore(t)
	// Add a SECOND user whose PIN hash must not leak to the admin lister.
	other := &User{ID: "uid-bob", Username: "bob", Email: "bob@test.local"}
	store.users[other.ID] = other
	op := DefaultProfile(other.ID, "Bob")
	store.profiles[other.ID] = op
	if err := store.SetPIN(other.ID, "9999"); err != nil {
		t.Fatalf("SetPIN bob: %v", err)
	}
	bobHash := store.profiles[other.ID].PinHash

	h := NewHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/profiles", nil)
	req.Header.Set("X-User-ID", u.ID) // u is admin
	w := httptest.NewRecorder()
	h.handleListProfiles(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("handleListProfiles status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	assertNoSecrets(t, "handleListProfiles", body, pinHash)
	if strings.Contains(body, bobHash) {
		t.Fatalf("handleListProfiles leaked another user's PIN hash: %s", body)
	}

	// Sanity: the list actually contained the profiles (not an empty array).
	var profs []Profile
	if err := json.Unmarshal(w.Body.Bytes(), &profs); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(profs) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profs))
	}
}

func TestHandleUpdateProfile_ScrubsPinHashInResponse(t *testing.T) {
	store, u, _, pinHash := seedProfileStore(t)
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodPut, "/api/profiles/"+u.ID, strings.NewReader(`{"display_name":"Alice2"}`))
	req.Header.Set("X-User-ID", u.ID)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("userId", u.ID)
	w := httptest.NewRecorder()
	h.handleUpdateProfile(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("handleUpdateProfile status = %d: %s", w.Code, w.Body.String())
	}
	assertNoSecrets(t, "handleUpdateProfile", w.Body.String(), pinHash)

	// The update itself must still have persisted, with the PIN intact.
	if store.profiles[u.ID].DisplayName != "Alice2" {
		t.Fatal("update did not persist DisplayName")
	}
	if store.profiles[u.ID].PinHash != pinHash {
		t.Fatal("handleUpdateProfile clobbered the stored PinHash")
	}
}
