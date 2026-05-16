package peering

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newShareTestService creates a CollabShareService with a known local ID.
func newShareTestService(localVulaID string) *CollabShareService {
	return newCollabShareService(NewShareStore(), localVulaID)
}

// shareMux registers handlers on a fresh mux and returns it.
func shareMux(svc *CollabShareService) *http.ServeMux {
	mux := http.NewServeMux()
	registerCollabShareHandlersWithService(mux, svc)
	return mux
}

// doShareRequest performs an HTTP request against mux and returns the response.
func doShareRequest(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// ShareStore unit tests
// ---------------------------------------------------------------------------

func TestShareStore_AddAndGet(t *testing.T) {
	s := NewShareStore()
	doc := &ShareDocEntry{
		DocID:   "doc-1",
		DocType: "doc",
		Title:   "Test Doc",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulaID: "bob", Perm: SharePermEdit}},
	}
	if err := s.Add(doc); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := s.Get("doc-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DocID != "doc-1" {
		t.Errorf("got DocID %q, want %q", got.DocID, "doc-1")
	}
	if got.OwnerID != "alice" {
		t.Errorf("got OwnerID %q, want alice", got.OwnerID)
	}
}

func TestShareStore_AddIdempotent(t *testing.T) {
	s := NewShareStore()
	doc := &ShareDocEntry{DocID: "doc-1", OwnerID: "alice", Badge: ShareBadgeOwned}
	_ = s.Add(doc)
	// Second Add should be idempotent (no error, no duplicate).
	if err := s.Add(doc); err != nil {
		t.Fatalf("second Add returned error: %v", err)
	}
	if n := len(s.List()); n != 1 {
		t.Errorf("List length = %d, want 1", n)
	}
}

func TestShareStore_AddNilDoc(t *testing.T) {
	s := NewShareStore()
	if err := s.Add(nil); err == nil {
		t.Error("Add(nil) should return an error")
	}
}

func TestShareStore_AddEmptyDocID(t *testing.T) {
	s := NewShareStore()
	if err := s.Add(&ShareDocEntry{OwnerID: "alice"}); err == nil {
		t.Error("Add with empty DocID should return an error")
	}
}

func TestShareStore_GetNotFound(t *testing.T) {
	s := NewShareStore()
	if _, err := s.Get("nonexistent"); err == nil {
		t.Error("Get on missing doc should return error")
	}
}

func TestShareStore_List(t *testing.T) {
	s := NewShareStore()
	for _, id := range []string{"doc-a", "doc-b", "doc-c"} {
		_ = s.Add(&ShareDocEntry{DocID: id, OwnerID: "alice", Badge: ShareBadgeOwned})
	}
	if n := len(s.List()); n != 3 {
		t.Errorf("List length = %d, want 3", n)
	}
}

func TestShareStore_Remove(t *testing.T) {
	s := NewShareStore()
	_ = s.Add(&ShareDocEntry{DocID: "doc-1", OwnerID: "alice", Badge: ShareBadgeOwned})
	if err := s.Remove("doc-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := s.Get("doc-1"); err == nil {
		t.Error("Get after Remove should return error")
	}
}

func TestShareStore_RemoveNotFound(t *testing.T) {
	s := NewShareStore()
	if err := s.Remove("nonexistent"); err == nil {
		t.Error("Remove on missing doc should return error")
	}
}

func TestShareStore_SetPerm(t *testing.T) {
	s := NewShareStore()
	_ = s.Add(&ShareDocEntry{
		DocID:   "doc-1",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulaID: "bob", Perm: SharePermEdit}},
	})
	if err := s.SetPerm("doc-1", "alice", "bob", SharePermView); err != nil {
		t.Fatalf("SetPerm: %v", err)
	}
	perm, err := s.PeerPerm("doc-1", "bob")
	if err != nil {
		t.Fatalf("PeerPerm: %v", err)
	}
	if perm != SharePermView {
		t.Errorf("got perm %q, want %q", perm, SharePermView)
	}
}

func TestShareStore_SetPerm_OnlyOwner(t *testing.T) {
	s := NewShareStore()
	_ = s.Add(&ShareDocEntry{
		DocID:   "doc-1",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulaID: "bob", Perm: SharePermEdit}},
	})
	if err := s.SetPerm("doc-1", "bob", "alice", SharePermView); err == nil {
		t.Error("SetPerm by non-owner should return error")
	}
}

func TestShareStore_SetPerm_InvalidPerm(t *testing.T) {
	s := NewShareStore()
	_ = s.Add(&ShareDocEntry{DocID: "doc-1", OwnerID: "alice", Badge: ShareBadgeOwned})
	if err := s.SetPerm("doc-1", "alice", "bob", "superuser"); err == nil {
		t.Error("SetPerm with invalid perm should return error")
	}
}

func TestShareStore_Revoke(t *testing.T) {
	s := NewShareStore()
	_ = s.Add(&ShareDocEntry{
		DocID:   "doc-1",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulaID: "bob", Perm: SharePermEdit}},
	})
	if err := s.Revoke("doc-1", "alice"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// After revoke, PeerPerm should error.
	if _, err := s.PeerPerm("doc-1", "bob"); err == nil {
		t.Error("PeerPerm after Revoke should return error")
	}
}

func TestShareStore_Revoke_OnlyOwner(t *testing.T) {
	s := NewShareStore()
	_ = s.Add(&ShareDocEntry{
		DocID:   "doc-1",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulaID: "bob", Perm: SharePermEdit}},
	})
	if err := s.Revoke("doc-1", "bob"); err == nil {
		t.Error("Revoke by non-owner should return error")
	}
}

func TestShareStore_PeerPerm_Owner(t *testing.T) {
	s := NewShareStore()
	_ = s.Add(&ShareDocEntry{DocID: "doc-1", OwnerID: "alice", Badge: ShareBadgeOwned})
	perm, err := s.PeerPerm("doc-1", "alice")
	if err != nil {
		t.Fatalf("PeerPerm for owner: %v", err)
	}
	if perm != SharePermEdit {
		t.Errorf("owner perm = %q, want edit", perm)
	}
}

func TestShareStore_PeerPerm_NoPerm(t *testing.T) {
	s := NewShareStore()
	_ = s.Add(&ShareDocEntry{DocID: "doc-1", OwnerID: "alice", Badge: ShareBadgeOwned})
	if _, err := s.PeerPerm("doc-1", "eve"); err == nil {
		t.Error("PeerPerm for unknown peer should return error")
	}
}

// ---------------------------------------------------------------------------
// HTTP handler tests
// ---------------------------------------------------------------------------

func TestHandleShareDoc_Success(t *testing.T) {
	svc := newShareTestService("alice")
	mux := shareMux(svc)

	inv := ShareInvitePayload{
		DocID:   "doc-share-1",
		DocType: "doc",
		Title:   "Shared Doc",
		PeerID:  "bob",
		Perm:    SharePermEdit,
		FromID:  "alice",
	}
	rr := doShareRequest(t, mux, http.MethodPost, "/api/peering/collab/share", inv)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /share status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// The document should now be in the store.
	entry, err := svc.store.Get("doc-share-1")
	if err != nil {
		t.Fatalf("store.Get after share: %v", err)
	}
	if entry.Badge != ShareBadgeOwned {
		t.Errorf("badge = %q, want owned", entry.Badge)
	}
}

func TestHandleShareDoc_MissingDocID(t *testing.T) {
	mux := shareMux(newShareTestService("alice"))
	inv := ShareInvitePayload{PeerID: "bob", Perm: SharePermEdit}
	rr := doShareRequest(t, mux, http.MethodPost, "/api/peering/collab/share", inv)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleShareDoc_MissingPeerID(t *testing.T) {
	mux := shareMux(newShareTestService("alice"))
	inv := ShareInvitePayload{DocID: "doc-1", Perm: SharePermEdit}
	rr := doShareRequest(t, mux, http.MethodPost, "/api/peering/collab/share", inv)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleShareDoc_InvalidPerm(t *testing.T) {
	mux := shareMux(newShareTestService("alice"))
	inv := ShareInvitePayload{DocID: "doc-1", PeerID: "bob", Perm: "admin"}
	rr := doShareRequest(t, mux, http.MethodPost, "/api/peering/collab/share", inv)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleListDocs(t *testing.T) {
	svc := newShareTestService("alice")
	_ = svc.store.Add(&ShareDocEntry{DocID: "doc-1", OwnerID: "alice", Badge: ShareBadgeOwned, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	_ = svc.store.Add(&ShareDocEntry{DocID: "doc-2", OwnerID: "bob", Badge: ShareBadgeShared, CreatedAt: time.Now(), UpdatedAt: time.Now()})

	mux := shareMux(svc)
	rr := doShareRequest(t, mux, http.MethodGet, "/api/peering/collab/documents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var docs []ShareDocEntry
	if err := json.NewDecoder(rr.Body).Decode(&docs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(docs) != 2 {
		t.Errorf("len(docs) = %d, want 2", len(docs))
	}
}

func TestHandleGetDoc_Found(t *testing.T) {
	svc := newShareTestService("alice")
	_ = svc.store.Add(&ShareDocEntry{DocID: "doc-x", OwnerID: "alice", Badge: ShareBadgeOwned, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	mux := shareMux(svc)
	rr := doShareRequest(t, mux, http.MethodGet, "/api/peering/collab/doc-x", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleGetDoc_NotFound(t *testing.T) {
	mux := shareMux(newShareTestService("alice"))
	rr := doShareRequest(t, mux, http.MethodGet, "/api/peering/collab/nonexistent", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestHandleLeaveOrRevoke_Owner(t *testing.T) {
	svc := newShareTestService("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID: "doc-r", OwnerID: "alice", Badge: ShareBadgeOwned,
		Peers: []SharePeerEntry{{VulaID: "bob", Perm: SharePermEdit}},
	})
	mux := shareMux(svc)
	rr := doShareRequest(t, mux, http.MethodDelete, "/api/peering/collab/doc-r?caller=alice", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "revoked" {
		t.Errorf("status = %q, want revoked", resp["status"])
	}
	// Document should be removed.
	if _, err := svc.store.Get("doc-r"); err == nil {
		t.Error("doc should be removed after owner revoke")
	}
}

func TestHandleLeaveOrRevoke_Peer(t *testing.T) {
	svc := newShareTestService("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID: "doc-l", OwnerID: "alice", Badge: ShareBadgeOwned,
		Peers: []SharePeerEntry{{VulaID: "bob", Perm: SharePermEdit}},
	})
	mux := shareMux(svc)
	rr := doShareRequest(t, mux, http.MethodDelete, "/api/peering/collab/doc-l?caller=bob", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "left" {
		t.Errorf("status = %q, want left", resp["status"])
	}
}

func TestHandleSetPerms_OwnerCanChange(t *testing.T) {
	svc := newShareTestService("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID: "doc-p", OwnerID: "alice", Badge: ShareBadgeOwned,
		Peers: []SharePeerEntry{{VulaID: "bob", Perm: SharePermEdit}},
	})
	mux := shareMux(svc)
	body := sharePermRequest{TargetVulaID: "bob", Perm: SharePermView}
	rr := doShareRequest(t, mux, http.MethodPut, "/api/peering/collab/doc-p/perms?caller=alice", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	// Verify perm was updated.
	perm, _ := svc.store.PeerPerm("doc-p", "bob")
	if perm != SharePermView {
		t.Errorf("perm = %q, want view", perm)
	}
}

func TestHandleSetPerms_NonOwnerForbidden(t *testing.T) {
	svc := newShareTestService("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID: "doc-p", OwnerID: "alice", Badge: ShareBadgeOwned,
		Peers: []SharePeerEntry{{VulaID: "bob", Perm: SharePermEdit}},
	})
	mux := shareMux(svc)
	body := sharePermRequest{TargetVulaID: "alice", Perm: SharePermView}
	rr := doShareRequest(t, mux, http.MethodPut, "/api/peering/collab/doc-p/perms?caller=bob", body)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestHandleInboundInvite_Accept(t *testing.T) {
	svc := newShareTestService("bob")
	mux := shareMux(svc)

	inv := ShareInvitePayload{
		DocID:   "doc-inv-1",
		DocType: "doc",
		Title:   "Collab Doc",
		PeerID:  "bob",
		Perm:    SharePermEdit,
		FromID:  "alice",
	}
	rr := doShareRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-invite", inv)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "accepted" {
		t.Errorf("status = %v, want accepted", resp["status"])
	}
	if badge, _ := resp["badge"].(string); badge != string(ShareBadgeShared) {
		t.Errorf("badge = %q, want shared", badge)
	}

	// The document should be registered locally with badge=shared.
	entry, err := svc.store.Get("doc-inv-1")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if entry.Badge != ShareBadgeShared {
		t.Errorf("badge = %q, want shared", entry.Badge)
	}
}

func TestHandleInboundInvite_Idempotent(t *testing.T) {
	svc := newShareTestService("bob")
	mux := shareMux(svc)

	inv := ShareInvitePayload{DocID: "doc-idem", Perm: SharePermView, FromID: "alice", PeerID: "bob"}
	// First invite.
	rr1 := doShareRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-invite", inv)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first invite status = %d", rr1.Code)
	}
	// Second invite (re-delivery) should also succeed without creating a duplicate.
	rr2 := doShareRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-invite", inv)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second invite status = %d", rr2.Code)
	}
	if n := len(svc.store.List()); n != 1 {
		t.Errorf("list length = %d after two invites, want 1", n)
	}
}

func TestHandleInboundInvite_MissingDocID(t *testing.T) {
	mux := shareMux(newShareTestService("bob"))
	inv := ShareInvitePayload{Perm: SharePermEdit, FromID: "alice"}
	rr := doShareRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-invite", inv)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleInboundInvite_MissingFromID(t *testing.T) {
	mux := shareMux(newShareTestService("bob"))
	inv := ShareInvitePayload{DocID: "doc-1", Perm: SharePermEdit}
	rr := doShareRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-invite", inv)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// TestHandleInboundUpdate_EditAllowed verifies that a peer with edit
// permission may submit a CRDT update.
func TestHandleInboundUpdate_EditAllowed(t *testing.T) {
	svc := newShareTestService("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID:   "doc-upd",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulaID: "bob", Perm: SharePermEdit}},
	})
	mux := shareMux(svc)
	body := map[string]string{"doc_id": "doc-upd", "sender_id": "bob"}
	rr := doShareRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-update", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleInboundUpdate_ViewOnly_Returns403 is the key view-only AC.
func TestHandleInboundUpdate_ViewOnly_Returns403(t *testing.T) {
	svc := newShareTestService("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID:   "doc-view",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulaID: "carol", Perm: SharePermView}},
	})
	mux := shareMux(svc)
	body := map[string]string{"doc_id": "doc-view", "sender_id": "carol"}
	rr := doShareRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-update", body)
	if rr.Code != http.StatusForbidden {
		t.Errorf("view-only update: status = %d, want 403", rr.Code)
	}
}

// TestHandleInboundUpdate_Revoked_Returns410 verifies AC "revoke stops updates".
func TestHandleInboundUpdate_Revoked_Returns410(t *testing.T) {
	svc := newShareTestService("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID:   "doc-rev",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulaID: "bob", Perm: SharePermEdit}},
	})
	_ = svc.store.Revoke("doc-rev", "alice")
	// Even though Revoke succeeded, Remove is called separately in the HTTP layer.
	// For this test we verify PeerPerm returns "revoked" error which maps to 410.
	mux := shareMux(svc)
	body := map[string]string{"doc_id": "doc-rev", "sender_id": "bob"}
	rr := doShareRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-update", body)
	if rr.Code != http.StatusGone {
		t.Errorf("revoked update: status = %d, want 410", rr.Code)
	}
}

func TestHandleInboundUpdate_UnknownDoc_Returns403(t *testing.T) {
	mux := shareMux(newShareTestService("alice"))
	body := map[string]string{"doc_id": "no-such-doc", "sender_id": "eve"}
	rr := doShareRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-update", body)
	if rr.Code != http.StatusForbidden {
		t.Errorf("unknown doc update: status = %d, want 403", rr.Code)
	}
}

func TestHandleInboundSync_AllowedPeer(t *testing.T) {
	svc := newShareTestService("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID:   "doc-sync",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulaID: "bob", Perm: SharePermView}},
	})
	mux := shareMux(svc)
	rr := doShareRequest(t, mux, http.MethodGet, "/api/peering/inbound/collab-sync?doc_id=doc-sync&sender_id=bob", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleInboundSync_MissingDocID(t *testing.T) {
	mux := shareMux(newShareTestService("alice"))
	rr := doShareRequest(t, mux, http.MethodGet, "/api/peering/inbound/collab-sync?sender_id=bob", nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleInboundSync_UnknownPeer_Returns403(t *testing.T) {
	svc := newShareTestService("alice")
	_ = svc.store.Add(&ShareDocEntry{DocID: "doc-s", OwnerID: "alice", Badge: ShareBadgeOwned})
	mux := shareMux(svc)
	rr := doShareRequest(t, mux, http.MethodGet, "/api/peering/inbound/collab-sync?doc_id=doc-s&sender_id=eve", nil)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// RegisterCollabShareHandlers smoke test
// ---------------------------------------------------------------------------

func TestRegisterCollabShareHandlers_Smoke(t *testing.T) {
	mux := http.NewServeMux()
	// Must not panic.
	RegisterCollabShareHandlers(mux)

	// Routes are verified by checking that the mux does NOT return the
	// standard "404 page not found" response that the Go mux emits when no
	// route matches. Application-level 404s (e.g. "document not found") have
	// a Content-Type of application/json and a different body.
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/peering/collab/share"},
		{http.MethodGet, "/api/peering/collab/documents"},
		{http.MethodGet, "/api/peering/collab/some-doc"},
		{http.MethodDelete, "/api/peering/collab/some-doc"},
		{http.MethodPut, "/api/peering/collab/some-doc/perms"},
		{http.MethodPost, "/api/peering/inbound/collab-invite"},
		{http.MethodPost, "/api/peering/inbound/collab-update"},
		{http.MethodGet, "/api/peering/inbound/collab-sync"},
	}
	for _, rt := range routes {
		req := httptest.NewRequest(rt.method, rt.path, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		// The mux emits "404 page not found\n" (text/plain) when no pattern
		// matches. Our handlers never return that exact string — they return
		// JSON. So a text/plain 404 is a genuine "route not registered" error.
		ct := rr.Result().Header.Get("Content-Type")
		if rr.Code == http.StatusNotFound && ct != "application/json" {
			t.Errorf("route %s %s not registered (mux returned 404 page not found)", rt.method, rt.path)
		}
	}
}
