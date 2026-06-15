package auth

// sqlite_security_test.go — SECAUDIT2 / CLUSTER-02 adversarial regressions.
//
// All SQLite access in sqlite.go uses parameter placeholders (?), never
// string concatenation. These tests assert that user-controlled values
// containing SQL metacharacters round-trip as opaque data (no injection) and
// that session-expiry is still enforced after the SQLite-backed load path.

import (
	"strings"
	"testing"
	"time"
)

// TestSQLInjection_UsernameIsOpaque registers users whose usernames are
// classic SQL-injection payloads and verifies they are stored/queried as
// literal data — the store still works and the payloads do not corrupt other
// rows or authenticate anyone.
func TestSQLInjection_UsernameIsOpaque(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	payloads := []string{
		`a'; DROP TABLE users;--`,
		`b" OR "1"="1`,
		`c') ; DELETE FROM sessions; --`,
		`d\` + "`" + `; UPDATE profiles SET role='admin'`,
	}
	created := make([]*User, 0, len(payloads))
	for i, p := range payloads {
		u, err := s.Register(p, "pw"+strings_repeat("x", i+12), "Inj")
		if err != nil {
			t.Fatalf("Register(%q): %v", p, err)
		}
		created = append(created, u)
	}

	// Reopen from SQLite — if any injection had executed, tables would be
	// gone / rows altered and this would fail or lose users.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen NewStore: %v", err)
	}
	for i, u := range created {
		got, ok := s2.GetUser(u.ID)
		if !ok {
			t.Fatalf("CLUSTER-02 REGRESSION: user with injection-payload "+
				"username %q vanished after SQLite reload — query is not "+
				"parameterised.", payloads[i])
		}
		if got.Username != payloads[i] {
			t.Fatalf("username not stored verbatim: got %q want %q",
				got.Username, payloads[i])
		}
	}

	// The injected `UPDATE profiles SET role='admin'` must NOT have taken
	// effect: none of these users should be admin except (by first-user rule)
	// the very first registered one.
	for i := 1; i < len(created); i++ {
		p, ok := s2.GetProfile(created[i].ID)
		if ok && p.Role == RoleAdmin {
			t.Fatalf("CLUSTER-02 REGRESSION: injection payload escalated "+
				"user %d to admin — SQL injection succeeded.", i)
		}
	}
}

// TestSessionExpiryEnforcedAfterSQLiteLoad confirms that an expired session
// persisted to SQLite is NOT honoured after a store reopen (the load path
// must drop expired rows, mirroring the in-memory ValidateToken check).
func TestSessionExpiryEnforcedAfterSQLiteLoad(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	u, err := s.Register("bob", "pw12345678901", "Bob")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	sess := s.CreateSession(u, "dev")

	// Backdate expiry and re-persist directly through the write-through path.
	sess.ExpiresAt = time.Now().Add(-2 * time.Hour)
	s.persistSession(sess)

	// Live store must already reject it.
	if _, ok := s.ValidateToken(sess.Token); ok {
		t.Fatal("ValidateToken returned an expired session (in-memory check broken)")
	}

	// After reload from SQLite it must still be rejected.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok := s2.ValidateToken(sess.Token); ok {
		t.Fatal("CLUSTER-02 REGRESSION: an expired session was honoured " +
			"after the SQLite-backed reload — session-expiry not enforced " +
			"post-SQLite.")
	}
}

// strings_repeat avoids importing strings.Repeat at call site noise.
func strings_repeat(s string, n int) string { return strings.Repeat(s, n) }
