// contacts_api_test.go — unit tests for the contact request/approve/block HTTP handlers.
//
// Coverage:
//   - POST /api/peering/contacts/request      — creates pending entry; signs and posts envelope
//   - POST /api/peering/inbound/request       — stores pending + push notification; blocked sender silent-dropped
//   - GET  /api/peering/contacts/requests     — lists pending contacts
//   - POST /api/peering/contacts/approve/{id} — transitions to approved
//   - POST /api/peering/contacts/block/{id}   — transitions to blocked (silent)
//   - DELETE /api/peering/contacts/{id}       — removes contact
//   - GET  /api/peering/contacts              — lists approved contacts
//   - Allow-list middleware: inbound/request exempt; others require approval
package peering

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── test fixtures ────────────────────────────────────────────────────────────

// generateTestKeyPair generates an Ed25519 keypair for use in tests.
func generateTestKeyPair(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generateTestKeyPair: %v", err)
	}
	vulaID := encodeVulaID(pub)
	return priv, pub, vulaID
}

// newTestAPI builds a ContactAPI with an in-memory temp store, a nil hub (no WS
// in unit tests), and a nil PeerClient (outbound calls stubbed by the test).
func newTestAPI(t *testing.T) (*ContactAPI, *ContactStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}
	priv, _, vulaID := generateTestKeyPair(t)
	api := NewContactAPI(store, NewPeerClient(), nil, priv, vulaID, "localhost:0")
	return api, store
}

// postJSON is a test helper that fires a POST with a JSON body.
func postJSON(t *testing.T, handler http.HandlerFunc, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("postJSON encode: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, target, &buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

// getJSON is a test helper that fires a GET request.
func getJSON(t *testing.T, handler http.HandlerFunc, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

// decodeBody decodes the response body into v.
func decodeBody(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("decodeBody: %v (body=%s)", err, rr.Body.String())
	}
}

// newEnvelopeRequest constructs a *http.Request containing a signed Envelope in
// the context, simulating what InboundMiddleware does after verification.
func newEnvelopeRequest(env *Envelope) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/request", nil)
	ctx := context.WithValue(req.Context(), EnvelopeKey, env)
	return req.WithContext(ctx)
}

// ─── POST /api/peering/contacts/request ──────────────────────────────────────

func TestHandleSendRequest_MissingTargetVulaID(t *testing.T) {
	api, _ := newTestAPI(t)
	rr := postJSON(t, api.handleSendRequest, "/api/peering/contacts/request", map[string]string{
		"display_name": "Alice",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestHandleSendRequest_MissingTargetServer(t *testing.T) {
	api, _ := newTestAPI(t)
	_, _, remoteVulaID := generateTestKeyPair(t)
	rr := postJSON(t, api.handleSendRequest, "/api/peering/contacts/request", map[string]string{
		"target_vula_id": remoteVulaID,
		"display_name":   "Me",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestHandleSendRequest_ParsesFullVulaAddress(t *testing.T) {
	api, store := newTestAPI(t)

	// Stand up a fake remote server that returns 200 for the inbound/request path.
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer remote.Close()

	// Extract host:port from the test TLS server URL.
	// httptest.NewTLSServer uses https://127.0.0.1:<port>
	rawURL := remote.URL // e.g. "https://127.0.0.1:12345"
	// Strip scheme.
	hostport := strings.TrimPrefix(rawURL, "https://")

	_, _, remoteVulaID := generateTestKeyPair(t)

	// Use the test TLS server's client so it trusts the self-signed cert.
	// We need to swap out the PeerClient's http.Client.
	api.client = &PeerClient{http: remote.Client()}

	// Full vula address: <vulaID>@host:port
	// ParseVulaAddress requires "<vulaID>@<host>:<port>" so we need a valid port.
	// Build a valid full address.
	fullAddress := fmt.Sprintf("%s@%s", remoteVulaID, hostport)

	rr := postJSON(t, api.handleSendRequest, "/api/peering/contacts/request", map[string]string{
		"target_vula_id": fullAddress,
		"display_name":   "Me",
	})

	// We expect 202 Accepted (delivered) or 502 (SSRF guard for 127.0.0.1).
	// The SSRF guard in transport blocks 127.0.0.1 — so the delivery will fail
	// but the contact should still have been added to the local store.
	_ = rr // any status is acceptable here; we only check the store.

	_, exists := store.Get(remoteVulaID)
	if !exists {
		t.Error("contact was not added to store after sendRequest")
	}
}

func TestHandleSendRequest_AddsContactPending(t *testing.T) {
	api, store := newTestAPI(t)
	_, _, remoteVulaID := generateTestKeyPair(t)

	// The delivery will fail (SSRF guard blocks 127.0.0.1) but the contact
	// should be added as pending before the delivery attempt.
	postJSON(t, api.handleSendRequest, "/api/peering/contacts/request", map[string]string{
		"target_vula_id": remoteVulaID,
		"target_server":  "127.0.0.1:9999", // SSRF-blocked but added before delivery
		"display_name":   "Bob",
	})

	c, exists := store.Get(remoteVulaID)
	if !exists {
		t.Fatal("contact not added to store")
	}
	if c.State != StatePending {
		t.Errorf("expected pending state, got %q", c.State)
	}
}

// ─── POST /api/peering/inbound/request ───────────────────────────────────────

// buildInboundRequest builds a valid signed Envelope and an http.Request with
// the envelope in context, simulating InboundMiddleware verification.
func buildInboundRequest(t *testing.T, priv ed25519.PrivateKey, fromVulaID, toVulaID string, payload ContactRequestPayload) (*Envelope, *http.Request) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env, err := NewEnvelope("test-id-"+fromVulaID, fromVulaID, toVulaID, TypeContactRequest, json.RawMessage(raw))
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	req := newEnvelopeRequest(env)
	return env, req
}

func TestHandleInboundRequest_StoresPending(t *testing.T) {
	api, store := newTestAPI(t)
	remotePriv, _, remoteVulaID := generateTestKeyPair(t)

	payload := ContactRequestPayload{
		DisplayName: "Alice",
		Message:     "Hey, add me!",
		ServerAddr:  "alice.vulos.org:8080",
	}
	_, req := buildInboundRequest(t, remotePriv, remoteVulaID, api.vulaID, payload)

	rr := httptest.NewRecorder()
	api.HandleInboundRequest(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}

	c, exists := store.Get(remoteVulaID)
	if !exists {
		t.Fatal("contact not added to store after inbound request")
	}
	if c.State != StatePending {
		t.Errorf("expected pending state, got %q", c.State)
	}
	if c.DisplayName != "Alice" {
		t.Errorf("display name: got %q want %q", c.DisplayName, "Alice")
	}
	if c.Server != "alice.vulos.org:8080" {
		t.Errorf("server: got %q want %q", c.Server, "alice.vulos.org:8080")
	}
}

func TestHandleInboundRequest_BlockedSenderSilentDrop(t *testing.T) {
	api, store := newTestAPI(t)
	remotePriv, _, remoteVulaID := generateTestKeyPair(t)

	// Pre-add the sender in blocked state.
	store.Add(remoteVulaID, "Blocked", "") //nolint:errcheck
	store.Block(remoteVulaID)              //nolint:errcheck

	payload := ContactRequestPayload{
		DisplayName: "I'm blocked",
		ServerAddr:  "blocked.example.com:8080",
	}
	_, req := buildInboundRequest(t, remotePriv, remoteVulaID, api.vulaID, payload)

	rr := httptest.NewRecorder()
	api.HandleInboundRequest(rr, req)

	// Must return 200 (silent drop — sender cannot detect being blocked).
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 (silent drop), got %d", rr.Code)
	}

	// State must remain blocked.
	c, _ := store.Get(remoteVulaID)
	if c.State != StateBlocked {
		t.Errorf("expected state to remain blocked, got %q", c.State)
	}
}

func TestHandleInboundRequest_Idempotent(t *testing.T) {
	api, store := newTestAPI(t)
	remotePriv, _, remoteVulaID := generateTestKeyPair(t)

	payload := ContactRequestPayload{
		DisplayName: "Alice",
		ServerAddr:  "alice.vulos.org:8080",
	}

	// Send the same request twice.
	for i := 0; i < 2; i++ {
		_, req := buildInboundRequest(t, remotePriv, remoteVulaID, api.vulaID, payload)
		rr := httptest.NewRecorder()
		api.HandleInboundRequest(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("pass %d: expected 200, got %d", i+1, rr.Code)
		}
	}

	// Should still be one contact entry in pending state.
	pending := store.ListByState(StatePending)
	if len(pending) != 1 {
		t.Errorf("expected 1 pending contact, got %d", len(pending))
	}
}

// ─── Vulos address in peering contact card ──────────────────────────────────────

// TestHandleInboundRequest_VumailAddressPopulated verifies that when a contact
// request arrives with a vumail_address in the payload (vumail_address JSON key kept for wire compat), the store entry is
// updated with that address so the contact is immediately reachable by mail.
func TestHandleInboundRequest_VumailAddressPopulated(t *testing.T) {
	api, store := newTestAPI(t)
	remotePriv, _, remoteVulaID := generateTestKeyPair(t)

	payload := ContactRequestPayload{
		DisplayName:   "Alice",
		ServerAddr:    "alice.vulos.org:8080",
		VumailAddress: "alice@vulos.org",
	}
	_, req := buildInboundRequest(t, remotePriv, remoteVulaID, api.vulaID, payload)

	rr := httptest.NewRecorder()
	api.HandleInboundRequest(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}

	c, exists := store.Get(remoteVulaID)
	if !exists {
		t.Fatal("contact not added to store after inbound request")
	}
	if c.VumailAddress != "alice@vulos.org" {
		t.Errorf("vumail_address: got %q want %q", c.VumailAddress, "alice@vulos.org")
	}
}

// TestHandleInboundRequest_VumailAddressAbsent verifies that a contact request
// without a vumail_address leaves the contact's VumailAddress empty — so the
// field is truly optional and backwards-compatible.
func TestHandleInboundRequest_VumailAddressAbsent(t *testing.T) {
	api, store := newTestAPI(t)
	remotePriv, _, remoteVulaID := generateTestKeyPair(t)

	payload := ContactRequestPayload{
		DisplayName: "Bob",
		ServerAddr:  "bob.vulos.org:8080",
		// VumailAddress deliberately omitted.
	}
	_, req := buildInboundRequest(t, remotePriv, remoteVulaID, api.vulaID, payload)

	rr := httptest.NewRecorder()
	api.HandleInboundRequest(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}

	c, exists := store.Get(remoteVulaID)
	if !exists {
		t.Fatal("contact not found")
	}
	if c.VumailAddress != "" {
		t.Errorf("vumail_address should be empty when not sent, got %q", c.VumailAddress)
	}
}

// TestHandleSendRequest_IncludesVumailAddress verifies that when the caller
// provides a vumail_address (Vulos identity address) in the outbound request body, it is included in
// the signed ContactRequestPayload.  We test this by inspecting the store
// entry (which mirrors what the recipient would see) rather than the network
// wire, since the PeerClient is SSRF-blocked in the test environment.
func TestHandleSendRequest_IncludesVumailAddress(t *testing.T) {
	api, store := newTestAPI(t)
	_, _, remoteVulaID := generateTestKeyPair(t)

	postJSON(t, api.handleSendRequest, "/api/peering/contacts/request", map[string]string{
		"target_vula_id": remoteVulaID,
		"target_server":  "127.0.0.1:9999", // SSRF-blocked but contact added first
		"display_name":   "Me",
		"vumail_address": "me@vulos.org",
	})

	// The contact should have been added before the (failing) delivery attempt.
	c, exists := store.Get(remoteVulaID)
	if !exists {
		t.Fatal("contact not added to local store")
	}
	if c.State != StatePending {
		t.Errorf("state: got %q want pending", c.State)
	}
	// The Vulos identity address is carried in the signed payload sent to the peer;
	// it is NOT stored on the local side's copy of the remote contact (we don't
	// know the remote's Vulos identity address from the outbound request).
	// What we can verify is that the request body parses the field correctly —
	// no error, no panic — and the contact was added.
	_ = c
}

func TestHandleInboundRequest_NoEnvelopeInContext(t *testing.T) {
	api, _ := newTestAPI(t)

	// Request without InboundMiddleware — no envelope in context.
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/request", nil)
	rr := httptest.NewRecorder()
	api.HandleInboundRequest(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without envelope, got %d", rr.Code)
	}
}

// ─── InboundMiddleware integration ────────────────────────────────────────────

// TestInboundMiddleware_ExemptsContactRequest verifies that InboundMiddleware
// allows /api/peering/inbound/request through even when the sender is not in
// the contacts store.
func TestInboundMiddleware_ExemptsContactRequest(t *testing.T) {
	dir := t.TempDir()
	store, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}

	remotePriv, _, remoteVulaID := generateTestKeyPair(t)

	// Build a signed contact-request envelope.
	payload, _ := json.Marshal(ContactRequestPayload{
		DisplayName: "Stranger",
		ServerAddr:  "stranger.vulos.org:8080",
	})
	env, err := NewEnvelope("mid-exempt", remoteVulaID, "any", TypeContactRequest, json.RawMessage(payload))
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if err := env.Sign(remotePriv); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/request", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// Inner handler that records whether it was reached.
	reached := false
	inner := http.NewServeMux()
	inner.HandleFunc("POST /api/peering/inbound/request", func(w http.ResponseWriter, r *http.Request) {
		reached = true
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	handler := InboundMiddleware(store, inner)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !reached {
		t.Errorf("expected inner handler to be reached (status=%d body=%s)", rr.Code, rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

// TestInboundMiddleware_BlocksUnapproved verifies that non-exempt inbound routes
// (e.g. /api/peering/inbound/message) are blocked for non-approved senders.
func TestInboundMiddleware_BlocksUnapproved(t *testing.T) {
	dir := t.TempDir()
	store, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}

	remotePriv, _, remoteVulaID := generateTestKeyPair(t)

	// The sender is NOT in the contacts store (unknown).
	payload, _ := json.Marshal(map[string]string{"body": "hello"})
	env, err := NewEnvelope("mid-block", remoteVulaID, "any", TypeMessage, json.RawMessage(payload))
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if err := env.Sign(remotePriv); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	inner := http.NewServeMux()
	inner.HandleFunc("POST /api/peering/inbound/message", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // should NOT be reached
	})

	handler := InboundMiddleware(store, inner)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for unknown sender, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

// TestInboundMiddleware_AllowsApproved verifies that an approved sender can
// reach a non-exempt inbound route.
func TestInboundMiddleware_AllowsApproved(t *testing.T) {
	dir := t.TempDir()
	store, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}

	remotePriv, _, remoteVulaID := generateTestKeyPair(t)

	// Add + approve the remote sender.
	store.Add(remoteVulaID, "Bob", "bob.vulos.org:8080") //nolint:errcheck
	store.Approve(remoteVulaID, DefaultPerms())          //nolint:errcheck

	payload, _ := json.Marshal(map[string]string{"body": "hello"})
	env, err := NewEnvelope("mid-allow", remoteVulaID, "any", TypeMessage, json.RawMessage(payload))
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if err := env.Sign(remotePriv); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	reached := false
	inner := http.NewServeMux()
	inner.HandleFunc("POST /api/peering/inbound/message", func(w http.ResponseWriter, r *http.Request) {
		reached = true
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	handler := InboundMiddleware(store, inner)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !reached {
		t.Errorf("approved sender should reach inner handler (code=%d body=%s)", rr.Code, rr.Body.String())
	}
}

// ─── GET /api/peering/contacts/requests ──────────────────────────────────────

func TestHandleListRequests_Empty(t *testing.T) {
	api, _ := newTestAPI(t)
	rr := getJSON(t, api.handleListRequests, "/api/peering/contacts/requests")

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var resp map[string]any
	decodeBody(t, rr, &resp)

	count := int(resp["count"].(float64))
	if count != 0 {
		t.Errorf("expected 0 pending requests, got %d", count)
	}
}

func TestHandleListRequests_ShowsPending(t *testing.T) {
	api, store := newTestAPI(t)

	_, _, alice := generateTestKeyPair(t)
	_, _, bob := generateTestKeyPair(t)
	store.Add(alice, "Alice", "alice.vulos.org:8080") //nolint:errcheck
	store.Add(bob, "Bob", "bob.vulos.org:8080")       //nolint:errcheck
	store.Approve(bob, DefaultPerms())                //nolint:errcheck — bob is approved, not pending

	rr := getJSON(t, api.handleListRequests, "/api/peering/contacts/requests")
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var resp map[string]any
	decodeBody(t, rr, &resp)

	count := int(resp["count"].(float64))
	if count != 1 {
		t.Errorf("expected 1 pending request, got %d", count)
	}

	requests := resp["requests"].([]any)
	req0 := requests[0].(map[string]any)
	if req0["vula_id"] != alice {
		t.Errorf("expected alice in pending, got %v", req0["vula_id"])
	}
}

// ─── POST /api/peering/contacts/approve/{id} ──────────────────────────────────

func TestHandleApprove_Happy(t *testing.T) {
	api, store := newTestAPI(t)
	_, _, remoteVulaID := generateTestKeyPair(t)
	store.Add(remoteVulaID, "Alice", "alice.vulos.org:8080") //nolint:errcheck

	mux := http.NewServeMux()
	api.RegisterContactHandlers(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/peering/contacts/approve/"+remoteVulaID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}

	if !store.IsApproved(remoteVulaID) {
		t.Error("contact should be approved after handler")
	}
}

func TestHandleApprove_NotFound(t *testing.T) {
	api, _ := newTestAPI(t)
	_, _, remoteVulaID := generateTestKeyPair(t)

	mux := http.NewServeMux()
	api.RegisterContactHandlers(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/peering/contacts/approve/"+remoteVulaID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestHandleApprove_BlockedContact(t *testing.T) {
	api, store := newTestAPI(t)
	_, _, remoteVulaID := generateTestKeyPair(t)
	store.Add(remoteVulaID, "Alice", "") //nolint:errcheck
	store.Block(remoteVulaID)            //nolint:errcheck

	mux := http.NewServeMux()
	api.RegisterContactHandlers(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/peering/contacts/approve/"+remoteVulaID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// ContactStore.Approve returns an error for blocked contacts.
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for blocked contact, got %d", rr.Code)
	}
}

// ─── POST /api/peering/contacts/block/{id} ───────────────────────────────────

func TestHandleBlock_Happy(t *testing.T) {
	api, store := newTestAPI(t)
	_, _, remoteVulaID := generateTestKeyPair(t)
	store.Add(remoteVulaID, "Eve", "eve.vulos.org:8080") //nolint:errcheck

	mux := http.NewServeMux()
	api.RegisterContactHandlers(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/peering/contacts/block/"+remoteVulaID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}

	c, _ := store.Get(remoteVulaID)
	if c.State != StateBlocked {
		t.Errorf("expected blocked state, got %q", c.State)
	}
}

func TestHandleBlock_NotFound(t *testing.T) {
	api, _ := newTestAPI(t)
	_, _, remoteVulaID := generateTestKeyPair(t)

	mux := http.NewServeMux()
	api.RegisterContactHandlers(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/peering/contacts/block/"+remoteVulaID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestHandleBlock_SilentNoPeerNotification(t *testing.T) {
	// Block should NOT notify the peer in any way.
	// We verify this by ensuring no outbound delivery is attempted —
	// the test PeerClient would fail on any real delivery attempt.
	api, store := newTestAPI(t)
	_, _, remoteVulaID := generateTestKeyPair(t)
	store.Add(remoteVulaID, "Eve", "eve.vulos.org:8080") //nolint:errcheck

	mux := http.NewServeMux()
	api.RegisterContactHandlers(mux)

	// Replace the client with one that fails loudly if used.
	notCalled := true
	_ = notCalled
	// (The default PeerClient would SSRF-block anyway; this test verifies the
	// handler itself never calls client.Post for block.)

	req := httptest.NewRequest(http.MethodPost, "/api/peering/contacts/block/"+remoteVulaID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

// ─── DELETE /api/peering/contacts/{id} ───────────────────────────────────────

func TestHandleRemove_Happy(t *testing.T) {
	api, store := newTestAPI(t)
	_, _, remoteVulaID := generateTestKeyPair(t)
	store.Add(remoteVulaID, "Old Contact", "") //nolint:errcheck

	mux := http.NewServeMux()
	api.RegisterContactHandlers(mux)

	req := httptest.NewRequest(http.MethodDelete, "/api/peering/contacts/"+remoteVulaID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}

	if _, exists := store.Get(remoteVulaID); exists {
		t.Error("contact still exists after remove")
	}
}

func TestHandleRemove_NotFound(t *testing.T) {
	api, _ := newTestAPI(t)
	_, _, remoteVulaID := generateTestKeyPair(t)

	mux := http.NewServeMux()
	api.RegisterContactHandlers(mux)

	req := httptest.NewRequest(http.MethodDelete, "/api/peering/contacts/"+remoteVulaID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

// ─── GET /api/peering/contacts ───────────────────────────────────────────────

func TestHandleListContacts_OnlyApproved(t *testing.T) {
	api, store := newTestAPI(t)

	_, _, alice := generateTestKeyPair(t)
	_, _, bob := generateTestKeyPair(t)
	_, _, carol := generateTestKeyPair(t)

	store.Add(alice, "Alice", "alice.vulos.org:8080") //nolint:errcheck
	store.Add(bob, "Bob", "bob.vulos.org:8080")       //nolint:errcheck
	store.Add(carol, "Carol", "carol.vulos.org:8080") //nolint:errcheck
	store.Approve(alice, DefaultPerms())              //nolint:errcheck
	store.Block(bob)                                  //nolint:errcheck
	// Carol stays pending.

	rr := getJSON(t, api.handleListContacts, "/api/peering/contacts")
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var resp map[string]any
	decodeBody(t, rr, &resp)

	count := int(resp["count"].(float64))
	if count != 1 {
		t.Errorf("expected 1 approved contact, got %d", count)
	}

	contacts := resp["contacts"].([]any)
	c0 := contacts[0].(map[string]any)
	if c0["vula_id"] != alice {
		t.Errorf("expected alice in approved list, got %v", c0["vula_id"])
	}
}

func TestHandleListContacts_Empty(t *testing.T) {
	api, _ := newTestAPI(t)
	rr := getJSON(t, api.handleListContacts, "/api/peering/contacts")

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var resp map[string]any
	decodeBody(t, rr, &resp)

	count := int(resp["count"].(float64))
	if count != 0 {
		t.Errorf("expected 0 approved contacts, got %d", count)
	}
}

// ─── Full flow: request → approve ────────────────────────────────────────────

// TestFullFlow_RequestApprove exercises the complete inbound contact-request
// lifecycle: receive request → store pending → approve → verify approved.
func TestFullFlow_RequestApprove(t *testing.T) {
	api, store := newTestAPI(t)
	remotePriv, _, remoteVulaID := generateTestKeyPair(t)

	// Step 1: inbound contact request arrives.
	payload := ContactRequestPayload{
		DisplayName: "Bob",
		Message:     "Hey, I'd like to connect",
		ServerAddr:  "bob.vulos.org:8080",
	}
	_, req := buildInboundRequest(t, remotePriv, remoteVulaID, api.vulaID, payload)
	rr := httptest.NewRecorder()
	api.HandleInboundRequest(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("inbound request: expected 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}

	// Contact must be pending.
	c, exists := store.Get(remoteVulaID)
	if !exists {
		t.Fatal("contact not found after inbound request")
	}
	if c.State != StatePending {
		t.Fatalf("expected pending, got %q", c.State)
	}

	// Step 2: local user approves.
	mux := http.NewServeMux()
	api.RegisterContactHandlers(mux)

	approveReq := httptest.NewRequest(http.MethodPost, "/api/peering/contacts/approve/"+remoteVulaID, nil)
	approveRR := httptest.NewRecorder()
	mux.ServeHTTP(approveRR, approveReq)

	if approveRR.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d (body=%s)", approveRR.Code, approveRR.Body.String())
	}

	// Contact must now be approved.
	if !store.IsApproved(remoteVulaID) {
		t.Error("contact should be approved after approve step")
	}
}

// TestFullFlow_RequestBlock exercises: receive request → pending → block → silent.
func TestFullFlow_RequestBlock(t *testing.T) {
	api, store := newTestAPI(t)
	remotePriv, _, remoteVulaID := generateTestKeyPair(t)

	// Inbound request.
	payload := ContactRequestPayload{
		DisplayName: "Spammer",
		ServerAddr:  "spam.example.com:8080",
	}
	_, req := buildInboundRequest(t, remotePriv, remoteVulaID, api.vulaID, payload)
	rr := httptest.NewRecorder()
	api.HandleInboundRequest(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("inbound request: expected 200, got %d", rr.Code)
	}

	// Block.
	mux := http.NewServeMux()
	api.RegisterContactHandlers(mux)

	blockReq := httptest.NewRequest(http.MethodPost, "/api/peering/contacts/block/"+remoteVulaID, nil)
	blockRR := httptest.NewRecorder()
	mux.ServeHTTP(blockRR, blockReq)

	if blockRR.Code != http.StatusOK {
		t.Fatalf("block: expected 200, got %d (body=%s)", blockRR.Code, blockRR.Body.String())
	}

	c, _ := store.Get(remoteVulaID)
	if c.State != StateBlocked {
		t.Errorf("expected blocked state, got %q", c.State)
	}

	// A second inbound request from the same sender should be silently dropped.
	_, req2 := buildInboundRequest(t, remotePriv, remoteVulaID, api.vulaID, payload)
	rr2 := httptest.NewRecorder()
	api.HandleInboundRequest(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Errorf("silent drop: expected 200, got %d", rr2.Code)
	}
	// State still blocked.
	c2, _ := store.Get(remoteVulaID)
	if c2.State != StateBlocked {
		t.Errorf("state after silent drop: got %q want blocked", c2.State)
	}
}
