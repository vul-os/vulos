package peering

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Contract 4 — per-document authorization on the collab inbound + WS paths.
//
// These tests pin the security fix: with a ShareStore wired, the live inbound
// CRDT path (HandleInboundCollabUpdate) must reject a view-only peer's write and
// a write to a document not shared with the sender, while accepting an editor's
// write; and the room WebSocket authorizer must reject unauthenticated joins and
// non-shared peers.

// collabStoreWithShares builds a CollabStore (temp dir) wired to a fresh
// ShareStore, plus the ShareStore for the test to seed entries.
func collabStoreWithShares(t *testing.T) (*CollabStore, *ShareStore) {
	t.Helper()
	s := newTestCollabStore(t)
	shares := NewShareStore()
	s.WithShareStore(shares)
	return s, shares
}

// inboundUpdateReq builds a POST inbound collab-update request whose verified
// envelope is set on the context (as InboundMiddleware would).
func inboundUpdateReq(from, docID string, ops []byte) *http.Request {
	inner, _ := json.Marshal(map[string]string{
		"doc_id": docID,
		"ops":    base64.StdEncoding.EncodeToString(ops),
	})
	env := &Envelope{ID: "t", From: from, Type: TypeCollabUpdate, Payload: json.RawMessage(inner)}
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/collab-update", nil)
	return req.WithContext(context.WithValue(req.Context(), EnvelopeKey, env))
}

func TestInboundCollabUpdate_ViewOnlyPeerRejected(t *testing.T) {
	s, shares := collabStoreWithShares(t)
	const peer = "vula:ed25519:viewer"
	// Owner shared doc with the peer as VIEW only.
	_ = shares.Add(&ShareDocEntry{
		DocID:   "doc-v",
		OwnerID: "vula:ed25519:owner",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulaID: peer, Perm: SharePermView}},
	})

	rr := httptest.NewRecorder()
	s.HandleInboundCollabUpdate(rr, inboundUpdateReq(peer, "doc-v", []byte("evil-ops")))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("view-only write status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
	// Must NOT have persisted anything.
	if state, _ := s.loadState("doc-v"); state != nil {
		t.Errorf("view-only write was persisted (%q); it must be rejected before persist", state)
	}
}

func TestInboundCollabUpdate_NonSharedDocRejected(t *testing.T) {
	s, _ := collabStoreWithShares(t) // empty share store
	rr := httptest.NewRecorder()
	s.HandleInboundCollabUpdate(rr, inboundUpdateReq("vula:ed25519:stranger", "ghost-doc", []byte("ops")))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-shared doc write status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
	if state, _ := s.loadState("ghost-doc"); state != nil {
		t.Errorf("write to non-shared doc was persisted; must be rejected")
	}
}

func TestInboundCollabUpdate_EditorAccepted(t *testing.T) {
	s, shares := collabStoreWithShares(t)
	const peer = "vula:ed25519:editor"
	_ = shares.Add(&ShareDocEntry{
		DocID:   "doc-e",
		OwnerID: "vula:ed25519:owner",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulaID: peer, Perm: SharePermEdit}},
	})

	ops := []byte("legit-crdt-ops")
	rr := httptest.NewRecorder()
	s.HandleInboundCollabUpdate(rr, inboundUpdateReq(peer, "doc-e", ops))

	if rr.Code != http.StatusOK {
		t.Fatalf("editor write status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if state, _ := s.loadState("doc-e"); string(state) != string(ops) {
		t.Errorf("editor ops not persisted: got %q want %q", state, ops)
	}
}

func TestInboundCollabUpdate_RevokedDocGone(t *testing.T) {
	s, shares := collabStoreWithShares(t)
	const owner = "vula:ed25519:owner"
	const peer = "vula:ed25519:editor"
	_ = shares.Add(&ShareDocEntry{
		DocID:   "doc-r",
		OwnerID: owner,
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulaID: peer, Perm: SharePermEdit}},
	})
	_ = shares.Revoke("doc-r", owner)

	rr := httptest.NewRecorder()
	s.HandleInboundCollabUpdate(rr, inboundUpdateReq(peer, "doc-r", []byte("ops")))
	if rr.Code != http.StatusGone {
		t.Fatalf("revoked doc write status = %d, want 410; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAuthorizeRoom_UnauthenticatedRejected(t *testing.T) {
	s, _ := collabStoreWithShares(t)
	req := httptest.NewRequest(http.MethodGet, "/api/peering/collab/doc-x/sync", nil)
	if _, status := s.authorizeRoom(req, "doc-x"); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated WS join status = %d, want 401", status)
	}
}

func TestAuthorizeRoom_SharedPeerAllowed_StrangerRejected(t *testing.T) {
	s, shares := collabStoreWithShares(t)
	const viewer = "vula:ed25519:viewer"
	_ = shares.Add(&ShareDocEntry{
		DocID:   "doc-room",
		OwnerID: "vula:ed25519:owner",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulaID: viewer, Perm: SharePermView}},
	})

	// A viewer may JOIN the room (to receive ops); status 0 = authorized.
	req := httptest.NewRequest(http.MethodGet, "/api/peering/collab/doc-room/sync", nil)
	req.Header.Set("X-Vula-ID", viewer)
	if id, status := s.authorizeRoom(req, "doc-room"); status != 0 || id != viewer {
		t.Fatalf("viewer room join = (%q,%d), want (%q,0)", id, status, viewer)
	}

	// A stranger with no entry on a tracked shared doc is rejected.
	req2 := httptest.NewRequest(http.MethodGet, "/api/peering/collab/doc-room/sync", nil)
	req2.Header.Set("X-Vula-ID", "vula:ed25519:stranger")
	if _, status := s.authorizeRoom(req2, "doc-room"); status != http.StatusForbidden {
		t.Fatalf("stranger room join status = %d, want 403", status)
	}
}

func TestAuthorizeRoom_PrivateDocAuthedAllowed(t *testing.T) {
	s, _ := collabStoreWithShares(t) // doc not tracked as a share
	req := httptest.NewRequest(http.MethodGet, "/api/peering/collab/local-doc/sync", nil)
	req.Header.Set("X-User-ID", "os-user-1")
	if id, status := s.authorizeRoom(req, "local-doc"); status != 0 || id != "os-user-1" {
		t.Fatalf("authed private-doc join = (%q,%d), want (\"os-user-1\",0)", id, status)
	}
}
