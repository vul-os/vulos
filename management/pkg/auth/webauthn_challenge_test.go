package auth

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// openSharedTestStores opens TWO auth Store instances backed by the SAME shared
// in-memory SQLite database. This simulates two machines in a fleet talking to
// one shared `auth` schema, so we can prove that WebAuthn ceremony state written
// by one machine is readable by another (IDENTITY-SERVICE §4).
func openSharedTestStores(t *testing.T) (a, b *Store) {
	t.Helper()
	dsn := "file:washared" + t.Name() + "?mode=memory&cache=shared"
	dbA, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("open shared db A: %v", err)
	}
	a, err = OpenAuthStore(dbA, []byte("test-secret-key-1234567890123456"))
	if err != nil {
		t.Fatalf("open store A: %v", err)
	}
	t.Cleanup(func() { a.Close() })

	dbB, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("open shared db B: %v", err)
	}
	b, err = OpenAuthStore(dbB, []byte("test-secret-key-1234567890123456"))
	if err != nil {
		t.Fatalf("open store B: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return a, b
}

// TestWebAuthnChallengeCrossMachine proves the multi-machine property: a
// ceremony begun (challenge stored) on machine A is completed (challenge taken)
// on machine B, because the state lives in the shared auth schema rather than a
// per-process in-memory map.
func TestWebAuthnChallengeCrossMachine(t *testing.T) {
	setupWebAuthnEnv(t)
	stA, stB := openSharedTestStores(t)
	ctx := context.Background()

	u, _, err := stA.Signup(ctx, "cross@example.com", "correct-horse-battery-staple", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	insertTestCredential(t, stA, u.ID, makeTestCredential("x1", 1), "platform")

	// BEGIN on machine A: produce a real SessionData and stash it.
	wu, err := stA.LoadWebAuthnUserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("load webauthn user: %v", err)
	}
	_, session, err := stA.BeginWebAuthnLogin(ctx, wu)
	if err != nil {
		t.Fatalf("begin webauthn login: %v", err)
	}
	if err := stA.PutWebAuthnChallenge(ctx, u.ID, WAKindLogin, session); err != nil {
		t.Fatalf("put challenge on A: %v", err)
	}

	// FINISH on machine B: the challenge MUST be retrievable, and identical.
	got, ok, err := stB.TakeWebAuthnChallenge(ctx, u.ID, WAKindLogin)
	if err != nil {
		t.Fatalf("take challenge on B: %v", err)
	}
	if !ok {
		t.Fatal("challenge stored on A was not found on B — multi-machine store is broken")
	}
	if string(got.Challenge) != string(session.Challenge) {
		t.Fatalf("challenge mismatch: got %x want %x", got.Challenge, session.Challenge)
	}

	// Single-use: a second take (on either machine) must miss.
	if _, ok, _ := stA.TakeWebAuthnChallenge(ctx, u.ID, WAKindLogin); ok {
		t.Fatal("challenge was reusable after take — must be single-use")
	}
}

// TestWebAuthnChallengeKindIsolation verifies (user, kind) keying: a register
// challenge and a login challenge for the same user coexist independently.
func TestWebAuthnChallengeKindIsolation(t *testing.T) {
	setupWebAuthnEnv(t)
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "kinds@example.com", "correct-horse-battery-staple", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	insertTestCredential(t, st, u.ID, makeTestCredential("k1", 1), "platform")

	wu, _ := st.LoadWebAuthnUserByID(ctx, u.ID)
	_, regSession, err := st.BeginWebAuthnRegistration(ctx, &User{ID: u.ID, Email: u.Email})
	if err != nil {
		t.Fatalf("begin registration: %v", err)
	}
	_, loginSession, err := st.BeginWebAuthnLogin(ctx, wu)
	if err != nil {
		t.Fatalf("begin login: %v", err)
	}
	if err := st.PutWebAuthnChallenge(ctx, u.ID, WAKindRegister, regSession); err != nil {
		t.Fatalf("put register: %v", err)
	}
	if err := st.PutWebAuthnChallenge(ctx, u.ID, WAKindLogin, loginSession); err != nil {
		t.Fatalf("put login: %v", err)
	}

	// Taking login must NOT consume register.
	gotLogin, ok, _ := st.TakeWebAuthnChallenge(ctx, u.ID, WAKindLogin)
	if !ok || string(gotLogin.Challenge) != string(loginSession.Challenge) {
		t.Fatal("login challenge not isolated from register")
	}
	gotReg, ok, _ := st.TakeWebAuthnChallenge(ctx, u.ID, WAKindRegister)
	if !ok || string(gotReg.Challenge) != string(regSession.Challenge) {
		t.Fatal("register challenge was consumed or corrupted by login take")
	}
}

// TestWebAuthnChallengeExpiry verifies an expired challenge is treated as
// absent. We drive time via the package-level now() hook.
func TestWebAuthnChallengeExpiry(t *testing.T) {
	setupWebAuthnEnv(t)
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "expiry@example.com", "correct-horse-battery-staple", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	insertTestCredential(t, st, u.ID, makeTestCredential("e1", 1), "platform")
	wu, _ := st.LoadWebAuthnUserByID(ctx, u.ID)
	_, session, _ := st.BeginWebAuthnLogin(ctx, wu)

	base := now()
	now = func() time.Time { return base }
	t.Cleanup(func() { now = func() time.Time { return time.Now().UTC() } })

	if err := st.PutWebAuthnChallenge(ctx, u.ID, WAKindLogin, session); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Advance past the TTL.
	now = func() time.Time { return base.Add(waChallengeTTL + time.Minute) }
	if _, ok, _ := st.TakeWebAuthnChallenge(ctx, u.ID, WAKindLogin); ok {
		t.Fatal("expired challenge should not be returned")
	}
}
