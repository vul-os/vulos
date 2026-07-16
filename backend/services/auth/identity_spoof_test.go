package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// identity_spoof_test.go — SEC-A / C1 regression: the auth Middleware strips any
// client-supplied X-User-ID / X-User-Email BEFORE it derives identity from a
// verified credential, so a caller can never impersonate another account by
// simply setting the header the entire OS API trusts. Every downstream handler
// (Files control plane, browser-profiles, missions, the assistant, storage) reads
// identity from X-User-ID; if a raw request header reached them, it would be a
// total, box-wide identity-forgery hole.
//
// Until now this was guaranteed only "structurally" by the two Del() calls at the
// top of Middleware, with no test pinning it. A refactor that reordered the gate,
// moved the deletes below the session lookup, or dropped them would silently
// reintroduce the hole. These tests encode the attacker directly.

// echoIdentityHandler reports the identity headers the request carries when it
// finally reaches the application layer — i.e. AFTER Middleware has run.
func echoIdentityHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-User", r.Header.Get("X-User-ID"))
		w.Header().Set("X-Seen-Email", r.Header.Get("X-User-Email"))
		w.WriteHeader(http.StatusOK)
	})
}

// TestMiddleware_SpoofedIdentityStrippedOnPublicPath sends a forged
// X-User-ID/X-User-Email on a PUBLIC path (which the session gate lets through to
// the app layer, so we can observe exactly what survives). The downstream handler
// must see NEITHER header — both are deleted before any identity logic runs.
func TestMiddleware_SpoofedIdentityStrippedOnPublicPath(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewHandler(store)
	mw := h.Middleware(echoIdentityHandler())

	// /assets/ is a public prefix (static assets), so the request passes through —
	// the ideal probe for what reaches the app layer.
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req.Header.Set("X-User-ID", "victim-owner-id")
	req.Header.Set("X-User-Email", "victim@example.com")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("public path should pass through, got %d", rr.Code)
	}
	if got := rr.Header().Get("X-Seen-User"); got != "" {
		t.Fatalf("SEC-A REGRESSION: spoofed X-User-ID survived to the app layer as %q", got)
	}
	if got := rr.Header().Get("X-Seen-Email"); got != "" {
		t.Fatalf("SEC-A REGRESSION: spoofed X-User-Email survived to the app layer as %q", got)
	}
}

// TestMiddleware_SpoofedUserIDRejectedOnProtectedPath fires the same forgery at a
// SESSION-GATED path with no real credential. It must be rejected outright (401):
// a spoofed header can never satisfy the auth gate.
func TestMiddleware_SpoofedUserIDRejectedOnProtectedPath(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewHandler(store)
	mw := h.Middleware(echoIdentityHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/files/list", nil)
	req.Header.Set("X-User-ID", "victim-owner-id")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("SEC-A REGRESSION: spoofed X-User-ID on a protected path was not rejected (got %d, want 401)", rr.Code)
	}
	if got := rr.Header().Get("X-Seen-User"); got != "" {
		t.Fatalf("SEC-A REGRESSION: a rejected request still leaked identity %q to the app layer", got)
	}
}

// TestMiddleware_SessionIdentityOverridesSpoofedHeader is the ride-along attack: a
// caller holds a VALID session for Alice but also spoofs X-User-ID=Victim, trying
// to act as Victim under Alice's authenticated request. The downstream handler
// must see Alice — never Victim — because the forged header is deleted before the
// session is resolved, so the session-derived identity always wins.
func TestMiddleware_SessionIdentityOverridesSpoofedHeader(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	alice, err := store.Register("alice", "password-alice-123", "Alice")
	if err != nil {
		t.Fatalf("register alice: %v", err)
	}
	victim, err := store.Register("victim", "password-victim-123", "Victim")
	if err != nil {
		t.Fatalf("register victim: %v", err)
	}
	sess := store.CreateSession(alice, "device-a")

	h := NewHandler(store)
	mw := h.Middleware(echoIdentityHandler())

	// GET so the cookie-CSRF check (POST/PUT/PATCH/DELETE only) does not apply.
	req := httptest.NewRequest(http.MethodGet, "/api/files/list", nil)
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: sess.Token})
	req.Header.Set("X-User-ID", victim.ID) // attacker rides Alice's session as Victim
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("valid session should pass, got %d", rr.Code)
	}
	got := rr.Header().Get("X-Seen-User")
	if got == victim.ID {
		t.Fatalf("SEC-A REGRESSION: session request was hijacked to the spoofed victim id %q", victim.ID)
	}
	if got != alice.ID {
		t.Fatalf("SEC-A REGRESSION: downstream identity = %q, want session owner alice %q", got, alice.ID)
	}
}
