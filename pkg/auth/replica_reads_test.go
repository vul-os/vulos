package auth

import (
	"context"
	"testing"
)

// TestReplicaReadsFlagPrimaryFallback verifies that turning on AUTH_REPLICA_READS
// with NO replica configured (the SQLite / self-host case, and cloud before a
// replica URL is wired) leaves every login-critical read correct: it falls back
// to the primary. This is the reversibility/no-op guarantee of the Phase-1 seam
// (IDENTITY-SERVICE §2.3). read-your-writes is exercised because the session is
// created and immediately validated on the same (primary) pool.
func TestReplicaReadsFlagPrimaryFallback(t *testing.T) {
	t.Setenv("AUTH_REPLICA_READS", "1")
	st := openTestStore(t)
	ctx := context.Background()

	u, token, err := st.Signup(ctx, "replica@example.com", "correct-horse-battery-staple", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	// LookupSession (read-your-writes): the just-minted session must validate.
	got, err := st.LookupSession(ctx, token)
	if err != nil {
		t.Fatalf("LookupSession with replica flag on: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("LookupSession returned wrong user: %s != %s", got.ID, u.ID)
	}

	// IntrospectSession must agree.
	intro := st.IntrospectSession(ctx, token)
	if !intro.Valid || intro.UserID != u.ID {
		t.Fatalf("IntrospectSession: valid=%v user=%s", intro.Valid, intro.UserID)
	}

	// Login user-lookup read path still works under the flag.
	res, err := st.Login(ctx, "replica@example.com", "correct-horse-battery-staple", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Login with replica flag on: %v", err)
	}
	if res.User == nil || res.User.ID != u.ID {
		t.Fatalf("Login returned wrong user")
	}

	// Revocation propagation: after logout, the session must be invalid on the
	// next read even with replica reads enabled (primary is authoritative here).
	if err := st.DeleteSession(ctx, token); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := st.LookupSession(ctx, token); err == nil {
		t.Fatal("revoked session should not validate")
	}
	if st.IntrospectSession(ctx, token).Valid {
		t.Fatal("revoked session should not introspect as valid")
	}
}
