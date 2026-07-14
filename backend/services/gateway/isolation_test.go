package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/appnet"
	"vulos/backend/services/auth"
)

// isolation_test.go — cross-account isolation through the REAL gateway Handler.
//
// The gateway resolves an app's namespace with GetForProfile(appID,
// session.UserID, profile): the user component ALWAYS comes from the validated
// session, never from anything the caller controls (path, body, or the profile
// label in the subdomain). This test drives the actual Handler end to end to pin
// that one authenticated account can never be routed into another account's
// running app namespace — the request is 404'd at dispatch and the victim app's
// bytes never leave its process.

// registerUser creates a distinct local user and an open session, returning the
// user id and session token.
func registerUser(t *testing.T, store *auth.Store, username string) (userID, token string) {
	t.Helper()
	u, err := store.Register(username, "password-"+username+"-123", username)
	if err != nil {
		t.Fatalf("Register(%s): %v", username, err)
	}
	sess := store.CreateSession(u, "device-"+username)
	return u.ID, sess.Token
}

// startMarkedApp launches an upstream app for (appID, ownerID) whose body carries
// a unique secret marker, so a test can prove whether a foreign request ever
// reached it.
func startMarkedApp(t *testing.T, mgr *appnet.Manager, appID, ownerID, marker string) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "SECRET:%s user=%s", marker, r.Header.Get("X-Vulos-User-ID"))
	}))
	t.Cleanup(upstream.Close)

	addr := upstream.Listener.Addr().String()
	colon := strings.LastIndex(addr, ":")
	ip := addr[:colon]
	var port int
	fmt.Sscanf(addr[colon+1:], "%d", &port)

	ns := &appnet.Namespace{
		Name:    "vulos_" + appID,
		AppID:   appID,
		OwnerID: ownerID,
		NSIP:    ip,
		AppPort: port,
		Active:  true,
	}
	mgr.AddNamespace(ownerID+"-"+appID, ns)
}

// TestCrossUser_CannotReachAnotherUsersAppNamespace: Alice runs "vault" (holding a
// secret). Bob, fully authenticated as himself, asks for /app/vault/. Because the
// gateway keys the namespace lookup on Bob's SESSION user id — not on the app id
// or anything Bob supplies — Bob has no "vault" namespace and gets a 404. Alice's
// secret must never appear in Bob's response.
func TestCrossUser_CannotReachAnotherUsersAppNamespace(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	aliceID, _ := registerUser(t, store, "alice")
	_, bobToken := registerUser(t, store, "bob")

	const secret = "alice-vault-contents-42"
	startMarkedApp(t, mgr, "vault", aliceID, secret)

	gw := New(store, mgr, pool)

	req := httptest.NewRequest("GET", "/app/vault/", nil)
	req.Header.Set("Authorization", "Bearer "+bobToken)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user app request should 404 at dispatch, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("ISOLATION REGRESSION: Alice's app secret leaked into Bob's response: %q", rec.Body.String())
	}
}

// TestCrossUser_OwnerReachesOwnAppNamespace is the positive control: the owner of
// the app reaches it and the gateway stamps HER id (not the raw request) onto the
// upstream identity header.
func TestCrossUser_OwnerReachesOwnAppNamespace(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	aliceID, aliceToken := registerUser(t, store, "alice")

	const secret = "alice-vault-contents-42"
	startMarkedApp(t, mgr, "vault", aliceID, secret)

	gw := New(store, mgr, pool)

	req := httptest.NewRequest("GET", "/app/vault/", nil)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("owner should reach her own app, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, secret) {
		t.Fatalf("owner request did not reach the upstream app: %q", body)
	}
	if !strings.Contains(body, "user="+aliceID) {
		t.Fatalf("gateway did not stamp the session-derived user id; body=%q", body)
	}
}

// TestCrossUser_SpoofedIdentityHeaderIgnoredByGateway: Bob authenticates as
// himself but also sets X-Vulos-User-ID=alice, hoping the gateway forwards the
// forged id (or routes on it). The gateway strips inbound X-Vulos-* and routes on
// the session id, so Bob still gets his own (absent) namespace — a 404 — and the
// spoofed id never reaches an upstream.
func TestCrossUser_SpoofedIdentityHeaderIgnoredByGateway(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	aliceID, _ := registerUser(t, store, "alice")
	_, bobToken := registerUser(t, store, "bob")

	const secret = "alice-vault-contents-42"
	startMarkedApp(t, mgr, "vault", aliceID, secret)

	gw := New(store, mgr, pool)

	req := httptest.NewRequest("GET", "/app/vault/", nil)
	req.Header.Set("Authorization", "Bearer "+bobToken)
	req.Header.Set("X-Vulos-User-ID", aliceID) // spoof
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("spoofed identity must not route into Alice's namespace, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("ISOLATION REGRESSION: spoofed X-Vulos-User-ID reached Alice's app: %q", rec.Body.String())
	}
}
