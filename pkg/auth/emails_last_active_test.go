package auth

import (
	"context"
	"testing"
	"time"
)

// TestEmailsAndLastActive_Batch verifies the batched reader returns email +
// most-recent session activity for many account ids in one call, matching the
// per-member LastActiveAt / email lookups it replaces (the N+1 fix).
func TestEmailsAndLastActive_Batch(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	orig := NowFunc()
	defer SetNowFunc(orig)

	base := time.Date(2026, 5, 24, 9, 0, 0, 0, time.UTC)
	SetNowFunc(func() time.Time { return base })

	u1, _, err := st.Signup(ctx, "alice@example.com", "securePass1234!", "1.1.1.1", "ua")
	if err != nil {
		t.Fatalf("signup alice: %v", err)
	}
	t2 := base.Add(15 * time.Minute)
	SetNowFunc(func() time.Time { return t2 })
	u2, _, err := st.Signup(ctx, "bob@example.com", "securePass1234!", "2.2.2.2", "ua")
	if err != nil {
		t.Fatalf("signup bob: %v", err)
	}

	got, err := st.EmailsAndLastActive(ctx, []string{u1.ID, u2.ID, "UNKNOWNID", u1.ID /* dup */})
	if err != nil {
		t.Fatalf("EmailsAndLastActive: %v", err)
	}

	a, ok := got[u1.ID]
	if !ok || a.Email != "alice@example.com" {
		t.Fatalf("alice info = %+v ok=%v, want email alice@example.com", a, ok)
	}
	if !a.LastActiveOK || !a.LastActive.Equal(base) {
		t.Errorf("alice last-active = %v ok=%v, want %v ok=true", a.LastActive, a.LastActiveOK, base)
	}

	b, ok := got[u2.ID]
	if !ok || b.Email != "bob@example.com" {
		t.Fatalf("bob info = %+v ok=%v", b, ok)
	}
	if !b.LastActiveOK || !b.LastActive.Equal(t2) {
		t.Errorf("bob last-active = %v, want %v", b.LastActive, t2)
	}

	// Unknown id must simply be absent (caller leaves Email empty).
	if _, ok := got["UNKNOWNID"]; ok {
		t.Error("unknown id should be absent from the result map")
	}

	// Cross-check the batch matches the per-member readers it replaces.
	when, found, _ := st.LastActiveAt(ctx, u1.ID)
	if found != a.LastActiveOK || !when.Equal(a.LastActive) {
		t.Errorf("batch diverges from LastActiveAt for alice: batch=(%v,%v) single=(%v,%v)",
			a.LastActive, a.LastActiveOK, when, found)
	}
}

// TestEmailsAndLastActive_Empty verifies empty / no-id inputs return an empty
// map without touching the DB.
func TestEmailsAndLastActive_Empty(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	for _, ids := range [][]string{nil, {}, {""}, {"", ""}} {
		got, err := st.EmailsAndLastActive(ctx, ids)
		if err != nil {
			t.Fatalf("EmailsAndLastActive(%v): %v", ids, err)
		}
		if len(got) != 0 {
			t.Errorf("EmailsAndLastActive(%v) = %v, want empty", ids, got)
		}
	}
}

// TestEmailsAndLastActive_NoSessionFallsBack verifies a user whose sessions
// were all deleted (full logout) reports LastActiveOK=false so the caller falls
// back to JoinedAt — mirroring LastActiveAt's after-logout semantics.
func TestEmailsAndLastActive_NoSessionFallsBack(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "loggedout@example.com", "securePass1234!", "1.1.1.1", "ua")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	if err := st.DeleteAllUserSessions(ctx, u.ID); err != nil {
		t.Fatalf("delete sessions: %v", err)
	}

	got, err := st.EmailsAndLastActive(ctx, []string{u.ID})
	if err != nil {
		t.Fatalf("EmailsAndLastActive: %v", err)
	}
	mi := got[u.ID]
	if mi.Email != "loggedout@example.com" {
		t.Errorf("email = %q, want loggedout@example.com", mi.Email)
	}
	if mi.LastActiveOK {
		t.Error("LastActiveOK should be false after full logout")
	}
}
