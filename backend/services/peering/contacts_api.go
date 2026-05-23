// contacts_api.go — HTTP handlers for the contact request/approve/block flow (PEER-07).
//
// # Overview
//
// This file wires the full contact lifecycle as HTTP endpoints without touching
// any existing file in the peering package.  All state lives in a ContactStore
// (contacts.go), all outbound deliveries use PeerClient (transport.go), and
// real-time browser notifications are pushed via Hub (ws.go).
//
// # Trust state machine recap
//
//	Unknown ──► Pending ──► Approved
//	                │            │
//	                └──► Blocked ◄┘
//
// # Endpoint map
//
//	POST   /api/peering/contacts/request      — send a contact request to a remote peer
//	GET    /api/peering/contacts/requests     — list pending incoming requests (Pending state)
//	POST   /api/peering/contacts/approve/{id} — approve a pending request (→ Approved)
//	POST   /api/peering/contacts/block/{id}   — block a contact (any state → Blocked)
//	DELETE /api/peering/contacts/{id}         — remove a contact (any state → gone)
//	GET    /api/peering/contacts              — list approved contacts
//
//	POST   /api/peering/inbound/request       — receive a contact request from a remote peer
//	                                            (exempt from allow-list — InboundMiddleware
//	                                             already handles signature verification only)
//
// # Usage
//
//	api := peering.NewContactAPI(store, client, hub, priv, vulaID, myServer)
//	api.RegisterContactHandlers(mux)
//
//	// Wire the inbound subtree through InboundMiddleware separately:
//	inboundMux := http.NewServeMux()
//	inboundMux.HandleFunc("POST /api/peering/inbound/request", api.HandleInboundRequest)
//	mux.Handle("/api/peering/inbound/", peering.InboundMiddleware(store, inboundMux))
package peering

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/google/uuid"
)

// ─── ContactAPI ───────────────────────────────────────────────────────────────

// ContactAPI bundles the dependencies for the contact request/approve/block HTTP
// handlers.  Obtain one via NewContactAPI; the zero value is not usable.
type ContactAPI struct {
	store    *ContactStore
	client   *PeerClient
	hub      *Hub
	priv     ed25519.PrivateKey
	vulaID   string // local node's Vula ID
	myServer string // local node's publicly reachable "host:port"

	// OnApprove is an optional hook called after a contact transitions to
	// StateApproved.  It receives the approved contact's Vula ID and the
	// server address stored in the contact record.  The hook is invoked
	// synchronously before the HTTP response is written, but implementations
	// should be non-blocking (e.g. use FetchPeerProfileAsync).
	OnApprove func(vulaID, serverAddr string)
}

// NewContactAPI constructs a ContactAPI.
//
//   - store     — the local contacts store (reads/writes allowed)
//   - client    — outbound HTTP client for server-to-server delivery
//   - hub       — WebSocket hub for real-time browser notifications (may be nil in tests)
//   - priv      — local Ed25519 private key used to sign outbound envelopes
//   - vulaID    — canonical Vula ID of the local node ("vula:ed25519:<base58>")
//   - myServer  — publicly reachable address of this node ("host:port"), sent in contact
//     requests so the recipient can reply
func NewContactAPI(
	store *ContactStore,
	client *PeerClient,
	hub *Hub,
	priv ed25519.PrivateKey,
	vulaID string,
	myServer string,
) *ContactAPI {
	return &ContactAPI{
		store:    store,
		client:   client,
		hub:      hub,
		priv:     priv,
		vulaID:   vulaID,
		myServer: myServer,
	}
}

// RegisterContactHandlers wires all contact-management routes onto mux.
//
// Routes registered:
//
//	POST   /api/peering/contacts/request
//	GET    /api/peering/contacts/requests
//	POST   /api/peering/contacts/approve/{id}
//	POST   /api/peering/contacts/block/{id}
//	DELETE /api/peering/contacts/{id}
//	GET    /api/peering/contacts
//	POST   /api/peering/inbound/request
//
// The inbound/request route does NOT go through InboundMiddleware here; it is
// expected that the caller wraps /api/peering/inbound/ with InboundMiddleware
// in the main mux assembly.  If the caller registers all routes on a single
// mux that is itself wrapped, this registration is correct.
// In any case InboundMiddleware already exempts /api/peering/inbound/request
// from the allow-list check (signature-only).
func (a *ContactAPI) RegisterContactHandlers(mux *http.ServeMux) {
	// Outbound — local-user-initiated.
	mux.HandleFunc("POST /api/peering/contacts/request", a.handleSendRequest)
	mux.HandleFunc("GET /api/peering/contacts/requests", a.handleListRequests)
	mux.HandleFunc("POST /api/peering/contacts/approve/{id}", a.handleApprove)
	mux.HandleFunc("POST /api/peering/contacts/block/{id}", a.handleBlock)
	mux.HandleFunc("DELETE /api/peering/contacts/{id}", a.handleRemove)
	mux.HandleFunc("GET /api/peering/contacts", a.handleListContacts)

	// Inbound — delivered by a remote peer's server.
	mux.HandleFunc("POST /api/peering/inbound/request", a.HandleInboundRequest)
}

// ─── Outbound handlers ────────────────────────────────────────────────────────

// sendContactRequest body.
type sendRequestBody struct {
	// TargetServer is the peer's "host:port".  Required if TargetVulaID is
	// bare (no @host:port suffix).  May be omitted when TargetVulaID is a full
	// Vula address ("vula:ed25519:...@host:port").
	TargetServer string `json:"target_server"`

	// TargetVulaID is the recipient's Vula ID or full Vula address.
	TargetVulaID string `json:"target_vula_id"`

	// DisplayName is our own display name, sent to the peer.
	DisplayName string `json:"display_name"`

	// Message is an optional intro message (visible to recipient).
	Message string `json:"message,omitempty"`

	// VulosAddress is the local node's Vulos identity address
	// ("user@vulos.org").  When present it is embedded in the signed
	// contact-card payload so the recipient can populate their copy of
	// this contact with our mail address immediately.
	VulosAddress string `json:"vulos_address,omitempty"`
}

// handleSendRequest implements POST /api/peering/contacts/request.
//
// Signs a contact-request envelope with the local private key and POSTs it to
// the target peer's /api/peering/inbound/request endpoint.  Also adds the
// target to the local store in StatePending so the UI can show "request sent".
func (a *ContactAPI) handleSendRequest(w http.ResponseWriter, r *http.Request) {
	var body sendRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request body: " + err.Error(),
		})
		return
	}

	if body.TargetVulaID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "target_vula_id is required",
		})
		return
	}

	// Resolve target server address.
	targetVulaID := body.TargetVulaID
	targetServer := body.TargetServer

	// If the vula ID looks like a full address (<vulaID>@host:port), parse it.
	if addr, err := ParseVulaAddress(body.TargetVulaID); err == nil {
		targetVulaID = addr.VulaID
		if targetServer == "" {
			targetServer = fmt.Sprintf("%s:%d", addr.Host, addr.Port)
		}
	}

	if targetServer == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "target_server is required (or provide a full vula address with @host:port)",
		})
		return
	}

	// Build the inbound base URL.
	baseURL := "https://" + targetServer

	// Add target to local store as pending (idempotent: ignore already-exists error).
	if err := a.store.Add(targetVulaID, body.DisplayName, targetServer); err != nil {
		// Contact might already exist — check.
		if _, exists := a.store.Get(targetVulaID); !exists {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "failed to add contact: " + err.Error(),
			})
			return
		}
		// Already exists — just update server address if needed.
		_ = a.store.UpdateServer(targetVulaID, targetServer)
	}

	// Build the signed envelope.
	payload, err := json.Marshal(ContactRequestPayload{
		DisplayName:   body.DisplayName,
		Message:       body.Message,
		ServerAddr:    a.myServer,
		VulosAddress: body.VulosAddress,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to marshal payload: " + err.Error(),
		})
		return
	}

	env, err := NewEnvelope(
		uuid.New().String(),
		a.vulaID,
		targetVulaID,
		TypeContactRequest,
		json.RawMessage(payload),
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to create envelope: " + err.Error(),
		})
		return
	}

	if err := env.Sign(a.priv); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to sign envelope: " + err.Error(),
		})
		return
	}

	if err := a.client.Post(r.Context(), baseURL, "request", env); err != nil {
		log.Printf("[peering/contacts] sendRequest delivery error to %s: %v", baseURL, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "failed to deliver contact request to peer",
			"detail": err.Error(),
		})
		return
	}

	log.Printf("[peering/contacts] contact request sent to %s (%s)", targetVulaID, targetServer)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":         "sent",
		"target_vula_id": targetVulaID,
		"target_server":  targetServer,
	})
}

// handleListRequests implements GET /api/peering/contacts/requests.
//
// Returns all contacts in StatePending — these are inbound requests awaiting
// local approval or block.
func (a *ContactAPI) handleListRequests(w http.ResponseWriter, r *http.Request) {
	pending := a.store.ListByState(StatePending)

	type requestItem struct {
		VulaID      string `json:"vula_id"`
		DisplayName string `json:"display_name"`
		Server      string `json:"server,omitempty"`
		AddedAt     string `json:"added_at"`
	}

	items := make([]requestItem, 0, len(pending))
	for _, c := range pending {
		items = append(items, requestItem{
			VulaID:      c.VulaID,
			DisplayName: c.DisplayName,
			Server:      c.Server,
			AddedAt:     c.AddedAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"requests": items,
		"count":    len(items),
	})
}

// handleApprove implements POST /api/peering/contacts/approve/{id}.
//
// Transitions the contact to StateApproved with DefaultPerms.
// Sends a "contact-approved" notification to the peer so they know
// they've been accepted (best-effort; delivery failure is logged, not fatal).
func (a *ContactAPI) handleApprove(w http.ResponseWriter, r *http.Request) {
	vulaID := r.PathValue("id")
	if vulaID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "contact id is required",
		})
		return
	}

	c, exists := a.store.Get(vulaID)
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "contact not found",
		})
		return
	}

	if err := a.store.Approve(vulaID, DefaultPerms()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": err.Error(),
		})
		return
	}

	log.Printf("[peering/contacts] approved contact %s", vulaID)

	// Trigger a profile fetch for the newly-approved peer so our cache is
	// warm before the UI asks for it (PEER-12 AC: "approve triggers fetch").
	if a.OnApprove != nil {
		a.OnApprove(vulaID, c.Server)
	}

	// Push a real-time notification to browser tabs so the UI can update.
	a.pushNotification("contact_approved", map[string]any{
		"vula_id":      vulaID,
		"display_name": c.DisplayName,
	})

	// Best-effort: notify the peer that their request was approved.
	// We don't block on this; a failure here is not critical.
	go a.notifyPeerApproved(c.Server, vulaID)

	c2, _ := a.store.Get(vulaID)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "approved",
		"contact": c2,
	})
}

// handleBlock implements POST /api/peering/contacts/block/{id}.
//
// Silently transitions the contact to StateBlocked.
// No notification is sent to the blocked peer — they receive no confirmation.
func (a *ContactAPI) handleBlock(w http.ResponseWriter, r *http.Request) {
	vulaID := r.PathValue("id")
	if vulaID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "contact id is required",
		})
		return
	}

	if _, exists := a.store.Get(vulaID); !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "contact not found",
		})
		return
	}

	if err := a.store.Block(vulaID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
		return
	}

	log.Printf("[peering/contacts] blocked contact %s (silent)", vulaID)

	// Push browser notification.
	a.pushNotification("contact_blocked", map[string]any{
		"vula_id": vulaID,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "blocked",
		"vula_id": vulaID,
	})
}

// handleRemove implements DELETE /api/peering/contacts/{id}.
//
// Permanently removes the contact from the local store.
func (a *ContactAPI) handleRemove(w http.ResponseWriter, r *http.Request) {
	vulaID := r.PathValue("id")
	if vulaID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "contact id is required",
		})
		return
	}

	if _, exists := a.store.Get(vulaID); !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "contact not found",
		})
		return
	}

	if err := a.store.Remove(vulaID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
		return
	}

	log.Printf("[peering/contacts] removed contact %s", vulaID)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "removed",
		"vula_id": vulaID,
	})
}

// handleListContacts implements GET /api/peering/contacts.
//
// Returns all contacts in StateApproved.
func (a *ContactAPI) handleListContacts(w http.ResponseWriter, r *http.Request) {
	approved := a.store.ListByState(StateApproved)

	writeJSON(w, http.StatusOK, map[string]any{
		"contacts": approved,
		"count":    len(approved),
	})
}

// ─── Inbound handler ──────────────────────────────────────────────────────────

// HandleInboundRequest implements POST /api/peering/inbound/request.
//
// This endpoint is called by a remote Vula peer's server when they want to add
// the local user as a contact.  InboundMiddleware has already:
//
//  1. Verified the envelope's Ed25519 signature (401 on failure).
//  2. Skipped the allow-list check (this is the exempt path).
//
// The handler:
//
//  1. Reads the verified *Envelope from the request context.
//  2. Decodes the ContactRequestPayload.
//  3. If the sender is already blocked — silently drops (200 OK, no details).
//  4. Otherwise adds the sender to the local store in StatePending (idempotent).
//  5. Pushes a real-time "contact_request" notification to browser tabs.
//
// The method is exported so callers can register it independently on a
// sub-mux that is wrapped with InboundMiddleware.
func (a *ContactAPI) HandleInboundRequest(w http.ResponseWriter, r *http.Request) {
	// Retrieve the pre-verified envelope placed in context by InboundMiddleware.
	env, ok := r.Context().Value(EnvelopeKey).(*Envelope)
	if !ok || env == nil {
		// This handler is called without InboundMiddleware only in tests.
		// In production InboundMiddleware always sets the envelope.
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "envelope not verified — ensure InboundMiddleware is applied",
		})
		return
	}

	// Decode the contact-request payload.
	var payload ContactRequestPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid contact-request payload: " + err.Error(),
		})
		return
	}

	senderID := env.From

	// Check if this sender is explicitly blocked — silently drop their request.
	if existing, exists := a.store.Get(senderID); exists && existing.State == StateBlocked {
		log.Printf("[peering/contacts] inbound request from blocked sender %s — silently dropped", senderID)
		// Return 200 so the sender cannot infer they are blocked.
		writeJSON(w, http.StatusOK, map[string]any{"status": "received"})
		return
	}

	// Add to local store as pending (idempotent — ignore already-exists).
	displayName := payload.DisplayName
	if displayName == "" {
		displayName = senderID // fallback display name
	}

	if err := a.store.Add(senderID, displayName, payload.ServerAddr); err != nil {
		// Contact might already be pending — that's fine; update server if needed.
		if _, exists := a.store.Get(senderID); exists {
			if payload.ServerAddr != "" {
				_ = a.store.UpdateServer(senderID, payload.ServerAddr)
			}
		} else {
			log.Printf("[peering/contacts] inbound request store error for %s: %v", senderID, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "failed to store contact request",
			})
			return
		}
	}

	// Populate the Vulos identity address from the signed contact-card payload so the
	// recipient has the sender's mail address without a separate relay lookup.
	if payload.VulosAddress != "" {
		if err := a.store.UpdateVulosAddress(senderID, payload.VulosAddress); err != nil {
			// Non-fatal: log and continue — the contact is already stored.
			log.Printf("[peering/contacts] UpdateVulosAddress for %s: %v", senderID, err)
		}
	}

	log.Printf("[peering/contacts] inbound contact request from %s (%s)", senderID, payload.ServerAddr)

	// Push a real-time notification to connected browser tabs.
	a.pushNotification("contact_request", map[string]any{
		"vula_id":        senderID,
		"display_name":   displayName,
		"message":        payload.Message,
		"server":         payload.ServerAddr,
		"vulos_address": payload.VulosAddress,
	})

	writeJSON(w, http.StatusOK, map[string]any{"status": "received"})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// pushNotification delivers a real-time event to all connected browser tabs.
// If the hub is nil (e.g. in unit tests) the call is a no-op.
func (a *ContactAPI) pushNotification(event string, data map[string]any) {
	if a.hub == nil {
		return
	}

	data["event"] = event

	payload, err := json.Marshal(data)
	if err != nil {
		log.Printf("[peering/contacts] pushNotification marshal error: %v", err)
		return
	}

	// Broadcast to all connected users — in a single-user Vula instance this
	// is equivalent to pushing to the owner's browser tabs.
	a.hub.Broadcast(Frame{
		Channel: ChannelNotification,
		From:    "peering",
		Payload: json.RawMessage(payload),
	})
}

// notifyPeerApproved sends a best-effort notification to the peer that their
// contact request was approved, allowing them to update their local store.
// Runs in its own goroutine; errors are only logged.
func (a *ContactAPI) notifyPeerApproved(peerServer, peerVulaID string) {
	if peerServer == "" || a.priv == nil {
		return
	}

	baseURL := "https://" + peerServer

	approvalPayload, err := json.Marshal(map[string]string{
		"status":       "approved",
		"approved_by":  a.vulaID,
		"display_name": "", // profile name, not yet wired
	})
	if err != nil {
		log.Printf("[peering/contacts] notifyPeerApproved marshal: %v", err)
		return
	}

	env, err := NewEnvelope(
		uuid.New().String(),
		a.vulaID,
		peerVulaID,
		TypeContactRequest, // reuse contact-request type for the approval notification
		json.RawMessage(approvalPayload),
	)
	if err != nil {
		log.Printf("[peering/contacts] notifyPeerApproved envelope: %v", err)
		return
	}

	if err := env.Sign(a.priv); err != nil {
		log.Printf("[peering/contacts] notifyPeerApproved sign: %v", err)
		return
	}

	ctx := context.Background()
	if err := a.client.Post(ctx, baseURL, "request", env); err != nil {
		log.Printf("[peering/contacts] notifyPeerApproved delivery to %s: %v", baseURL, err)
	}
}
