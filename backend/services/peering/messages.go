// messages.go — server-to-server text message delivery (PEER-14).
//
// # Overview
//
// MessageAPI handles the full outbound message pipeline for Vula peering:
//
//  1. Browser calls POST /api/peering/conversations/{conv_id}/send with a text body.
//  2. MessageAPI creates a signed Envelope (TypeMessage) using the local private key.
//  3. The envelope is delivered to the recipient's /api/peering/inbound/message
//     endpoint via PeerClient.
//  4. The message is stored locally in the outbox.
//  5. A real-time WebSocket frame is pushed to the sender's own browser tabs.
//
// Inbound delivery (/api/peering/inbound/message) is handled in inbox.go.
//
// # Conversation ID
//
// Conversations are identified by a canonical ID derived from the two Vula IDs:
//
//	conv_id = "<lower-vula-id>_<higher-vula-id>"
//
// This ensures both sides of the conversation agree on a single stable folder
// name regardless of which party initiated the conversation.
//
// # Routes registered by RegisterMessageHandlers
//
//	POST /api/peering/conversations/{conv_id}/send   — send a text message to a peer
//	GET  /api/peering/conversations                  — list all conversations
//	GET  /api/peering/conversations/{conv_id}/messages — fetch message history
//
// The inbound route POST /api/peering/inbound/message is registered here too
// but must be wrapped in InboundMiddleware by the orchestrator.
//
// # Usage
//
//	api := peering.NewMessageAPI(store, inbox, client, hub, priv, vulosID)
//	api.RegisterMessageHandlers(mux)
package peering

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ─── MessageAPI ───────────────────────────────────────────────────────────────

// MessageAPI bundles the dependencies for message send/receive HTTP handlers.
// Obtain one via NewMessageAPI; the zero value is not usable.
type MessageAPI struct {
	store   *ContactStore
	inbox   *InboxStore
	client  *PeerClient
	hub     *Hub
	priv    ed25519.PrivateKey
	vulosID string       // local node's Vula ID
	outbox  *OutboxQueue // optional; if set, failed deliveries are enqueued
}

// NewMessageAPI constructs a MessageAPI.
//
//   - store  — contacts store used to gate permission checks
//   - inbox  — inbox/outbox storage for messages
//   - client — outbound HTTP client for server-to-server delivery
//   - hub    — WebSocket hub for real-time browser pushes (may be nil in tests)
//   - priv   — local Ed25519 private key used to sign outbound envelopes
//   - vulosID — canonical Vula ID of the local node ("vulos:ed25519:<base58>")
func NewMessageAPI(
	store *ContactStore,
	inbox *InboxStore,
	client *PeerClient,
	hub *Hub,
	priv ed25519.PrivateKey,
	vulosID string,
) *MessageAPI {
	return &MessageAPI{
		store:   store,
		inbox:   inbox,
		client:  client,
		hub:     hub,
		priv:    priv,
		vulosID: vulosID,
	}
}

// WithOutbox attaches an OutboxQueue to the MessageAPI. When set, any message
// that cannot be delivered immediately (network error or non-2xx response) is
// persisted in the outbox for automatic retry rather than returning an error to
// the caller.
//
// Call this once after NewMessageAPI, before registering handlers. It is safe
// to omit — delivery remains synchronous when no outbox is configured.
func (a *MessageAPI) WithOutbox(q *OutboxQueue) *MessageAPI {
	a.outbox = q
	return a
}

// RegisterMessageHandlers wires all message-related routes onto mux.
//
// Routes registered:
//
//	GET  /api/peering/conversations
//	GET  /api/peering/conversations/{conv_id}/messages
//	POST /api/peering/conversations/{conv_id}/send
//	POST /api/peering/inbound/message
//
// The caller is responsible for wrapping /api/peering/inbound/ with
// InboundMiddleware before or after this registration.
func (a *MessageAPI) RegisterMessageHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/peering/conversations", a.handleListConversations)
	mux.HandleFunc("GET /api/peering/conversations/{conv_id}/messages", a.handleGetMessages)
	mux.HandleFunc("POST /api/peering/conversations/{conv_id}/send", a.handleSend)
	mux.HandleFunc("POST /api/peering/inbound/message", a.HandleInboundMessage)
}

// ─── Conversation ID helper ───────────────────────────────────────────────────

// ConversationID computes the canonical conversation ID for two Vula IDs.
//
// The result is deterministic: the lexicographically lower Vula ID comes first,
// separated by an underscore. This guarantees both parties produce the same ID.
func ConversationID(vulosIDA, vulosIDB string) string {
	ids := []string{vulosIDA, vulosIDB}
	sort.Strings(ids)
	return ids[0] + "_" + ids[1]
}

// ─── Outbound: send a message ─────────────────────────────────────────────────

// sendMessageRequest is the JSON body for POST /api/peering/conversations/{conv_id}/send.
type sendMessageRequest struct {
	// Body is the plain-text content of the message.
	Body string `json:"body"`
}

// handleSend implements POST /api/peering/conversations/{conv_id}/send.
//
// The {conv_id} path parameter must be the canonical conversation ID
// (ConversationID(localVulosID, peerVulosID)).
//
// Steps:
//  1. Parse and validate the request body.
//  2. Derive the peer's Vula ID from conv_id and the local vulosID.
//  3. Check the peer is approved and has PermMessage.
//  4. Build and sign a TypeMessage Envelope.
//  5. Deliver to the peer via PeerClient.
//  6. Store the message in the local outbox.
//  7. Push a "message" WebSocket frame to the sender's tabs.
func (a *MessageAPI) handleSend(w http.ResponseWriter, r *http.Request) {
	convID := r.PathValue("conv_id")
	if convID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "conv_id is required"})
		return
	}

	var req sendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request body: " + err.Error(),
		})
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must not be empty"})
		return
	}

	// Derive peer Vula ID from the conversation ID.
	peerVulosID, err := peerFromConvID(convID, a.vulosID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "cannot derive peer from conv_id: " + err.Error(),
		})
		return
	}

	// Permission check — peer must be approved and have PermMessage.
	if !a.store.IsApproved(peerVulosID) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "peer is not an approved contact",
			"peer":  peerVulosID,
		})
		return
	}
	if !a.store.Can(peerVulosID, PermMessage) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "peer does not have message permission",
			"peer":  peerVulosID,
		})
		return
	}

	// Look up the peer's server address.
	contact, exists := a.store.Get(peerVulosID)
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "contact not found in store",
		})
		return
	}
	if contact.Server == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "contact has no server address; cannot deliver",
		})
		return
	}

	// Build the signed envelope.
	msgID := uuid.New().String()
	now := time.Now().UTC()

	payload, err := json.Marshal(MessagePayload{
		Type: "text",
		Body: req.Body,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "marshal payload: " + err.Error(),
		})
		return
	}

	env, err := NewEnvelope(msgID, a.vulosID, peerVulosID, TypeMessage, json.RawMessage(payload))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "create envelope: " + err.Error(),
		})
		return
	}
	env.Timestamp = now.Format(time.RFC3339)

	if err := env.Sign(a.priv); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "sign envelope: " + err.Error(),
		})
		return
	}

	// Deliver to the remote peer. resolvePeerBaseURL is the single reachability
	// seam (CONSOLIDATION B-0): today a pass-through to "https://<contact.Server>".
	baseURL := resolvePeerBaseURL(contact.VulosID, contact.Server)
	deliveryErr := a.client.Post(r.Context(), baseURL, "message", env)
	if deliveryErr != nil {
		log.Printf("[peering/messages] delivery error to %s: %v", baseURL, deliveryErr)
		if a.outbox != nil {
			// Enqueue for persistent retry rather than failing the request.
			if qErr := a.outbox.Enqueue(peerVulosID, baseURL, env); qErr != nil {
				log.Printf("[peering/messages] outbox enqueue error: %v", qErr)
			} else {
				log.Printf("[peering/messages] message %s queued for retry to %s", msgID, peerVulosID)
			}
		} else {
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error":  "delivery failed",
				"detail": deliveryErr.Error(),
			})
			return
		}
	}

	// Store in local outbox.
	stored := StoredMessage{
		ID:        msgID,
		ConvID:    convID,
		From:      a.vulosID,
		To:        peerVulosID,
		Type:      "text",
		Body:      req.Body,
		Timestamp: now,
		Direction: DirectionOut,
	}
	if err := a.inbox.StoreMessage(stored); err != nil {
		// Non-fatal: delivery already succeeded; log and continue.
		log.Printf("[peering/messages] outbox store error: %v", err)
	}

	// Push real-time frame to sender's own browser tabs.
	a.pushMessageFrame(convID, stored)

	status := "delivered"
	if deliveryErr != nil {
		status = "queued"
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":      msgID,
		"conv_id": convID,
		"status":  status,
	})
}

// ─── Inbound: receive a message ───────────────────────────────────────────────

// HandleInboundMessage implements POST /api/peering/inbound/message.
//
// InboundMiddleware has already:
//  1. Verified the envelope's Ed25519 signature (401 on failure).
//  2. Confirmed the sender is in the approved contacts list (403 on failure).
//
// This handler:
//  1. Reads the pre-verified *Envelope from the request context.
//  2. Checks that the sender has PermMessage.
//  3. Decodes the MessagePayload.
//  4. Stores the message in the local inbox.
//  5. Pushes a "message" WebSocket frame to the recipient's browser tabs.
//
// The method is exported so the orchestrator can register it on an
// InboundMiddleware-wrapped sub-mux.
func (a *MessageAPI) HandleInboundMessage(w http.ResponseWriter, r *http.Request) {
	env, ok := r.Context().Value(EnvelopeKey).(*Envelope)
	if !ok || env == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "envelope not found in context — ensure InboundMiddleware is applied",
		})
		return
	}

	senderID := env.From

	// Additional permission gate: sender needs PermMessage specifically.
	if !a.store.Can(senderID, PermMessage) {
		log.Printf("[peering/messages] inbound message from %s denied: no message permission", senderID)
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "sender does not have message permission",
		})
		return
	}

	// Decode message payload.
	var mp MessagePayload
	if err := json.Unmarshal(env.Payload, &mp); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid message payload: " + err.Error(),
		})
		return
	}
	if mp.Type == "" {
		mp.Type = "text"
	}

	// Parse timestamp from the envelope.
	ts, err := time.Parse(time.RFC3339, env.Timestamp)
	if err != nil {
		ts = time.Now().UTC()
	}

	convID := ConversationID(senderID, a.vulosID)

	stored := StoredMessage{
		ID:        env.ID,
		ConvID:    convID,
		From:      senderID,
		To:        a.vulosID,
		Type:      mp.Type,
		Body:      mp.Body,
		Timestamp: ts,
		Direction: DirectionIn,
	}

	if err := a.inbox.StoreMessage(stored); err != nil {
		log.Printf("[peering/messages] inbox store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to store message",
		})
		return
	}

	log.Printf("[peering/messages] stored inbound message %s from %s (conv=%s)", env.ID, senderID, convID)

	// Push real-time frame to connected browser tabs.
	a.pushMessageFrame(convID, stored)

	writeJSON(w, http.StatusOK, map[string]any{"status": "received"})
}

// ─── Read handlers ────────────────────────────────────────────────────────────

// handleListConversations implements GET /api/peering/conversations.
//
// Returns a list of all conversations with their most recent message timestamp,
// sorted newest-first.
func (a *MessageAPI) handleListConversations(w http.ResponseWriter, r *http.Request) {
	convs, err := a.inbox.ListConversations()
	if err != nil {
		log.Printf("[peering/messages] list conversations error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to list conversations",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"conversations": convs,
		"count":         len(convs),
	})
}

// handleGetMessages implements GET /api/peering/conversations/{conv_id}/messages.
//
// Returns messages in the conversation, oldest-first.
// Optional query parameters:
//
//	since — RFC3339 timestamp; only return messages after this time
//	limit — maximum number of messages to return (default: 100)
func (a *MessageAPI) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	convID := r.PathValue("conv_id")
	if convID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "conv_id is required"})
		return
	}

	msgs, err := a.inbox.GetMessages(convID)
	if err != nil {
		log.Printf("[peering/messages] get messages error (conv=%s): %v", convID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to retrieve messages",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"conv_id":  convID,
		"messages": msgs,
		"count":    len(msgs),
	})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// peerFromConvID extracts the peer Vula ID from a canonical conversation ID.
//
// conv_id has the form "<lower_vulos_id>_<upper_vulos_id>". Given the local
// vulosID, the function returns the other half. Returns an error if the local
// vulosID is not a component of the conv_id.
func peerFromConvID(convID, localVulosID string) (string, error) {
	// The separator between two Vula IDs inside a conv_id is "_" but Vula IDs
	// themselves contain ":" characters — never "_". So we split on "_" and
	// rejoin the two halves ("vula", "ed25519", "<base58>").
	//
	// Expected segments after split:
	//   [0] "vula"
	//   [1] "ed25519"
	//   [2] "<base58-A>"      ← first vula ID ends here (everything before second "vula")
	//   [3] "vula"
	//   [4] "ed25519"
	//   [5] "<base58-B>"
	//
	// A Vula ID is "vulos:ed25519:<base58>". In the conv_id the ":" separator
	// in each Vula ID is preserved; only the join between the two IDs uses "_".
	// So conv_id looks like:
	//
	//   vulos:ed25519:<base58A>_vulos:ed25519:<base58B>
	//
	// We can split on "_vulos:ed25519:" to recover both halves.
	const vulaPrefix = "vulos:ed25519:"
	sep := "_" + vulaPrefix

	idx := strings.Index(convID, sep)
	if idx < 0 {
		return "", fmt.Errorf("invalid conv_id format: %q", convID)
	}

	idA := convID[:idx]
	idB := vulaPrefix + convID[idx+len(sep):]

	switch localVulosID {
	case idA:
		return idB, nil
	case idB:
		return idA, nil
	default:
		return "", fmt.Errorf("local vula ID %q is not part of conv_id %q", localVulosID, convID)
	}
}

// pushMessageFrame encodes stored and broadcasts a ChannelMessage frame to
// all of the local user's connected browser tabs.
func (a *MessageAPI) pushMessageFrame(convID string, msg StoredMessage) {
	if a.hub == nil {
		return
	}

	payload, err := json.Marshal(map[string]any{
		"event":     "message",
		"conv_id":   convID,
		"id":        msg.ID,
		"from":      msg.From,
		"to":        msg.To,
		"type":      msg.Type,
		"body":      msg.Body,
		"timestamp": msg.Timestamp.Format(time.RFC3339),
		"direction": string(msg.Direction),
	})
	if err != nil {
		log.Printf("[peering/messages] pushMessageFrame marshal error: %v", err)
		return
	}

	a.hub.Broadcast(Frame{
		Channel: ChannelMessage,
		From:    msg.From,
		Payload: json.RawMessage(payload),
	})
}
