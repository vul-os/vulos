package peering

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// newSharesSvc creates a SharesService with an in-memory store and no
// signing key (priv=nil) for unit tests that do not exercise envelope signing.
func newSharesSvc(localVulosID string) *SharesService {
	return NewSharesService(nil, NewShareStore(), nil, nil, localVulosID)
}

// sharesMux registers handlers on a fresh mux and returns it.
func sharesMux(svc *SharesService) *http.ServeMux {
	mux := http.NewServeMux()
	svc.RegisterSharesHandlers(mux)
	return mux
}

// doSharesRequest performs an HTTP request against mux.
func doSharesRequest(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
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

// doSharesRequestWithEnv performs a request with a pre-verified envelope stored
// in the request context, simulating what InboundMiddleware would set.
func doSharesRequestWithEnv(t *testing.T, mux *http.ServeMux, method, path string, env *Envelope) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	ctx := context.WithValue(req.Context(), EnvelopeKey, env)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// ─── Envelope type constants ──────────────────────────────────────────────────

func TestTypeCollabInviteConstant(t *testing.T) {
	if TypeCollabInvite != "collab-invite" {
		t.Errorf("TypeCollabInvite = %q, want %q", TypeCollabInvite, "collab-invite")
	}
}

func TestTypeCollabUpdateConstant(t *testing.T) {
	if TypeCollabUpdate != "collab-update" {
		t.Errorf("TypeCollabUpdate = %q, want %q", TypeCollabUpdate, "collab-update")
	}
}

// ─── Outbound share ───────────────────────────────────────────────────────────

// TestSharesHandleShareDoc_LocalOnly verifies that sharing without a peer
// server address still registers the document locally.
func TestSharesHandleShareDoc_LocalOnly(t *testing.T) {
	svc := newSharesSvc("alice")
	mux := sharesMux(svc)

	req := shareInviteRequest{
		ShareInvitePayload: ShareInvitePayload{
			DocID:   "doc-local",
			DocType: "doc",
			Title:   "Local Share",
			PeerID:  "bob",
			Perm:    SharePermEdit,
			FromID:  "alice",
		},
	}
	rr := doSharesRequest(t, mux, http.MethodPost, "/api/peering/collab/share", req)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /collab/share status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Document should be in the store with badge=owned.
	entry, err := svc.store.Get("doc-local")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if entry.Badge != ShareBadgeOwned {
		t.Errorf("badge = %q, want owned", entry.Badge)
	}
	if entry.OwnerID != "alice" {
		t.Errorf("owner = %q, want alice", entry.OwnerID)
	}
}

func TestSharesHandleShareDoc_MissingDocID(t *testing.T) {
	mux := sharesMux(newSharesSvc("alice"))
	req := shareInviteRequest{ShareInvitePayload: ShareInvitePayload{PeerID: "bob", Perm: SharePermEdit}}
	rr := doSharesRequest(t, mux, http.MethodPost, "/api/peering/collab/share", req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestSharesHandleShareDoc_MissingPeerID(t *testing.T) {
	mux := sharesMux(newSharesSvc("alice"))
	req := shareInviteRequest{ShareInvitePayload: ShareInvitePayload{DocID: "doc-1", Perm: SharePermEdit}}
	rr := doSharesRequest(t, mux, http.MethodPost, "/api/peering/collab/share", req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestSharesHandleShareDoc_InvalidPerm(t *testing.T) {
	mux := sharesMux(newSharesSvc("alice"))
	req := shareInviteRequest{ShareInvitePayload: ShareInvitePayload{DocID: "doc-1", PeerID: "bob", Perm: "admin"}}
	rr := doSharesRequest(t, mux, http.MethodPost, "/api/peering/collab/share", req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ─── HandleInboundShare — envelope path ───────────────────────────────────────

// TestHandleInboundShare_WithEnvelope simulates InboundMiddleware: the envelope
// is stored in the request context and HandleInboundShare reads it.
func TestHandleInboundShare_WithEnvelope(t *testing.T) {
	svc := newSharesSvc("bob")
	mux := sharesMux(svc)

	inv := ShareInvitePayload{
		DocID:   "doc-env-1",
		DocType: "doc",
		Title:   "Env Doc",
		PeerID:  "bob",
		Perm:    SharePermEdit,
		FromID:  "alice",
	}
	payload, _ := json.Marshal(inv)
	env := &Envelope{
		ID:        "env-id-1",
		From:      "alice",
		To:        "bob",
		Type:      TypeCollabInvite,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   json.RawMessage(payload),
	}

	rr := doSharesRequestWithEnv(t, mux, http.MethodPost, "/api/peering/inbound/collab-invite", env)
	if rr.Code != http.StatusOK {
		t.Fatalf("inbound invite (env) status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "accepted" {
		t.Errorf("status = %v, want accepted", resp["status"])
	}
	if badge, _ := resp["badge"].(string); badge != string(ShareBadgeShared) {
		t.Errorf("badge = %q, want shared", badge)
	}

	// Document should be registered with badge=shared; owner is the envelope sender.
	entry, err := svc.store.Get("doc-env-1")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if entry.Badge != ShareBadgeShared {
		t.Errorf("badge = %q, want shared", entry.Badge)
	}
	if entry.OwnerID != "alice" {
		t.Errorf("owner = %q, want alice (from envelope.From)", entry.OwnerID)
	}
}

// TestHandleInboundShare_EnvelopeTrustsSender verifies that the envelope's
// From field is used as the owner, not the payload's from_id.
func TestHandleInboundShare_EnvelopeTrustsSender(t *testing.T) {
	svc := newSharesSvc("bob")
	mux := sharesMux(svc)

	// Payload claims from_id=mallory but the envelope says From=alice.
	inv := ShareInvitePayload{
		DocID:  "doc-trust",
		Perm:   SharePermView,
		FromID: "mallory", // should be overridden by envelope.From
	}
	payload, _ := json.Marshal(inv)
	env := &Envelope{
		ID:        "env-trust-1",
		From:      "alice", // authoritative sender
		To:        "bob",
		Type:      TypeCollabInvite,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   json.RawMessage(payload),
	}

	rr := doSharesRequestWithEnv(t, mux, http.MethodPost, "/api/peering/inbound/collab-invite", env)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}

	entry, _ := svc.store.Get("doc-trust")
	if entry.OwnerID != "alice" {
		t.Errorf("owner = %q, want alice (envelope.From should override payload.from_id)", entry.OwnerID)
	}
}

// ─── HandleInboundShare — direct/fallback path (no envelope in ctx) ───────────

func TestHandleInboundShare_Direct_Accept(t *testing.T) {
	svc := newSharesSvc("bob")
	mux := sharesMux(svc)

	inv := ShareInvitePayload{
		DocID:   "doc-direct-1",
		DocType: "doc",
		Title:   "Direct Doc",
		PeerID:  "bob",
		Perm:    SharePermView,
		FromID:  "alice",
	}
	rr := doSharesRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-invite", inv)
	if rr.Code != http.StatusOK {
		t.Fatalf("direct invite status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	entry, err := svc.store.Get("doc-direct-1")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if entry.Badge != ShareBadgeShared {
		t.Errorf("badge = %q, want shared", entry.Badge)
	}
}

func TestHandleInboundShare_Idempotent(t *testing.T) {
	svc := newSharesSvc("bob")
	mux := sharesMux(svc)

	inv := ShareInvitePayload{DocID: "doc-idem2", Perm: SharePermView, FromID: "alice", PeerID: "bob"}
	for i := 0; i < 2; i++ {
		rr := doSharesRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-invite", inv)
		if rr.Code != http.StatusOK {
			t.Fatalf("invite %d status = %d", i+1, rr.Code)
		}
	}
	if n := len(svc.store.List()); n != 1 {
		t.Errorf("list length = %d, want 1 after two invites", n)
	}
}

func TestHandleInboundShare_MissingDocID(t *testing.T) {
	mux := sharesMux(newSharesSvc("bob"))
	inv := ShareInvitePayload{Perm: SharePermEdit, FromID: "alice"}
	rr := doSharesRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-invite", inv)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleInboundShare_MissingFromID(t *testing.T) {
	mux := sharesMux(newSharesSvc("bob"))
	inv := ShareInvitePayload{DocID: "doc-1", Perm: SharePermEdit}
	rr := doSharesRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-invite", inv)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ─── Inbound update — view-only AC ───────────────────────────────────────────

func TestSharesInboundUpdate_EditAllowed(t *testing.T) {
	svc := newSharesSvc("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID:   "doc-upd2",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulosID: "bob", Perm: SharePermEdit}},
	})
	mux := sharesMux(svc)
	body := map[string]string{"doc_id": "doc-upd2", "sender_id": "bob"}
	rr := doSharesRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-update", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("edit update status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestSharesInboundUpdate_ViewOnly_Returns403 is the key view-only AC.
func TestSharesInboundUpdate_ViewOnly_Returns403(t *testing.T) {
	svc := newSharesSvc("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID:   "doc-view2",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulosID: "carol", Perm: SharePermView}},
	})
	mux := sharesMux(svc)
	body := map[string]string{"doc_id": "doc-view2", "sender_id": "carol"}
	rr := doSharesRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-update", body)
	if rr.Code != http.StatusForbidden {
		t.Errorf("view-only update: status = %d, want 403", rr.Code)
	}
}

// TestSharesInboundUpdate_Revoked_Returns410 is the "revoke stops updates" AC.
func TestSharesInboundUpdate_Revoked_Returns410(t *testing.T) {
	svc := newSharesSvc("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID:   "doc-rev2",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulosID: "bob", Perm: SharePermEdit}},
	})
	_ = svc.store.Revoke("doc-rev2", "alice")
	mux := sharesMux(svc)
	body := map[string]string{"doc_id": "doc-rev2", "sender_id": "bob"}
	rr := doSharesRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-update", body)
	if rr.Code != http.StatusGone {
		t.Errorf("revoked update: status = %d, want 410", rr.Code)
	}
}

func TestSharesInboundUpdate_UnknownDoc(t *testing.T) {
	mux := sharesMux(newSharesSvc("alice"))
	body := map[string]string{"doc_id": "no-such", "sender_id": "eve"}
	rr := doSharesRequest(t, mux, http.MethodPost, "/api/peering/inbound/collab-update", body)
	if rr.Code != http.StatusForbidden {
		t.Errorf("unknown doc: status = %d, want 403", rr.Code)
	}
}

// TestSharesInboundUpdate_WithEnvelope verifies the envelope-path extraction.
func TestSharesInboundUpdate_WithEnvelope(t *testing.T) {
	svc := newSharesSvc("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID:   "doc-env-upd",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulosID: "bob", Perm: SharePermEdit}},
	})
	mux := sharesMux(svc)

	updateBody := sharesUpdateBody{DocID: "doc-env-upd"}
	payload, _ := json.Marshal(updateBody)
	env := &Envelope{
		ID:        "env-upd-1",
		From:      "bob",
		To:        "alice",
		Type:      TypeCollabUpdate,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   json.RawMessage(payload),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/collab-update", nil)
	ctx := context.WithValue(req.Context(), EnvelopeKey, env)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("envelope update status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// ─── collab-sync ──────────────────────────────────────────────────────────────

func TestSharesInboundSync_Allowed(t *testing.T) {
	svc := newSharesSvc("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID:   "doc-sync2",
		OwnerID: "alice",
		Badge:   ShareBadgeOwned,
		Peers:   []SharePeerEntry{{VulosID: "bob", Perm: SharePermView}},
	})
	mux := sharesMux(svc)
	rr := doSharesRequest(t, mux, http.MethodGet, "/api/peering/inbound/collab-sync?doc_id=doc-sync2&sender_id=bob", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("sync status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestSharesInboundSync_MissingDocID(t *testing.T) {
	mux := sharesMux(newSharesSvc("alice"))
	rr := doSharesRequest(t, mux, http.MethodGet, "/api/peering/inbound/collab-sync?sender_id=bob", nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestSharesInboundSync_UnknownPeer(t *testing.T) {
	svc := newSharesSvc("alice")
	_ = svc.store.Add(&ShareDocEntry{DocID: "doc-s2", OwnerID: "alice", Badge: ShareBadgeOwned})
	mux := sharesMux(svc)
	rr := doSharesRequest(t, mux, http.MethodGet, "/api/peering/inbound/collab-sync?doc_id=doc-s2&sender_id=eve", nil)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

// ─── List / Get / Leave / Revoke / SetPerms ───────────────────────────────────

func TestSharesListDocs(t *testing.T) {
	svc := newSharesSvc("alice")
	_ = svc.store.Add(&ShareDocEntry{DocID: "d1", OwnerID: "alice", Badge: ShareBadgeOwned, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	_ = svc.store.Add(&ShareDocEntry{DocID: "d2", OwnerID: "bob", Badge: ShareBadgeShared, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	mux := sharesMux(svc)
	rr := doSharesRequest(t, mux, http.MethodGet, "/api/peering/collab/documents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var docs []ShareDocEntry
	json.NewDecoder(rr.Body).Decode(&docs)
	if len(docs) != 2 {
		t.Errorf("len = %d, want 2", len(docs))
	}
}

func TestSharesGetDoc_Found(t *testing.T) {
	svc := newSharesSvc("alice")
	_ = svc.store.Add(&ShareDocEntry{DocID: "doc-g", OwnerID: "alice", Badge: ShareBadgeOwned, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	mux := sharesMux(svc)
	rr := doSharesRequest(t, mux, http.MethodGet, "/api/peering/collab/doc-g", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
}

func TestSharesGetDoc_NotFound(t *testing.T) {
	mux := sharesMux(newSharesSvc("alice"))
	rr := doSharesRequest(t, mux, http.MethodGet, "/api/peering/collab/nope", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestSharesLeaveOrRevoke_Owner(t *testing.T) {
	svc := newSharesSvc("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID: "doc-rv", OwnerID: "alice", Badge: ShareBadgeOwned,
		Peers: []SharePeerEntry{{VulosID: "bob", Perm: SharePermEdit}},
	})
	mux := sharesMux(svc)
	rr := doSharesRequest(t, mux, http.MethodDelete, "/api/peering/collab/doc-rv?caller=alice", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "revoked" {
		t.Errorf("status = %q, want revoked", resp["status"])
	}
}

func TestSharesLeaveOrRevoke_Peer(t *testing.T) {
	svc := newSharesSvc("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID: "doc-lv", OwnerID: "alice", Badge: ShareBadgeOwned,
		Peers: []SharePeerEntry{{VulosID: "bob", Perm: SharePermEdit}},
	})
	mux := sharesMux(svc)
	rr := doSharesRequest(t, mux, http.MethodDelete, "/api/peering/collab/doc-lv?caller=bob", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "left" {
		t.Errorf("status = %q, want left", resp["status"])
	}
}

func TestSharesSetPerms_OwnerCanChange(t *testing.T) {
	svc := newSharesSvc("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID: "doc-pp", OwnerID: "alice", Badge: ShareBadgeOwned,
		Peers: []SharePeerEntry{{VulosID: "bob", Perm: SharePermEdit}},
	})
	mux := sharesMux(svc)
	body := sharesPermRequest{TargetVulosID: "bob", Perm: SharePermView}
	rr := doSharesRequest(t, mux, http.MethodPut, "/api/peering/collab/doc-pp/perms?caller=alice", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
	perm, _ := svc.store.PeerPerm("doc-pp", "bob")
	if perm != SharePermView {
		t.Errorf("perm = %q, want view", perm)
	}
}

func TestSharesSetPerms_NonOwnerForbidden(t *testing.T) {
	svc := newSharesSvc("alice")
	_ = svc.store.Add(&ShareDocEntry{
		DocID: "doc-pp2", OwnerID: "alice", Badge: ShareBadgeOwned,
		Peers: []SharePeerEntry{{VulosID: "bob", Perm: SharePermEdit}},
	})
	mux := sharesMux(svc)
	body := sharesPermRequest{TargetVulosID: "alice", Perm: SharePermView}
	rr := doSharesRequest(t, mux, http.MethodPut, "/api/peering/collab/doc-pp2/perms?caller=bob", body)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

// ─── Registration smoke test ──────────────────────────────────────────────────

func TestSharesRegistration_Smoke(t *testing.T) {
	svc := newSharesSvc("alice")
	mux := http.NewServeMux()
	svc.RegisterSharesHandlers(mux)

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
		ct := rr.Result().Header.Get("Content-Type")
		if rr.Code == http.StatusNotFound && ct != "application/json" {
			t.Errorf("route %s %s not registered (mux returned 404 page not found)", rt.method, rt.path)
		}
	}
}
