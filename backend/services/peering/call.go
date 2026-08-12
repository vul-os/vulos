// call.go — call signaling relay (PEER-19).
//
// # Architecture
//
//	Alice's browser → POST /api/peering/call/initiate
//	  → CallRelay.handleCallInitiate
//	    → PeerClient.Post (signed TypeSignaling envelope) →
//	        Bob's server /api/peering/inbound/signal
//	          → InboundMiddleware verifies Ed25519 sig + allow-list
//	            → CallRelay.HandleInboundCallSignal pushes frame to Bob's browser
//
//	SDP/ICE candidates travel the same path:
//	  POST /api/peering/call/signal  →  POST /api/peering/inbound/signal
//	  Servers relay the opaque SDP/ICE payload; neither node inspects it.
//
//	answer / reject / hangup relay a typed SignalingPayload to the remote server
//	and terminate the local session.
//
// # Transport (PEER-03/PEER-04)
//
// Every server-to-server message is a signed Envelope (TypeSignaling) delivered
// via PeerClient.  InboundMiddleware verifies the signature and allow-list
// before calling HandleInboundCallSignal, which reads the pre-verified
// *Envelope from the request context (EnvelopeKey).
//
// # Contact & permission checks
//
// Every outbound request verifies that the target Vulos ID is an approved
// contact with PermCall.  HandleInboundCallSignal performs the same gate on
// inbound envelopes (the sender must have PermCall).
//
// # Routes registered by RegisterCallHandlers
//
//	POST /api/peering/call/initiate    — caller starts a new call
//	POST /api/peering/call/answer      — callee accepts
//	POST /api/peering/call/reject      — callee declines
//	POST /api/peering/call/signal      — relay opaque SDP/ICE payload
//	POST /api/peering/call/hangup      — either party ends the call
//	POST /api/peering/inbound/signal   — S2S inbound (via InboundMiddleware)
//
// All identifiers are prefixed call/Call to avoid collisions with types in
// contacts.go (Contact, Perm, …) and ws.go (Hub, Frame, …).
package peering

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ─── Narrow interfaces (satisfied by *ContactStore and *Hub) ─────────────────

// callContactLookup is the subset of *ContactStore that CallRelay needs.
// *ContactStore satisfies this interface.
type callContactLookup interface {
	// Get returns a copy of the contact, or (nil, false) if unknown.
	Get(vulosID string) (*Contact, bool)
}

// callWSHub is the subset of *Hub that CallRelay needs.
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

// callSession tracks a live call between two Vulos IDs.
type callSession struct {
	id        string // unique call ID (UUID, caller-chosen)
	callerID  string // Vulos ID of the initiating party
	calleeID  string // Vulos ID of the receiving party
	state     callState
	createdAt time.Time
}

// ─── SignalingPayload kinds ───────────────────────────────────────────────────

// Signaling kind constants used inside SignalingPayload.Kind (envelope.go).
const (
	sigKindIncomingCall = "incoming-call"
	sigKindAnswer       = "answer"
	sigKindReject       = "reject"
	sigKindHangup       = "hangup"
	sigKindSignal       = "signal"
)

// ─── Wire types for local (browser → server) endpoints ───────────────────────

// callInitiateReq is the JSON body of POST /api/peering/call/initiate.
type callInitiateReq struct {
	CallID   string `json:"call_id"`   // UUID chosen by the caller's browser
	CalleeID string `json:"callee_id"` // Vulos ID of the intended callee
}

// callAnswerReq is the JSON body of POST /api/peering/call/answer.
type callAnswerReq struct {
	CallID string `json:"call_id"`
}

// callRejectReq is the JSON body of POST /api/peering/call/reject.
type callRejectReq struct {
	CallID string `json:"call_id"`
}

// callHangupReq is the JSON body of POST /api/peering/call/hangup.
type callHangupReq struct {
	CallID string `json:"call_id"`
}

// callSignalReq is the JSON body of POST /api/peering/call/signal.
// Payload is opaque — servers never inspect SDP/ICE content.
type callSignalReq struct {
	CallID  string          `json:"call_id"`
	PeerID  string          `json:"peer_id"` // Vulos ID of the other party
	Payload json.RawMessage `json:"payload"` // opaque SDP offer/answer / ICE candidate
}

// callWSFrame is the WebSocket frame pushed to the local browser after an
// inbound signaling envelope is received.
type callWSFrame struct {
	Channel string          `json:"channel"` // always ChannelSignal
	Type    string          `json:"type"`    // mirrors SignalingPayload.Kind
	CallID  string          `json:"call_id"`
	FromID  string          `json:"from_id"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ─── CallRelay ────────────────────────────────────────────────────────────────

// CallRelay handles call lifecycle and signaling for this Vulos node.
// It is safe for concurrent use.
type CallRelay struct {
	mu       sync.RWMutex
	sessions map[string]*callSession // call_id → session

	contacts callContactLookup
	hub      callWSHub
	client   *PeerClient        // outbound S2S transport (PEER-04)
	priv     ed25519.PrivateKey // signing key for outbound envelopes (PEER-03)
	selfID   string             // Vulos ID of this node
}

// NewCallRelay constructs a CallRelay.
//
//   - selfID   – this node's Vulos ID ("vulos:ed25519:<base58>")
//   - contacts – contact lookup and permission gate (pass *ContactStore)
//   - hub      – WebSocket push (pass *Hub; may be nil in tests)
//   - client   – outbound S2S client (pass peering.NewPeerClient())
//   - priv     – local Ed25519 private key used to sign outbound envelopes
func NewCallRelay(
	selfID string,
	contacts callContactLookup,
	hub callWSHub,
	client *PeerClient,
	priv ed25519.PrivateKey,
) *CallRelay {
	return &CallRelay{
		sessions: make(map[string]*callSession),
		contacts: contacts,
		hub:      hub,
		client:   client,
		priv:     priv,
		selfID:   selfID,
	}
}

// RegisterCallHandlers wires all call-related routes onto mux.
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
//	POST /api/peering/inbound/signal   — must be wrapped with InboundMiddleware
func RegisterCallHandlers(mux *http.ServeMux, relay *CallRelay) {
	mux.HandleFunc("POST /api/peering/call/initiate", relay.handleCallInitiate)
	mux.HandleFunc("POST /api/peering/call/answer", relay.handleCallAnswer)
	mux.HandleFunc("POST /api/peering/call/reject", relay.handleCallReject)
	mux.HandleFunc("POST /api/peering/call/signal", relay.handleCallSignal)
	mux.HandleFunc("POST /api/peering/call/hangup", relay.handleCallHangup)
	mux.HandleFunc("POST /api/peering/inbound/signal", relay.HandleInboundCallSignal)
}

// ─── Local handlers (browser → server) ───────────────────────────────────────

// handleCallInitiate processes POST /api/peering/call/initiate.
//
// The caller's browser posts { call_id, callee_id }.  The server:
//  1. Reads the caller's Vulos ID from the X-Vulos-ID header.
//  2. Verifies the callee is an approved contact with PermCall.
//  3. Records a ringing session.
//  4. Builds and signs a TypeSignaling envelope carrying { kind:"incoming-call", call_id }.
//  5. Delivers the envelope to the callee's server via PeerClient.
func (cr *CallRelay) handleCallInitiate(w http.ResponseWriter, r *http.Request) {
	callerID := r.Header.Get("X-Vulos-ID")
	if callerID == "" {
		writeCallErr(w, http.StatusUnauthorized, "missing X-Vulos-ID header")
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

	if err := cr.sendSignal(r.Context(), callee, sigKindIncomingCall, req.CallID, nil); err != nil {
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
// to active and notifies the caller's server via a signed "answer" envelope.
func (cr *CallRelay) handleCallAnswer(w http.ResponseWriter, r *http.Request) {
	calleeID := r.Header.Get("X-Vulos-ID")
	if calleeID == "" {
		writeCallErr(w, http.StatusUnauthorized, "missing X-Vulos-ID header")
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

	if err := cr.sendSignal(r.Context(), caller, sigKindAnswer, req.CallID, nil); err != nil {
		log.Printf("[call] answer: relay to caller %s failed: %v", caller.Server, err)
	}

	log.Printf("[call] %s answered by %s", req.CallID, calleeID)
	writeCallOK(w, map[string]string{"call_id": req.CallID, "status": "active"})
}

// handleCallReject processes POST /api/peering/call/reject.
//
// The callee declines the call.  The session is removed and the caller is
// notified via a signed "reject" envelope.
func (cr *CallRelay) handleCallReject(w http.ResponseWriter, r *http.Request) {
	calleeID := r.Header.Get("X-Vulos-ID")
	if calleeID == "" {
		writeCallErr(w, http.StatusUnauthorized, "missing X-Vulos-ID header")
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
		if err := cr.sendSignal(r.Context(), caller, sigKindReject, req.CallID, nil); err != nil {
			log.Printf("[call] reject: relay to caller %s failed: %v", caller.Server, err)
		}
	}

	log.Printf("[call] %s rejected by %s", req.CallID, calleeID)
	writeCallOK(w, map[string]string{"call_id": req.CallID, "status": "rejected"})
}

// handleCallHangup processes POST /api/peering/call/hangup.
//
// Either participant may hang up.  The session is removed and the other party
// is notified via a signed "hangup" envelope.
func (cr *CallRelay) handleCallHangup(w http.ResponseWriter, r *http.Request) {
	vulosID := r.Header.Get("X-Vulos-ID")
	if vulosID == "" {
		writeCallErr(w, http.StatusUnauthorized, "missing X-Vulos-ID header")
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
	if sess.callerID != vulosID && sess.calleeID != vulosID {
		cr.mu.Unlock()
		writeCallErr(w, http.StatusForbidden, "not a participant in this call")
		return
	}
	otherID := sess.calleeID
	if vulosID == sess.calleeID {
		otherID = sess.callerID
	}
	sess.state = callStateEnded
	delete(cr.sessions, req.CallID)
	cr.mu.Unlock()

	if other, ok := cr.contacts.Get(otherID); ok {
		if err := cr.sendSignal(r.Context(), other, sigKindHangup, req.CallID, nil); err != nil {
			log.Printf("[call] hangup: relay to %s failed: %v", other.Server, err)
		}
	}

	log.Printf("[call] %s hung up by %s", req.CallID, vulosID)
	writeCallOK(w, map[string]string{"call_id": req.CallID, "status": "ended"})
}

// handleCallSignal processes POST /api/peering/call/signal.
//
// Relays an opaque SDP offer/answer or ICE candidate payload to the peer's
// server via a signed TypeSignaling envelope.  The payload is never inspected.
func (cr *CallRelay) handleCallSignal(w http.ResponseWriter, r *http.Request) {
	vulosID := r.Header.Get("X-Vulos-ID")
	if vulosID == "" {
		writeCallErr(w, http.StatusUnauthorized, "missing X-Vulos-ID header")
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
	if ok && sess.callerID != vulosID && sess.calleeID != vulosID {
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

	if err := cr.sendSignal(r.Context(), peer, sigKindSignal, req.CallID, req.Payload); err != nil {
		log.Printf("[call] signal: relay to %s failed: %v", peer.Server, err)
		writeCallErr(w, http.StatusBadGateway, "could not reach peer server")
		return
	}

	writeCallOK(w, map[string]string{"status": "relayed"})
}

// ─── Inbound handler (remote server → this server, via InboundMiddleware) ────

// HandleInboundCallSignal processes POST /api/peering/inbound/signal.
//
// InboundMiddleware has already:
//  1. Verified the envelope's Ed25519 signature (401 on failure).
//  2. Confirmed the sender is in the approved contacts list (403 on failure).
//
// This handler:
//  1. Reads the pre-verified *Envelope from the request context (EnvelopeKey).
//  2. Checks the sender has PermCall.
//  3. Decodes the SignalingPayload from the envelope.
//  4. Updates local session state (ringing / active / ended).
//  5. Pushes a ChannelSignal WebSocket frame to the local user's browser tabs.
//
// The method is exported so the orchestrator can register it on the
// InboundMiddleware-wrapped sub-mux (see RegisterCallHandlers).
func (cr *CallRelay) HandleInboundCallSignal(w http.ResponseWriter, r *http.Request) {
	env, ok := r.Context().Value(EnvelopeKey).(*Envelope)
	if !ok || env == nil {
		writeCallErr(w, http.StatusUnauthorized,
			"envelope not found in context — ensure InboundMiddleware is applied")
		return
	}

	senderID := env.From

	// Additional permission gate: sender must have PermCall.
	sender, exists := cr.contacts.Get(senderID)
	if !exists || sender.State != StateApproved {
		writeCallErr(w, http.StatusForbidden, "sender is not an approved contact")
		return
	}
	if !sender.hasPermission(PermCall) {
		log.Printf("[call] inbound signal from %s denied: no call permission", senderID)
		writeCallErr(w, http.StatusForbidden, "sender has not been granted call permission")
		return
	}

	// Decode the signaling payload.
	var sp SignalingPayload
	if err := json.Unmarshal(env.Payload, &sp); err != nil {
		writeCallErr(w, http.StatusBadRequest, "invalid signaling payload: "+err.Error())
		return
	}
	if sp.Kind == "" || sp.CallID == "" {
		writeCallErr(w, http.StatusBadRequest, "signaling payload must include kind and call_id")
		return
	}

	// Update local session state based on the signal kind.
	switch sp.Kind {
	case sigKindIncomingCall:
		cr.mu.Lock()
		if _, exists := cr.sessions[sp.CallID]; !exists {
			cr.sessions[sp.CallID] = &callSession{
				id:        sp.CallID,
				callerID:  senderID,
				calleeID:  cr.selfID,
				state:     callStateRinging,
				createdAt: time.Now(),
			}
		}
		cr.mu.Unlock()

	case sigKindAnswer:
		cr.mu.Lock()
		if sess, exists := cr.sessions[sp.CallID]; exists {
			sess.state = callStateActive
		}
		cr.mu.Unlock()

	case sigKindReject, sigKindHangup:
		cr.mu.Lock()
		if sess, exists := cr.sessions[sp.CallID]; exists {
			sess.state = callStateEnded
			delete(cr.sessions, sp.CallID)
		}
		cr.mu.Unlock()
	}

	// Push a ChannelSignal frame to the local user's browser tabs.
	framePayload, err := json.Marshal(callWSFrame{
		Channel: ChannelSignal,
		Type:    sp.Kind,
		CallID:  sp.CallID,
		FromID:  senderID,
		Payload: sp.Data,
	})
	if err != nil {
		log.Printf("[call] inbound: marshal ws frame: %v", err)
		writeCallErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	cr.hub.Push(cr.selfID, Frame{
		Channel: ChannelSignal,
		From:    senderID,
		Payload: json.RawMessage(framePayload),
	})

	log.Printf("[call] inbound %s call=%s from=%s pushed to hub", sp.Kind, sp.CallID, senderID)
	writeCallOK(w, map[string]string{"status": "delivered"})
}

// ─── S2S relay helper ─────────────────────────────────────────────────────────

// sendSignal builds, signs, and delivers a TypeSignaling envelope to the
// contact's Vulos server via PeerClient (PEER-04).
//
//   - ctx     — request context for cancellation/timeout
//   - contact — the recipient; contact.Server is "host:port"
//   - kind    — one of the sigKind* constants
//   - callID  — identifies the call session
//   - data    — opaque SDP/ICE payload; nil for control signals
func (cr *CallRelay) sendSignal(
	ctx context.Context,
	contact *Contact,
	kind, callID string,
	data json.RawMessage,
) error {
	if contact.Server == "" {
		return fmt.Errorf("call/sendSignal: contact %s has no server address", contact.VulosID)
	}

	sp := SignalingPayload{
		Kind:   kind,
		CallID: callID,
		Data:   data,
	}
	payloadBytes, err := json.Marshal(sp)
	if err != nil {
		return fmt.Errorf("call/sendSignal: marshal payload: %w", err)
	}

	env, err := NewEnvelope(uuid.New().String(), cr.selfID, contact.VulosID,
		TypeSignaling, json.RawMessage(payloadBytes))
	if err != nil {
		return fmt.Errorf("call/sendSignal: create envelope: %w", err)
	}
	if err := env.Sign(cr.priv); err != nil {
		return fmt.Errorf("call/sendSignal: sign envelope: %w", err)
	}

	baseURL := "https://" + contact.Server
	if err := cr.client.Post(ctx, baseURL, "signal", env); err != nil {
		return fmt.Errorf("call/sendSignal: deliver to %s: %w", contact.Server, err)
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

// Compile-time interface assertions.
var _ callContactLookup = (*ContactStore)(nil)
var _ callWSHub = (*Hub)(nil)
