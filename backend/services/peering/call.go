// call.go — call signaling relay (PEER-19).
//
// Architecture
//
//	Alice's browser → POST /api/peering/call/initiate
//	  → CallRelay.handleCallInitiate
//	    → HTTP POST to Bob's server /api/peering/inbound/signal  (server-to-server)
//	      → Bob's server pushes { type:"incoming-call" } frame to Bob's WebSocket clients
//
//	SDP/ICE candidates travel the same path:
//	  POST /api/peering/call/signal  →  POST /api/peering/inbound/signal
//	  Servers never inspect the opaque payload.
//
//	answer / reject / hangup close or refuse the session locally, then relay a
//	termination envelope to the remote server.
//
// Contact & permission checks
//
//	Every request verifies that the relevant Vula ID is an approved contact
//	with PermCall (contacts.go PermCall constant).  The inbound endpoint
//	performs the same check for the remote side.
//
// All new identifiers are prefixed call/Call to avoid clashing with the
// types already defined in contacts.go (ContactStore, Contact, Perm, …)
// and ws.go (Hub, Frame, …).
package peering

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// ─── Narrow interfaces (satisfied by *ContactStore and *Hub) ─────────────────

// callContactLookup is the subset of *ContactStore that the call relay needs.
// *ContactStore satisfies this interface.
type callContactLookup interface {
	// Get returns a copy of the contact, or (nil, false) if unknown.
	Get(vulaID string) (*Contact, bool)
}

// callWSHub is the subset of *Hub that the call relay needs.
// *Hub satisfies this interface.
type callWSHub interface {
	// Push delivers frame to every open connection for userID.
	Push(userID string, frame Frame)
}

// ─── Call session state ───────────────────────────────────────────────────────

// callState is the lifecycle state of a call session.
type callState string

const (
	callStateRinging callState = "ringing"
	callStateActive  callState = "active"
	callStateEnded   callState = "ended"
)

// callSession tracks a live call between two Vula IDs.
type callSession struct {
	id        string // unique call ID (UUID, caller-chosen)
	callerID  string // Vula ID of the initiating party
	calleeID  string // Vula ID of the receiving party
	state     callState
	createdAt time.Time
}

// ─── Wire types ───────────────────────────────────────────────────────────────

// callInitiateReq is the body of POST /api/peering/call/initiate.
type callInitiateReq struct {
	CallID   string `json:"call_id"`   // UUID chosen by caller's browser
	CalleeID string `json:"callee_id"` // Vula ID of the intended callee
}

// callAnswerReq is the body of POST /api/peering/call/answer.
type callAnswerReq struct {
	CallID string `json:"call_id"`
}

// callRejectReq is the body of POST /api/peering/call/reject.
type callRejectReq struct {
	CallID string `json:"call_id"`
}

// callHangupReq is the body of POST /api/peering/call/hangup.
type callHangupReq struct {
	CallID string `json:"call_id"`
}

// callSignalReq is the body of POST /api/peering/call/signal.
// Payload is opaque — servers never inspect SDP/ICE content.
type callSignalReq struct {
	CallID  string          `json:"call_id"`
	PeerID  string          `json:"peer_id"` // Vula ID of the other party
	Payload json.RawMessage `json:"payload"` // opaque SDP offer/answer / ICE candidate
}

// callEnvelope is what one Vula server POSTs to another at
// /api/peering/inbound/signal.
type callEnvelope struct {
	Type    string          `json:"type"` // "incoming-call"|"answer"|"reject"|"hangup"|"signal"
	CallID  string          `json:"call_id"`
	FromID  string          `json:"from_id"`           // sender's Vula ID
	Payload json.RawMessage `json:"payload,omitempty"` // opaque; absent for control messages
}

// callWSFrame is the WebSocket frame pushed to the local browser.
type callWSFrame struct {
	Channel string          `json:"channel"` // always ChannelSignal
	Type    string          `json:"type"`    // mirrors callEnvelope.Type
	CallID  string          `json:"call_id"`
	FromID  string          `json:"from_id"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ─── CallRelay ────────────────────────────────────────────────────────────────

// CallRelay handles call lifecycle and signaling for this Vula node.
// It is safe for concurrent use.
type CallRelay struct {
	mu       sync.RWMutex
	sessions map[string]*callSession // call_id → session

	contacts callContactLookup
	hub      callWSHub

	// callHTTPClient is used for outbound S2S requests; replaced in tests.
	callHTTPClient *http.Client

	// selfID is the Vula ID of this node, used to populate from_id in envelopes.
	selfID string
}

// NewCallRelay constructs a CallRelay.
//
//   - selfID   – this node's Vula ID
//   - contacts – contact lookup (pass *ContactStore)
//   - hub      – WebSocket push (pass *Hub)
func NewCallRelay(selfID string, contacts callContactLookup, hub callWSHub) *CallRelay {
	return &CallRelay{
		sessions:       make(map[string]*callSession),
		contacts:       contacts,
		hub:            hub,
		callHTTPClient: &http.Client{Timeout: 10 * time.Second},
		selfID:         selfID,
	}
}

// RegisterCallHandlers registers all call-related HTTP routes onto mux.
//
// Local (browser → server) routes:
//
//	POST /api/peering/call/initiate
//	POST /api/peering/call/answer
//	POST /api/peering/call/reject
//	POST /api/peering/call/signal
//	POST /api/peering/call/hangup
//
// Inbound (remote server → this server) route:
//
//	POST /api/peering/inbound/signal
func RegisterCallHandlers(mux *http.ServeMux, relay *CallRelay) {
	mux.HandleFunc("POST /api/peering/call/initiate", relay.handleCallInitiate)
	mux.HandleFunc("POST /api/peering/call/answer", relay.handleCallAnswer)
	mux.HandleFunc("POST /api/peering/call/reject", relay.handleCallReject)
	mux.HandleFunc("POST /api/peering/call/signal", relay.handleCallSignal)
	mux.HandleFunc("POST /api/peering/call/hangup", relay.handleCallHangup)
	mux.HandleFunc("POST /api/peering/inbound/signal", relay.handleCallInbound)
}

// ─── Local handlers (browser → server) ───────────────────────────────────────

// handleCallInitiate processes POST /api/peering/call/initiate.
//
// The caller's browser posts { call_id, callee_id }.  The server:
//  1. Authenticates the caller via X-Vula-ID header.
//  2. Verifies the callee is an approved contact with PermCall.
//  3. Records a ringing session.
//  4. Relays an "incoming-call" envelope to the callee's server.
func (cr *CallRelay) handleCallInitiate(w http.ResponseWriter, r *http.Request) {
	callerID := r.Header.Get("X-Vula-ID")
	if callerID == "" {
		writeCallErr(w, http.StatusUnauthorized, "missing X-Vula-ID header")
		return
	}

	var req callInitiateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCallErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.CallID == "" || req.CalleeID == "" {
		writeCallErr(w, http.StatusBadRequest, "call_id and callee_id are required")
		return
	}

	callee, ok := cr.contacts.Get(req.CalleeID)
	if !ok || callee.State != StateApproved {
		writeCallErr(w, http.StatusForbidden, "callee is not an approved contact")
		return
	}
	if !callee.hasPermission(PermCall) {
		writeCallErr(w, http.StatusForbidden, "callee has not granted call permission")
		return
	}

	cr.mu.Lock()
	if _, exists := cr.sessions[req.CallID]; exists {
		cr.mu.Unlock()
		writeCallErr(w, http.StatusConflict, "call_id already in use")
		return
	}
	cr.sessions[req.CallID] = &callSession{
		id:        req.CallID,
		callerID:  callerID,
		calleeID:  req.CalleeID,
		state:     callStateRinging,
		createdAt: time.Now(),
	}
	cr.mu.Unlock()

	env := callEnvelope{
		Type:   "incoming-call",
		CallID: req.CallID,
		FromID: callerID,
	}
	if err := cr.callPostToServer(r.Context(), callee.Server, env); err != nil {
		log.Printf("[call] initiate: relay to %s failed: %v", callee.Server, err)
		cr.mu.Lock()
		delete(cr.sessions, req.CallID)
		cr.mu.Unlock()
		writeCallErr(w, http.StatusBadGateway, "could not reach callee server")
		return
	}

	log.Printf("[call] %s initiated by %s → %s", req.CallID, callerID, req.CalleeID)
	writeCallOK(w, map[string]string{"call_id": req.CallID, "status": "ringing"})
}

// handleCallAnswer processes POST /api/peering/call/answer.
//
// The callee's browser posts { call_id }.  The server transitions the session
// to active and notifies the caller's server.
func (cr *CallRelay) handleCallAnswer(w http.ResponseWriter, r *http.Request) {
	calleeID := r.Header.Get("X-Vula-ID")
	if calleeID == "" {
		writeCallErr(w, http.StatusUnauthorized, "missing X-Vula-ID header")
		return
	}

	var req callAnswerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCallErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	cr.mu.Lock()
	sess, ok := cr.sessions[req.CallID]
	if !ok {
		cr.mu.Unlock()
		writeCallErr(w, http.StatusNotFound, "call not found")
		return
	}
	if sess.calleeID != calleeID {
		cr.mu.Unlock()
		writeCallErr(w, http.StatusForbidden, "not the callee for this call")
		return
	}
	if sess.state != callStateRinging {
		cr.mu.Unlock()
		writeCallErr(w, http.StatusConflict, "call is not in ringing state")
		return
	}
	sess.state = callStateActive
	callerID := sess.callerID
	cr.mu.Unlock()

	caller, ok := cr.contacts.Get(callerID)
	if !ok {
		writeCallErr(w, http.StatusBadGateway, "caller not in contacts")
		return
	}

	env := callEnvelope{Type: "answer", CallID: req.CallID, FromID: calleeID}
	if err := cr.callPostToServer(r.Context(), caller.Server, env); err != nil {
		log.Printf("[call] answer: relay to caller %s failed: %v", caller.Server, err)
	}

	log.Printf("[call] %s answered by %s", req.CallID, calleeID)
	writeCallOK(w, map[string]string{"call_id": req.CallID, "status": "active"})
}

// handleCallReject processes POST /api/peering/call/reject.
//
// The callee declines the call.  The session is removed and the caller is
// notified via their server.
func (cr *CallRelay) handleCallReject(w http.ResponseWriter, r *http.Request) {
	calleeID := r.Header.Get("X-Vula-ID")
	if calleeID == "" {
		writeCallErr(w, http.StatusUnauthorized, "missing X-Vula-ID header")
		return
	}

	var req callRejectReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCallErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	cr.mu.Lock()
	sess, ok := cr.sessions[req.CallID]
	if !ok {
		cr.mu.Unlock()
		writeCallErr(w, http.StatusNotFound, "call not found")
		return
	}
	if sess.calleeID != calleeID {
		cr.mu.Unlock()
		writeCallErr(w, http.StatusForbidden, "not the callee for this call")
		return
	}
	callerID := sess.callerID
	sess.state = callStateEnded
	delete(cr.sessions, req.CallID)
	cr.mu.Unlock()

	if caller, ok := cr.contacts.Get(callerID); ok {
		env := callEnvelope{Type: "reject", CallID: req.CallID, FromID: calleeID}
		if err := cr.callPostToServer(r.Context(), caller.Server, env); err != nil {
			log.Printf("[call] reject: relay to caller %s failed: %v", caller.Server, err)
		}
	}

	log.Printf("[call] %s rejected by %s", req.CallID, calleeID)
	writeCallOK(w, map[string]string{"call_id": req.CallID, "status": "rejected"})
}

// handleCallHangup processes POST /api/peering/call/hangup.
//
// Either party may hang up.  The session is removed and the other participant
// is notified.
func (cr *CallRelay) handleCallHangup(w http.ResponseWriter, r *http.Request) {
	vulaID := r.Header.Get("X-Vula-ID")
	if vulaID == "" {
		writeCallErr(w, http.StatusUnauthorized, "missing X-Vula-ID header")
		return
	}

	var req callHangupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCallErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	cr.mu.Lock()
	sess, ok := cr.sessions[req.CallID]
	if !ok {
		cr.mu.Unlock()
		writeCallErr(w, http.StatusNotFound, "call not found")
		return
	}
	if sess.callerID != vulaID && sess.calleeID != vulaID {
		cr.mu.Unlock()
		writeCallErr(w, http.StatusForbidden, "not a participant in this call")
		return
	}
	otherID := sess.calleeID
	if vulaID == sess.calleeID {
		otherID = sess.callerID
	}
	sess.state = callStateEnded
	delete(cr.sessions, req.CallID)
	cr.mu.Unlock()

	if other, ok := cr.contacts.Get(otherID); ok {
		env := callEnvelope{Type: "hangup", CallID: req.CallID, FromID: vulaID}
		if err := cr.callPostToServer(r.Context(), other.Server, env); err != nil {
			log.Printf("[call] hangup: relay to %s failed: %v", other.Server, err)
		}
	}

	log.Printf("[call] %s hung up by %s", req.CallID, vulaID)
	writeCallOK(w, map[string]string{"call_id": req.CallID, "status": "ended"})
}

// handleCallSignal processes POST /api/peering/call/signal.
//
// Relays an opaque SDP offer/answer or ICE candidate payload to the peer's
// server.  The server never inspects the payload contents.
func (cr *CallRelay) handleCallSignal(w http.ResponseWriter, r *http.Request) {
	vulaID := r.Header.Get("X-Vula-ID")
	if vulaID == "" {
		writeCallErr(w, http.StatusUnauthorized, "missing X-Vula-ID header")
		return
	}

	var req callSignalReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCallErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.CallID == "" || req.PeerID == "" {
		writeCallErr(w, http.StatusBadRequest, "call_id and peer_id are required")
		return
	}

	// Sender must be a participant in the session.
	cr.mu.RLock()
	sess, ok := cr.sessions[req.CallID]
	if ok && sess.callerID != vulaID && sess.calleeID != vulaID {
		ok = false
	}
	cr.mu.RUnlock()
	if !ok {
		writeCallErr(w, http.StatusNotFound, "call not found or sender not a participant")
		return
	}

	peer, peerOK := cr.contacts.Get(req.PeerID)
	if !peerOK || peer.State != StateApproved {
		writeCallErr(w, http.StatusForbidden, "peer is not an approved contact")
		return
	}

	env := callEnvelope{
		Type:    "signal",
		CallID:  req.CallID,
		FromID:  vulaID,
		Payload: req.Payload,
	}
	if err := cr.callPostToServer(r.Context(), peer.Server, env); err != nil {
		log.Printf("[call] signal: relay to %s failed: %v", peer.Server, err)
		writeCallErr(w, http.StatusBadGateway, "could not reach peer server")
		return
	}

	writeCallOK(w, map[string]string{"status": "relayed"})
}

// ─── Inbound handler (remote server → this server) ────────────────────────────

// handleCallInbound processes POST /api/peering/inbound/signal.
//
// Receives a call envelope from a remote Vula server and delivers it to the
// local user's WebSocket connections.  The sender must be an approved contact
// with PermCall; non-call-permitted senders receive 403.
func (cr *CallRelay) handleCallInbound(w http.ResponseWriter, r *http.Request) {
	var env callEnvelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		writeCallErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if env.Type == "" || env.CallID == "" || env.FromID == "" {
		writeCallErr(w, http.StatusBadRequest, "type, call_id, and from_id are required")
		return
	}

	// Sender must be an approved contact with call permission.
	sender, ok := cr.contacts.Get(env.FromID)
	if !ok || sender.State != StateApproved {
		writeCallErr(w, http.StatusForbidden, "sender is not an approved contact")
		return
	}
	if !sender.hasPermission(PermCall) {
		writeCallErr(w, http.StatusForbidden, "sender has not been granted call permission")
		return
	}

	// Track session state on this (callee) side.
	switch env.Type {
	case "incoming-call":
		cr.mu.Lock()
		if _, exists := cr.sessions[env.CallID]; !exists {
			cr.sessions[env.CallID] = &callSession{
				id:        env.CallID,
				callerID:  env.FromID,
				calleeID:  cr.selfID,
				state:     callStateRinging,
				createdAt: time.Now(),
			}
		}
		cr.mu.Unlock()

	case "answer":
		cr.mu.Lock()
		if sess, exists := cr.sessions[env.CallID]; exists {
			sess.state = callStateActive
		}
		cr.mu.Unlock()

	case "reject", "hangup":
		cr.mu.Lock()
		if sess, exists := cr.sessions[env.CallID]; exists {
			sess.state = callStateEnded
			delete(cr.sessions, env.CallID)
		}
		cr.mu.Unlock()
	}

	// Push frame to all local WebSocket clients.
	frame := Frame{
		Channel: ChannelSignal,
		From:    env.FromID,
		Payload: mustMarshalCallFrame(callWSFrame{
			Channel: ChannelSignal,
			Type:    env.Type,
			CallID:  env.CallID,
			FromID:  env.FromID,
			Payload: env.Payload,
		}),
	}
	cr.hub.Push(cr.selfID, frame)

	log.Printf("[call] inbound %s call=%s from=%s pushed to hub", env.Type, env.CallID, env.FromID)
	writeCallOK(w, map[string]string{"status": "delivered"})
}

// ─── S2S HTTP relay ───────────────────────────────────────────────────────────

// callPostToServer POSTs a callEnvelope to a remote Vula server.
// server is "host:port".
func (cr *CallRelay) callPostToServer(ctx context.Context, server string, env callEnvelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("call/relay: marshal envelope: %w", err)
	}

	url := fmt.Sprintf("https://%s/api/peering/inbound/signal", server)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("call/relay: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := cr.callHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("call/relay: http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("call/relay: remote %s returned %d", server, resp.StatusCode)
	}
	return nil
}

// ─── Response helpers ─────────────────────────────────────────────────────────

// writeCallOK writes a 200 JSON response.
func writeCallOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// writeCallErr writes an error JSON response.
func writeCallErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}

// mustMarshalCallFrame marshals v and panics if it fails.  Only used for
// callWSFrame which has no non-serialisable fields.
func mustMarshalCallFrame(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("call/relay: mustMarshalCallFrame: %v", err))
	}
	return json.RawMessage(b)
}
