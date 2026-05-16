// Package peering implements Vula OS peer-to-peer communication services.
// callhistory.go — persistent call log storage and HTTP endpoints (PEER-24).
package peering

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// callhistCallStatus represents the outcome of a call leg.
type callhistCallStatus string

const (
	// CallHistStatusCompleted — call was answered and ended normally.
	CallHistStatusCompleted callhistCallStatus = "completed"
	// CallHistStatusMissed — incoming call was not answered.
	CallHistStatusMissed callhistCallStatus = "missed"
	// CallHistStatusRejected — incoming call was explicitly declined.
	CallHistStatusRejected callhistCallStatus = "rejected"
	// CallHistStatusOutgoing — outgoing call initiated by this node.
	CallHistStatusOutgoing callhistCallStatus = "outgoing"
)

// CallHistDirection indicates whether the call was incoming or outgoing.
type CallHistDirection string

const (
	// CallHistDirInbound — call received from a remote peer.
	CallHistDirInbound CallHistDirection = "inbound"
	// CallHistDirOutbound — call initiated by the local user.
	CallHistDirOutbound CallHistDirection = "outbound"
)

// CallHistEntry is a single call log record.
type CallHistEntry struct {
	ID          string             `json:"id"`
	PeerID      string             `json:"peer_id"`
	PeerDisplay string             `json:"peer_display"`
	Direction   CallHistDirection  `json:"direction"`
	Status      callhistCallStatus `json:"status"`
	StartedAt   time.Time          `json:"started_at"`
	EndedAt     *time.Time         `json:"ended_at,omitempty"`
	DurationSec int                `json:"duration_sec"`
}

// CallHistStore persists call history to ~/.vulos/peering/callhistory.json.
type CallHistStore struct {
	mu      sync.RWMutex
	path    string
	entries []*CallHistEntry
}

// callhistNewStore opens (or creates) the call history file at dataDir.
func callhistNewStore(dataDir string) *CallHistStore {
	p := filepath.Join(dataDir, "callhistory.json")
	s := &CallHistStore{path: p}
	if raw, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(raw, &s.entries); err != nil {
			log.Printf("[callhist] warn: could not parse %s: %v", p, err)
		}
	}
	return s
}

// callhistFlush writes entries to disk atomically.
func (s *CallHistStore) callhistFlush() error {
	raw, err := json.Marshal(s.entries)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// CallHistRecord appends a new entry to the call log.
func (s *CallHistStore) CallHistRecord(entry *CallHistEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
	// Keep at most 500 entries (trim oldest first).
	const maxEntries = 500
	if len(s.entries) > maxEntries {
		s.entries = s.entries[len(s.entries)-maxEntries:]
	}
	return s.callhistFlush()
}

// CallHistList returns all entries newest-first up to limit (0 = all).
func (s *CallHistStore) CallHistList(limit int) []*CallHistEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := len(s.entries)
	if limit <= 0 || limit > n {
		limit = n
	}
	result := make([]*CallHistEntry, limit)
	for i := 0; i < limit; i++ {
		result[i] = s.entries[n-1-i]
	}
	return result
}

// CallHistHandler holds HTTP handler dependencies for call history.
type CallHistHandler struct {
	store *CallHistStore
}

// callhistNewHandler creates a handler backed by the given store.
func callhistNewHandler(store *CallHistStore) *CallHistHandler {
	return &CallHistHandler{store: store}
}

// handleCallhistList serves GET /api/peering/call/history.
func (h *CallHistHandler) handleCallhistList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 100
	if q := r.URL.Query().Get("limit"); q != "" {
		var n int
		if _, err := parseIntQuery(q, &n); err == nil && n > 0 {
			limit = n
		}
	}
	entries := h.store.CallHistList(limit)
	if entries == nil {
		entries = []*CallHistEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// handleCallhistRecord serves POST /api/peering/call/history/record.
// This is an internal endpoint invoked by the signalling layer to persist call outcomes.
func (h *CallHistHandler) handleCallhistRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var entry CallHistEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if entry.ID == "" {
		entry.ID = time.Now().Format("20060102150405.000000")
	}
	if entry.StartedAt.IsZero() {
		entry.StartedAt = time.Now()
	}
	if err := h.store.CallHistRecord(&entry); err != nil {
		log.Printf("[callhist] record error: %v", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(entry)
}

// parseIntQuery parses a string into *dst and returns an error on failure.
func parseIntQuery(s string, dst *int) (int, error) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errNonDigit
		}
		n = n*10 + int(c-'0')
	}
	*dst = n
	return n, nil
}

// errNonDigit is a sentinel for non-digit characters.
var errNonDigit = errStr("non-digit character in query parameter")

type errStr string

func (e errStr) Error() string { return string(e) }

// RegisterCallHistoryHandlers registers all call-history HTTP routes into mux.
// dataDir should point to ~/.vulos/peering/ (or equivalent).
// Exported so cmd/server/main.go can wire it in.
func RegisterCallHistoryHandlers(mux *http.ServeMux, dataDir string) *CallHistStore {
	store := callhistNewStore(dataDir)
	h := callhistNewHandler(store)
	mux.HandleFunc("GET /api/peering/call/history", h.handleCallhistList)
	mux.HandleFunc("POST /api/peering/call/history/record", h.handleCallhistRecord)
	log.Printf("[callhist] history store at %s", store.path)
	return store
}
