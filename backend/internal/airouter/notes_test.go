package airouter

// AIROT-04 tests: note_embeddings store + HTTP handlers.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Store: note_embeddings round-trip
// ---------------------------------------------------------------------------

func TestUpsertListNoteEmbeddings(t *testing.T) {
	s := newTestStore(t)

	vec1 := []float32{0.1, 0.2, 0.3, 0.4}
	ne1 := NoteEmbedding{NoteID: "note-1", ModelSlug: "test-model", Vector: vec1}
	if err := s.UpsertNoteEmbedding(ne1); err != nil {
		t.Fatalf("UpsertNoteEmbedding: %v", err)
	}

	list, err := s.ListNoteEmbeddings()
	if err != nil {
		t.Fatalf("ListNoteEmbeddings: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(list))
	}
	if list[0].NoteID != "note-1" {
		t.Errorf("NoteID: got %q, want %q", list[0].NoteID, "note-1")
	}
	if len(list[0].Vector) != 4 {
		t.Errorf("vector length: got %d, want 4", len(list[0].Vector))
	}
	for i, v := range vec1 {
		if list[0].Vector[i] != v {
			t.Errorf("vector[%d]: got %v, want %v", i, list[0].Vector[i], v)
		}
	}
}

func TestUpsertNoteEmbedding_Upsert(t *testing.T) {
	s := newTestStore(t)

	ne := NoteEmbedding{NoteID: "note-X", ModelSlug: "m1", Vector: []float32{1, 0}}
	if err := s.UpsertNoteEmbedding(ne); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Upsert again with different vector.
	ne2 := NoteEmbedding{NoteID: "note-X", ModelSlug: "m1", Vector: []float32{0, 1}}
	if err := s.UpsertNoteEmbedding(ne2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	list, _ := s.ListNoteEmbeddings()
	if len(list) != 1 {
		t.Fatalf("expected 1 row after upsert, got %d", len(list))
	}
	if list[0].Vector[0] != 0 || list[0].Vector[1] != 1 {
		t.Errorf("vector not updated: %v", list[0].Vector)
	}
}

func TestDeleteNoteEmbedding(t *testing.T) {
	s := newTestStore(t)

	ne := NoteEmbedding{NoteID: "del-me", ModelSlug: "m1", Vector: []float32{1, 0}}
	if err := s.UpsertNoteEmbedding(ne); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.DeleteNoteEmbedding("del-me"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	list, _ := s.ListNoteEmbeddings()
	if len(list) != 0 {
		t.Errorf("expected empty list after delete, got %d", len(list))
	}
}

func TestSemanticSearchNotes(t *testing.T) {
	s := newTestStore(t)

	// Insert three notes with known vectors.
	notes := []NoteEmbedding{
		{NoteID: "n1", ModelSlug: "m", Vector: []float32{1, 0, 0}},
		{NoteID: "n2", ModelSlug: "m", Vector: []float32{0, 1, 0}},
		{NoteID: "n3", ModelSlug: "m", Vector: []float32{0.9, 0.1, 0}}, // close to n1
	}
	for _, ne := range notes {
		if err := s.UpsertNoteEmbedding(ne); err != nil {
			t.Fatalf("upsert %s: %v", ne.NoteID, err)
		}
	}

	// Search with a query vector very close to n1 and n3.
	queryVec := []float32{1, 0, 0}
	noteIDs, scores, err := s.SemanticSearchNotes(queryVec, 5, 0.5)
	if err != nil {
		t.Fatalf("SemanticSearchNotes: %v", err)
	}
	// n1 (exact match) and n3 (close) should be returned; n2 should not.
	found := map[string]bool{}
	for _, id := range noteIDs {
		found[id] = true
	}
	if !found["n1"] {
		t.Errorf("n1 (exact match) missing from results: %v", noteIDs)
	}
	if !found["n3"] {
		t.Errorf("n3 (close match) missing from results: %v", noteIDs)
	}
	if found["n2"] {
		t.Errorf("n2 (orthogonal) should not appear in results: %v", noteIDs)
	}
	// Scores should be in descending order.
	for i := 1; i < len(scores); i++ {
		if scores[i] > scores[i-1] {
			t.Errorf("scores not sorted descending: %v", scores)
		}
	}
}

func TestSemanticSearchNotes_TopK(t *testing.T) {
	s := newTestStore(t)

	for i := 0; i < 10; i++ {
		ne := NoteEmbedding{
			NoteID:    fmt.Sprintf("note-%d", i),
			ModelSlug: "m",
			Vector:    []float32{float32(i) / 10, 1 - float32(i)/10},
		}
		_ = s.UpsertNoteEmbedding(ne)
	}

	noteIDs, _, err := s.SemanticSearchNotes([]float32{1, 0}, 3, 0.0)
	if err != nil {
		t.Fatalf("SemanticSearchNotes: %v", err)
	}
	if len(noteIDs) > 3 {
		t.Errorf("expected at most 3 results, got %d", len(noteIDs))
	}
}

// ---------------------------------------------------------------------------
// HTTP: POST /api/ai/notes/index (with stub embedder)
// ---------------------------------------------------------------------------

func TestHandleNotesIndex_Success(t *testing.T) {
	// Stub embedding provider.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/embeddings" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"embedding":[0.1,0.2,0.3]}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer stub.Close()

	h, s := newTestHandler(t, stub.URL)
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	body := `{"note_id":"test-note","content":"Hello world"}`
	req := httptest.NewRequest("POST", "/api/ai/notes/index", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["note_id"] != "test-note" {
		t.Errorf("note_id: got %v", resp["note_id"])
	}

	// Verify it was stored.
	list, err := s.ListNoteEmbeddings()
	if err != nil || len(list) == 0 {
		t.Errorf("embedding not stored: %v, len=%d", err, len(list))
	}
}

func TestHandleNotesIndex_NoProviders_503(t *testing.T) {
	// No stub URL → no providers configured.
	h, _ := newTestHandler(t, "")
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	body := `{"note_id":"x","content":"some content"}`
	req := httptest.NewRequest("POST", "/api/ai/notes/index", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// Should be 503 (unconfigured) so the caller can degrade gracefully.
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503; body: %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// HTTP: POST /api/ai/notes/search (graceful degrade when unconfigured)
// ---------------------------------------------------------------------------

func TestHandleNotesSearch_GracefulDegrade(t *testing.T) {
	h, _ := newTestHandler(t, "")
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	body := `{"query":"what is the meaning of life"}`
	req := httptest.NewRequest("POST", "/api/ai/notes/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (graceful degrade); body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp["degraded"].(bool) {
		t.Errorf("expected degraded=true when no providers configured")
	}
	hits, ok := resp["hits"].([]any)
	if !ok || len(hits) != 0 {
		t.Errorf("expected empty hits array, got %v", resp["hits"])
	}
}

func TestHandleNotesSearch_EmptyQuery(t *testing.T) {
	h, _ := newTestHandler(t, "")
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	body := `{"query":""}`
	req := httptest.NewRequest("POST", "/api/ai/notes/search", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["degraded"].(bool) {
		t.Errorf("expected degraded=false for empty query")
	}
}

// ---------------------------------------------------------------------------
// HTTP: DELETE /api/ai/notes/{note_id}/embed
// ---------------------------------------------------------------------------

func TestHandleNotesDeleteEmbed(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/embeddings" {
			fmt.Fprint(w, `{"data":[{"embedding":[0.5,0.5]}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer stub.Close()

	h, s := newTestHandler(t, stub.URL)
	// Pre-insert an embedding directly.
	ne := NoteEmbedding{NoteID: "to-delete", ModelSlug: "m", Vector: []float32{1, 0}}
	if err := s.UpsertNoteEmbedding(ne); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	req := httptest.NewRequest("DELETE", "/api/ai/notes/to-delete/embed", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	list, _ := s.ListNoteEmbeddings()
	if len(list) != 0 {
		t.Errorf("embedding not deleted; list len=%d", len(list))
	}
}
