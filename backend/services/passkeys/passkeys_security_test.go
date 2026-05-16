package passkeys

// passkeys_security_test.go — SECAUDIT2 / AUTH-12 adversarial regressions.
//
// Invariants:
//   * userID and credID are attacker-influenced (userID comes from the
//     X-User-ID header set by the auth middleware; credID is a path value).
//     Both must be blocked from path traversal so one user cannot read/delete
//     another user's sealed credential files or escape the vault dir.
//   * A ceremony session is bound to the userID that began it (replay /
//     cross-user assertion confusion).

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestUserDir_RejectsTraversal(t *testing.T) {
	svc := newTestService(t)
	for _, uid := range []string{
		"..", "../", "../../etc", "a/../../b", "/abs", "foo/bar",
		"foo\\bar", ".", "..\\..\\windows",
	} {
		if _, err := svc.userDir(uid); err == nil {
			t.Fatalf("AUTH-12 REGRESSION: userDir(%q) returned no error — a "+
				"crafted X-User-ID can escape the passkeys vault directory.", uid)
		}
	}
	// A legitimate id must resolve UNDER the vault dir.
	got, err := svc.userDir("user-123")
	if err != nil {
		t.Fatalf("legitimate userID rejected: %v", err)
	}
	if !strings.HasPrefix(got, filepath.Clean(svc.vaultDir)) {
		t.Fatalf("userDir escaped vault: %q not under %q", got, svc.vaultDir)
	}
}

func TestCredPath_RejectsTraversal(t *testing.T) {
	svc := newTestService(t)
	for _, cid := range []string{
		"..", "../evil", "a/../../b", "x/y", "x\\y", "..%2f", "....//",
		"foo..bar", // contains ".." substring → rejected by design
	} {
		if _, err := svc.au12CredPath("validuser", cid); err == nil {
			t.Fatalf("AUTH-12 REGRESSION: au12CredPath cred id %q accepted — "+
				"path traversal via credential id is possible.", cid)
		}
	}
	// An empty cred id must also be refused.
	if _, err := svc.au12CredPath("validuser", ""); err == nil {
		t.Fatal("empty credential id must be rejected")
	}
}

// TestConsumeSession_CrossUserRejected asserts a ceremony session begun by
// user A cannot be consumed/validated as user B (cross-user confusion). A
// fresh session is used because au12ConsumeSession is single-use (it removes
// the token on lookup), which is also the replay defence.
func TestConsumeSession_CrossUserRejected(t *testing.T) {
	svc := newTestService(t)

	_, sessionData, err := svc.BeginRegistration("alice", "Alice")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}

	// Consuming as a different user must return an error, NOT mallory's
	// session bound to alice's ceremony.
	if _, err := svc.au12ConsumeSession("mallory", sessionData); err == nil {
		t.Fatal("AUTH-12 REGRESSION: a ceremony session was consumable by a " +
			"DIFFERENT userID than the one that started it (session binding " +
			"broken — cross-user assertion confusion).")
	}
}

// TestConsumeSession_SingleUse asserts a token cannot be replayed: once
// successfully consumed by its owner it is gone.
func TestConsumeSession_SingleUse(t *testing.T) {
	svc := newTestService(t)

	_, sessionData, err := svc.BeginRegistration("alice", "Alice")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}

	if _, err := svc.au12ConsumeSession("alice", sessionData); err != nil {
		t.Fatalf("legitimate owner could not consume their own session: %v", err)
	}
	if _, err := svc.au12ConsumeSession("alice", sessionData); err == nil {
		t.Fatal("AUTH-12 REGRESSION: a consumed ceremony session token was " +
			"replayable (no single-use enforcement).")
	}
}
