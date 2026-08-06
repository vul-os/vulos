package main

// lan_pairing_test.go — PAIR-01: the LAN pairing endpoint must never be
// reachable without a session.
//
// The SPKI fingerprint itself is not secret — any TLS client on the LAN sees
// it in the handshake. The reason this route is gated is different and worse
// than disclosure: an UNAUTHENTICATED pairing surface is an invitation to pin
// the wrong box. If an attacker can serve a pairing payload, they can get a
// victim's client to pin THEIR certificate, and every later connection then
// authenticates successfully against the attacker. Pinning converts a
// first-contact decision into a permanent one, so the first contact is exactly
// what has to be protected.
//
// services/security_test.go's SEC-HARD-08 already asserts publicPaths is an
// exhaustive allow-list, which makes gating the DEFAULT for any new route.
// That is a structural argument. This test is the empirical one: it drives a
// real request through the real middleware at the real path and asserts 401.
// The two fail for different reasons — SEC-HARD-08 breaks if someone adds this
// path to publicPaths, this breaks if the route is ever registered outside the
// middleware — so both are worth having.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"vulos/backend/internal/config"
	"vulos/backend/services/auth"
)

// TestLANPairingRoute_RequiresSession asserts GET /api/lan/pairing returns 401
// with no credentials.
//
// certSrc is deliberately nil: if the gate works the handler never runs, so a
// nil source cannot be dereferenced. That makes this test fail LOUDLY (nil
// panic) rather than quietly if the route ever stops being gated and the
// handler starts executing — a 500 from a nil deref would also fail the 401
// assertion, but the panic names the cause.
func TestLANPairingRoute_RequiresSession(t *testing.T) {
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	handler := auth.NewHandler(store)
	handler.OnUserCreated = nil
	handler.OnUserLogin = nil
	handler.OnRoleChanged = nil

	mux := http.NewServeMux()
	handler.Register(mux)
	registerLANPairingRoutes(mux, &config.Config{Hostname: "testbox"}, nil)

	srv := httptest.NewServer(handler.Middleware(mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/lan/pairing")
	if err != nil {
		t.Fatalf("GET /api/lan/pairing: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("PAIR-01 REGRESSION: GET /api/lan/pairing returned %d without a session, want 401 — "+
			"an unauthenticated pairing surface lets an attacker get a client to pin the wrong box",
			resp.StatusCode)
	}
}
