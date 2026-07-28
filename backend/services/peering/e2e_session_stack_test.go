// e2e_session_stack_test.go — full-stack box-to-box delivery through BOTH the OS
// session middleware AND the peering InboundMiddleware, exactly as production
// composes them (auth.Handler.Middleware wrapping the mux that mounts
// /api/peering/inbound/ behind InboundMiddleware).
//
// This is the regression guard for the reachability fix: a REMOTE peer has no OS
// session on the receiving box, so a signed inbound envelope must pass the OS
// session gate (because the S2S peering paths are public) and then be
// authenticated by InboundMiddleware (Ed25519 signature + approved-contact
// allow-list + revocation). Before the fix the OS session middleware 401'd the
// request before InboundMiddleware ran, so all box-to-box peering was dead.
//
// The existing e2e_peering_test.go fixture wires ONLY InboundMiddleware (no
// session gate), which is why it stayed green while production was broken — this
// test closes that blind spot by stacking the real session middleware in front.
//
// ── Why this file is `package peering_test` ──────────────────────────────────
//
// It must import services/auth to stack the REAL session middleware, and
// auth → devicekey → fleetid → peering. As an in-package (`package peering`)
// test that is an import cycle, which Go rejects at compile time:
//
//	imports vulos/backend/services/auth from e2e_session_stack_test.go
//	imports .../devicekey → .../fleetid → .../peering: import cycle not allowed in test
//
// That was not a soft failure. It made the whole `peering` test binary fail to
// build, so EVERY test in this package — not just these three — silently stopped
// running while still looking like coverage. An external test package breaks the
// cycle (peering_test may import both peering and auth), at the cost of using
// only peering's exported API. The local fixture below does exactly that; it
// deliberately does not reuse newTestPeer(), which is in-package and therefore
// not visible from here.
package peering_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	authsvc "vulos/backend/services/auth"
	"vulos/backend/services/peering"
)

// sessionStackPeer is a receiving peer built entirely from peering's exported
// API (see the package comment above for why newTestPeer is not reused).
type sessionStackPeer struct {
	svc      *peering.Service
	contacts *peering.ContactStore
}

// newSessionStackServer builds a receiving peer whose HTTP handler is the
// PRODUCTION composition: authsvc.Handler.Middleware( mux ), where mux mounts
// /api/peering/inbound/ behind InboundMiddleware. No OS user/session exists on
// this box, matching a real remote-peer delivery.
func newSessionStackServer(t *testing.T) (*sessionStackPeer, *httptest.Server) {
	t.Helper()

	home := t.TempDir()
	svc := peering.New(home)

	contacts, err := peering.NewContactStore(home)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}
	inbox, err := peering.NewInboxStore(home)
	if err != nil {
		t.Fatalf("NewInboxStore: %v", err)
	}

	msgAPI := peering.NewMessageAPI(
		contacts, inbox, peering.NewPeerClient(), nil, svc.PrivateKey(), svc.VulaID(),
	)

	// Inner inbound mux: the message handler behind InboundMiddleware.
	inner := http.NewServeMux()
	msgAPI.RegisterMessageHandlers(inner)

	mux := http.NewServeMux()
	mux.Handle("/api/peering/inbound/", peering.InboundMiddleware(contacts, inner))

	// Real OS session middleware in front, with an EMPTY auth store (no users, no
	// sessions) — a remote peer never has a session on the receiving box.
	authStore, err := authsvc.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	authHandler := authsvc.NewHandler(authStore)

	srv := httptest.NewServer(authHandler.Middleware(mux))
	t.Cleanup(srv.Close)

	return &sessionStackPeer{svc: svc, contacts: contacts}, srv
}

// newSenderPeer is a minimal sending identity — it only ever signs envelopes.
func newSenderPeer(t *testing.T) *peering.Service {
	t.Helper()
	return peering.New(t.TempDir())
}

// signedMessageEnvelope builds and signs a TypeMessage envelope from sender to
// recipient.
func signedMessageEnvelope(t *testing.T, sender *peering.Service, toVulaID, body string) *peering.Envelope {
	t.Helper()
	payload, err := json.Marshal(peering.MessagePayload{Type: "text", Body: body})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env, err := peering.NewEnvelope("msg-"+body, sender.VulaID(), toVulaID, peering.TypeMessage, payload)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if err := env.Sign(sender.PrivateKey()); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return env
}

func postEnvelope(t *testing.T, srvURL, envType string, env *peering.Envelope) *http.Response {
	t.Helper()
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srvURL+"/api/peering/inbound/"+envType, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// TestE2E_FullStack_SignedEnvelopeDeliversThroughSessionGate proves the core
// "make peering work" path: an APPROVED remote peer's signed message envelope
// flows through the OS session gate (public S2S path) and InboundMiddleware
// (signature + allow-list) into the recipient's inbox — with NO OS session on
// the receiving box.
func TestE2E_FullStack_SignedEnvelopeDeliversThroughSessionGate(t *testing.T) {
	recv, srv := newSessionStackServer(t)
	sender := newSenderPeer(t)

	// Recipient approves the sender as a contact (mutual-trust precondition).
	if err := recv.contacts.Add(sender.VulaID(), "sender", "sender.example:443"); err != nil {
		t.Fatalf("contacts.Add: %v", err)
	}
	if err := recv.contacts.Approve(sender.VulaID(), peering.DefaultPerms()); err != nil {
		t.Fatalf("contacts.Approve: %v", err)
	}

	env := signedMessageEnvelope(t, sender, recv.svc.VulaID(), "hello-fullstack")
	resp := postEnvelope(t, srv.URL, "message", env)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("signed approved-contact envelope should deliver through the full stack, got %d (was the S2S path 401'd by the session gate?)", resp.StatusCode)
	}
}

// TestE2E_FullStack_UnsignedEnvelopeRejectedByInbound verifies the
// rejection is done by InboundMiddleware (401 "invalid or missing signature"),
// i.e. the request DID reach the peering gate rather than being blocked earlier
// by the session middleware.
func TestE2E_FullStack_UnsignedEnvelopeRejectedByInbound(t *testing.T) {
	recv, srv := newSessionStackServer(t)
	sender := newSenderPeer(t)
	if err := recv.contacts.Add(sender.VulaID(), "sender", "sender.example:443"); err != nil {
		t.Fatalf("contacts.Add: %v", err)
	}
	if err := recv.contacts.Approve(sender.VulaID(), peering.DefaultPerms()); err != nil {
		t.Fatalf("contacts.Approve: %v", err)
	}

	// Build the envelope but do NOT sign it.
	payload, _ := json.Marshal(peering.MessagePayload{Type: "text", Body: "unsigned"})
	env, _ := peering.NewEnvelope("msg-unsigned", sender.VulaID(), recv.svc.VulaID(), peering.TypeMessage, payload)
	resp := postEnvelope(t, srv.URL, "message", env)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned envelope must be 401'd by InboundMiddleware, got %d", resp.StatusCode)
	}
	// Confirm the body is the InboundMiddleware signature error, proving the
	// request reached the peering gate (not a generic session-gate 401).
	var errBody map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody["error"] != "invalid or missing signature" {
		t.Fatalf("expected InboundMiddleware signature rejection, got body %v", errBody)
	}
}

// TestE2E_FullStack_UnapprovedSenderRejectedByInbound verifies a correctly
// SIGNED envelope from a peer who is NOT an approved contact is rejected by
// InboundMiddleware's allow-list (403), again proving the request reached the
// peering gate through the (now-public) S2S session path.
func TestE2E_FullStack_UnapprovedSenderRejectedByInbound(t *testing.T) {
	recv, srv := newSessionStackServer(t)
	stranger := newSenderPeer(t) // never approved on recv

	env := signedMessageEnvelope(t, stranger, recv.svc.VulaID(), "stranger")
	resp := postEnvelope(t, srv.URL, "message", env)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("signed-but-unapproved sender must be 403'd by InboundMiddleware allow-list, got %d", resp.StatusCode)
	}
	var errBody map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody["error"] != "sender is not in the approved contacts list" {
		t.Fatalf("expected InboundMiddleware allow-list rejection, got body %v", errBody)
	}
}
