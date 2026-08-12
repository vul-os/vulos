// Package peering — collab_share.go
//
// Document share/accept flow, per-peer permissions, edit/view enforcement,
// owner revoke, and document list/leave.
//
// All HTTP endpoints are registered by [RegisterCollabShareHandlers].
// The types and helpers in this file are prefixed with "Share" / "share"
// to avoid any symbol collision with collab.go.
//
// HTTP surface registered by RegisterCollabShareHandlers:
//
//	POST   /api/peering/collab/share              send doc-share invitation
//	GET    /api/peering/collab/documents           list shared docs
//	GET    /api/peering/collab/{doc_id}            get doc metadata + perm
//	DELETE /api/peering/collab/{doc_id}            leave (peer) or revoke (owner)
//	PUT    /api/peering/collab/{doc_id}/perms      update a peer's permission
//
//	POST /api/peering/inbound/collab-invite        receive invitation → Shared badge
//	POST /api/peering/inbound/collab-update        receive CRDT ops (view-only → 403)
//	GET  /api/peering/inbound/collab-sync          diff request from peer
package peering

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Domain constants
// ---------------------------------------------------------------------------

// SharePerm defines what a peer may do with a shared document.
type SharePerm string

const (
	// SharePermEdit allows the peer to send and receive CRDT operations.
	SharePermEdit SharePerm = "edit"

	// SharePermView allows the peer to receive CRDT operations only.
	// Attempts to push operations from a view-only peer are rejected 403.
	SharePermView SharePerm = "view"
)

// ShareDocBadge describes how a document appears in the local list.
type ShareDocBadge string

const (
	// ShareBadgeOwned marks documents this instance created and shared out.
	ShareBadgeOwned ShareDocBadge = "owned"

	// ShareBadgeShared marks documents received via an invitation.
	ShareBadgeShared ShareDocBadge = "shared"
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// SharePeerEntry records a single peer's access level for a shared document.
type SharePeerEntry struct {
	VulosID string    `json:"vulos_id"`
	Perm    SharePerm `json:"perm"` // SharePermEdit | SharePermView
}

// ShareDocEntry is the in-memory + on-wire record for one shared document.
type ShareDocEntry struct {
	DocID     string           `json:"doc_id"`
	DocType   string           `json:"doc_type"` // "doc","sheet","slide","note","code"
	Title     string           `json:"title"`
	OwnerID   string           `json:"owner_id"`
	Badge     ShareDocBadge    `json:"badge"`             // ShareBadgeOwned | ShareBadgeShared
	Revoked   bool             `json:"revoked,omitempty"` // true after owner revoke
	Peers     []SharePeerEntry `json:"peers"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// ShareInvitePayload is the body sent by POST /api/peering/collab/share
// and forwarded (conceptually) to the peer as an invitation.
type ShareInvitePayload struct {
	DocID   string    `json:"doc_id"`
	DocType string    `json:"doc_type"`
	Title   string    `json:"title"`
	PeerID  string    `json:"peer_id"` // recipient's Vula ID
	Perm    SharePerm `json:"perm"`    // SharePermEdit | SharePermView
	FromID  string    `json:"from_id"` // sender's Vula ID
}

// shareUpdateBody is the body expected by POST /api/peering/inbound/collab-update.
type shareUpdateBody struct {
	DocID    string `json:"doc_id"`
	SenderID string `json:"sender_id"`
}

// sharePermRequest is the body for PUT /api/peering/collab/{doc_id}/perms.
type sharePermRequest struct {
	TargetVulosID string    `json:"target_vulos_id"`
	Perm          SharePerm `json:"perm"`
}

// ---------------------------------------------------------------------------
// ShareStore — in-memory store
// ---------------------------------------------------------------------------

// ShareStore holds all shared-document state.  It is goroutine-safe.
type ShareStore struct {
	mu   sync.RWMutex
	docs map[string]*ShareDocEntry // doc_id → entry
}

// NewShareStore creates an empty ShareStore.
func NewShareStore() *ShareStore {
	return &ShareStore{docs: make(map[string]*ShareDocEntry)}
}

// Add registers a new shared document.  If the doc already exists, Add is a no-op
// and returns nil (idempotent re-delivery support).
func (s *ShareStore) Add(doc *ShareDocEntry) error {
	if doc == nil {
		return errors.New("share: nil doc")
	}
	if doc.DocID == "" {
		return errors.New("share: empty doc_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.docs[doc.DocID]; !exists {
		cp := *doc
		if cp.CreatedAt.IsZero() {
			cp.CreatedAt = time.Now().UTC()
		}
		cp.UpdatedAt = time.Now().UTC()
		s.docs[doc.DocID] = &cp
	}
	return nil
}

// Get returns a copy of the document entry for docID.
func (s *ShareStore) Get(docID string) (ShareDocEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.docs[docID]
	if !ok {
		return ShareDocEntry{}, errors.New("share: document not found")
	}
	return *e, nil
}

// List returns copies of all document entries.
func (s *ShareStore) List() []ShareDocEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ShareDocEntry, 0, len(s.docs))
	for _, e := range s.docs {
		out = append(out, *e)
	}
	return out
}

// Remove deletes a document entry entirely.
func (s *ShareStore) Remove(docID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.docs[docID]; !ok {
		return errors.New("share: document not found")
	}
	delete(s.docs, docID)
	return nil
}

// SetPerm changes the permission for targetVulosID on docID.
// callerVulosID must be the document owner.
func (s *ShareStore) SetPerm(docID, callerVulosID, targetVulosID string, perm SharePerm) error {
	if err := shareValidatePerm(perm); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.docs[docID]
	if !ok {
		return errors.New("share: document not found")
	}
	if e.OwnerID != callerVulosID {
		return errors.New("share: only the owner may change permissions")
	}
	for i, p := range e.Peers {
		if p.VulosID == targetVulosID {
			e.Peers[i].Perm = perm
			e.UpdatedAt = time.Now().UTC()
			return nil
		}
	}
	e.Peers = append(e.Peers, SharePeerEntry{VulosID: targetVulosID, Perm: perm})
	e.UpdatedAt = time.Now().UTC()
	return nil
}

// Revoke marks a document as revoked. Only the owner may revoke.
// After revocation, PeerPerm returns an error indicating the document is gone.
func (s *ShareStore) Revoke(docID, callerVulosID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.docs[docID]
	if !ok {
		return errors.New("share: document not found")
	}
	if e.OwnerID != callerVulosID {
		return errors.New("share: only the owner may revoke")
	}
	e.Revoked = true
	e.UpdatedAt = time.Now().UTC()
	return nil
}

// PeerPerm returns the permission for vulosID on docID.
// Returns an error if the document is not found, the peer has no entry, or
// the document has been revoked.
func (s *ShareStore) PeerPerm(docID, vulosID string) (SharePerm, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.docs[docID]
	if !ok {
		return "", errors.New("share: document not found")
	}
	if e.Revoked {
		return "", errors.New("share: document has been revoked")
	}
	// Owner always has edit permission.
	if e.OwnerID == vulosID {
		return SharePermEdit, nil
	}
	for _, p := range e.Peers {
		if p.VulosID == vulosID {
			return p.Perm, nil
		}
	}
	return "", errors.New("share: peer has no permission for this document")
}

// ---------------------------------------------------------------------------
// CollabShareService — HTTP layer
// ---------------------------------------------------------------------------

// CollabShareService wires the ShareStore to the HTTP mux.
type CollabShareService struct {
	store        *ShareStore
	localVulosID string // this instance's Vula ID (populated from env/identity)
}

// newCollabShareService creates a service backed by store.
func newCollabShareService(store *ShareStore, localVulosID string) *CollabShareService {
	return &CollabShareService{store: store, localVulosID: localVulosID}
}

// localID returns the best available local Vula ID.
func (svc *CollabShareService) localID() string {
	if svc.localVulosID != "" {
		return svc.localVulosID
	}
	return "local"
}

// ---------------------------------------------------------------------------
// RegisterCollabShareHandlers — public registration entry point
// ---------------------------------------------------------------------------

// RegisterCollabShareHandlers wires all document share/accept endpoints onto
// mux using a fresh in-memory ShareStore.
//
// Call this once at startup alongside RegisterCollabHandlers when both the
// collab transport and share-permission layers are needed.
//
//	POST   /api/peering/collab/share              send doc-share invitation
//	GET    /api/peering/collab/documents           list shared docs
//	GET    /api/peering/collab/{doc_id}            get doc metadata + perm
//	DELETE /api/peering/collab/{doc_id}            leave or revoke
//	PUT    /api/peering/collab/{doc_id}/perms      update a peer's permission
//
//	POST /api/peering/inbound/collab-invite        receive invitation
//	POST /api/peering/inbound/collab-update        receive CRDT ops (view-only → 403)
//	GET  /api/peering/inbound/collab-sync          diff request from peer
func RegisterCollabShareHandlers(mux *http.ServeMux) {
	store := NewShareStore()
	svc := newCollabShareService(store, "")
	registerCollabShareHandlersWithService(mux, svc)
}

// registerCollabShareHandlersWithService is the testable inner registration.
// Tests inject a pre-configured service (with a known localVulosID).
func registerCollabShareHandlersWithService(mux *http.ServeMux, svc *CollabShareService) {
	// Client-facing
	mux.HandleFunc("POST /api/peering/collab/share", svc.handleShareDoc)
	mux.HandleFunc("GET /api/peering/collab/documents", svc.handleListDocs)
	mux.HandleFunc("GET /api/peering/collab/{doc_id}", svc.handleGetDoc)
	mux.HandleFunc("DELETE /api/peering/collab/{doc_id}", svc.handleLeaveOrRevoke)
	mux.HandleFunc("PUT /api/peering/collab/{doc_id}/perms", svc.handleSetPerms)

	// Server-to-server inbound
	mux.HandleFunc("POST /api/peering/inbound/collab-invite", svc.handleInboundInvite)
	mux.HandleFunc("POST /api/peering/inbound/collab-update", svc.handleInboundUpdate)
	mux.HandleFunc("GET /api/peering/inbound/collab-sync", svc.handleInboundSync)
}

// ---------------------------------------------------------------------------
// Handler: POST /api/peering/collab/share
// ---------------------------------------------------------------------------

// handleShareDoc sends a doc-share invitation to a peer.
// The caller must supply a ShareInvitePayload.  The handler:
//  1. Validates the request.
//  2. Registers the document in the local ShareStore with badge=owned.
//  3. Returns the InvitePayload that would be forwarded to the peer server.
func (svc *CollabShareService) handleShareDoc(w http.ResponseWriter, r *http.Request) {
	var inv ShareInvitePayload
	if err := json.NewDecoder(r.Body).Decode(&inv); err != nil {
		shareWriteErr(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if inv.DocID == "" {
		shareWriteErr(w, "doc_id is required", http.StatusBadRequest)
		return
	}
	if inv.PeerID == "" {
		shareWriteErr(w, "peer_id is required", http.StatusBadRequest)
		return
	}
	if err := shareValidatePerm(inv.Perm); err != nil {
		shareWriteErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if inv.FromID == "" {
		inv.FromID = svc.localID()
	}

	entry := &ShareDocEntry{
		DocID:     inv.DocID,
		DocType:   inv.DocType,
		Title:     inv.Title,
		OwnerID:   inv.FromID,
		Badge:     ShareBadgeOwned,
		Peers:     []SharePeerEntry{{VulosID: inv.PeerID, Perm: inv.Perm}},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := svc.store.Add(entry); err != nil {
		shareWriteErr(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("[peering/share] shared doc %s with peer %s (perm=%s)", inv.DocID, inv.PeerID, inv.Perm)
	shareWriteJSON(w, inv)
}

// ---------------------------------------------------------------------------
// Handler: GET /api/peering/collab/documents
// ---------------------------------------------------------------------------

func (svc *CollabShareService) handleListDocs(w http.ResponseWriter, r *http.Request) {
	docs := svc.store.List()
	if docs == nil {
		docs = []ShareDocEntry{}
	}
	shareWriteJSON(w, docs)
}

// ---------------------------------------------------------------------------
// Handler: GET /api/peering/collab/{doc_id}
// ---------------------------------------------------------------------------

func (svc *CollabShareService) handleGetDoc(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("doc_id")
	if docID == "" {
		shareWriteErr(w, "missing doc_id", http.StatusBadRequest)
		return
	}
	entry, err := svc.store.Get(docID)
	if err != nil {
		shareWriteErr(w, "document not found", http.StatusNotFound)
		return
	}
	shareWriteJSON(w, entry)
}

// ---------------------------------------------------------------------------
// Handler: DELETE /api/peering/collab/{doc_id}
// ---------------------------------------------------------------------------

// handleLeaveOrRevoke removes a document from the local store.
//   - If the caller is the owner, the document is marked Revoked before removal.
//   - Peers (non-owners) simply leave (remove their local record).
//
// Query param ?caller=<vulos_id> identifies the caller; falls back to localID.
func (svc *CollabShareService) handleLeaveOrRevoke(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("doc_id")
	if docID == "" {
		shareWriteErr(w, "missing doc_id", http.StatusBadRequest)
		return
	}

	callerID := r.URL.Query().Get("caller")
	if callerID == "" {
		callerID = svc.localID()
	}

	entry, err := svc.store.Get(docID)
	if err != nil {
		shareWriteErr(w, "document not found", http.StatusNotFound)
		return
	}

	if entry.OwnerID == callerID {
		// Owner revokes: mark revoked first, then remove.
		_ = svc.store.Revoke(docID, callerID)
		if err := svc.store.Remove(docID); err != nil {
			shareWriteErr(w, "failed to remove document", http.StatusInternalServerError)
			return
		}
		log.Printf("[peering/share] owner %s revoked doc %s", callerID, docID)
		shareWriteJSON(w, map[string]string{"status": "revoked", "doc_id": docID})
		return
	}

	// Non-owner: just leave.
	if err := svc.store.Remove(docID); err != nil {
		shareWriteErr(w, "failed to remove document", http.StatusInternalServerError)
		return
	}
	log.Printf("[peering/share] peer %s left doc %s", callerID, docID)
	shareWriteJSON(w, map[string]string{"status": "left", "doc_id": docID})
}

// ---------------------------------------------------------------------------
// Handler: PUT /api/peering/collab/{doc_id}/perms
// ---------------------------------------------------------------------------

// handleSetPerms updates the permission for a specific peer on a document.
// The caller must be the document owner.
// Query param ?caller=<vulos_id> identifies the owner; falls back to localID.
func (svc *CollabShareService) handleSetPerms(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("doc_id")
	if docID == "" {
		shareWriteErr(w, "missing doc_id", http.StatusBadRequest)
		return
	}

	callerID := r.URL.Query().Get("caller")
	if callerID == "" {
		callerID = svc.localID()
	}

	var req sharePermRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		shareWriteErr(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.TargetVulosID == "" {
		shareWriteErr(w, "target_vulos_id is required", http.StatusBadRequest)
		return
	}
	if err := shareValidatePerm(req.Perm); err != nil {
		shareWriteErr(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := svc.store.SetPerm(docID, callerID, req.TargetVulosID, req.Perm); err != nil {
		switch {
		case err.Error() == "share: document not found":
			shareWriteErr(w, err.Error(), http.StatusNotFound)
		case err.Error() == "share: only the owner may change permissions":
			shareWriteErr(w, err.Error(), http.StatusForbidden)
		default:
			shareWriteErr(w, err.Error(), http.StatusBadRequest)
		}
		return
	}

	entry, _ := svc.store.Get(docID)
	log.Printf("[peering/share] perm updated for peer %s on doc %s: %s", req.TargetVulosID, docID, req.Perm)
	shareWriteJSON(w, entry)
}

// ---------------------------------------------------------------------------
// Handler: POST /api/peering/inbound/collab-invite
// ---------------------------------------------------------------------------

// handleInboundInvite receives a doc-share invitation from a peer server and
// registers the document locally with badge=shared.
// The request body is a ShareInvitePayload sent by the originating instance.
func (svc *CollabShareService) handleInboundInvite(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		shareWriteErr(w, "failed to read body", http.StatusBadRequest)
		return
	}
	var inv ShareInvitePayload
	if err := json.Unmarshal(body, &inv); err != nil {
		shareWriteErr(w, "invalid invite payload", http.StatusBadRequest)
		return
	}
	if inv.DocID == "" {
		shareWriteErr(w, "doc_id is required", http.StatusBadRequest)
		return
	}
	if inv.FromID == "" {
		shareWriteErr(w, "from_id is required", http.StatusBadRequest)
		return
	}
	if err := shareValidatePerm(inv.Perm); err != nil {
		shareWriteErr(w, err.Error(), http.StatusBadRequest)
		return
	}

	entry := &ShareDocEntry{
		DocID:     inv.DocID,
		DocType:   inv.DocType,
		Title:     inv.Title,
		OwnerID:   inv.FromID,
		Badge:     ShareBadgeShared, // this instance is a recipient
		Peers:     []SharePeerEntry{{VulosID: inv.FromID, Perm: SharePermEdit}},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := svc.store.Add(entry); err != nil {
		shareWriteErr(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("[peering/share] inbound invite for doc %s from %s (perm=%s)", inv.DocID, inv.FromID, inv.Perm)
	shareWriteJSON(w, map[string]any{
		"status": "accepted",
		"doc_id": inv.DocID,
		"badge":  ShareBadgeShared,
	})
}

// ---------------------------------------------------------------------------
// Handler: POST /api/peering/inbound/collab-update
// ---------------------------------------------------------------------------

// handleInboundUpdate receives a CRDT update from a peer.
// If the sender's permission is SharePermView (or the document is revoked /
// unknown), the request is rejected with 403 Forbidden or 410 Gone.
//
// Body fields used:
//
//	doc_id     — document identifier
//	sender_id  — Vula ID of the sending peer
func (svc *CollabShareService) handleInboundUpdate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		shareWriteErr(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var msg shareUpdateBody
	if err := json.Unmarshal(body, &msg); err != nil {
		shareWriteErr(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if msg.DocID == "" {
		shareWriteErr(w, "doc_id is required", http.StatusBadRequest)
		return
	}
	if msg.SenderID == "" {
		shareWriteErr(w, "sender_id is required", http.StatusBadRequest)
		return
	}

	perm, err := svc.store.PeerPerm(msg.DocID, msg.SenderID)
	if err != nil {
		if err.Error() == "share: document has been revoked" {
			shareWriteErr(w, "document has been revoked", http.StatusGone)
			return
		}
		// Document not found or peer has no permission.
		shareWriteErr(w, "forbidden: "+err.Error(), http.StatusForbidden)
		return
	}

	if perm == SharePermView {
		shareWriteErr(w, "forbidden: view-only peers may not send updates", http.StatusForbidden)
		return
	}

	// Permission is edit — accept the update.
	shareWriteJSON(w, map[string]string{"status": "ok", "doc_id": msg.DocID})
}

// ---------------------------------------------------------------------------
// Handler: GET /api/peering/inbound/collab-sync
// ---------------------------------------------------------------------------

// handleInboundSync responds to a catch-up state request from a peer.
// The caller passes ?doc_id=<id>&sender_id=<vulos_id>.
// Permission is verified before returning state.
func (svc *CollabShareService) handleInboundSync(w http.ResponseWriter, r *http.Request) {
	docID := r.URL.Query().Get("doc_id")
	senderID := r.URL.Query().Get("sender_id")

	if docID == "" {
		shareWriteErr(w, "doc_id query param required", http.StatusBadRequest)
		return
	}
	if senderID == "" {
		shareWriteErr(w, "sender_id query param required", http.StatusBadRequest)
		return
	}

	perm, err := svc.store.PeerPerm(docID, senderID)
	if err != nil {
		if err.Error() == "share: document has been revoked" {
			shareWriteErr(w, "document has been revoked", http.StatusGone)
			return
		}
		shareWriteErr(w, "forbidden: "+err.Error(), http.StatusForbidden)
		return
	}

	// Both edit and view peers may request a sync (read-only operation).
	log.Printf("[peering/share] inbound sync for doc %s from %s (perm=%s)", docID, senderID, perm)
	shareWriteJSON(w, map[string]string{"status": "ok", "doc_id": docID})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func shareValidatePerm(p SharePerm) error {
	switch p {
	case SharePermEdit, SharePermView:
		return nil
	case "":
		return errors.New("share: perm is required")
	default:
		return errors.New("share: invalid perm; must be \"edit\" or \"view\"")
	}
}

func shareWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[peering/share] json encode error: %v", err)
	}
}

func shareWriteErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
