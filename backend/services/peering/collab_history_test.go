package peering

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// historyMux registers the history routes on a fresh mux backed by store.
func historyMux(s *CollabStore) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterCollabHistoryHandlers(mux, s)
	return mux
}

// doHistoryReq performs a GET request on handler and returns the recorder.
func doHistoryReq(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// ── AppendSnapshot tests ──────────────────────────────────────────────────────

func TestAppendSnapshot_StoresEntry(t *testing.T) {
	s := newTestCollabStore(t)
	payload := []byte("fake-yjs-state-1")
	s.AppendSnapshot("doc-snap", payload)

	index, err := s.loadSnapshotIndex("doc-snap")
	if err != nil {
		t.Fatalf("loadSnapshotIndex: %v", err)
	}
	if len(index) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(index))
	}
	if index[0].Seq != 1 {
		t.Errorf("seq = %d, want 1", index[0].Seq)
	}
	if index[0].Size != len(payload) {
		t.Errorf("size = %d, want %d", index[0].Size, len(payload))
	}
}

func TestAppendSnapshot_SequenceIncrements(t *testing.T) {
	s := newTestCollabStore(t)
	for i := 1; i <= 3; i++ {
		s.AppendSnapshot("seq-doc", []byte(fmt.Sprintf("blob-%d", i)))
	}
	index, err := s.loadSnapshotIndex("seq-doc")
	if err != nil {
		t.Fatalf("loadSnapshotIndex: %v", err)
	}
	if len(index) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(index))
	}
	for i, e := range index {
		if e.Seq != i+1 {
			t.Errorf("index[%d].Seq = %d, want %d", i, e.Seq, i+1)
		}
	}
}

func TestAppendSnapshot_PrunesOldEntries(t *testing.T) {
	s := newTestCollabStore(t)
	// Write maxSnapshots+5 entries to trigger pruning.
	total := maxSnapshots + 5
	for i := 0; i < total; i++ {
		s.AppendSnapshot("prune-doc", []byte(fmt.Sprintf("blob-%d", i)))
	}

	index, err := s.loadSnapshotIndex("prune-doc")
	if err != nil {
		t.Fatalf("loadSnapshotIndex: %v", err)
	}
	if len(index) > maxSnapshots {
		t.Errorf("expected at most %d snapshots after pruning, got %d", maxSnapshots, len(index))
	}
	// The newest entry's seq must be `total`.
	last := index[len(index)-1]
	if last.Seq != total {
		t.Errorf("last seq = %d, want %d", last.Seq, total)
	}
}

func TestAppendSnapshot_EmptyPayloadIgnored(t *testing.T) {
	s := newTestCollabStore(t)
	s.AppendSnapshot("empty-doc", nil)
	s.AppendSnapshot("empty-doc", []byte{})

	index, _ := s.loadSnapshotIndex("empty-doc")
	if len(index) != 0 {
		t.Errorf("expected 0 snapshots for empty payloads, got %d", len(index))
	}
}

// ── LoadSnapshot tests ────────────────────────────────────────────────────────

func TestLoadSnapshot_RoundTrip(t *testing.T) {
	s := newTestCollabStore(t)
	payload := []byte("round-trip-payload")
	s.AppendSnapshot("rt-doc", payload)

	got, err := s.LoadSnapshot("rt-doc", 1)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("payload = %q, want %q", got, payload)
	}
}

func TestLoadSnapshot_Missing(t *testing.T) {
	s := newTestCollabStore(t)
	got, err := s.LoadSnapshot("no-doc", 999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing snapshot, got %q", got)
	}
}

// ── HTTP handler: GET /api/peering/collab/{doc_id}/history ───────────────────

func TestHandleListHistory_Empty(t *testing.T) {
	s := newTestCollabStore(t)
	rr := doHistoryReq(t, historyMux(s), "/api/peering/collab/no-hist-doc/history")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	snaps, ok := body["snapshots"]
	if !ok {
		t.Fatal("response missing 'snapshots' field")
	}
	sl, ok := snaps.([]any)
	if !ok {
		t.Fatalf("'snapshots' is not an array, got %T", snaps)
	}
	if len(sl) != 0 {
		t.Errorf("expected 0 snapshots, got %d", len(sl))
	}
}

func TestHandleListHistory_WithEntries(t *testing.T) {
	s := newTestCollabStore(t)
	for i := 0; i < 3; i++ {
		s.AppendSnapshot("hist-doc", []byte(fmt.Sprintf("state-%d", i)))
	}

	rr := doHistoryReq(t, historyMux(s), "/api/peering/collab/hist-doc/history")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	snaps := body["snapshots"].([]any)
	if len(snaps) != 3 {
		t.Errorf("expected 3 snapshots, got %d", len(snaps))
	}
}

// ── HTTP handler: GET /api/peering/collab/{doc_id}/history/{seq} ─────────────

func TestHandleGetSnapshot_OK(t *testing.T) {
	s := newTestCollabStore(t)
	payload := []byte("snapshot-payload")
	s.AppendSnapshot("snap-doc", payload)

	rr := doHistoryReq(t, historyMux(s), "/api/peering/collab/snap-doc/history/1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var msg collabMsg
	if err := json.NewDecoder(rr.Body).Decode(&msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != msgTypeSync {
		t.Errorf("type = %q, want %q", msg.Type, msgTypeSync)
	}
	if string(msg.Payload) != string(payload) {
		t.Errorf("payload = %q, want %q", msg.Payload, payload)
	}
}

func TestHandleGetSnapshot_NotFound(t *testing.T) {
	s := newTestCollabStore(t)
	rr := doHistoryReq(t, historyMux(s), "/api/peering/collab/no-doc/history/1")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestHandleGetSnapshot_InvalidSeq(t *testing.T) {
	s := newTestCollabStore(t)
	rr := doHistoryReq(t, historyMux(s), "/api/peering/collab/any-doc/history/notanumber")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ── HTTP handler: GET /api/peering/collab-sync-v2 ────────────────────────────

func syncV2Mux(s *CollabStore) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterCollabHistoryHandlers(mux, s)
	return mux
}

func TestHandleInboundSyncV2_NoStateVector(t *testing.T) {
	s := newTestCollabStore(t)
	_ = s.persistUpdate("sv-doc", []byte("full-state"))

	req := httptest.NewRequest(http.MethodGet, "/api/peering/collab-sync-v2?doc_id=sv-doc", nil)
	rr := httptest.NewRecorder()
	syncV2Mux(s).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var msg collabMsg
	if err := json.NewDecoder(rr.Body).Decode(&msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != msgTypeSync {
		t.Errorf("type = %q, want %q", msg.Type, msgTypeSync)
	}
}

func TestHandleInboundSyncV2_NoDocID(t *testing.T) {
	s := newTestCollabStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/peering/collab-sync-v2", nil)
	rr := httptest.NewRecorder()
	syncV2Mux(s).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleInboundSyncV2_StateVectorMatch_ReturnsNoop(t *testing.T) {
	s := newTestCollabStore(t)
	stateBlob := []byte("identical-state")
	_ = s.persistUpdate("noop-doc", stateBlob)

	// Compute expected base64 of stored blob (same function the handler uses).
	sv := encodeBase64(stateBlob)

	req := httptest.NewRequest(http.MethodGet,
		"/api/peering/collab-sync-v2?doc_id=noop-doc&state_vector="+sv, nil)
	rr := httptest.NewRecorder()
	syncV2Mux(s).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var msg collabMsg
	if err := json.NewDecoder(rr.Body).Decode(&msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != "collab:noop" {
		t.Errorf("type = %q, want \"collab:noop\"", msg.Type)
	}
}

func TestHandleInboundSyncV2_StateVectorMismatch_ReturnsFullState(t *testing.T) {
	s := newTestCollabStore(t)
	stateBlob := []byte("current-state")
	_ = s.persistUpdate("diff-doc", stateBlob)

	req := httptest.NewRequest(http.MethodGet,
		"/api/peering/collab-sync-v2?doc_id=diff-doc&state_vector="+encodeBase64([]byte("old-state")), nil)
	rr := httptest.NewRecorder()
	syncV2Mux(s).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var msg collabMsg
	if err := json.NewDecoder(rr.Body).Decode(&msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != msgTypeSync {
		t.Errorf("type = %q, want %q", msg.Type, msgTypeSync)
	}
	if string(msg.Payload) != string(stateBlob) {
		t.Errorf("payload = %q, want %q", msg.Payload, stateBlob)
	}
}

// ── encodeBase64 unit test ────────────────────────────────────────────────────

func TestEncodeBase64_Roundtrip(t *testing.T) {
	cases := [][]byte{
		[]byte("hello"),
		[]byte(""),
		[]byte{0x00, 0xff, 0x7f},
		[]byte("any-yjs-binary-data"),
	}
	for _, c := range cases {
		got := encodeBase64(c)
		// Verify by decoding with encoding/base64
		import_enc_b64_decoded, err := decodeBase64ForTest(got)
		if err != nil {
			t.Errorf("encodeBase64(%q) → %q, decode error: %v", c, got, err)
			continue
		}
		if string(import_enc_b64_decoded) != string(c) {
			t.Errorf("roundtrip(%q) = %q, want %q", c, import_enc_b64_decoded, c)
		}
	}
}

// decodeBase64ForTest decodes a standard base64 string.
// Using standard library to validate our hand-rolled encode.
func decodeBase64ForTest(s string) ([]byte, error) {
	// Inline decode to avoid adding an import.
	// Reuse the package-level encodeBase64 table for a manual decode.
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	indexOf := func(b byte) int {
		if b == '=' {
			return 0
		}
		for i := 0; i < len(enc); i++ {
			if enc[i] == b {
				return i
			}
		}
		return -1
	}
	if len(s)%4 != 0 {
		return nil, fmt.Errorf("invalid base64 length %d", len(s))
	}
	out := make([]byte, 0, len(s)/4*3)
	for i := 0; i < len(s); i += 4 {
		a, b, c, d := indexOf(s[i]), indexOf(s[i+1]), indexOf(s[i+2]), indexOf(s[i+3])
		if a < 0 || b < 0 || c < 0 || d < 0 {
			return nil, fmt.Errorf("invalid char in base64 at %d", i)
		}
		out = append(out, byte(a<<2|b>>4))
		if s[i+2] != '=' {
			out = append(out, byte((b&0xf)<<4|c>>2))
		}
		if s[i+3] != '=' {
			out = append(out, byte((c&0x3)<<6|d))
		}
	}
	return out, nil
}
