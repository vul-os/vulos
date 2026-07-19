package auth

// apptoken_class_test.go — SECURITY-C1: the credential-class boundary.
//
// The reverse proxy hands a lower-trust app backend (Office/Board/Files) an
// app-identity token INSTEAD of the user's CP session. The whole point
// of that swap is that the token it receives is useless as a session: whatever
// an app does with what we gave it, it must never be able to act as the user
// against the session-gated CP surface.
//
// These tests pin that property directly, so it can never regress into being
// true only by accident (an app token would also miss the sessions table — but
// the boundary must be explicit, not incidental).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/apptoken"
)

const testAppKey = "test-apptoken-key-0123456789abcdef"

// mintTestAppToken mints an app token the way the composition root does.
func mintTestAppToken(t *testing.T, sub, aud string) string {
	t.Helper()
	tok, err := apptoken.NewMinter([]byte(testAppKey), apptoken.DefaultTTL).Mint(sub, aud)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return tok
}

// wireTestIntrospector teaches st to verify the tokens mintTestAppToken makes,
// mirroring initAppIdentity in cmd/server.
func wireTestIntrospector(st *Store) {
	st.SetAppTokenIntrospector(func(_ context.Context, token, expectedAud string) (string, time.Time, bool) {
		c, err := apptoken.VerifyAny([][]byte{[]byte(testAppKey)}, token, expectedAud, time.Now())
		if err != nil {
			return "", time.Time{}, false
		}
		return c.Sub, time.Unix(c.Exp, 0).UTC(), true
	})
}

// THE headline invariant: an app token presented in the session cookie is NOT a
// session, on any route. This is what stops a compromised Office/Board backend
// from replaying what the proxy gave it against the ~217 session-gated routes.
func TestRequireSession_RejectsAppToken(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "class-boundary@vulos.test", "correct horse battery staple", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	wireTestIntrospector(st)

	// A token minted for THIS very user, by us, entirely authentic.
	tok := mintTestAppToken(t, u.ID, "office")

	r := httptest.NewRequest(http.MethodGet, "/api/billing/summary", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: tok})
	w := httptest.NewRecorder()

	if got := st.RequireSession(ctx, w, r); got != nil {
		t.Fatalf("app token was accepted as a session for user %s — SECURITY-C1 regression", got.ID)
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want %d (app_token_is_not_a_session)", w.Code, http.StatusForbidden)
	}
}

// The same boundary at the store level: LookupSession must not resolve one.
func TestLookupSession_RejectsAppToken(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "class-lookup@vulos.test", "correct horse battery staple", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	wireTestIntrospector(st)

	if _, err := st.LookupSession(ctx, mintTestAppToken(t, u.ID, "office")); err == nil {
		t.Fatal("LookupSession resolved an app token — it is not a session")
	}
}

// …while a REAL session still works, so the guard is a class check, not a
// blanket denial.
func TestRequireSession_StillAcceptsRealSession(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "class-real@vulos.test", "correct horse battery staple", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	wireTestIntrospector(st)
	tok, err := st.createSession(ctx, u.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: tok})
	w := httptest.NewRecorder()

	got := st.RequireSession(ctx, w, r)
	if got == nil || got.ID != u.ID {
		t.Fatalf("a real session must still authenticate (status %d)", w.Code)
	}
}

// Introspection is the OTHER half of the swap: it must resolve app tokens, or
// Office (which introspects the cookie to learn who the user is) breaks.
func TestIntrospectSession_ResolvesAppToken(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "class-intro@vulos.test", "correct horse battery staple", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	wireTestIntrospector(st)

	got := st.IntrospectSession(ctx, mintTestAppToken(t, u.ID, "office"))
	if !got.Valid || got.UserID != u.ID || got.TenantID != u.ID {
		t.Fatalf("app token must introspect to its owner: %+v (want user %s)", got, u.ID)
	}
}

// Same fail-closed, content-blind contract as a session: a token we did not
// sign resolves to nothing.
func TestIntrospectSession_ForgedAppTokenIsNotValid(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "class-forged@vulos.test", "correct horse battery staple", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	wireTestIntrospector(st)

	forged, err := apptoken.NewMinter([]byte("attacker-key-not-ours-0123456789"), apptoken.DefaultTTL).Mint(u.ID, "office")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if got := st.IntrospectSession(ctx, forged); got.Valid || got.UserID != "" {
		t.Fatalf("a token signed with a foreign key must be content-blind not-valid: %+v", got)
	}
}

// An expired token is dead even though it is authentic — the short TTL is a
// real bound, not decoration.
func TestIntrospectSession_ExpiredAppTokenIsNotValid(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "class-expired@vulos.test", "correct horse battery staple", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	wireTestIntrospector(st)

	// Issued 30 minutes ago — well past the 2-minute TTL.
	stale, err := apptoken.NewMinter([]byte(testAppKey), apptoken.DefaultTTL).
		MintAt(u.ID, "office", time.Now().Add(-30*time.Minute))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if got := st.IntrospectSession(ctx, stale); got.Valid {
		t.Fatalf("an expired app token must not introspect as valid: %+v", got)
	}
}

// With no verifier wired (a build with no app proxy), app tokens resolve to
// nothing rather than to a bare "trust the sub claim".
func TestIntrospectSession_AppTokenInertWithoutVerifier(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "class-noverifier@vulos.test", "correct horse battery staple", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	// Deliberately NOT wiring an introspector.
	if got := st.IntrospectSession(ctx, mintTestAppToken(t, u.ID, "office")); got.Valid {
		t.Fatalf("app token must not resolve without a wired verifier: %+v", got)
	}
}
