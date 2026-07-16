package idp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/idp"
)

var testDBN atomic.Int64

// newIDP builds an in-memory auth store + an idp handler over it, and registers
// a verified test user. AUTH_ALLOW_UNVERIFIED_LOGIN=1 so the login round-trip
// reaches a full session without an email-verify step (the gate is exercised by
// the auth package's own tests).
func newIDP(t *testing.T, svcAuth interface {
	Configured(context.Context) bool
	Valid(context.Context, string) bool
}) (http.Handler, *auth.Store, *auth.User) {
	t.Helper()
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	dsn := fmt.Sprintf("file:idptest%d?mode=memory&cache=shared", testDBN.Add(1))
	db, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st, err := auth.OpenAuthStore(db, []byte("test-secret-key-1234567890123456"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	u, _, err := st.Signup(context.Background(), "idp@example.com", "correct-horse-battery-staple", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	deps := idp.Deps{Store: st}
	if svcAuth != nil {
		deps.Auth = svcAuth
	}
	return idp.Handler(deps), st, u
}

// loginForToken drives a real login through the handler and returns the issued
// session cookie value (a realistic way to obtain a valid session for tests
// without exporting internal session-mint helpers).
func loginForToken(t *testing.T, h http.Handler) string {
	t.Helper()
	body := `{"email":"idp@example.com","password":"correct-horse-battery-staple"}`
	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
	req.RemoteAddr = "198.51.100.5:2222"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loginForToken: status %d (body=%s)", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			return c.Value
		}
	}
	t.Fatal("loginForToken: no session cookie")
	return ""
}

// TestIDPLoginRoundTrip verifies the isolated idp login path issues a session
// cookie and that the account is server-derived (the response is the account
// whose credentials were verified, not anything from the request body).
func TestIDPLoginRoundTrip(t *testing.T) {
	h, _, u := newIDP(t, nil)
	body := `{"email":"idp@example.com","password":"correct-horse-battery-staple"}`
	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.7:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var got auth.User
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("login returned account %q, want server-derived %q", got.ID, u.ID)
	}
	// Session cookie must be set.
	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("no session cookie issued on successful login")
	}
}

// TestIDPLoginBadPassword confirms a wrong password fails closed (401), no cookie.
func TestIDPLoginBadPassword(t *testing.T) {
	h, _, _ := newIDP(t, nil)
	body := `{"email":"idp@example.com","password":"wrong-password"}`
	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.8:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad password status = %d, want 401", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			t.Fatal("session cookie issued on failed login (fail-open!)")
		}
	}
}

// TestIDPMeRequiresSession confirms GET /api/auth/me is fail-closed without a
// valid session, and returns the session's own account when present.
func TestIDPMeRequiresSession(t *testing.T) {
	h, st, u := newIDP(t, nil)
	// No cookie → 401.
	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("me without session = %d, want 401", rec.Code)
	}
	// With a valid session → the OWN account. Obtain a real session by logging in.
	token := loginForToken(t, h)
	_ = st
	req2 := httptest.NewRequest("GET", "/api/auth/me", nil)
	req2.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("me with session = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}
	var got auth.User
	_ = json.Unmarshal(rec2.Body.Bytes(), &got)
	if got.ID != u.ID {
		t.Fatalf("me returned %q, want %q", got.ID, u.ID)
	}
}

// fakeSvcAuth is a test serviceAuthChecker.
type fakeSvcAuth struct {
	configured bool
	secret     string
}

func (f fakeSvcAuth) Configured(context.Context) bool { return f.configured }
func (f fakeSvcAuth) Valid(_ context.Context, presented string) bool {
	return f.configured && presented != "" && presented == f.secret
}

// TestIDPIntrospectFailClosedNoSecret: with no shared secret configured the
// introspect endpoint returns 503 — it never authenticates a peer open.
func TestIDPIntrospectFailClosedNoSecret(t *testing.T) {
	h, _, _ := newIDP(t, fakeSvcAuth{configured: false})
	req := httptest.NewRequest("POST", "/api/session/introspect", strings.NewReader(`{"session":"x"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("introspect with no secret = %d, want 503", rec.Code)
	}
}

// TestIDPIntrospectRejectsBadAuth: wrong X-Relay-Auth → 401.
func TestIDPIntrospectRejectsBadAuth(t *testing.T) {
	h, _, _ := newIDP(t, fakeSvcAuth{configured: true, secret: "s3cr3t"})
	req := httptest.NewRequest("POST", "/api/session/introspect", strings.NewReader(`{"session":"x"}`))
	req.Header.Set("X-Relay-Auth", "wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("introspect bad auth = %d, want 401", rec.Code)
	}
}

// TestIDPIntrospectValid: authorized peer, valid session → {valid:true, own account};
// content-blind on an invalid token → {valid:false}.
func TestIDPIntrospectValid(t *testing.T) {
	h, _, u := newIDP(t, fakeSvcAuth{configured: true, secret: "s3cr3t"})
	token := loginForToken(t, h)

	call := func(session string) map[string]any {
		req := httptest.NewRequest("POST", "/api/session/introspect",
			strings.NewReader(fmt.Sprintf(`{"session":%q}`, session)))
		req.Header.Set("X-Relay-Auth", "s3cr3t")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("introspect status = %d, want 200", rec.Code)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}

	valid := call(token)
	if valid["valid"] != true {
		t.Fatalf("valid session introspect = %v, want valid:true", valid)
	}
	if valid["userId"] != u.ID {
		t.Fatalf("introspect userId = %v, want %q", valid["userId"], u.ID)
	}

	invalid := call("not-a-real-token")
	if invalid["valid"] != false {
		t.Fatalf("invalid session introspect = %v, want valid:false", invalid)
	}
	if invalid["userId"] != nil && invalid["userId"] != "" {
		t.Fatalf("invalid introspect leaked userId: %v", invalid["userId"])
	}
}

// TestIDPHealthFailClosed verifies /healthz reports ready when the DB is up and
// /livez is always 200.
func TestIDPHealthReady(t *testing.T) {
	h, _, _ := newIDP(t, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200 when DB up", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/livez", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("/livez = %d, want 200", rec2.Code)
	}
}

// TestIDPHealthFailClosedOnClosedDB verifies /healthz reports 503 (fail-closed)
// when the auth DB is unreachable, so the machine removes itself from rotation.
func TestIDPHealthFailClosedOnClosedDB(t *testing.T) {
	h, st, _ := newIDP(t, nil)
	_ = st.Close() // simulate DB loss
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/healthz with closed DB = %d, want 503 (fail-closed)", rec.Code)
	}
}
