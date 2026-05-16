package peering

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---- store tests ----------------------------------------------------------

func TestCallHistStoreRecordAndList(t *testing.T) {
	dir := t.TempDir()
	store := callhistNewStore(dir)

	now := time.Now().UTC().Truncate(time.Second)
	entry := &CallHistEntry{
		ID:          "test-001",
		PeerID:      "vula:ed25519:abc123",
		PeerDisplay: "Alice",
		Direction:   CallHistDirInbound,
		Status:      CallHistStatusCompleted,
		StartedAt:   now,
		DurationSec: 42,
	}

	if err := store.CallHistRecord(entry); err != nil {
		t.Fatalf("CallHistRecord: %v", err)
	}

	list := store.CallHistList(0)
	if len(list) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(list))
	}
	got := list[0]
	if got.ID != entry.ID {
		t.Errorf("ID: want %q, got %q", entry.ID, got.ID)
	}
	if got.DurationSec != 42 {
		t.Errorf("DurationSec: want 42, got %d", got.DurationSec)
	}
}

func TestCallHistStoreLimit(t *testing.T) {
	dir := t.TempDir()
	store := callhistNewStore(dir)

	for i := 0; i < 10; i++ {
		e := &CallHistEntry{
			ID:          time.Now().Format("20060102150405.000000"),
			PeerDisplay: "Peer",
			Direction:   CallHistDirOutbound,
			Status:      CallHistStatusCompleted,
			StartedAt:   time.Now(),
		}
		if err := store.CallHistRecord(e); err != nil {
			t.Fatalf("CallHistRecord[%d]: %v", i, err)
		}
	}

	all := store.CallHistList(0)
	if len(all) != 10 {
		t.Fatalf("want 10, got %d", len(all))
	}

	limited := store.CallHistList(3)
	if len(limited) != 3 {
		t.Fatalf("want 3 with limit=3, got %d", len(limited))
	}
}

func TestCallHistStoreOrderNewestFirst(t *testing.T) {
	dir := t.TempDir()
	store := callhistNewStore(dir)

	ids := []string{"first", "second", "third"}
	for _, id := range ids {
		e := &CallHistEntry{
			ID:        id,
			StartedAt: time.Now(),
			Direction: CallHistDirInbound,
			Status:    CallHistStatusMissed,
		}
		if err := store.CallHistRecord(e); err != nil {
			t.Fatalf("CallHistRecord %s: %v", id, err)
		}
	}

	list := store.CallHistList(0)
	if list[0].ID != "third" {
		t.Errorf("newest first: want %q at index 0, got %q", "third", list[0].ID)
	}
	if list[2].ID != "first" {
		t.Errorf("oldest last: want %q at index 2, got %q", "first", list[2].ID)
	}
}

func TestCallHistStorePersistence(t *testing.T) {
	dir := t.TempDir()
	s1 := callhistNewStore(dir)
	_ = s1.CallHistRecord(&CallHistEntry{
		ID:        "persist-1",
		StartedAt: time.Now(),
		Direction: CallHistDirInbound,
		Status:    CallHistStatusMissed,
	})

	// Re-open store from same dir — should reload data.
	s2 := callhistNewStore(dir)
	list := s2.CallHistList(0)
	if len(list) != 1 {
		t.Fatalf("expected 1 entry after reload, got %d", len(list))
	}
	if list[0].ID != "persist-1" {
		t.Errorf("wrong ID after reload: %q", list[0].ID)
	}
}

func TestCallHistStoreAtomicFlush(t *testing.T) {
	dir := t.TempDir()
	store := callhistNewStore(dir)
	_ = store.CallHistRecord(&CallHistEntry{
		ID:        "atomic-1",
		StartedAt: time.Now(),
		Direction: CallHistDirOutbound,
		Status:    CallHistStatusOutgoing,
	})

	// Temporary file should be gone after flush.
	tmp := filepath.Join(dir, "callhistory.json.tmp")
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("tmp file should have been renamed away, stat err: %v", err)
	}
}

// ---- HTTP handler tests ---------------------------------------------------

func newTestHandler(t *testing.T) *CallHistHandler {
	t.Helper()
	dir := t.TempDir()
	store := callhistNewStore(dir)
	return callhistNewHandler(store)
}

func TestCallhistListEmpty(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/peering/call/history", nil)
	w := httptest.NewRecorder()
	h.handleCallhistList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var entries []CallHistEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("want empty slice, got %d entries", len(entries))
	}
}

func TestCallhistRecord(t *testing.T) {
	h := newTestHandler(t)

	body, _ := json.Marshal(CallHistEntry{
		PeerID:      "vula:ed25519:xyz",
		PeerDisplay: "Bob",
		Direction:   CallHistDirInbound,
		Status:      CallHistStatusCompleted,
		StartedAt:   time.Now(),
		DurationSec: 120,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/peering/call/history/record", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.handleCallhistRecord(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d — body: %s", w.Code, w.Body.String())
	}

	var created CallHistEntry
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("parse created entry: %v", err)
	}
	if created.PeerDisplay != "Bob" {
		t.Errorf("want PeerDisplay=Bob, got %q", created.PeerDisplay)
	}
	if created.ID == "" {
		t.Error("auto-generated ID should not be empty")
	}
}

func TestCallhistRecordThenList(t *testing.T) {
	h := newTestHandler(t)

	for _, status := range []callhistCallStatus{CallHistStatusCompleted, CallHistStatusMissed} {
		body, _ := json.Marshal(CallHistEntry{
			PeerID:    "vula:ed25519:test",
			Direction: CallHistDirInbound,
			Status:    status,
			StartedAt: time.Now(),
		})
		req := httptest.NewRequest(http.MethodPost, "/api/peering/call/history/record", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.handleCallhistRecord(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("record %s: want 201, got %d", status, w.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/peering/call/history", nil)
	w := httptest.NewRecorder()
	h.handleCallhistList(w, req)

	var entries []*CallHistEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	// Newest first: second recorded entry should be index 0.
	if entries[0].Status != CallHistStatusMissed {
		t.Errorf("newest entry should be missed, got %s", entries[0].Status)
	}
}

func TestCallhistMethodNotAllowed(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/peering/call/history", nil)
	w := httptest.NewRecorder()
	h.handleCallhistList(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET-only: want 405, got %d", w.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/peering/call/history/record", nil)
	w2 := httptest.NewRecorder()
	h.handleCallhistRecord(w2, req2)
	if w2.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST-only: want 405, got %d", w2.Code)
	}
}

func TestCallhistLimitQueryParam(t *testing.T) {
	h := newTestHandler(t)

	for i := 0; i < 5; i++ {
		body, _ := json.Marshal(CallHistEntry{
			Direction: CallHistDirInbound,
			Status:    CallHistStatusCompleted,
			StartedAt: time.Now(),
		})
		req := httptest.NewRequest(http.MethodPost, "/api/peering/call/history/record", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.handleCallhistRecord(w, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/peering/call/history?limit=2", nil)
	w := httptest.NewRecorder()
	h.handleCallhistList(w, req)

	var entries []*CallHistEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("want 2 with limit=2, got %d", len(entries))
	}
}

func TestRegisterCallHistoryHandlers(t *testing.T) {
	dir := t.TempDir()
	mux := http.NewServeMux()
	store := RegisterCallHistoryHandlers(mux, dir)
	if store == nil {
		t.Fatal("RegisterCallHistoryHandlers returned nil store")
	}

	// Verify the GET endpoint is registered.
	req := httptest.NewRequest(http.MethodGet, "/api/peering/call/history", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
}
