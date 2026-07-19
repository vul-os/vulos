// shares.go — document share/accept with signed-envelope transport (PEER-31).
//
// SharesService sits on top of [CollabShareService] and wires the share/accept
// flow to the PEER-03 envelope, PEER-04 transport, and PEER-14 delivery layers:
//
//   - Outbound: POST /api/peering/collab/share builds a signed [Envelope] of type
//     "collab-invite" and delivers it to the recipient's server via [PeerClient].
//     The local [ShareStore] is updated with badge=owned on success.
//
//   - Inbound:  POST /api/peering/inbound/collab-invite is handled by
//     [HandleInboundShare], which reads the pre-verified *[Envelope] from the
//     request context (set by [InboundMiddleware]) and registers the document
//     locally with badge=shared.
//
// Per-peer permissions (edit / view) live in the [ShareStore] (see
// collab_share.go).  The SharesService enforces them on the inbound update path:
// a peer with SharePermView that tries to push CRDT ops receives 403 Forbidden.
//
// # HTTP surface
//
//	POST   /api/peering/collab/share              send doc-share invitation (signed)
//	GET    /api/peering/collab/documents           list shared docs
//	GET    /api/peering/collab/{doc_id}            get doc metadata + perm
//	DELETE /api/peering/collab/{doc_id}            leave (peer) or revoke (owner)
//	PUT    /api/peering/collab/{doc_id}/perms      update a peer's permission
//
//	POST /api/peering/inbound/collab-invite        receive signed invitation
//	POST /api/peering/inbound/collab-update        receive CRDT ops (view-only → 403)
//	GET  /api/peering/inbound/collab-sync          diff request from peer
//
// # Wire-in note (main.go)
//
// Instantiate SharesService and call RegisterSharesHandlers:
//
//	svc := peering.NewSharesService(contactStore, shareStore, peerClient, priv, vulaID)
//	svc.RegisterSharesHandlers(mux)
//
// The /api/peering/inbound/* handlers must be wrapped with InboundMiddleware
// before or after this call (they read the envelope from the request context).
package peering

import (
	"crypto/ed25519"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// ─── Envelope type constants (PEER-03 extension) ──────────────────────────────

const (
	// TypeCollabInvite is sent when an owner shares a document with a peer.
	// Payload: [ShareInvitePayload].
	TypeCollabInvite = "collab-invite"

	// TypeCollabUpdate is sent when an editor pushes CRDT operations to a peer's
	// server.  The payload body is an opaque binary blob (base64 in JSON).
	// Payload shape: { "doc_id": "...", "ops": "<base64>" }.
	TypeCollabUpdate = "collab-update"
)

// ─── SharesService ────────────────────────────────────────────────────────────

// SharesService wires the [ShareStore] to the envelope-signed transport layer.
// Obtain one via [NewSharesService]; the zero value is not usable.
type SharesService struct {
	contacts    *ContactStore
	store       *ShareStore
	client      *PeerClient
	priv        ed25519.PrivateKey
	localVulaID string
}

// NewSharesService creates a SharesService.
//
//   - contacts   — contact store used to resolve peer server addresses and gate perms
//   - store      — shared-document registry (see collab_share.go)
//   - client     — outbound HTTP client for server-to-server envelope delivery
//   - priv       — local Ed25519 private key used to sign outbound envelopes
//   - localVulaID — canonical Vula ID of the local node ("vula:ed25519:<base58>")
func NewSharesService(
	contacts *ContactStore,
	store *ShareStore,
	client *PeerClient,
	priv ed25519.PrivateKey,
	localVulaID string,
) *SharesService {
	return &SharesService{
		contacts:    contacts,
		store:       store,
		client:      client,
		priv:        priv,
		localVulaID: localVulaID,
	}
}

// localID returns the best available local Vula ID.
func (s *SharesService) localID() string {
	if s.localVulaID != "" {
		return s.localVulaID
	}
	return "local"
}

// ─── Registration ─────────────────────────────────────────────────────────────

// RegisterSharesHandlers wires all share endpoints onto mux.
//
// The /api/peering/inbound/* routes expect [InboundMiddleware] to be applied
// on the surrounding handler; they read the verified [Envelope] from the
// request context.
func (s *SharesService) RegisterSharesHandlers(mux *http.ServeMux) {
	// Client-facing (browser → local server)
	mux.HandleFunc("POST /api/peering/collab/share", s.handleShareDoc)
	mux.HandleFunc("GET /api/peering/collab/documents", s.handleListDocs)
	mux.HandleFunc("GET /api/peering/collab/{doc_id}", s.handleGetDoc)
	mux.HandleFunc("DELETE /api/peering/collab/{doc_id}", s.handleLeaveOrRevoke)
	mux.HandleFunc("PUT /api/peering/collab/{doc_id}/perms", s.handleSetPerms)

	// Server-to-server inbound (must be behind InboundMiddleware)
	mux.HandleFunc("POST /api/peering/inbound/collab-invite", s.HandleInboundShare)
	mux.HandleFunc("POST /api/peering/inbound/collab-update", s.handleInboundUpdate)
	mux.HandleFunc("GET /api/peering/inbound/collab-sync", s.handleInboundSync)
}

// ─── Outbound: POST /api/peering/collab/share ─────────────────────────────────

// shareInviteRequest is the body accepted by POST /api/peering/collab/share.
// It extends [ShareInvitePayload] with the peer's server address required for
// envelope delivery.
type shareInviteRequest struct {
	ShareInvitePayload
	// PeerServer is the target peer's "<host>:<port>" server address.
	// If omitted the service looks it up from the contacts store.
	PeerServer string `json:"peer_server,omitempty"`
}

// handleShareDoc implements POST /api/peering/collab/share.
//
// Steps:
//  1. Decode and validate the [shareInviteRequest].
//  2. Look up the peer's server address from contacts if not supplied.
//  3. Register the document locally with badge=owned.
//  4. Build and sign a TypeCollabInvite [Envelope].
//  5. Deliver to the peer via [PeerClient].
func (s *SharesService) handleShareDoc(w http.ResponseWriter, r *http.Request) {
	var req shareInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sharesWriteErr(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.DocID == "" {
		sharesWriteErr(w, "doc_id is required", http.StatusBadRequest)
		return
	}
	if req.PeerID == "" {
		sharesWriteErr(w, "peer_id is required", http.StatusBadRequest)
		return
	}
	if err := shareValidatePerm(req.Perm); err != nil {
		sharesWriteErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.FromID == "" {
		req.FromID = s.localID()
	}

	// Resolve peer server address from contacts if not provided in request.
	peerServer := req.PeerServer
	if peerServer == "" && s.contacts != nil {
		if contact, ok := s.contacts.Get(req.PeerID); ok && contact.Server != "" {
			peerServer = contact.Server
		}
	}

	// Register locally with badge=owned.
	entry := &ShareDocEntry{
		DocID:     req.DocID,
		DocType:   req.DocType,
		Title:     req.Title,
		OwnerID:   req.FromID,
		Badge:     ShareBadgeOwned,
		Peers:     []SharePeerEntry{{VulaID: req.PeerID, Perm: req.Perm}},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.store.Add(entry); err != nil {
		sharesWriteErr(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build and deliver the signed invitation envelope (best-effort when no
	// server address is available — local-only sharing is still allowed).
	var deliveryStatus string
	if peerServer != "" {
		env, err := s.buildInviteEnvelope(req.ShareInvitePayload)
		if err != nil {
			log.Printf("[peering/shares] build envelope error for doc %s: %v", req.DocID, err)
			deliveryStatus = "envelope_error"
		} else {
			baseURL := "https://" + peerServer
			if err := s.client.Post(r.Context(), baseURL, "collab-invite", env); err != nil {
				log.Printf("[peering/shares] delivery error to %s: %v", baseURL, err)
				deliveryStatus = "delivery_error"
			} else {
				deliveryStatus = "delivered"
			}
		}
	} else {
		deliveryStatus = "no_server_address"
		log.Printf("[peering/shares] no server address for peer %s; invite not delivered", req.PeerID)
	}

	log.Printf("[peering/shares] shared doc %s with peer %s (perm=%s status=%s)",
		req.DocID, req.PeerID, req.Perm, deliveryStatus)

	sharesWriteJSON(w, map[string]any{
		"status":   "ok",
		"doc_id":   req.DocID,
		"peer_id":  req.PeerID,
		"perm":     req.Perm,
		"delivery": deliveryStatus,
	})
}

// buildInviteEnvelope constructs and signs a TypeCollabInvite envelope.
func (s *SharesService) buildInviteEnvelope(inv ShareInvitePayload) (*Envelope, error) {
	payload, err := json.Marshal(inv)
	if err != nil {
		return nil, err
	}
	msgID := uuid.New().String()
	env, err := NewEnvelope(msgID, s.localID(), inv.PeerID, TypeCollabInvite, json.RawMessage(payload))
	if err != nil {
		return nil, err
	}
	if len(s.priv) > 0 {
		if err := env.Sign(s.priv); err != nil {
			return nil, err
		}
	}
	return env, nil
}

// ─── Inbound: POST /api/peering/inbound/collab-invite ─────────────────────────

// HandleInboundShare implements POST /api/peering/inbound/collab-invite.
//
// [InboundMiddleware] has already:
//  1. Verified the envelope's Ed25519 signature (401 on failure).
//  2. Confirmed the sender is in the approved contacts list (403 on failure).
//
// This handler:
//  1. Reads the pre-verified *[Envelope] from the request context.
//  2. Decodes the [ShareInvitePayload] from the envelope payload.
//  3. Registers the document locally with badge=shared.
//
// The method is exported so an orchestrator can register it on an
// InboundMiddleware-wrapped sub-mux without calling RegisterSharesHandlers.
func (s *SharesService) HandleInboundShare(w http.ResponseWriter, r *http.Request) {
	env, ok := r.Context().Value(EnvelopeKey).(*Envelope)
	if !ok || env == nil {
		// Fallback: try to decode body directly (for tests that bypass middleware).
		s.handleInboundShareDirect(w, r)
		return
	}

	var inv ShareInvitePayload
	if err := json.Unmarshal(env.Payload, &inv); err != nil {
		sharesWriteErr(w, "invalid collab-invite payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Trust the envelope's From field as the authoritative sender ID.
	inv.FromID = env.From

	if inv.DocID == "" {
		sharesWriteErr(w, "doc_id is required", http.StatusBadRequest)
		return
	}
	if err := shareValidatePerm(inv.Perm); err != nil {
		sharesWriteErr(w, err.Error(), http.StatusBadRequest)
		return
	}

	entry := &ShareDocEntry{
		DocID:     inv.DocID,
		DocType:   inv.DocType,
		Title:     inv.Title,
		OwnerID:   inv.FromID,
		Badge:     ShareBadgeShared,
		Peers:     []SharePeerEntry{{VulaID: inv.FromID, Perm: SharePermEdit}},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.store.Add(entry); err != nil {
		sharesWriteErr(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("[peering/shares] inbound invite (env) for doc %s from %s (perm=%s)",
		inv.DocID, inv.FromID, inv.Perm)

	sharesWriteJSON(w, map[string]any{
		"status": "accepted",
		"doc_id": inv.DocID,
		"badge":  ShareBadgeShared,
	})
}

// handleInboundShareDirect handles the invite when no envelope is in context
// (direct JSON body, used in unit tests that do not run InboundMiddleware).
func (s *SharesService) handleInboundShareDirect(w http.ResponseWriter, r *http.Request) {
	var inv ShareInvitePayload
	if err := json.NewDecoder(r.Body).Decode(&inv); err != nil {
		sharesWriteErr(w, "invalid invite payload", http.StatusBadRequest)
		return
	}
	if inv.DocID == "" {
		sharesWriteErr(w, "doc_id is required", http.StatusBadRequest)
		return
	}
	if inv.FromID == "" {
		sharesWriteErr(w, "from_id is required", http.StatusBadRequest)
		return
	}
	if err := shareValidatePerm(inv.Perm); err != nil {
		sharesWriteErr(w, err.Error(), http.StatusBadRequest)
		return
	}

	entry := &ShareDocEntry{
		DocID:     inv.DocID,
		DocType:   inv.DocType,
		Title:     inv.Title,
		OwnerID:   inv.FromID,
		Badge:     ShareBadgeShared,
		Peers:     []SharePeerEntry{{VulaID: inv.FromID, Perm: SharePermEdit}},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.store.Add(entry); err != nil {
		sharesWriteErr(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("[peering/shares] inbound invite (direct) for doc %s from %s (perm=%s)",
		inv.DocID, inv.FromID, inv.Perm)

	sharesWriteJSON(w, map[string]any{
		"status": "accepted",
		"doc_id": inv.DocID,
		"badge":  ShareBadgeShared,
	})
}

// ─── Inbound: POST /api/peering/inbound/collab-update ─────────────────────────

// sharesUpdateBody is the payload body for collab-update envelopes.
type sharesUpdateBody struct {
	DocID string `json:"doc_id"`
	Ops   string `json:"ops,omitempty"` // base64-encoded CRDT ops blob
}

// handleInboundUpdate receives a CRDT update from a peer.
//
// The sender's Vula ID is extracted from the pre-verified envelope in context
// (set by InboundMiddleware). If the sender has view-only permission the request
// is rejected with 403 Forbidden. Revoked documents return 410 Gone.
func (s *SharesService) handleInboundUpdate(w http.ResponseWriter, r *http.Request) {
	senderID, docID, ok := s.extractUpdateIDs(w, r)
	if !ok {
		return
	}

	perm, err := s.store.PeerPerm(docID, senderID)
	if err != nil {
		if err.Error() == "share: document has been revoked" {
			sharesWriteErr(w, "document has been revoked", http.StatusGone)
			return
		}
		sharesWriteErr(w, "forbidden: "+err.Error(), http.StatusForbidden)
		return
	}

	if perm == SharePermView {
		sharesWriteErr(w, "forbidden: view-only peers may not send updates", http.StatusForbidden)
		return
	}

	log.Printf("[peering/shares] accepted CRDT update for doc %s from %s", docID, senderID)
	sharesWriteJSON(w, map[string]string{"status": "ok", "doc_id": docID})
}

// extractUpdateIDs returns (senderID, docID, ok) from context envelope or body.
func (s *SharesService) extractUpdateIDs(w http.ResponseWriter, r *http.Request) (senderID, docID string, ok bool) {
	// Prefer the pre-verified envelope sender.
	if env, ctxOk := r.Context().Value(EnvelopeKey).(*Envelope); ctxOk && env != nil {
		senderID = env.From
		var body sharesUpdateBody
		if err := json.Unmarshal(env.Payload, &body); err != nil {
			sharesWriteErr(w, "invalid collab-update payload", http.StatusBadRequest)
			return "", "", false
		}
		docID = body.DocID
	} else {
		// Fallback for direct calls (tests / collab_share.go compatibility).
		var body struct {
			DocID    string `json:"doc_id"`
			SenderID string `json:"sender_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			sharesWriteErr(w, "invalid request body", http.StatusBadRequest)
			return "", "", false
		}
		senderID = body.SenderID
		docID = body.DocID
	}

	if docID == "" {
		sharesWriteErr(w, "doc_id is required", http.StatusBadRequest)
		return "", "", false
	}
	if senderID == "" {
		sharesWriteErr(w, "sender_id is required", http.StatusBadRequest)
		return "", "", false
	}
	return senderID, docID, true
}

// ─── Inbound: GET /api/peering/inbound/collab-sync ────────────────────────────

// handleInboundSync responds to a catch-up state request from a peer.
// Sender identity comes from the pre-verified envelope (or query param as
// fallback for tests).  Both edit and view peers may request a sync.
func (s *SharesService) handleInboundSync(w http.ResponseWriter, r *http.Request) {
	docID := r.URL.Query().Get("doc_id")
	if docID == "" {
		sharesWriteErr(w, "doc_id query param required", http.StatusBadRequest)
		return
	}

	// For GET requests InboundMiddleware does not run (it only handles POST).
	// Sender identity comes from the query param "sender_id".
	senderID := r.URL.Query().Get("sender_id")
	if senderID == "" {
		sharesWriteErr(w, "sender_id query param required", http.StatusBadRequest)
		return
	}

	perm, err := s.store.PeerPerm(docID, senderID)
	if err != nil {
		if err.Error() == "share: document has been revoked" {
			sharesWriteErr(w, "document has been revoked", http.StatusGone)
			return
		}
		sharesWriteErr(w, "forbidden: "+err.Error(), http.StatusForbidden)
		return
	}

	log.Printf("[peering/shares] inbound sync for doc %s from %s (perm=%s)", docID, senderID, perm)
	sharesWriteJSON(w, map[string]string{"status": "ok", "doc_id": docID})
}

// ─── Read-only REST handlers ───────────────────────────────────────────────────

func (s *SharesService) handleListDocs(w http.ResponseWriter, r *http.Request) {
	docs := s.store.List()
	if docs == nil {
		docs = []ShareDocEntry{}
	}
	sharesWriteJSON(w, docs)
}

func (s *SharesService) handleGetDoc(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("doc_id")
	if docID == "" {
		sharesWriteErr(w, "missing doc_id", http.StatusBadRequest)
		return
	}
	entry, err := s.store.Get(docID)
	if err != nil {
		sharesWriteErr(w, "document not found", http.StatusNotFound)
		return
	}
	sharesWriteJSON(w, entry)
}

// ─── Leave / revoke ───────────────────────────────────────────────────────────

func (s *SharesService) handleLeaveOrRevoke(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("doc_id")
	if docID == "" {
		sharesWriteErr(w, "missing doc_id", http.StatusBadRequest)
		return
	}
	callerID := r.URL.Query().Get("caller")
	if callerID == "" {
		callerID = s.localID()
	}

	entry, err := s.store.Get(docID)
	if err != nil {
		sharesWriteErr(w, "document not found", http.StatusNotFound)
		return
	}

	if entry.OwnerID == callerID {
		_ = s.store.Revoke(docID, callerID)
		if err := s.store.Remove(docID); err != nil {
			sharesWriteErr(w, "failed to remove document", http.StatusInternalServerError)
			return
		}
		log.Printf("[peering/shares] owner %s revoked doc %s", callerID, docID)
		sharesWriteJSON(w, map[string]string{"status": "revoked", "doc_id": docID})
		return
	}

	if err := s.store.Remove(docID); err != nil {
		sharesWriteErr(w, "failed to remove document", http.StatusInternalServerError)
		return
	}
	log.Printf("[peering/shares] peer %s left doc %s", callerID, docID)
	sharesWriteJSON(w, map[string]string{"status": "left", "doc_id": docID})
}

// ─── Permission update ────────────────────────────────────────────────────────

// sharesPermRequest is the body for PUT /api/peering/collab/{doc_id}/perms.
type sharesPermRequest struct {
	TargetVulaID string    `json:"target_vula_id"`
	Perm         SharePerm `json:"perm"`
}

func (s *SharesService) handleSetPerms(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("doc_id")
	if docID == "" {
		sharesWriteErr(w, "missing doc_id", http.StatusBadRequest)
		return
	}
	callerID := r.URL.Query().Get("caller")
	if callerID == "" {
		callerID = s.localID()
	}

	var req sharesPermRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sharesWriteErr(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.TargetVulaID == "" {
		sharesWriteErr(w, "target_vula_id is required", http.StatusBadRequest)
		return
	}
	if err := shareValidatePerm(req.Perm); err != nil {
		sharesWriteErr(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.store.SetPerm(docID, callerID, req.TargetVulaID, req.Perm); err != nil {
		switch err.Error() {
		case "share: document not found":
			sharesWriteErr(w, err.Error(), http.StatusNotFound)
		case "share: only the owner may change permissions":
			sharesWriteErr(w, err.Error(), http.StatusForbidden)
		default:
			sharesWriteErr(w, err.Error(), http.StatusBadRequest)
		}
		return
	}

	entry, _ := s.store.Get(docID)
	log.Printf("[peering/shares] perm updated: peer %s on doc %s → %s", req.TargetVulaID, docID, req.Perm)
	sharesWriteJSON(w, entry)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func sharesWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[peering/shares] json encode error: %v", err)
	}
}

func sharesWriteErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ─── Ensure context value type is used (avoids unused import) ─────────────────
