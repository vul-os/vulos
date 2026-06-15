package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- D71 regression guard: these five named tests MUST pass alongside the
// new SQLite tests. They are the explicit acceptance gate. ---

// TestRegisterLoginRoundTrip exercises the core register -> login flow and
// confirms password verification still works through the SQLite write-through.
func TestRegisterLoginRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	u, err := s.Register("alice", "hunter2-xtra!", "Alice")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if u.Username != "alice" {
		t.Fatalf("username = %q, want alice", u.Username)
	}

	got, err := s.Login("alice", "hunter2-xtra!")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("login returned id %q, want %q", got.ID, u.ID)
	}

	if _, err := s.Login("alice", "wrong"); err == nil {
		t.Fatalf("Login with wrong password should fail")
	}
}

// TestUsersPersistAcrossRestart confirms users written by one Store instance
// are visible to a fresh Store opened on the same data dir (durable SQLite).
func TestUsersPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #1: %v", err)
	}
	if _, err := s1.Register("bob", "passw0rd-extra", "Bob"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	s1.Close()

	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #2: %v", err)
	}
	defer s2.Close()

	got, err := s2.Login("bob", "passw0rd-extra")
	if err != nil {
		t.Fatalf("Login after restart: %v", err)
	}
	if got.Username != "bob" {
		t.Fatalf("username = %q, want bob", got.Username)
	}
}

// TestSessionsSurviveRestart confirms a created session validates after a
// restart.
func TestSessionsSurviveRestart(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #1: %v", err)
	}
	u, err := s1.Register("carol", "secret1-xtra!!", "Carol")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	sess := s1.CreateSession(u, "dev-1")
	token := sess.Token
	s1.Close()

	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #2: %v", err)
	}
	defer s2.Close()

	got, ok := s2.ValidateToken(token)
	if !ok {
		t.Fatalf("session token should still be valid after restart")
	}
	if got.UserID != u.ID {
		t.Fatalf("session user_id = %q, want %q", got.UserID, u.ID)
	}
}

// TestExpiredSessionsNotReturned confirms an expired session row is not loaded
// back into the working set after a restart.
func TestExpiredSessionsNotReturned(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #1: %v", err)
	}
	u, err := s1.Register("dave", "secret1-xtra!!", "Dave")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	sess := s1.CreateSession(u, "dev-1")
	// Force the session to be expired and re-persist it.
	sess.ExpiresAt = time.Now().Add(-time.Hour)
	s1.persistSession(sess)
	token := sess.Token
	s1.Close()

	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #2: %v", err)
	}
	defer s2.Close()

	if _, ok := s2.ValidateToken(token); ok {
		t.Fatalf("expired session should not be returned after restart")
	}
}

// TestRevokeSessionPersists confirms a revoked session does not come back
// after a restart (delete write-through to SQLite).
func TestRevokeSessionPersists(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #1: %v", err)
	}
	u, err := s1.Register("erin", "secret1-xtra!!", "Erin")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	sess := s1.CreateSession(u, "dev-1")
	token := sess.Token
	s1.RevokeSession(token)
	s1.Close()

	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #2: %v", err)
	}
	defer s2.Close()

	if _, ok := s2.ValidateToken(token); ok {
		t.Fatalf("revoked session should stay revoked after restart")
	}
}

// --- New CLUSTER-02 SQLite tests ---

// TestSQLiteRoundTrip checks profiles + role + PIN write-through round-trips.
func TestSQLiteRoundTrip(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #1: %v", err)
	}
	u, err := s1.Register("frank", "secret1-xtra!!", "Frank")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s1.SetRole(u.ID, RoleAdmin); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if err := s1.SetPIN(u.ID, "4242"); err != nil {
		t.Fatalf("SetPIN: %v", err)
	}
	s1.Close()

	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #2: %v", err)
	}
	defer s2.Close()

	p, ok := s2.GetProfile(u.ID)
	if !ok {
		t.Fatalf("profile missing after restart")
	}
	if p.Role != RoleAdmin {
		t.Fatalf("role = %q, want admin", p.Role)
	}
	if !s2.ValidatePIN(u.ID, "4242") {
		t.Fatalf("PIN should validate after restart")
	}
}

// TestOneTimeImportSentinel confirms a legacy auth.json is imported exactly
// once, and that subsequent boots do NOT re-import (sentinel-guarded) even if
// the JSON file still exists with different content.
func TestOneTimeImportSentinel(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "auth.json")

	legacy := `{
	  "users": [{"id":"u-legacy","username":"legacy","name":"Legacy","created_at":"2020-01-01T00:00:00Z","last_login":"2020-01-01T00:00:00Z"}],
	  "profiles": [{"user_id":"u-legacy","role":"admin","display_name":"Legacy","theme":"dark","locale":"en","timezone":"UTC","initiative":"balanced","created_at":"2020-01-01T00:00:00Z","updated_at":"2020-01-01T00:00:00Z"}]
	}`
	if err := os.WriteFile(jsonPath, []byte(legacy), 0600); err != nil {
		t.Fatalf("write legacy json: %v", err)
	}

	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #1: %v", err)
	}
	if _, ok := s1.GetUser("u-legacy"); !ok {
		t.Fatalf("legacy user should be imported on first boot")
	}
	if !s1.legacyImported() {
		t.Fatalf("sentinel should be set after import")
	}
	s1.Close()

	// Tamper the JSON: if the importer ran again it would pull this in.
	tampered := `{
	  "users": [{"id":"u-should-not-appear","username":"ghost","name":"Ghost","created_at":"2020-01-01T00:00:00Z","last_login":"2020-01-01T00:00:00Z"}]
	}`
	if err := os.WriteFile(jsonPath, []byte(tampered), 0600); err != nil {
		t.Fatalf("rewrite json: %v", err)
	}

	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #2: %v", err)
	}
	defer s2.Close()

	if _, ok := s2.GetUser("u-legacy"); !ok {
		t.Fatalf("originally imported user should still be present")
	}
	if _, ok := s2.GetUser("u-should-not-appear"); ok {
		t.Fatalf("import must NOT run a second time (sentinel violated)")
	}
}

// TestChangePasswordRevokesAndPersists confirms the D71 fix: SEC-J's session
// revoke loop and the SQLite write-through coexist — after ChangePassword the
// old sessions are gone both in memory and after a restart, and the new
// password works after a restart.
func TestChangePasswordRevokesAndPersists(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #1: %v", err)
	}
	u, err := s1.Register("grace", "oldpassword1!", "Grace")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	sess := s1.CreateSession(u, "dev-1")
	token := sess.Token

	if err := s1.ChangePassword(u.ID, "oldpassword1!", "newpassword1!"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	// SEC-J revoke loop: session must be gone immediately in memory.
	if _, ok := s1.ValidateToken(token); ok {
		t.Fatalf("session should be revoked in memory after ChangePassword")
	}
	s1.Close()

	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore #2: %v", err)
	}
	defer s2.Close()

	// Revocation must survive restart (deleted from SQLite).
	if _, ok := s2.ValidateToken(token); ok {
		t.Fatalf("revoked session should stay revoked after restart")
	}
	// New password must work after restart; old must not.
	if _, err := s2.Login("grace", "newpassword1!"); err != nil {
		t.Fatalf("login with new password after restart: %v", err)
	}
	if _, err := s2.Login("grace", "oldpassword1!"); err == nil {
		t.Fatalf("login with old password should fail after change")
	}
}

// TestDegradedModeNoCrash confirms a Store still boots and works when SQLite
// cannot be opened (db == nil path is exercised via no-op persistence).
func TestDegradedModeNoCrash(t *testing.T) {
	dir := t.TempDir()
	s := &Store{
		users:    make(map[string]*User),
		sessions: make(map[string]*Session),
		profiles: make(map[string]*Profile),
		path:     filepath.Join(dir, "auth.json"),
		secret:   make([]byte, 32),
		// db left nil on purpose -> degraded mode
	}
	u, err := s.Register("heidi", "secret1-xtra!!", "Heidi")
	if err != nil {
		t.Fatalf("Register in degraded mode: %v", err)
	}
	if _, err := s.Login("heidi", "secret1-xtra!!"); err != nil {
		t.Fatalf("Login in degraded mode: %v", err)
	}
	if err := s.ChangePassword(u.ID, "secret1-xtra!!", "secret2-xtra!!"); err != nil {
		t.Fatalf("ChangePassword in degraded mode: %v", err)
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush should be a no-op nil: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close in degraded mode: %v", err)
	}
}
