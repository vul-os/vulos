package auth

// introspect_capstone_test.go — CAPSTONE coverage for IntrospectSession at the
// store level. The SSO service-seam (POST /api/session/introspect) is the trust
// bridge between the CP identity provider and untrusted products, so every
// not-valid branch is asserted directly: a partial (pre-2FA) session, a
// suspended account, and an expired session must ALL resolve to {valid:false}
// with no user/tenant leaked (content-blind, no oracle).

import (
	"context"
	"testing"
	"time"
)

func TestIntrospectSession_ValidFullSession(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "intro-valid@vulos.test", "correct horse battery staple", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	tok, err := st.createSession(ctx, u.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	got := st.IntrospectSession(ctx, tok)
	if !got.Valid || got.UserID != u.ID || got.TenantID != u.ID {
		t.Fatalf("valid session introspect wrong: %+v (want valid, user %s)", got, u.ID)
	}
}

func TestIntrospectSession_EmptyAndForged(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if got := st.IntrospectSession(ctx, ""); got.Valid || got.UserID != "" {
		t.Fatalf("empty token must be content-blind not-valid: %+v", got)
	}
	if got := st.IntrospectSession(ctx, "forged-token-does-not-exist"); got.Valid || got.UserID != "" {
		t.Fatalf("forged token must be content-blind not-valid: %+v", got)
	}
}

// TestIntrospectSession_PartialNotValid: a pre-2FA PARTIAL session must never
// introspect as a valid full session (otherwise the 2FA step is bypassable via
// the service seam).
func TestIntrospectSession_PartialNotValid(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "intro-partial@vulos.test", "correct horse battery staple", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	partial, err := st.CreatePartialSession(ctx, u.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("CreatePartialSession: %v", err)
	}
	got := st.IntrospectSession(ctx, partial)
	if got.Valid || got.UserID != "" {
		t.Fatalf("partial (pre-2FA) session must be not-valid, got %+v", got)
	}
}

// TestIntrospectSession_RevokedNotValid: a revoked session is not valid.
func TestIntrospectSession_RevokedNotValid(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "intro-revoked@vulos.test", "correct horse battery staple", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	tok, err := st.createSession(ctx, u.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	if err := st.RevokeSession(ctx, tok); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if got := st.IntrospectSession(ctx, tok); got.Valid {
		t.Fatalf("revoked session must be not-valid, got %+v", got)
	}
}

// TestIntrospectSession_SuspendedAccountNotValid: a live session on a SUSPENDED
// account must stop introspecting as valid immediately (parity with LookupSession).
func TestIntrospectSession_SuspendedAccountNotValid(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "intro-suspended@vulos.test", "correct horse battery staple", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	tok, err := st.createSession(ctx, u.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	// Sanity: valid before suspension.
	if got := st.IntrospectSession(ctx, tok); !got.Valid {
		t.Fatalf("precondition: session should be valid, got %+v", got)
	}
	if _, err := st.db.ExecContext(ctx, st.db.Rebind(`UPDATE users SET suspended = 1 WHERE id = ?`), u.ID); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if got := st.IntrospectSession(ctx, tok); got.Valid || got.UserID != "" {
		t.Fatalf("suspended account session must be not-valid, got %+v", got)
	}
}

// TestIntrospectSession_ExpiredNotValid: an expired session is not valid. We
// backdate the session's expiry directly to avoid a real time wait.
func TestIntrospectSession_ExpiredNotValid(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "intro-expired@vulos.test", "correct horse battery staple", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	tok, err := st.createSession(ctx, u.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if _, err := st.db.ExecContext(ctx, st.db.Rebind(`UPDATE sessions SET expires_at = ? WHERE id = ?`), past, tok); err != nil {
		t.Fatalf("backdate expiry: %v", err)
	}
	if got := st.IntrospectSession(ctx, tok); got.Valid {
		t.Fatalf("expired session must be not-valid, got %+v", got)
	}
}
