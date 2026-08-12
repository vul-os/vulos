// call_test.go — unit tests for the call signaling relay (PEER-19).
//
// All server-to-server delivery uses the real PeerClient plumbing but with an
// http.RoundTripper that redirects https:// → a local httptest.Server.  This
// lets tests capture outbound signed Envelope objects without a network.
//
// Inbound tests inject a pre-verified *Envelope into the request context
// (simulating InboundMiddleware), as done in messages_test.go.
package peering

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── Test doubles ─────────────────────────────────────────────────────────────

// callFakeContacts is an in-memory callContactLookup for tests.
type callFakeContacts struct {
	mu   sync.RWMutex
	data map[string]*Contact
}

func newCallFakeContacts() *callFakeContacts {
	return &callFakeContacts{data: make(map[string]*Contact)}
}

// add inserts or replaces a contact.
func (f *callFakeContacts) add(c *Contact) {
	f.mu.Lock()
	f.data[c.VulosID] = c
	f.mu.Unlock()
}

// Get implements callContactLookup.
func (f *callFakeContacts) Get(vulosID string) (*Contact, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	c, ok := f.data[vulosID]
	if !ok {
		return nil, false
	}
	dup := *c
	return &dup, true
}

// callFakeHub captures frames pushed via callWSHub.Push.
type callFakeHub struct {
	mu     sync.Mutex
	frames []Frame
}

// Push implements callWSHub.
func (h *callFakeHub) Push(userID string, frame Frame) {
	h.mu.Lock()
	h.frames = append(h.frames, frame)
	h.mu.Unlock()
}

// lastFrame returns the most recently pushed frame, or (Frame{}, false).
func (h *callFakeHub) lastFrame() (Frame, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.frames) == 0 {
		return Frame{}, false
	}
	return h.frames[len(h.frames)-1], true
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// callApprovedContact returns a Contact with PermCall granted.
func callApprovedContact(vulosID, server string) *Contact {
	return &Contact{
		VulosID:     vulosID,
		DisplayName: "Test User",
		Server:      server,
		State:       StateApproved,
		ApprovedAt:  time.Now().UTC(),
		Permissions: []Perm{PermMessage, PermCall},
	}
}

// callPostJSON sends a POST with a JSON body and optional X-Vulos-ID header,
// returning the recorder.
func callPostJSON(t *testing.T, handler http.HandlerFunc, vulosID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("callPostJSON: marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if vulosID != "" {
		req.Header.Set("X-Vulos-ID", vulosID)
	}
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

// callDecodeBody unmarshals the recorder body into v.
func callDecodeBody(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(v); err != nil {
		t.Fatalf("callDecodeBody: %v", err)
	}
}

// callRoundTripFunc is a test http.RoundTripper backed by a function.
type callRoundTripFunc func(*http.Request) (*http.Response, error)

func (f callRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// callTestIdentity generates an Ed25519 key pair and a valid Vula ID.
func callTestIdentity(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return priv, encodeVulosID(pub)
}

// newCallRelay builds a CallRelay with a fake outbound HTTP server that
// captures inbound signed Envelopes.  Returns the relay, a channel of
// captured *Envelope values, and the remote server's host:port.
func newCallRelay(
	t *testing.T,
	selfID string,
	contacts *callFakeContacts,
	hub *callFakeHub,
) (*CallRelay, chan *Envelope, string) {
	t.Helper()

	captured := make(chan *Envelope, 16)

	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env Envelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		captured <- &env
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"status":"ok"}`) //nolint:errcheck
	}))
	t.Cleanup(remoteServer.Close)

	remoteAddr := strings.TrimPrefix(remoteServer.URL, "http://")

	priv, _ := callTestIdentity(t)
	// selfID is a synthetic ID for tests; use a real Vula ID only where needed.
	pc := NewPeerClient()
	// Override transport to redirect https:// → fake test server.
	pc.http = &http.Client{
		Timeout: 5 * time.Second,
		Transport: callRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			req2 := req.Clone(req.Context())
			req2.URL.Scheme = "http"
			req2.URL.Host = strings.TrimPrefix(remoteServer.URL, "http://")
			return remoteServer.Client().Transport.RoundTrip(req2)
		}),
	}

	relay := NewCallRelay(selfID, contacts, hub, pc, priv)
	return relay, captured, remoteAddr
}

// callInboundRequest constructs an *http.Request whose context carries env
// under EnvelopeKey, simulating what InboundMiddleware does for downstream
// handlers.
func callInboundRequest(env *Envelope) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/signal", nil)
	ctx := context.WithValue(req.Context(), EnvelopeKey, env)
	return req.WithContext(ctx)
}

// callMakeSignalingEnvelope builds an *Envelope carrying a SignalingPayload.
// fromVulosID can be any string in tests that do not verify the signature.
func callMakeSignalingEnvelope(
	fromVulosID, toVulosID, kind, callID string,
	data json.RawMessage,
) *Envelope {
	sp := SignalingPayload{
		Kind:   kind,
		CallID: callID,
		Data:   data,
	}
	payload, _ := json.Marshal(sp)
	env, _ := NewEnvelope("test-id", fromVulosID, toVulosID, TypeSignaling, json.RawMessage(payload))
	return env
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestCallInitiate_RelaysIncomingCall verifies that POST /call/initiate causes
// a signed TypeSignaling envelope to reach the callee's server.
func TestCallInitiate_RelaysIncomingCall(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	relay, captured, remoteAddr := newCallRelay(t, "vula:self", contacts, hub)

	contacts.add(callApprovedContact("vula:bob", remoteAddr))

	rr := callPostJSON(t, relay.handleCallInitiate, "vula:alice", callInitiateReq{
		CallID:   "call-001",
		CalleeID: "vula:bob",
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}

	select {
	case env := <-captured:
		if env.Type != TypeSignaling {
			t.Errorf("type: got %q, want %q", env.Type, TypeSignaling)
		}
		// Decode SignalingPayload from envelope.
		var sp SignalingPayload
		if err := json.Unmarshal(env.Payload, &sp); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if sp.Kind != sigKindIncomingCall {
			t.Errorf("kind: got %q, want %q", sp.Kind, sigKindIncomingCall)
		}
		if sp.CallID != "call-001" {
			t.Errorf("call_id: got %q, want call-001", sp.CallID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: no envelope received by callee server")
	}
}

// TestCallInitiate_NoCallPermission verifies that a callee without PermCall is rejected.
func TestCallInitiate_NoCallPermission(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	relay, _, remoteAddr := newCallRelay(t, "vula:self", contacts, hub)

	// Only PermMessage — no PermCall.
	contacts.add(&Contact{
		VulosID:     "vula:bob",
		Server:      remoteAddr,
		State:       StateApproved,
		ApprovedAt:  time.Now().UTC(),
		Permissions: []Perm{PermMessage},
	})

	rr := callPostJSON(t, relay.handleCallInitiate, "vula:alice", callInitiateReq{
		CallID:   "call-002",
		CalleeID: "vula:bob",
	})

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

// TestCallInitiate_UnknownCallee verifies 403 for an unknown peer.
func TestCallInitiate_UnknownCallee(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	relay, _, _ := newCallRelay(t, "vula:self", contacts, hub)

	rr := callPostJSON(t, relay.handleCallInitiate, "vula:alice", callInitiateReq{
		CallID:   "call-003",
		CalleeID: "vula:nobody",
	})

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

// TestCallReject_TerminatesBothSides verifies that reject removes the session
// and sends a "reject" SignalingPayload to the caller's server.
func TestCallReject_TerminatesBothSides(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	relay, captured, remoteAddr := newCallRelay(t, "vula:bob", contacts, hub)

	contacts.add(callApprovedContact("vula:alice", remoteAddr))
	contacts.add(callApprovedContact("vula:bob", remoteAddr))

	// Pre-seed a ringing session (as if incoming-call was received).
	relay.mu.Lock()
	relay.sessions["call-010"] = &callSession{
		id:        "call-010",
		callerID:  "vula:alice",
		calleeID:  "vula:bob",
		state:     callStateRinging,
		createdAt: time.Now(),
	}
	relay.mu.Unlock()

	rr := callPostJSON(t, relay.handleCallReject, "vula:bob", callRejectReq{CallID: "call-010"})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}

	// Session must be removed.
	relay.mu.RLock()
	_, exists := relay.sessions["call-010"]
	relay.mu.RUnlock()
	if exists {
		t.Error("session should be gone after reject")
	}

	// Caller receives a "reject" SignalingPayload inside a TypeSignaling envelope.
	select {
	case env := <-captured:
		var sp SignalingPayload
		if err := json.Unmarshal(env.Payload, &sp); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if sp.Kind != sigKindReject {
			t.Errorf("kind: got %q, want %q", sp.Kind, sigKindReject)
		}
		if sp.CallID != "call-010" {
			t.Errorf("call_id: got %q, want call-010", sp.CallID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: no reject envelope received")
	}
}

// TestCallHangup_TerminatesBothSides verifies that hangup removes the session
// and notifies the other party.
func TestCallHangup_TerminatesBothSides(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	relay, captured, remoteAddr := newCallRelay(t, "vula:alice", contacts, hub)

	contacts.add(callApprovedContact("vula:bob", remoteAddr))

	relay.mu.Lock()
	relay.sessions["call-020"] = &callSession{
		id:        "call-020",
		callerID:  "vula:alice",
		calleeID:  "vula:bob",
		state:     callStateActive,
		createdAt: time.Now(),
	}
	relay.mu.Unlock()

	rr := callPostJSON(t, relay.handleCallHangup, "vula:alice", callHangupReq{CallID: "call-020"})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}

	relay.mu.RLock()
	_, exists := relay.sessions["call-020"]
	relay.mu.RUnlock()
	if exists {
		t.Error("session should be gone after hangup")
	}

	select {
	case env := <-captured:
		var sp SignalingPayload
		if err := json.Unmarshal(env.Payload, &sp); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if sp.Kind != sigKindHangup {
			t.Errorf("kind: got %q, want %q", sp.Kind, sigKindHangup)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: no hangup envelope received")
	}
}

// TestCallSDPICERelay_EndToEnd verifies that SDP/ICE payloads travel end-to-end
// without modification inside the SignalingPayload.Data field.
func TestCallSDPICERelay_EndToEnd(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	relay, captured, remoteAddr := newCallRelay(t, "vula:alice", contacts, hub)

	contacts.add(callApprovedContact("vula:bob", remoteAddr))

	relay.mu.Lock()
	relay.sessions["call-030"] = &callSession{
		id:        "call-030",
		callerID:  "vula:alice",
		calleeID:  "vula:bob",
		state:     callStateActive,
		createdAt: time.Now(),
	}
	relay.mu.Unlock()

	sdp := json.RawMessage(`{"type":"offer","sdp":"v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\n"}`)
	rr := callPostJSON(t, relay.handleCallSignal, "vula:alice", callSignalReq{
		CallID:  "call-030",
		PeerID:  "vula:bob",
		Payload: sdp,
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}

	select {
	case env := <-captured:
		if env.Type != TypeSignaling {
			t.Errorf("envelope type: got %q, want %q", env.Type, TypeSignaling)
		}
		var sp SignalingPayload
		if err := json.Unmarshal(env.Payload, &sp); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if sp.Kind != sigKindSignal {
			t.Errorf("kind: got %q, want %q", sp.Kind, sigKindSignal)
		}
		if string(sp.Data) != string(sdp) {
			t.Errorf("data mismatch: got %s", sp.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: no signal envelope received")
	}
}

// TestCallInbound_PushesFrameToHub verifies that an inbound "incoming-call"
// SignalingPayload is pushed to the local hub as a ChannelSignal frame.
func TestCallInbound_PushesFrameToHub(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	_, selfVulosID := callTestIdentity(t)
	relay, _, _ := newCallRelay(t, selfVulosID, contacts, hub)

	// Alice is an approved contact with call permission.
	contacts.add(callApprovedContact("vula:alice", "alice.vulos.org:8080"))

	env := callMakeSignalingEnvelope("vula:alice", selfVulosID, sigKindIncomingCall, "call-040", nil)
	req := callInboundRequest(env)
	rr := httptest.NewRecorder()
	relay.HandleInboundCallSignal(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}

	f, ok := hub.lastFrame()
	if !ok {
		t.Fatal("expected a frame to be pushed to hub")
	}
	if f.Channel != ChannelSignal {
		t.Errorf("channel: got %q, want %q", f.Channel, ChannelSignal)
	}

	// Unmarshal the payload and check the inner fields.
	var inner callWSFrame
	if err := json.Unmarshal(f.Payload, &inner); err != nil {
		t.Fatalf("unmarshal inner frame: %v", err)
	}
	if inner.Type != sigKindIncomingCall {
		t.Errorf("inner.type: got %q, want incoming-call", inner.Type)
	}
	if inner.CallID != "call-040" {
		t.Errorf("inner.call_id: got %q, want call-040", inner.CallID)
	}
}

// TestCallInbound_UnknownSender verifies 403 for an unknown sender.
func TestCallInbound_UnknownSender(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	_, selfID := callTestIdentity(t)
	relay, _, _ := newCallRelay(t, selfID, contacts, hub)

	// "vula:stranger" is not in contacts.
	env := callMakeSignalingEnvelope("vula:stranger", selfID, sigKindIncomingCall, "call-050", nil)
	rr := httptest.NewRecorder()
	relay.HandleInboundCallSignal(rr, callInboundRequest(env))

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

// TestCallInbound_NoCallPermission verifies 403 for a sender without PermCall.
func TestCallInbound_NoCallPermission(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	_, selfID := callTestIdentity(t)
	relay, _, _ := newCallRelay(t, selfID, contacts, hub)

	contacts.add(&Contact{
		VulosID:     "vula:charlie",
		Server:      "charlie.vulos.org:8080",
		State:       StateApproved,
		ApprovedAt:  time.Now().UTC(),
		Permissions: []Perm{PermMessage}, // no PermCall
	})

	env := callMakeSignalingEnvelope("vula:charlie", selfID, sigKindIncomingCall, "call-060", nil)
	rr := httptest.NewRecorder()
	relay.HandleInboundCallSignal(rr, callInboundRequest(env))

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-call permission, got %d", rr.Code)
	}
}

// TestCallInbound_MissingEnvelopeContext verifies 401 when the request
// context does not carry a pre-verified envelope (InboundMiddleware absent).
func TestCallInbound_MissingEnvelopeContext(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	_, selfID := callTestIdentity(t)
	relay, _, _ := newCallRelay(t, selfID, contacts, hub)

	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/signal", nil)
	rr := httptest.NewRecorder()
	relay.HandleInboundCallSignal(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

// TestCallSignal_NonParticipant verifies 404 when a non-participant tries to relay.
func TestCallSignal_NonParticipant(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	relay, _, remoteAddr := newCallRelay(t, "vula:self", contacts, hub)

	contacts.add(callApprovedContact("vula:bob", remoteAddr))

	relay.mu.Lock()
	relay.sessions["call-070"] = &callSession{
		id:        "call-070",
		callerID:  "vula:alice",
		calleeID:  "vula:bob",
		state:     callStateActive,
		createdAt: time.Now(),
	}
	relay.mu.Unlock()

	// "vula:eve" is not a participant.
	rr := callPostJSON(t, relay.handleCallSignal, "vula:eve", callSignalReq{
		CallID:  "call-070",
		PeerID:  "vula:bob",
		Payload: json.RawMessage(`{}`),
	})

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-participant, got %d", rr.Code)
	}
}

// TestCallMissingVulosIDHeader verifies 401 when X-Vulos-ID is absent from
// local-facing endpoints.
func TestCallMissingVulosIDHeader(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	relay, _, _ := newCallRelay(t, "vula:self", contacts, hub)

	cases := []struct {
		name    string
		handler http.HandlerFunc
		body    any
	}{
		{"initiate", relay.handleCallInitiate, callInitiateReq{}},
		{"answer", relay.handleCallAnswer, callAnswerReq{}},
		{"reject", relay.handleCallReject, callRejectReq{}},
		{"hangup", relay.handleCallHangup, callHangupReq{}},
		{"signal", relay.handleCallSignal, callSignalReq{}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rr := callPostJSON(t, tc.handler, "" /* no header */, tc.body)
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s: expected 401, got %d", tc.name, rr.Code)
			}
		})
	}
}

// TestCallAnswer_TransitionsToActive verifies that answering a ringing session
// moves it to active state and notifies the caller via a signed envelope.
func TestCallAnswer_TransitionsToActive(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	relay, captured, remoteAddr := newCallRelay(t, "vula:bob", contacts, hub)

	contacts.add(callApprovedContact("vula:alice", remoteAddr))

	relay.mu.Lock()
	relay.sessions["call-080"] = &callSession{
		id:        "call-080",
		callerID:  "vula:alice",
		calleeID:  "vula:bob",
		state:     callStateRinging,
		createdAt: time.Now(),
	}
	relay.mu.Unlock()

	rr := callPostJSON(t, relay.handleCallAnswer, "vula:bob", callAnswerReq{CallID: "call-080"})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}

	// Session should be active.
	relay.mu.RLock()
	sess, exists := relay.sessions["call-080"]
	relay.mu.RUnlock()
	if !exists {
		t.Fatal("session should still exist after answer")
	}
	if sess.state != callStateActive {
		t.Errorf("state: got %q, want active", sess.state)
	}

	// Caller receives "answer" SignalingPayload inside a TypeSignaling envelope.
	select {
	case env := <-captured:
		var sp SignalingPayload
		if err := json.Unmarshal(env.Payload, &sp); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if sp.Kind != sigKindAnswer {
			t.Errorf("kind: got %q, want %q", sp.Kind, sigKindAnswer)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: no answer envelope received")
	}
}

// TestCallInbound_RegistersSessionOnIncomingCall verifies that an inbound
// "incoming-call" envelope creates a ringing session on the callee's relay.
func TestCallInbound_RegistersSessionOnIncomingCall(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	_, selfID := callTestIdentity(t)
	relay, _, _ := newCallRelay(t, selfID, contacts, hub)

	contacts.add(callApprovedContact("vula:alice", "alice.vulos.org:8080"))

	env := callMakeSignalingEnvelope("vula:alice", selfID, sigKindIncomingCall, "call-090", nil)
	rr := httptest.NewRecorder()
	relay.HandleInboundCallSignal(rr, callInboundRequest(env))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}

	relay.mu.RLock()
	sess, exists := relay.sessions["call-090"]
	relay.mu.RUnlock()

	if !exists {
		t.Fatal("session should have been created by incoming-call")
	}
	if sess.callerID != "vula:alice" {
		t.Errorf("callerID: got %q, want vula:alice", sess.callerID)
	}
	if sess.calleeID != selfID {
		t.Errorf("calleeID: got %q, want %q", sess.calleeID, selfID)
	}
	if sess.state != callStateRinging {
		t.Errorf("state: got %q, want ringing", sess.state)
	}
}

// TestCallInbound_HangupRemovesSession verifies that an inbound "hangup"
// removes the tracked session.
func TestCallInbound_HangupRemovesSession(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	_, selfID := callTestIdentity(t)
	relay, _, _ := newCallRelay(t, selfID, contacts, hub)

	contacts.add(callApprovedContact("vula:alice", "alice.vulos.org:8080"))

	// Pre-seed a session.
	relay.mu.Lock()
	relay.sessions["call-100"] = &callSession{
		id:        "call-100",
		callerID:  "vula:alice",
		calleeID:  selfID,
		state:     callStateActive,
		createdAt: time.Now(),
	}
	relay.mu.Unlock()

	env := callMakeSignalingEnvelope("vula:alice", selfID, sigKindHangup, "call-100", nil)
	rr := httptest.NewRecorder()
	relay.HandleInboundCallSignal(rr, callInboundRequest(env))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}

	relay.mu.RLock()
	_, exists := relay.sessions["call-100"]
	relay.mu.RUnlock()
	if exists {
		t.Error("session should have been removed by inbound hangup")
	}
}

// TestCallInbound_SDPPayloadPassthrough verifies that opaque SDP/ICE data
// inside a "signal" kind envelope is passed through unchanged to the hub frame.
func TestCallInbound_SDPPayloadPassthrough(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	_, selfID := callTestIdentity(t)
	relay, _, _ := newCallRelay(t, selfID, contacts, hub)

	contacts.add(callApprovedContact("vula:alice", "alice.vulos.org:8080"))

	sdp := json.RawMessage(`{"type":"offer","sdp":"v=0\r\n"}`)
	env := callMakeSignalingEnvelope("vula:alice", selfID, sigKindSignal, "call-110", sdp)
	rr := httptest.NewRecorder()
	relay.HandleInboundCallSignal(rr, callInboundRequest(env))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}

	f, ok := hub.lastFrame()
	if !ok {
		t.Fatal("no hub frame pushed")
	}

	var inner callWSFrame
	if err := json.Unmarshal(f.Payload, &inner); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if inner.Type != sigKindSignal {
		t.Errorf("type: got %q, want %q", inner.Type, sigKindSignal)
	}
	if string(inner.Payload) != string(sdp) {
		t.Errorf("payload mismatch: got %s, want %s", inner.Payload, sdp)
	}
}

// Verify that *ContactStore and *Hub structurally satisfy the narrow interfaces
// used by CallRelay — compilation-only assertions (in call.go).
var _ callContactLookup = (*ContactStore)(nil)
var _ callWSHub = (*Hub)(nil)

// callDecodeBodyUnused is referenced to prevent the import from being flagged
// unused if all callers are removed in future edits.
var _ = callDecodeBody
