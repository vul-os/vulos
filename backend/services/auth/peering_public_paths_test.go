package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// makePeeringGateHandler returns the auth Middleware wrapping an echo handler
// that reports 200 when a request is ALLOWED through (i.e. not 401'd by the
// session gate). No user/session is seeded, so any request without an
// authenticated X-User-ID either passes (public path) or is 401'd.
func makePeeringGateHandler(t *testing.T) http.Handler {
	t.Helper()
	store := &Store{
		users:    make(map[string]*User),
		sessions: make(map[string]*Session),
		profiles: make(map[string]*Profile),
		secret:   []byte("test-secret-peering"),
	}
	h := NewHandler(store)
	echo := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return h.Middleware(echo)
}

// TestPeeringS2SRoutesAreSessionless is the regression guard for the box-to-box
// reachability fix: a remote peer has NO OS session on this box, so every
// server-to-server peering route MUST pass the OS session gate and reach its own
// downstream authentication (InboundMiddleware signature/contacts/revocation,
// Ed25519-signed relay, prekey-signature, public well-known). Before the fix,
// these were 401'd by the session middleware BEFORE their real gate ran, which
// made all box-to-box peering dead in production.
func TestPeeringS2SRoutesAreSessionless(t *testing.T) {
	mw := makePeeringGateHandler(t)

	// method, path — each must NOT be 401'd by the session gate (unauthenticated).
	s2s := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/.well-known/vula-id"},
		{http.MethodPost, "/api/peering/inbound/message"},
		{http.MethodPost, "/api/peering/inbound/request"},
		{http.MethodPost, "/api/peering/inbound/signal"},
		{http.MethodPost, "/api/peering/inbound/media"},
		{http.MethodPost, "/api/peering/inbound/group-message"},
		{http.MethodPost, "/api/peering/inbound/collab-update"},
		{http.MethodPost, "/api/peering/inbound/collab-invite"},
		{http.MethodPost, "/api/peering/inbound/presence"},
		{http.MethodPost, "/api/peering/inbound/drop"},
		{http.MethodPost, "/api/peering/relay/deposit"},
		{http.MethodGet, "/api/peering/relay/pickup"},
		{http.MethodPost, "/api/peering/relay/ack"},
		{http.MethodGet, "/api/peering/relay/attest"},
		{http.MethodPost, "/api/peering/prekeys/claim"},
		{http.MethodPost, "/api/peering/prekeys/publish"},
		{http.MethodPost, "/api/peering/profile/notify-change"},
	}

	for _, tc := range s2s {
		// application/json content-type keeps the CSRF check (cookie-auth only)
		// irrelevant; there is no cookie here anyway.
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, req)
		if w.Code == http.StatusUnauthorized {
			t.Errorf("S2S route %s %s was 401'd by the session gate — box-to-box peering is unreachable for this route", tc.method, tc.path)
		}
	}
}

// TestPeeringClientRoutesStaySessionGated is the counterpart guard: the
// CLIENT-facing peering routes (browser → own box) must STAY session-authed. A
// request with no authenticated session must be 401'd. This prevents the fix
// from over-exposing owner-only surfaces (contacts, conversations, groups,
// calls, media upload, identity export/import/revoke, profile PUT, collab WS,
// discover).
func TestPeeringClientRoutesStaySessionGated(t *testing.T) {
	mw := makePeeringGateHandler(t)

	gated := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/peering/contacts"},
		{http.MethodPost, "/api/peering/contacts/request"},
		{http.MethodPost, "/api/peering/contacts/approve/vula:ed25519:abc"},
		{http.MethodGet, "/api/peering/conversations"},
		{http.MethodPost, "/api/peering/conversations/c1/send"},
		{http.MethodPost, "/api/peering/groups"},
		{http.MethodPost, "/api/peering/call/initiate"},
		{http.MethodPost, "/api/peering/media/upload"},
		{http.MethodPost, "/api/peering/identity/export"},
		{http.MethodPost, "/api/peering/identity/import"},
		{http.MethodPost, "/api/peering/identity/revoke"},
		{http.MethodPut, "/api/peering/profile"},
		{http.MethodGet, "/api/peering/discover"},
		{http.MethodGet, "/api/peering/profile/vula:ed25519:abc"}, // client fetch-and-cache proxy
	}

	for _, tc := range gated {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("client-facing route %s %s returned %d, want 401 (must stay OS-session-authed)", tc.method, tc.path, w.Code)
		}
	}
}

// TestPeeringPublicPrefix_NoTraversalBypass is the path-traversal auth-bypass
// guard for the /api/peering/inbound/ public PREFIX. A request whose raw path
// prefix-matches the public inbound subtree but resolves (after ServeMux path
// cleaning) to a SESSION-GATED peering route must NOT skip the session gate.
// isPublicPath must normalise the path before the prefix decision so it matches
// the path ServeMux will actually route to.
func TestPeeringPublicPrefix_NoTraversalBypass(t *testing.T) {
	mw := makePeeringGateHandler(t)

	// Each of these raw paths prefix-matches "/api/peering/inbound/" literally but
	// cleans to a gated conversations/contacts route — must still be 401 (gated).
	traversals := []string{
		"/api/peering/inbound/../conversations/c1/send",
		"/api/peering/inbound/x/../../conversations",
		"/api/peering/inbound/../contacts/approve/vula:ed25519:abc",
	}
	for _, p := range traversals {
		req := httptest.NewRequest(http.MethodPost, p, nil)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("path-traversal %q bypassed the session gate (got %d, want 401)", p, w.Code)
		}
	}
}

// TestPublicPrefixesStillMatch confirms the path-cleaning hardening did NOT
// break legitimate public-prefix routes (a real request under a public prefix
// must still be treated as public).
func TestPublicPrefixesStillMatch(t *testing.T) {
	cases := map[string]bool{
		"/api/peering/inbound/message": true,  // real inbound route — public
		"/assets/app.js":               true,  // static — public
		"/api/peering/conversations":   false, // gated
		"/api/apps/running":            false, // apps API — gated
	}
	for p, want := range cases {
		if got := isPublicPath(p); got != want {
			t.Errorf("isPublicPath(%q) = %v, want %v", p, got, want)
		}
	}
}
