package security

// auth_middleware_security_test.go — the single most important regression
// suite of SECAUDIT2.
//
// Invariants asserted here (any failure == a real vulnerability):
//
//   C1 / SEC-A : the auth Middleware MUST strip attacker-supplied
//                X-User-ID / X-User-Email headers BEFORE it consults the
//                AUTH-10 admin bearer token, so a client can never spoof an
//                identity by sending the header itself — even when:
//                  * no session exists,
//                  * no admin bearer is presented,
//                  * an admin bearer IS presented (the injected identity must
//                    come from at10FirstUser, not from the client header).
//   AUTH-10    : a Bearer token that does not match the on-disk admin token
//                must NOT authenticate; a matching one authenticates as the
//                first admin user (server-chosen identity, never client-chosen).
//   C2/C3/C4   : protected endpoints reject unauthenticated requests with 401.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"vulos/backend/services/auth"
)

// newAuthStore builds an auth.Store rooted at a throwaway dir.
func newAuthStore(t *testing.T) *auth.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := auth.NewStore(dir)
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	return st
}

// echoIdentity is the protected handler used as the middleware's "next". It
// reports exactly what identity the middleware decided the request carries.
func echoIdentity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Seen-User", r.Header.Get("X-User-ID"))
	w.Header().Set("X-Seen-Email", r.Header.Get("X-User-Email"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// TestC1_SpoofedHeaderWithNoSessionIsUnauthenticated is the core C1/SEC-A
// invariant: a request that supplies X-User-ID directly, with no session
// cookie/token and no admin bearer, must be treated as UNAUTHENTICATED. If
// this fails, the unauth-RCE chain (D24 C1) is back.
func TestC1_SpoofedHeaderWithNoSessionIsUnauthenticated(t *testing.T) {
	st := newAuthStore(t)
	// Seed one user so the store is "live" (HasAnyUsers true) — this models a
	// real deployment where /api/exec etc. exist.
	if _, err := st.Register("admin", "adminpw-secure-1", "Admin"); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	h := auth.NewHandler(st)
	srv := h.Middleware(http.HandlerFunc(echoIdentity))

	req := httptest.NewRequest(http.MethodPost, "/api/exec", nil)
	req.Header.Set("X-User-ID", "admin-spoofed")
	req.Header.Set("X-User-Email", "attacker@evil.test")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("C1 REGRESSION: spoofed X-User-ID on /api/exec with no session "+
			"was NOT rejected — got status %d, want 401. The header-spoof "+
			"unauth-RCE (D24 C1 / SEC-A) is exploitable on current main.", rr.Code)
	}
	if seen := rr.Header().Get("X-Seen-User"); seen != "" {
		t.Fatalf("C1 REGRESSION: protected handler observed spoofed identity %q; "+
			"the middleware did not strip the client X-User-ID header first.", seen)
	}
}

// TestC1_HeaderStripRunsBeforeBearerLogic proves the ORDERING requirement:
// even with a VALID admin bearer token, the identity the protected handler
// sees must be the server-resolved first-admin user, NEVER the value the
// client put in X-User-ID. If the strip ran AFTER the bearer block (or not at
// all) a client could send `X-User-ID: someone-else` + a valid bearer and
// act as an arbitrary user.
func TestC1_HeaderStripRunsBeforeBearerLogic(t *testing.T) {
	// ValidateAdminToken reads $HOME/.vulos/db/admin-token.json and the
	// Middleware uses os.UserHomeDir(); point HOME at a temp dir.
	home := t.TempDir()
	t.Setenv("HOME", home)
	// On some platforms UserHomeDir consults these too.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	tok, err := auth.IssueAdminToken(home)
	if err != nil {
		t.Fatalf("IssueAdminToken: %v", err)
	}

	st := newAuthStore(t)
	u, err := st.Register("realadmin", "pw12345-secure!", "Real Admin")
	if err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	// Register makes the first user admin by default; assert that.
	if p, ok := st.GetProfile(u.ID); !ok || p.Role != auth.RoleAdmin {
		t.Fatalf("expected first user to be admin, got %+v ok=%v", p, ok)
	}

	h := auth.NewHandler(st)
	srv := h.Middleware(http.HandlerFunc(echoIdentity))

	req := httptest.NewRequest(http.MethodPost, "/api/exec", nil)
	// Attacker supplies a bogus identity AND a valid admin bearer.
	req.Header.Set("X-User-ID", "victim-user-id")
	req.Header.Set("X-User-Email", "victim@spoof.test")
	req.Header.Set("Authorization", "Bearer "+tok)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("valid admin bearer should authenticate; got %d", rr.Code)
	}
	seenUser := rr.Header().Get("X-Seen-User")
	seenEmail := rr.Header().Get("X-Seen-Email")
	if seenUser == "victim-user-id" || seenEmail == "victim@spoof.test" {
		t.Fatalf("C1 ORDERING REGRESSION: protected handler saw the "+
			"CLIENT-SUPPLIED identity (user=%q email=%q). The X-User-ID/"+
			"X-User-Email strip must run BEFORE the AUTH-10 bearer block so "+
			"the bearer path injects ONLY the server-chosen first-admin "+
			"identity. A client can currently impersonate any user.",
			seenUser, seenEmail)
	}
	if seenUser != u.ID {
		t.Fatalf("AUTH-10: with a valid admin bearer the injected identity "+
			"must be the first-admin user id %q, got %q", u.ID, seenUser)
	}
}

// TestAUTH10_InvalidBearerDoesNotAuthenticate ensures a wrong/forged bearer
// token cannot authenticate, and crucially that it cannot be combined with a
// spoofed header to gain access.
func TestAUTH10_InvalidBearerDoesNotAuthenticate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if _, err := auth.IssueAdminToken(home); err != nil {
		t.Fatalf("IssueAdminToken: %v", err)
	}

	st := newAuthStore(t)
	if _, err := st.Register("admin", "pw12345-secure!", "Admin"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := auth.NewHandler(st)
	srv := h.Middleware(http.HandlerFunc(echoIdentity))

	req := httptest.NewRequest(http.MethodPost, "/api/exec", nil)
	req.Header.Set("X-User-ID", "spoofed")
	// Correct prefix, wrong secret — must fail constant-time compare.
	req.Header.Set("Authorization", "Bearer "+auth.AT10TokenPrefix+"deadbeefdeadbeefdeadbeefdeadbeef")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("AUTH-10 REGRESSION: invalid admin bearer + spoofed header "+
			"was NOT rejected (status %d). Forged bearer authenticates.", rr.Code)
	}
	if seen := rr.Header().Get("X-Seen-User"); seen != "" {
		t.Fatalf("AUTH-10 REGRESSION: identity %q leaked through with an "+
			"invalid bearer token.", seen)
	}
}

// TestSessionTokenStillAuthenticates is a non-regression guard: the C1 strip
// must not have broken the legitimate cookie/bearer-session path.
func TestSessionTokenStillAuthenticates(t *testing.T) {
	st := newAuthStore(t)
	u, err := st.Register("alice", "pw12345-secure!", "Alice")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	sess := st.CreateSession(u, "dev1")

	h := auth.NewHandler(st)
	srv := h.Middleware(http.HandlerFunc(echoIdentity))

	req := httptest.NewRequest(http.MethodGet, "/api/passkeys", nil)
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: sess.Token})

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("legitimate session was rejected (status %d) — the C1 strip "+
			"must not break the real cookie path", rr.Code)
	}
	if rr.Header().Get("X-Seen-User") != u.ID {
		t.Fatalf("session auth resolved wrong user: got %q want %q",
			rr.Header().Get("X-Seen-User"), u.ID)
	}
}

// TestProtectedEndpointsRequireAuth checks a representative set of the
// high-value RCE-adjacent routes are NOT public and reject anonymous access.
func TestProtectedEndpointsRequireAuth(t *testing.T) {
	st := newAuthStore(t)
	if _, err := st.Register("admin", "pw12345-secure!", "Admin"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := auth.NewHandler(st)
	srv := h.Middleware(http.HandlerFunc(echoIdentity))

	for _, path := range []string{
		"/api/exec",
		"/api/sandbox/run",
		"/api/apps/launch",
		"/api/open", // SEC-H: must NOT be public
		"/api/store/registry/install",
		"/api/shell/native-launch",
		"/api/ai-apps/save",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("REGRESSION: %s is reachable without auth (status %d, "+
				"want 401). It must be behind the auth middleware.", path, rr.Code)
		}
	}
}

// TestPublicPathsAreMinimal asserts the unauthenticated allow-list does not
// silently grow to include dangerous endpoints. We probe via the middleware:
// a public path returns the next handler (200) with no identity; a dangerous
// path must 401.
func TestPublicPathsAreMinimal(t *testing.T) {
	st := newAuthStore(t)
	if _, err := st.Register("admin", "pw12345-secure!", "Admin"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := auth.NewHandler(st)
	srv := h.Middleware(http.HandlerFunc(echoIdentity))

	// These MUST stay public (setup-time / health).
	mustBePublic := []string{
		"/health", "/api/auth/status", "/api/auth/login",
		"/api/setup/join", "/api/setup/join/status", "/api/setup/join-code",
	}
	for _, p := range mustBePublic {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code == http.StatusUnauthorized {
			t.Fatalf("%s unexpectedly requires auth (setup/health must be public)", p)
		}
	}

	// These MUST NOT be public.
	mustBeProtected := []string{
		"/api/open", "/api/exec", "/api/sandbox/run",
		"/api/store/install", "/api/profiles",
	}
	for _, p := range mustBeProtected {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("REGRESSION: %s is in the public allow-list (status %d) "+
				"— it must require authentication.", p, rr.Code)
		}
	}
}

var _ = os.Getenv
