package peering

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// newTestCollabStore creates a CollabStore backed by a temp directory.
func newTestCollabStore(t *testing.T) *CollabStore {
	t.Helper()
	dir := t.TempDir()
	s, err := NewCollabStore(dir)
	if err != nil {
		t.Fatalf("NewCollabStore: %v", err)
	}
	return s
}

// doCollabReq is a helper that performs an HTTP request against the given handler
// and returns the ResponseRecorder.
func doCollabReq(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
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
	handler.ServeHTTP(rr, req)
	return rr
}

// ── CollabStore unit tests ────────────────────────────────────────────────────

func TestCollabStore_NewCollabStore_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "collab-new")
	if _, err := NewCollabStore(dir); err != nil {
		t.Fatalf("expected no error creating store, got: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("directory not created: %v", err)
	}
}

func TestCollabStore_PersistAndLoadState(t *testing.T) {
	s := newTestCollabStore(t)
	payload := []byte("fake-yjs-binary-blob")

	if err := s.persistUpdate("doc-1", payload); err != nil {
		t.Fatalf("persistUpdate: %v", err)
	}

	got, err := s.loadState("doc-1")
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("loadState = %q, want %q", got, payload)
	}
}

func TestCollabStore_LoadState_MissingDoc(t *testing.T) {
	s := newTestCollabStore(t)
	got, err := s.loadState("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error for missing doc: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil state for missing doc, got %q", got)
	}
}

func TestCollabStore_UpsertAndGetMeta(t *testing.T) {
	s := newTestCollabStore(t)
	m := DocMeta{
		DocID:   "doc-meta-1",
		Title:   "My Doc",
		DocType: "doc",
		Owner:   "alice",
	}
	if err := s.UpsertMeta(m); err != nil {
		t.Fatalf("UpsertMeta: %v", err)
	}

	got, err := s.GetMeta("doc-meta-1")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil meta")
	}
	if got.Title != m.Title {
		t.Errorf("Title = %q, want %q", got.Title, m.Title)
	}
	if got.DocType != m.DocType {
		t.Errorf("DocType = %q, want %q", got.DocType, m.DocType)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set by UpsertMeta")
	}
}

func TestCollabStore_GetMeta_Missing(t *testing.T) {
	s := newTestCollabStore(t)
	got, err := s.GetMeta("no-such-doc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing doc, got %+v", got)
	}
}

func TestCollabStore_ListDocs(t *testing.T) {
	s := newTestCollabStore(t)

	for _, id := range []string{"d1", "d2", "d3"} {
		if err := s.UpsertMeta(DocMeta{DocID: id, Title: "T-" + id, DocType: "doc"}); err != nil {
			t.Fatalf("UpsertMeta %s: %v", id, err)
		}
	}

	docs, err := s.ListDocs()
	if err != nil {
		t.Fatalf("ListDocs: %v", err)
	}
	if len(docs) != 3 {
		t.Errorf("len(ListDocs) = %d, want 3", len(docs))
	}
}

func TestCollabStore_DeleteDoc(t *testing.T) {
	s := newTestCollabStore(t)

	if err := s.UpsertMeta(DocMeta{DocID: "d-del", Title: "Del", DocType: "doc"}); err != nil {
		t.Fatalf("UpsertMeta: %v", err)
	}
	if err := s.persistUpdate("d-del", []byte("state")); err != nil {
		t.Fatalf("persistUpdate: %v", err)
	}

	if err := s.DeleteDoc("d-del"); err != nil {
		t.Fatalf("DeleteDoc: %v", err)
	}

	meta, _ := s.GetMeta("d-del")
	if meta != nil {
		t.Error("expected meta to be gone after DeleteDoc")
	}
	state, _ := s.loadState("d-del")
	if state != nil {
		t.Error("expected state to be gone after DeleteDoc")
	}
}

// ── Room broadcast tests ──────────────────────────────────────────────────────

func TestCollabRoom_BroadcastExcludesOrigin(t *testing.T) {
	room := newCollabRoom()

	c1 := &collabClient{send: make(chan []byte, 8), docID: "doc"}
	c2 := &collabClient{send: make(chan []byte, 8), docID: "doc"}
	room.join(c1)
	room.join(c2)

	msg := []byte(`{"type":"collab:update","doc_id":"doc","payload":""}`)
	room.broadcast(c1, msg)

	// c2 should receive it, c1 should not.
	select {
	case data := <-c2.send:
		if string(data) != string(msg) {
			t.Errorf("c2 received %q, want %q", data, msg)
		}
	default:
		t.Error("c2 did not receive broadcast")
	}

	select {
	case data := <-c1.send:
		t.Errorf("c1 (origin) should not receive its own broadcast, got %q", data)
	default:
		// correct
	}
}

func TestCollabRoom_Count(t *testing.T) {
	room := newCollabRoom()
	if room.count() != 0 {
		t.Errorf("initial count = %d, want 0", room.count())
	}
	c := &collabClient{send: make(chan []byte, 1), docID: "doc"}
	room.join(c)
	if room.count() != 1 {
		t.Errorf("count after join = %d, want 1", room.count())
	}
	room.leave(c)
	if room.count() != 0 {
		t.Errorf("count after leave = %d, want 0", room.count())
	}
}

// ── HTTP handler tests ────────────────────────────────────────────────────────

func collabMux(s *CollabStore) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterCollabHandlers(mux, s)
	return mux
}

func TestHandleListDocs_Empty(t *testing.T) {
	s := newTestCollabStore(t)
	rr := doCollabReq(t, collabMux(s), http.MethodGet, "/api/peering/collab/documents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var docs []*DocMeta
	if err := json.NewDecoder(rr.Body).Decode(&docs); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected 0 docs, got %d", len(docs))
	}
}

func TestCollabHandleGetDoc_NotFound(t *testing.T) {
	s := newTestCollabStore(t)
	rr := doCollabReq(t, collabMux(s), http.MethodGet, "/api/peering/collab/no-doc", nil)
	// GetDoc returns 200 with has_state=false and nil meta for unknown docs.
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	_ = rr
}

func TestHandleDeleteDoc_OK(t *testing.T) {
	s := newTestCollabStore(t)
	_ = s.UpsertMeta(DocMeta{DocID: "del-doc", Title: "X", DocType: "doc"})
	_ = s.persistUpdate("del-doc", []byte("state"))

	rr := doCollabReq(t, collabMux(s), http.MethodDelete, "/api/peering/collab/del-doc", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200", rr.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "deleted" {
		t.Errorf("status = %q, want \"deleted\"", resp["status"])
	}
}

func TestHandleShare_MissingDocID(t *testing.T) {
	s := newTestCollabStore(t)
	rr := doCollabReq(t, collabMux(s), http.MethodPost, "/api/peering/collab/share",
		map[string]string{"title": "My Doc"})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleShare_OK(t *testing.T) {
	s := newTestCollabStore(t)
	body := map[string]any{
		"doc_id":   "shared-doc",
		"title":    "Shared",
		"doc_type": "doc",
		"owner":    "alice",
	}
	rr := doCollabReq(t, collabMux(s), http.MethodPost, "/api/peering/collab/share", body)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// ── S2S inbound handler tests ─────────────────────────────────────────────────

func TestHandleInboundUpdate_OK(t *testing.T) {
	s := newTestCollabStore(t)

	payload := []byte("fake-crdt-update")
	msg := collabMsg{Type: msgTypeUpdate, DocID: "doc-s2s", Payload: payload}
	body, _ := json.Marshal(msg)

	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/collab-update",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	s.handleInboundUpdate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// State must now be persisted.
	state, err := s.loadState("doc-s2s")
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if string(state) != string(payload) {
		t.Errorf("persisted state = %q, want %q", state, payload)
	}
}

func TestHandleInboundUpdate_WrongType(t *testing.T) {
	s := newTestCollabStore(t)
	msg := collabMsg{Type: msgTypeAwareness, DocID: "doc-s2s", Payload: []byte("x")}
	body, _ := json.Marshal(msg)

	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/collab-update",
		bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleInboundUpdate(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleInboundSync_NoDocID(t *testing.T) {
	s := newTestCollabStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/peering/inbound/collab-sync", nil)
	rr := httptest.NewRecorder()
	s.handleInboundSync(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleInboundSync_WithState(t *testing.T) {
	s := newTestCollabStore(t)
	_ = s.persistUpdate("sync-doc", []byte("state-data"))

	req := httptest.NewRequest(http.MethodGet,
		"/api/peering/inbound/collab-sync?doc_id=sync-doc", nil)
	rr := httptest.NewRecorder()
	s.handleInboundSync(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var msg collabMsg
	if err := json.NewDecoder(rr.Body).Decode(&msg); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if msg.Type != msgTypeSync {
		t.Errorf("type = %q, want %q", msg.Type, msgTypeSync)
	}
	if string(msg.Payload) != "state-data" {
		t.Errorf("payload = %q, want \"state-data\"", msg.Payload)
	}
}

// ── HandleInboundCollabUpdate (signed S2S, envelope path) ────────────────────

func TestHandleInboundCollabUpdate_WithEnvelopeContext(t *testing.T) {
	s := newTestCollabStore(t)

	ops := []byte("crdt-update-binary")
	opsB64 := base64.StdEncoding.EncodeToString(ops)

	innerPayload, _ := json.Marshal(map[string]string{
		"doc_id": "env-doc",
		"ops":    opsB64,
	})
	env := &Envelope{
		ID:      "test-id",
		From:    "vula:ed25519:test",
		Type:    TypeCollabUpdate,
		Payload: json.RawMessage(innerPayload),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/collab-update", nil)
	ctx := context.WithValue(req.Context(), EnvelopeKey, env)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	s.HandleInboundCollabUpdate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// State must be persisted from the envelope ops.
	state, err := s.loadState("env-doc")
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if string(state) != string(ops) {
		t.Errorf("persisted state = %q, want %q", state, ops)
	}
}

func TestHandleInboundCollabUpdate_NoEnvelopeFallback(t *testing.T) {
	// When no envelope is in context, HandleInboundCollabUpdate falls back to
	// the plain handleInboundUpdate path.
	s := newTestCollabStore(t)

	payload := []byte("fallback-blob")
	msg := collabMsg{Type: msgTypeUpdate, DocID: "fallback-doc", Payload: payload}
	body, _ := json.Marshal(msg)

	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/collab-update",
		bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.HandleInboundCollabUpdate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	state, _ := s.loadState("fallback-doc")
	if string(state) != string(payload) {
		t.Errorf("persisted state = %q, want %q", state, payload)
	}
}

// ── RegisterCollabHandlers wiring regression ──────────────────────────────────

// TestRegisterCollabHandlers_InboundUsesEnvelopePath is a regression test for
// the CRDT inbound-handler wiring bug: the route POST
// /api/peering/inbound/collab-update MUST be wired to HandleInboundCollabUpdate
// (which reads the pre-verified envelope from context, set by InboundMiddleware)
// NOT to the plain handleInboundUpdate (which reads from the request body —
// a body that InboundMiddleware has already consumed).
//
// The test simulates what happens after InboundMiddleware runs:
//   - request body is empty (already consumed)
//   - the decoded Envelope is in the request context
//
// If the route were wired to handleInboundUpdate it would read an empty body,
// fail JSON decode, and return 400. The correct wiring (HandleInboundCollabUpdate)
// reads from context and returns 200.
func TestRegisterCollabHandlers_InboundUsesEnvelopePath(t *testing.T) {
	s := newTestCollabStore(t)

	mux := http.NewServeMux()
	RegisterCollabHandlers(mux, s)

	ops := []byte("crdt-ops-binary")
	opsB64 := base64.StdEncoding.EncodeToString(ops)
	innerPayload, _ := json.Marshal(map[string]string{
		"doc_id": "wiring-doc",
		"ops":    opsB64,
	})
	env := &Envelope{
		ID:      "reg-test-id",
		From:    "vula:ed25519:regtest",
		Type:    TypeCollabUpdate,
		Payload: json.RawMessage(innerPayload),
	}

	// Body is nil: simulating InboundMiddleware having already consumed it.
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/collab-update", nil)
	ctx := context.WithValue(req.Context(), EnvelopeKey, env)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("REGRESSION: RegisterCollabHandlers wired the wrong handler for "+
			"POST /api/peering/inbound/collab-update — got status %d (want 200). "+
			"The route must use HandleInboundCollabUpdate (reads envelope from "+
			"context), not handleInboundUpdate (reads from body, which InboundMiddleware "+
			"has already consumed). Body: %s", rr.Code, rr.Body.String())
	}

	// Confirm state was persisted from the envelope ops (not from body).
	state, err := s.loadState("wiring-doc")
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if string(state) != string(ops) {
		t.Errorf("persisted state = %q, want %q (envelope ops)", state, ops)
	}
}
