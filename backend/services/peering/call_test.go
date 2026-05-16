// call_test.go — unit tests for the call signaling relay (PEER-19).
//
// Test doubles implement the callContactLookup and callWSHub interfaces
// defined in call.go.  They never redeclare types from contacts.go or ws.go.
package peering

import (
	"bytes"
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
	f.data[c.VulaID] = c
	f.mu.Unlock()
}

// Get implements callContactLookup.
func (f *callFakeContacts) Get(vulaID string) (*Contact, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	c, ok := f.data[vulaID]
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
func callApprovedContact(vulaID, server string) *Contact {
	return &Contact{
		VulaID:      vulaID,
		DisplayName: "Test User",
		Server:      server,
		State:       StateApproved,
		ApprovedAt:  time.Now().UTC(),
		Permissions: []Perm{PermMessage, PermCall},
	}
}

// callPostJSON sends a POST with a JSON body and optional X-Vula-ID header,
// returning the recorder.
func callPostJSON(t *testing.T, handler http.HandlerFunc, vulaID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("callPostJSON: marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if vulaID != "" {
		req.Header.Set("X-Vula-ID", vulaID)
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

// newCallRelay builds a CallRelay with a fake outbound HTTP server that
// captures envelopes.  Returns the relay, a channel of captured callEnvelopes,
// and the remote server's host:port.
func newCallRelay(
	t *testing.T,
	selfID string,
	contacts *callFakeContacts,
	hub *callFakeHub,
) (*CallRelay, chan callEnvelope, string) {
	t.Helper()

	captured := make(chan callEnvelope, 16)

	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env callEnvelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		captured <- env
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"status":"ok"}`) //nolint:errcheck
	}))
	t.Cleanup(remoteServer.Close)

	remoteAddr := strings.TrimPrefix(remoteServer.URL, "http://")

	relay := NewCallRelay(selfID, contacts, hub)
	// Override HTTP client to redirect https:// → fake test server.
	relay.callHTTPClient = &http.Client{
		Timeout: 5 * time.Second,
		Transport: callRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			req2 := req.Clone(req.Context())
			req2.URL.Scheme = "http"
			req2.URL.Host = strings.TrimPrefix(remoteServer.URL, "http://")
			return remoteServer.Client().Transport.RoundTrip(req2)
		}),
	}

	return relay, captured, remoteAddr
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestCallInitiate_RelaysIncomingCall verifies that POST /call/initiate causes
// an "incoming-call" envelope to reach the callee's server.
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
		if env.Type != "incoming-call" {
			t.Errorf("type: got %q, want incoming-call", env.Type)
		}
		if env.CallID != "call-001" {
			t.Errorf("call_id: got %q, want call-001", env.CallID)
		}
		if env.FromID != "vula:alice" {
			t.Errorf("from_id: got %q, want vula:alice", env.FromID)
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
		VulaID:      "vula:bob",
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
// and sends a "reject" envelope to the caller's server.
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

	// Caller receives "reject" envelope.
	select {
	case env := <-captured:
		if env.Type != "reject" {
			t.Errorf("type: got %q, want reject", env.Type)
		}
		if env.CallID != "call-010" {
			t.Errorf("call_id: got %q, want call-010", env.CallID)
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
		if env.Type != "hangup" {
			t.Errorf("type: got %q, want hangup", env.Type)
		}
		if env.FromID != "vula:alice" {
			t.Errorf("from_id: got %q, want vula:alice", env.FromID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: no hangup envelope received")
	}
}

// TestCallSDPICERelay_EndToEnd verifies that SDP/ICE payloads travel end-to-end
// without modification.
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
		if env.Type != "signal" {
			t.Errorf("type: got %q, want signal", env.Type)
		}
		if string(env.Payload) != string(sdp) {
			t.Errorf("payload mismatch: got %s", env.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: no signal envelope received")
	}
}

// TestCallInbound_PushesFrameToHub verifies that an inbound "incoming-call"
// envelope is pushed to the local hub.
func TestCallInbound_PushesFrameToHub(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	relay, _, _ := newCallRelay(t, "vula:bob", contacts, hub)

	// Alice is an approved contact with call permission.
	contacts.add(callApprovedContact("vula:alice", "alice.vulos.org:8080"))

	env := callEnvelope{
		Type:   "incoming-call",
		CallID: "call-040",
		FromID: "vula:alice",
	}
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	relay.handleCallInbound(rr, req)

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
	if inner.Type != "incoming-call" {
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
	relay, _, _ := newCallRelay(t, "vula:bob", contacts, hub)

	// "vula:stranger" is not in contacts.
	env := callEnvelope{
		Type:   "incoming-call",
		CallID: "call-050",
		FromID: "vula:stranger",
	}
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	relay.handleCallInbound(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

// TestCallInbound_NoCallPermission verifies 403 for a sender without PermCall.
func TestCallInbound_NoCallPermission(t *testing.T) {
	contacts := newCallFakeContacts()
	hub := &callFakeHub{}
	relay, _, _ := newCallRelay(t, "vula:bob", contacts, hub)

	contacts.add(&Contact{
		VulaID:      "vula:charlie",
		Server:      "charlie.vulos.org:8080",
		State:       StateApproved,
		ApprovedAt:  time.Now().UTC(),
		Permissions: []Perm{PermMessage}, // no PermCall
	})

	env := callEnvelope{
		Type:   "incoming-call",
		CallID: "call-060",
		FromID: "vula:charlie",
	}
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	relay.handleCallInbound(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-call permission, got %d", rr.Code)
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

// TestCallMissingVulaIDHeader verifies 401 when X-Vula-ID is absent from
// local-facing endpoints.
func TestCallMissingVulaIDHeader(t *testing.T) {
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
// moves it to active state and notifies the caller.
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

	// Caller receives "answer" envelope.
	select {
	case env := <-captured:
		if env.Type != "answer" {
			t.Errorf("type: got %q, want answer", env.Type)
		}
		if env.FromID != "vula:bob" {
			t.Errorf("from_id: got %q, want vula:bob", env.FromID)
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
	relay, _, _ := newCallRelay(t, "vula:bob", contacts, hub)

	contacts.add(callApprovedContact("vula:alice", "alice.vulos.org:8080"))

	env := callEnvelope{
		Type:   "incoming-call",
		CallID: "call-090",
		FromID: "vula:alice",
	}
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	relay.handleCallInbound(rr, req)

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
	if sess.calleeID != "vula:bob" {
		t.Errorf("calleeID: got %q, want vula:bob", sess.calleeID)
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
	relay, _, _ := newCallRelay(t, "vula:bob", contacts, hub)

	contacts.add(callApprovedContact("vula:alice", "alice.vulos.org:8080"))

	// Pre-seed a session.
	relay.mu.Lock()
	relay.sessions["call-100"] = &callSession{
		id:        "call-100",
		callerID:  "vula:alice",
		calleeID:  "vula:bob",
		state:     callStateActive,
		createdAt: time.Now(),
	}
	relay.mu.Unlock()

	env := callEnvelope{
		Type:   "hangup",
		CallID: "call-100",
		FromID: "vula:alice",
	}
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	relay.handleCallInbound(rr, req)

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

// Verify that *ContactStore and *Hub structurally satisfy the narrow interfaces
// used by CallRelay — compilation-only assertions.
var _ callContactLookup = (*ContactStore)(nil)
var _ callWSHub = (*Hub)(nil)

// callDecodeBodyUnused is referenced to prevent the import from being flagged
// unused if all callers are removed in future edits.
var _ = callDecodeBody
