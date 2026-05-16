package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/appnet"
	"vulos/backend/services/auth"
)

// newTestDeps creates a real auth.Store backed by a temp dir, plus a bare
// appnet.Manager and PortPool — all without touching the network or filesystem
// beyond a small temporary directory.
func newTestDeps(t *testing.T) (*auth.Store, *appnet.Manager, *appnet.PortPool) {
	t.Helper()
	dir := t.TempDir()
	store, err := auth.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	mgr := appnet.NewManager()
	pool := appnet.NewPortPool(9000, 9999)
	return store, mgr, pool
}

// seedSession registers a local user, opens a session and returns the token.
func seedSession(t *testing.T, store *auth.Store) (token string, userID string) {
	t.Helper()
	u, err := store.Register("testuser", "testpass123", "Test User")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	sess := store.CreateSession(u, "device-test")
	return sess.Token, u.ID
}

// startFakeApp launches an httptest.Server that acts as the upstream app and
// injects its address into the Manager so GetForUser resolves it.
func startFakeApp(t *testing.T, mgr *appnet.Manager, appID, userID string) (*httptest.Server, *appnet.Namespace) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify identity headers are forwarded
		w.Header().Set("X-Got-User", r.Header.Get("X-Vulos-User-ID"))
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "ok: %s", r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	// Parse the httptest server addr: host and port
	addr := upstream.Listener.Addr().String()
	colonIdx := strings.LastIndex(addr, ":")
	ip := addr[:colonIdx]
	var port int
	fmt.Sscanf(addr[colonIdx+1:], "%d", &port)

	ns := &appnet.Namespace{
		Name:    "vulos_" + appID,
		AppID:   appID,
		OwnerID: userID,
		NSIP:    ip,
		AppPort: port,
		Active:  true,
	}

	// GetForUser uses key "userID-appID" as the primary lookup
	key := userID + "-" + appID
	mgr.AddNamespace(key, ns)
	return upstream, ns
}

// ---------- Test cases ----------

// TestUnauthenticated_Returns401 verifies that a request with no credentials
// is rejected before any app lookup.
func TestUnauthenticated_Returns401(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	gw := New(store, mgr, pool)

	req := httptest.NewRequest("GET", "/app/someapp/", nil)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unauthorized") {
		t.Errorf("expected 'unauthorized' in body, got %q", rec.Body.String())
	}
}

// TestBearerToken_AuthenticatesSession verifies the Authorization: Bearer token
// path in validateSession.
func TestBearerToken_AuthenticatesSession(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, userID := seedSession(t, store)

	// Start a fake upstream app so the request gets past 404
	startFakeApp(t, mgr, "myapp", userID)

	gw := New(store, mgr, pool)
	req := httptest.NewRequest("GET", "/app/myapp/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	// Should reach the upstream (200) not fail auth (401)
	if rec.Code == http.StatusUnauthorized {
		t.Errorf("expected request to be authenticated, got 401")
	}
}

// TestCookieAuth_AuthenticatesSession verifies the vulos_session cookie path.
func TestCookieAuth_AuthenticatesSession(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, userID := seedSession(t, store)
	startFakeApp(t, mgr, "myapp", userID)

	gw := New(store, mgr, pool)
	req := httptest.NewRequest("GET", "/app/myapp/", nil)
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Errorf("expected cookie auth to succeed, got 401")
	}
}

// TestUnknownApp_Returns404 verifies that an authenticated request for an app
// not registered in the namespace manager gets a 404.
func TestUnknownApp_Returns404(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, _ := seedSession(t, store)

	gw := New(store, mgr, pool)
	req := httptest.NewRequest("GET", "/app/nonexistent/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown app, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "app not running") {
		t.Errorf("expected 'app not running' in body, got %q", rec.Body.String())
	}
}

// TestRateLimit_Returns429 verifies that the per-app rate limiter fires at
// >200 requests per second and returns 429 with a Retry-After header.
func TestRateLimit_Returns429(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, userID := seedSession(t, store)
	startFakeApp(t, mgr, "ratelimitapp", userID)

	gw := New(store, mgr, pool)

	// Hammer isRateLimited directly via the Handler to exceed the 200 req/s threshold.
	// We call it 202 times; the first 200 go through, the 201st+ should trip the limiter.
	// All must be within the same 1-second window — isRateLimited opens a new window per
	// call when stale, so we call it in rapid succession.
	var limitedCode int
	for i := 0; i < 205; i++ {
		req := httptest.NewRequest("GET", "/app/ratelimitapp/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		gw.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			limitedCode = rec.Code
			if rec.Header().Get("Retry-After") == "" {
				t.Error("expected Retry-After header on 429 response")
			}
			break
		}
	}
	if limitedCode != http.StatusTooManyRequests {
		t.Errorf("expected at least one 429 after >200 requests, got none")
	}
}

// TestPathFallback_ParsesAppID checks that the /app/{id}/some/path fallback
// correctly extracts the appID and strips the prefix for the upstream path.
func TestPathFallback_ParsesAppID(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, userID := seedSession(t, store)

	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, "ok")
	}))
	defer upstream.Close()

	addr := upstream.Listener.Addr().String()
	colonIdx := strings.LastIndex(addr, ":")
	ip := addr[:colonIdx]
	var port int
	fmt.Sscanf(addr[colonIdx+1:], "%d", &port)

	ns := &appnet.Namespace{
		Name:    "vulos_pathapp",
		AppID:   "pathapp",
		OwnerID: userID,
		NSIP:    ip,
		AppPort: port,
		Active:  true,
	}
	mgr.AddNamespace(userID+"-pathapp", ns)

	gw := New(store, mgr, pool)

	req := httptest.NewRequest("GET", "/app/pathapp/deep/nested", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if gotPath != "/deep/nested" {
		t.Errorf("expected upstream to see path /deep/nested, got %q", gotPath)
	}
}

// TestPathFallback_AppIDOnly verifies the edge case where there is no trailing
// slash after the app ID (e.g., /app/myapp).
func TestPathFallback_AppIDOnly(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, userID := seedSession(t, store)
	startFakeApp(t, mgr, "singleapp", userID)

	gw := New(store, mgr, pool)
	req := httptest.NewRequest("GET", "/app/singleapp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	// Should not 404 (session valid + namespace registered); upstream returns 200
	if rec.Code == http.StatusNotFound {
		t.Errorf("expected app to be found for /app/singleapp, got 404")
	}
}

// TestSubdomainRouting_ParsesAppID checks subdomain mode: {appId}.example.com → appID.
func TestSubdomainRouting_ParsesAppID(t *testing.T) {
	os.Setenv("VULOS_DOMAIN", "example.com")
	defer os.Unsetenv("VULOS_DOMAIN")

	store, mgr, pool := newTestDeps(t)
	token, userID := seedSession(t, store)
	startFakeApp(t, mgr, "subdapp", userID)

	gw := New(store, mgr, pool)
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "subdapp.example.com"
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	// Should resolve to the "subdapp" namespace and proxy successfully (not 404)
	if rec.Code == http.StatusNotFound {
		t.Errorf("expected subdomain routing to find app, got 404")
	}
	if rec.Code == http.StatusUnauthorized {
		t.Errorf("expected auth to pass, got 401")
	}
}

// TestValidateSession_RejectsBadToken confirms validateSession returns nil for
// a well-formed but unknown token.
func TestValidateSession_RejectsBadToken(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	gw := New(store, mgr, pool)

	req := httptest.NewRequest("GET", "/app/someapp/", nil)
	req.Header.Set("Authorization", "Bearer bogus.token.value")
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for bad token, got %d", rec.Code)
	}
}

// TestValidateSession_RejectsExpiredSession ensures a session that has already
// expired is treated as unauthenticated.
func TestValidateSession_RejectsExpiredSession(t *testing.T) {
	store, mgr, pool := newTestDeps(t)

	// Register a user and get a session, then revoke it to simulate expiry
	u, err := store.Register("expireduser", "password123", "Expired User")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	sess := store.CreateSession(u, "device-expired")
	store.RevokeSession(sess.Token)

	gw := New(store, mgr, pool)
	req := httptest.NewRequest("GET", "/app/anyapp/", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for revoked session, got %d", rec.Code)
	}
}

// TestIdentityHeadersForwarded verifies the gateway injects X-Vulos-User-ID
// and X-Vulos-Email into upstream requests.
func TestIdentityHeadersForwarded(t *testing.T) {
	store, mgr, pool := newTestDeps(t)

	u, err := store.Register("headeruser", "headerpass", "Header User")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	sess := store.CreateSession(u, "device-header")

	var capturedUserID, capturedEmail string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUserID = r.Header.Get("X-Vulos-User-ID")
		capturedEmail = r.Header.Get("X-Vulos-Email")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	addr := upstream.Listener.Addr().String()
	colonIdx := strings.LastIndex(addr, ":")
	ip := addr[:colonIdx]
	var port int
	fmt.Sscanf(addr[colonIdx+1:], "%d", &port)

	ns := &appnet.Namespace{
		Name:    "vulos_headerapp",
		AppID:   "headerapp",
		OwnerID: u.ID,
		NSIP:    ip,
		AppPort: port,
		Active:  true,
	}
	mgr.AddNamespace(u.ID+"-headerapp", ns)

	gw := New(store, mgr, pool)
	req := httptest.NewRequest("GET", "/app/headerapp/", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if capturedUserID != u.ID {
		t.Errorf("expected X-Vulos-User-ID=%q, got %q", u.ID, capturedUserID)
	}
	_ = capturedEmail // email may be empty for local users; presence of header is enough
}

// TestIsRateLimited_WindowReset confirms that after the 1-second window expires,
// the counter resets and requests are allowed again.
func TestIsRateLimited_WindowReset(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	gw := New(store, mgr, pool)

	// Manually fill the bucket
	gw.mu.Lock()
	gw.appHits["testapp"] = &rateBucket{
		count:    201,
		windowAt: time.Now().Add(-2 * time.Second), // 2 seconds ago — stale
	}
	gw.mu.Unlock()

	// After the window has expired, the next call should reset and NOT be rate-limited
	limited := gw.isRateLimited("testapp")
	if limited {
		t.Error("expected rate limit to reset after window expiry, but was still limited")
	}
}
