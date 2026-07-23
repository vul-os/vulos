package auth

// oauth_gate_test.go — store-level adversarial tests for the MANDATORY-PASSWORD
// gate (LOCKED, 2026-07). A social sign-up holds the LockedPasswordHash sentinel
// until SetInitialPassword runs; until then its session must be unusable on EVERY
// session-gated surface. These tests prove the gate at the primitive level:
// LookupSession, RequireSession and IntrospectSession all fail closed, while the
// two setup-tolerant primitives (LookupSessionForSetup / RequireSessionAllowing
// Setup) admit it — and only it — so the account can be made usable.
//
// PG-backed (VULOS_TEST_POSTGRES); skipped otherwise.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mkPasswordlessSocialSession creates a social sign-up (locked-password sentinel)
// and returns (userID, fullSessionToken). The session is a FULL session exactly
// as resolveSocialLogin issues for a brand-new social account.
func mkPasswordlessSocialSession(t *testing.T, st *Store) (string, string) {
	t.Helper()
	ctx := context.Background()
	userID, err := st.CreateOAuthUser(ctx, "social-gate@example.com", true)
	if err != nil {
		t.Fatalf("CreateOAuthUser: %v", err)
	}
	tok, err := st.IssueOAuthSignupSession(ctx, userID, "127.0.0.1", "test-ua")
	if err != nil {
		t.Fatalf("IssueOAuthSignupSession: %v", err)
	}
	return userID, tok
}

func reqWithSession(tok string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/anything", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: tok})
	return r
}

// TestGate_PasswordlessSocialSession_FailsClosed proves the whole gate at the
// primitive level: the password-less session is refused by LookupSession /
// RequireSession / IntrospectSession, admitted only by the setup-tolerant
// primitives, and becomes fully usable the instant SetInitialPassword runs.
func TestGate_PasswordlessSocialSession_FailsClosed(t *testing.T) {
	st := openPGAuthStore(t)
	ctx := context.Background()
	userID, tok := mkPasswordlessSocialSession(t, st)

	// LookupSession: fail closed with the distinct sentinel.
	if _, err := st.LookupSession(ctx, tok); !errors.Is(err, ErrPasswordSetupRequired) {
		t.Fatalf("LookupSession on password-less session: err = %v, want ErrPasswordSetupRequired", err)
	}

	// LookupSessionForSetup: admits it, flags NeedsPasswordSetup.
	u, err := st.LookupSessionForSetup(ctx, tok)
	if err != nil {
		t.Fatalf("LookupSessionForSetup: unexpected err %v", err)
	}
	if u == nil || u.ID != userID || !u.NeedsPasswordSetup {
		t.Fatalf("LookupSessionForSetup: user=%+v, want id=%s NeedsPasswordSetup=true", u, userID)
	}

	// RequireSession (the ~217-route gate): 403 password_setup_required, nil user.
	rrDeny := httptest.NewRecorder()
	if got := st.RequireSession(ctx, rrDeny, reqWithSession(tok)); got != nil {
		t.Fatal("RequireSession admitted a password-less session — gate is open")
	}
	if rrDeny.Code != http.StatusForbidden {
		t.Errorf("RequireSession status = %d, want 403", rrDeny.Code)
	}
	if body := rrDeny.Body.String(); !contains(body, "password_setup_required") {
		t.Errorf("RequireSession body = %q, want password_setup_required", body)
	}

	// RequireSessionAllowingSetup: admits it.
	rrAllow := httptest.NewRecorder()
	if got := st.RequireSessionAllowingSetup(ctx, rrAllow, reqWithSession(tok)); got == nil || got.ID != userID {
		t.Fatalf("RequireSessionAllowingSetup rejected the setup session (code=%d)", rrAllow.Code)
	}

	// IntrospectSession (the apps' session→user path): fail closed.
	if intro := st.IntrospectSession(ctx, tok); intro.Valid {
		t.Fatal("IntrospectSession resolved a password-less session as valid — apps could use it")
	}

	// Set the mandatory password → the gate lifts everywhere.
	if err := st.SetInitialPassword(ctx, userID, "a-real-strong-password-123!"); err != nil {
		t.Fatalf("SetInitialPassword: %v", err)
	}
	u2, err := st.LookupSession(ctx, tok)
	if err != nil || u2 == nil || u2.ID != userID {
		t.Fatalf("LookupSession after set-initial: err=%v user=%+v, want usable", err, u2)
	}
	if u2.NeedsPasswordSetup {
		t.Error("NeedsPasswordSetup still true after SetInitialPassword")
	}
	if intro := st.IntrospectSession(ctx, tok); !intro.Valid || intro.UserID != userID {
		t.Fatalf("IntrospectSession after set-initial: %+v, want valid for %s", intro, userID)
	}
}

// TestGate_NormalAccount_Unaffected is the control: a real password account is
// never trapped by the gate.
func TestGate_NormalAccount_Unaffected(t *testing.T) {
	st := openPGAuthStore(t)
	ctx := context.Background()
	nu, tok, err := st.Signup(ctx, "normal-gate@vulos.org", "a-real-strong-password-123!", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	uid := nu.ID
	u, err := st.LookupSession(ctx, tok)
	if err != nil || u == nil || u.ID != uid {
		t.Fatalf("LookupSession on normal account: err=%v user=%+v", err, u)
	}
	if u.NeedsPasswordSetup {
		t.Error("NeedsPasswordSetup true on a normal password account")
	}
	if intro := st.IntrospectSession(ctx, tok); !intro.Valid {
		t.Error("IntrospectSession invalid for a normal password account")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
