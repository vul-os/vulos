package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/appnet"
)

// echoApp starts an upstream that echoes the X-Vulos-* headers it RECEIVED back
// as X-Echo-* response headers, so a test can assert exactly what the gateway
// forwarded to the app.
func echoApp(t *testing.T, mgr *appnet.Manager, appID, ownerID string) *appnet.Namespace {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo-User", r.Header.Get("X-Vulos-User-ID"))
		w.Header().Set("X-Echo-Session", r.Header.Get("X-Vulos-Session"))
		w.Header().Set("X-Echo-Email", r.Header.Get("X-Vulos-Email"))
		fmt.Fprint(w, "ok")
	}))
	t.Cleanup(upstream.Close)

	addr := upstream.Listener.Addr().String()
	colon := strings.LastIndex(addr, ":")
	ip := addr[:colon]
	var port int
	fmt.Sscanf(addr[colon+1:], "%d", &port)

	ns := &appnet.Namespace{
		Name: "vulos_" + appID, AppID: appID, OwnerID: ownerID,
		NSIP: ip, AppPort: port, Active: true,
	}
	mgr.AddNamespace(ownerID+"-"+appID, ns)
	return ns
}

// TestPublic_StripsSpoofedIdentityAndInjectsNone verifies Finding 1: the public
// path strips attacker-supplied X-Vulos-* headers and injects NO identity, so an
// app can never be identity-spoofed by an anonymous public visitor.
func TestPublic_StripsSpoofedIdentityAndInjectsNone(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	echoApp(t, mgr, "notes", "owner1")

	gw := New(store, mgr, pool)
	gw.SetPublicVisibility(func(appID string) bool { return appID == "notes" })

	req := httptest.NewRequest("GET", appnet.PubwebPathPrefix+"notes/page", nil)
	// Attacker attempts to spoof identity + a raw session over the public edge.
	req.Header.Set("X-Vulos-User-ID", "victim-admin")
	req.Header.Set("X-Vulos-Session", "stolen-session")
	req.Header.Set("X-Vulos-Email", "admin@example.com")
	rec := httptest.NewRecorder()
	gw.PublicHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Echo-User"); got != "" {
		t.Errorf("app received X-Vulos-User-ID=%q on public path — identity spoof not stripped", got)
	}
	if got := rec.Header().Get("X-Echo-Session"); got != "" {
		t.Errorf("app received X-Vulos-Session=%q on public path — must inject none", got)
	}
	if got := rec.Header().Get("X-Echo-Email"); got != "" {
		t.Errorf("app received X-Vulos-Email=%q on public path", got)
	}
}

// TestPublic_PrivateAppNotServed verifies a non-public app is 404 on the public
// path even though its namespace is running (opt-in enforcement).
func TestPublic_PrivateAppNotServed(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	echoApp(t, mgr, "secret", "owner1")

	gw := New(store, mgr, pool)
	gw.SetPublicVisibility(func(appID string) bool { return false }) // nothing public

	req := httptest.NewRequest("GET", appnet.PubwebPathPrefix+"secret/", nil)
	rec := httptest.NewRecorder()
	gw.PublicHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("private app served on public path: got %d, want 404", rec.Code)
	}
}

// TestPublic_NilVisibilityFailsClosed verifies that with no visibility resolver
// installed, nothing is public.
func TestPublic_NilVisibilityFailsClosed(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	echoApp(t, mgr, "notes", "owner1")
	gw := New(store, mgr, pool)
	// no SetPublicVisibility call

	req := httptest.NewRequest("GET", appnet.PubwebPathPrefix+"notes/", nil)
	rec := httptest.NewRecorder()
	gw.PublicHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 fail-closed with nil visibility, got %d", rec.Code)
	}
}

// TestPerAppSession_DerivedNotRaw verifies Finding 3: the injected X-Vulos-Session
// is a per-app derived correlator, never the raw session id, and differs across
// apps for the same session.
func TestPerAppSession_DerivedNotRaw(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	gw := New(store, mgr, pool)

	const sessionID = "raw-session-abc123"
	a := gw.perAppSession(sessionID, "app-a")
	b := gw.perAppSession(sessionID, "app-b")

	if a == sessionID || b == sessionID {
		t.Fatalf("perAppSession leaked the raw session id (a=%q b=%q)", a, b)
	}
	if a == b {
		t.Fatalf("perAppSession must differ across apps: got %q for both", a)
	}
	if a == "" || b == "" {
		t.Fatalf("perAppSession returned empty (a=%q b=%q)", a, b)
	}
	// Deterministic per (session, app).
	if a2 := gw.perAppSession(sessionID, "app-a"); a2 != a {
		t.Fatalf("perAppSession not stable: %q != %q", a2, a)
	}
	// Different process salt ⇒ different value (unlinkable across boots).
	gw2 := New(store, mgr, pool)
	if gw2.perAppSession(sessionID, "app-a") == a {
		t.Fatalf("perAppSession must depend on the per-process salt")
	}
}

// TestAdoptedPort_GetsAudienceBoundIdentityNeverRawSession verifies that a port
// adopted via the external-upstream registry is served THROUGH the full gateway
// pipeline: the app receives the real user id plus a DERIVED session correlator
// (never the raw session), proving adopt-a-port reuses the same identity seam.
func TestAdoptedPort_GetsAudienceBoundIdentityNeverRawSession(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, userID := seedSession(t, store)

	// Stand up an upstream and register it as an ADOPTED loopback port (no real
	// namespace exists for this appID).
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo-User", r.Header.Get("X-Vulos-User-ID"))
		w.Header().Set("X-Echo-Session", r.Header.Get("X-Vulos-Session"))
		fmt.Fprint(w, "ok")
	}))
	t.Cleanup(upstream.Close)
	addr := upstream.Listener.Addr().String()
	colon := strings.LastIndex(addr, ":")
	var port int
	fmt.Sscanf(addr[colon+1:], "%d", &port)

	if err := mgr.RegisterExternalUpstream(appnet.ExternalUpstream{
		AppID: "adopted", OwnerID: userID, Profile: "default", Port: port,
	}); err != nil {
		t.Fatalf("RegisterExternalUpstream: %v", err)
	}

	// The synthetic namespace points at 127.0.0.1:port; override NSIP via a real
	// namespace insertion is unnecessary because httptest binds 127.0.0.1.
	gw := New(store, mgr, pool)
	req := httptest.NewRequest("GET", "/app/adopted/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	// Attacker-style spoof attempt through the authed path too.
	req.Header.Set("X-Vulos-User-ID", "spoofed")
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("adopted port not served through gateway: got %d (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Echo-User"); got != userID {
		t.Errorf("adopted app got X-Vulos-User-ID=%q, want authenticated user %q (spoof leaked or identity missing)", got, userID)
	}
	sess := rec.Header().Get("X-Echo-Session")
	if sess == "" {
		t.Errorf("adopted app got no X-Vulos-Session; expected a derived correlator")
	}
	if sess == token {
		t.Errorf("adopted app received the RAW session token as X-Vulos-Session")
	}
	// The derived correlator is a 32-char hex truncation of an HMAC — assert the
	// shape so we know the app got the derived value, not any raw identifier.
	if len(sess) != 32 {
		t.Errorf("X-Vulos-Session = %q (len %d); want the 32-char derived correlator", sess, len(sess))
	}
}
