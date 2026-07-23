package auth

import (
	"context"
	"testing"
	"time"
)

// TestLastActiveAt_NoSession verifies that a user with no session rows reports
// found=false so the caller (org-admin Members adapter) can fall back to the
// membership join date.
func TestLastActiveAt_NoSession(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Unknown user id → not found, no error.
	when, found, err := st.LastActiveAt(ctx, "NONEXISTENTUSERID0000000000")
	if err != nil {
		t.Fatalf("LastActiveAt unknown: %v", err)
	}
	if found {
		t.Fatalf("expected found=false for user with no sessions, got %v", when)
	}

	// Empty user id → not found, no error.
	if _, found, err := st.LastActiveAt(ctx, ""); err != nil || found {
		t.Fatalf("LastActiveAt empty id: found=%v err=%v", found, err)
	}
}

// TestLastActiveAt_LatestSession verifies LastActiveAt returns the freshest
// session activity (MAX(last_seen_at)) across a user's sessions, and that a
// LookupSession touch advances it.
func TestLastActiveAt_LatestSession(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Drive the clock deterministically.
	orig := NowFunc()
	defer SetNowFunc(orig)

	base := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	SetNowFunc(func() time.Time { return base })

	// Signup creates the first session at t=base.
	u, tok1, err := st.Signup(ctx, "active@example.com", "securePass1234!", "1.1.1.1", "ua-1")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	when, found, err := st.LastActiveAt(ctx, u.ID)
	if err != nil {
		t.Fatalf("LastActiveAt after signup: %v", err)
	}
	if !found {
		t.Fatal("expected found=true after signup")
	}
	if !when.Equal(base) {
		t.Fatalf("last active: got %v, want %v", when, base)
	}

	// A second login 10 minutes later creates a fresher session.
	t2 := base.Add(10 * time.Minute)
	SetNowFunc(func() time.Time { return t2 })
	if _, err := st.Login(ctx, "active@example.com", "securePass1234!", "2.2.2.2", "ua-2"); err != nil {
		t.Fatalf("login: %v", err)
	}

	when, found, err = st.LastActiveAt(ctx, u.ID)
	if err != nil {
		t.Fatalf("LastActiveAt after login: %v", err)
	}
	if !found || !when.Equal(t2) {
		t.Fatalf("last active after 2nd login: got %v found=%v, want %v", when, found, t2)
	}

	// Touching the OLD session at an even later time advances last_seen_at on
	// that row, which LastActiveAt must reflect (it is the freshest activity).
	t3 := base.Add(30 * time.Minute)
	SetNowFunc(func() time.Time { return t3 })
	if _, err := st.LookupSession(ctx, tok1); err != nil {
		t.Fatalf("lookup (touch) session 1: %v", err)
	}

	when, found, err = st.LastActiveAt(ctx, u.ID)
	if err != nil {
		t.Fatalf("LastActiveAt after touch: %v", err)
	}
	if !found || !when.Equal(t3) {
		t.Fatalf("last active after touch: got %v found=%v, want %v", when, found, t3)
	}

	// Returned time is UTC.
	if when.Location() != time.UTC {
		t.Fatalf("expected UTC time, got location %v", when.Location())
	}
}

// TestLastActiveAt_RevokedStillCounts verifies that a soft-revoked session row
// (kept for audit) still contributes its last_seen_at — we want the freshest
// available activity, regardless of revoked state.
func TestLastActiveAt_RevokedStillCounts(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	orig := NowFunc()
	defer SetNowFunc(orig)

	base := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	SetNowFunc(func() time.Time { return base })

	u, tok, err := st.Signup(ctx, "revoked@example.com", "securePass1234!", "1.1.1.1", "ua-1")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	// Soft-revoke the only session (RevokeSession keeps the row).
	if err := st.RevokeSession(ctx, tok); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	when, found, err := st.LastActiveAt(ctx, u.ID)
	if err != nil {
		t.Fatalf("LastActiveAt after revoke: %v", err)
	}
	if !found || !when.Equal(base) {
		t.Fatalf("revoked session should still report last active: got %v found=%v, want %v", when, found, base)
	}
}

// TestLastActiveAt_AfterFullLogout verifies that when every session row is
// DELETEd (logout path), LastActiveAt reports found=false.
func TestLastActiveAt_AfterFullLogout(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, tok, err := st.Signup(ctx, "loggedout@example.com", "securePass1234!", "1.1.1.1", "ua-1")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	if err := st.DeleteSession(ctx, tok); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	_, found, err := st.LastActiveAt(ctx, u.ID)
	if err != nil {
		t.Fatalf("LastActiveAt after logout: %v", err)
	}
	if found {
		t.Fatal("expected found=false after all sessions deleted")
	}
}
