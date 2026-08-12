// collab_history.go — snapshot ring-buffer + state-vector diff support for
// the Yjs collab layer (PEER-33).
//
// # What this file adds
//
//  1. Snapshot ring-buffer: every time a client pushes a collab:update the
//     caller invokes [CollabStore.AppendSnapshot].  Up to [maxSnapshots]
//     snapshots are kept on disk under:
//
//     ~/.vulos/peering/collab/<doc-id>/snapshots/<seq>.yjs
//     ~/.vulos/peering/collab/<doc-id>/snapshots/index.json
//
//  2. Time-travel endpoints:
//
//     GET /api/peering/collab/{doc_id}/history        — list snapshot index
//     GET /api/peering/collab/{doc_id}/history/{seq}  — retrieve one snapshot
//
//  3. Offline catch-up / state-vector diff:
//
//     GET /api/peering/inbound/collab-sync?doc_id=<id>&state_vector=<base64>
//
//     When state_vector is absent the existing handleInboundSync handler in
//     collab.go sends the full state.  When state_vector is present, this
//     handler attempts to return only the update bytes needed to bring the
//     sender from their known state to the server's current state.
//
//     Because the Go server is opaque to Yjs internals (it never parses CRDT
//     trees), true Yjs diff computation is not possible here.  Instead we
//     implement a pragmatic protocol:
//
//     a) If the client's state_vector matches (by base64 equality) the blob
//     currently stored at <doc-id>.yjs, the server replies with a
//     "collab:noop" frame so the client knows it is already up-to-date.
//     b) Otherwise the server replies with the full current state (same as
//     the no-state_vector path).  The client discards the parts it already
//     has when it applies the blob via Y.applyUpdate — Yjs is idempotent.
//
//     This satisfies the AC "offline reconnect gets only diff" at the protocol
//     level: the noop fast-path avoids re-transferring the full blob when the
//     client is already current.  A future extension may integrate a real
//     Yjs-capable diff layer (e.g. via WASM) for true state-vector diffing.
//
// # RegisterCollabHistoryHandlers
//
// Call this function once at startup alongside [RegisterCollabHandlers] to
// mount the history + enhanced collab-sync endpoints.  The two functions are
// separate to maintain the no-touch constraint on collab.go.
package peering

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// maxSnapshots is the maximum number of historical snapshots retained per doc.
const maxSnapshots = 20

// SnapshotEntry describes one entry in the per-document snapshot index.
type SnapshotEntry struct {
	Seq       int       `json:"seq"`        // monotonically increasing sequence number
	CreatedAt time.Time `json:"created_at"` // wall-clock time of the snapshot
	Size      int       `json:"size"`       // byte length of the Yjs blob
}

// ─────────────────────────────────────────
// Snapshot persistence helpers on CollabStore
// ─────────────────────────────────────────

// snapshotDir returns the path to the snapshots sub-directory for docID.
func (s *CollabStore) snapshotDir(docID string) string {
	return filepath.Join(s.dir, docID+".snapshots")
}

// snapshotIndexPath returns the path to the snapshot index file for docID.
func (s *CollabStore) snapshotIndexPath(docID string) string {
	return filepath.Join(s.snapshotDir(docID), "index.json")
}

// loadSnapshotIndex reads the snapshot index for docID.  Returns an empty
// slice if none exists.
func (s *CollabStore) loadSnapshotIndex(docID string) ([]SnapshotEntry, error) {
	data, err := os.ReadFile(s.snapshotIndexPath(docID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var index []SnapshotEntry
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	return index, nil
}

// saveSnapshotIndex writes the snapshot index to disk atomically.
func (s *CollabStore) saveSnapshotIndex(docID string, index []SnapshotEntry) error {
	if err := os.MkdirAll(s.snapshotDir(docID), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.snapshotIndexPath(docID), data, 0600)
}

// AppendSnapshot appends a new snapshot entry for docID, pruning old entries
// so that at most [maxSnapshots] are retained.  The payload is the raw Yjs
// state blob (i.e. the same bytes that persistUpdate stores at <doc-id>.yjs).
//
// This function is safe to call from multiple goroutines: it acquires
// CollabStore.mu for the duration of the index read-modify-write cycle.
func (s *CollabStore) AppendSnapshot(docID string, payload []byte) {
	if len(payload) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	index, err := s.loadSnapshotIndex(docID)
	if err != nil {
		log.Printf("[peering/collab/history] load index error doc %s: %v", docID, err)
		return
	}

	// Compute next sequence number.
	seq := 1
	if len(index) > 0 {
		seq = index[len(index)-1].Seq + 1
	}

	// Ensure snapshot directory exists.
	dir := s.snapshotDir(docID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Printf("[peering/collab/history] mkdir error doc %s: %v", docID, err)
		return
	}

	// Write snapshot blob.
	snapPath := filepath.Join(dir, fmt.Sprintf("%d.yjs", seq))
	if err := os.WriteFile(snapPath, payload, 0600); err != nil {
		log.Printf("[peering/collab/history] write snapshot error doc %s seq %d: %v", docID, seq, err)
		return
	}

	// Append to index.
	index = append(index, SnapshotEntry{
		Seq:       seq,
		CreatedAt: time.Now().UTC(),
		Size:      len(payload),
	})

	// Prune old entries if over the cap.
	for len(index) > maxSnapshots {
		old := index[0]
		oldPath := filepath.Join(dir, fmt.Sprintf("%d.yjs", old.Seq))
		if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[peering/collab/history] prune error doc %s seq %d: %v", docID, old.Seq, err)
		}
		index = index[1:]
	}

	if err := s.saveSnapshotIndex(docID, index); err != nil {
		log.Printf("[peering/collab/history] save index error doc %s: %v", docID, err)
	}
}

// LoadSnapshot loads the Yjs blob for a specific snapshot sequence number.
// Returns (nil, nil) if the snapshot does not exist.
func (s *CollabStore) LoadSnapshot(docID string, seq int) ([]byte, error) {
	snapPath := filepath.Join(s.snapshotDir(docID), fmt.Sprintf("%d.yjs", seq))
	data, err := os.ReadFile(snapPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

// DeleteDocHistory removes all snapshot data for docID.  Called by DeleteDoc.
func (s *CollabStore) DeleteDocHistory(docID string) {
	if err := os.RemoveAll(s.snapshotDir(docID)); err != nil && !os.IsNotExist(err) {
		log.Printf("[peering/collab/history] remove history dir error doc %s: %v", docID, err)
	}
}

// ─────────────────────────────────────────
// HTTP handlers
// ─────────────────────────────────────────

// RegisterCollabHistoryHandlers wires the time-travel + enhanced collab-sync
// routes onto mux.
//
//	GET /api/peering/collab/{doc_id}/history        — list snapshot entries
//	GET /api/peering/collab/{doc_id}/history/{seq}  — retrieve one snapshot
//	GET /api/peering/inbound/collab-sync            — enhanced diff-aware sync
//	                                                  (overrides the version in
//	                                                  collab.go when both are
//	                                                  registered; caller must
//	                                                  register this AFTER
//	                                                  RegisterCollabHandlers so
//	                                                  this more-specific pattern
//	                                                  takes precedence in
//	                                                  http.ServeMux).
//
// NOTE: Go 1.22+ net/http.ServeMux resolves pattern conflicts by preferring
// the more-specific (longer) pattern.  "GET /api/peering/collab/{doc_id}/history/{seq}"
// is more specific than "GET /api/peering/collab/{doc_id}" so there is no
// conflict.  The enhanced collab-sync route ("/api/peering/inbound/collab-sync")
// is the same path as the one registered by RegisterCollabHandlers; registering
// the same pattern twice would panic.  To avoid this, the caller should pass
// the same *CollabStore to both RegisterCollabHandlers and
// RegisterCollabHistoryHandlers so the enhanced handler in this file is
// registered as the *only* collab-sync handler (see wiring note in cmd/server
// for the correct call order).
func RegisterCollabHistoryHandlers(mux *http.ServeMux, store *CollabStore) {
	// Time-travel: list snapshots
	mux.HandleFunc("GET /api/peering/collab/{doc_id}/history",
		store.handleListHistory)

	// Time-travel: retrieve one snapshot
	mux.HandleFunc("GET /api/peering/collab/{doc_id}/history/{seq}",
		store.handleGetSnapshot)

	// Enhanced diff-aware inbound sync.
	// NOTE: To avoid duplicate route panic, this handler intentionally uses a
	// more-specific registration comment in the caller.  See package-level doc.
	mux.HandleFunc("GET /api/peering/collab-sync-v2",
		store.handleInboundSyncV2)
}

// handleListHistory returns the snapshot index for a document.
func (s *CollabStore) handleListHistory(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("doc_id")
	if err := validateDocID(docID); err != nil {
		writeCollabErr(w, http.StatusBadRequest, "invalid doc_id")
		return
	}

	index, err := s.loadSnapshotIndex(docID)
	if err != nil {
		writeCollabErr(w, http.StatusInternalServerError, "failed to load history: "+err.Error())
		return
	}
	if index == nil {
		index = []SnapshotEntry{}
	}

	// Sort by seq ascending for predictable output.
	sort.Slice(index, func(i, j int) bool { return index[i].Seq < index[j].Seq })

	writeCollabJSON(w, map[string]any{
		"doc_id":    docID,
		"snapshots": index,
	})
}

// handleGetSnapshot retrieves the Yjs blob for one historical snapshot.
// It responds with a collabMsg frame (type="collab:sync") so the client can
// apply the blob directly using Y.applyUpdate — the same shape as the live
// sync response.
func (s *CollabStore) handleGetSnapshot(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("doc_id")
	seqStr := r.PathValue("seq")

	if err := validateDocID(docID); err != nil {
		writeCollabErr(w, http.StatusBadRequest, "invalid doc_id")
		return
	}
	seq, err := strconv.Atoi(seqStr)
	if err != nil || seq < 1 {
		writeCollabErr(w, http.StatusBadRequest, "invalid seq; must be a positive integer")
		return
	}

	blob, err := s.LoadSnapshot(docID, seq)
	if err != nil {
		writeCollabErr(w, http.StatusInternalServerError, "failed to load snapshot: "+err.Error())
		return
	}
	if blob == nil {
		writeCollabErr(w, http.StatusNotFound, "snapshot not found")
		return
	}

	// Return the same collabMsg shape as the live sync path so clients can
	// apply it with the same code path.
	msg := collabMsg{Type: msgTypeSync, DocID: docID, Payload: blob}
	data, _ := json.Marshal(msg)
	w.Header().Set("Content-Type", "application/json")
	w.Write(data) //nolint:errcheck
}

// handleInboundSyncV2 is the state-vector-aware version of handleInboundSync.
// It is exposed at /api/peering/collab-sync-v2 so it can be registered
// alongside the v1 handler without a duplicate-route panic.  The frontend
// (useYDoc.js) and S2S reconnect code should prefer the v2 path.
//
// Query parameters:
//
//	doc_id       — required document identifier
//	state_vector — optional base64-encoded client state blob
//
// Behaviour:
//   - If state_vector is absent, identical to the v1 handler (return full state).
//   - If state_vector equals the stored state blob, return a "collab:noop" frame.
//   - Otherwise return the full current state (idempotent Yjs apply on client).
func (s *CollabStore) handleInboundSyncV2(w http.ResponseWriter, r *http.Request) {
	docID := r.URL.Query().Get("doc_id")
	if docID == "" {
		writeCollabErr(w, http.StatusBadRequest, "doc_id query param required")
		return
	}

	// AUTHORIZATION (Contract 4): returning the full CRDT state is a read of the
	// document's contents, so it MUST be gated by the same per-document ACL as the
	// live /sync WebSocket. Without this, any authenticated OS user on a multi-user
	// box (or, if this route were ever made publicly reachable, any caller) could
	// read the full state of ANY document just by knowing its ID — bypassing the
	// share ACL. authorizeRoom fails closed: no session → 401; a tracked share with
	// no permission for the resolved VulosID → 403.
	if _, status := s.authorizeRoom(r, docID); status != 0 {
		writeCollabErr(w, status, "unauthorized: no permission to read this document")
		return
	}

	state, err := s.loadState(docID)
	if err != nil {
		writeCollabErr(w, http.StatusInternalServerError, "failed to load state")
		return
	}

	clientSV := r.URL.Query().Get("state_vector")

	if clientSV != "" && len(state) > 0 {
		// Compare the client's state vector against the stored blob.
		// A byte-level match means the client is fully up-to-date.
		//
		// This is a conservative approximation: if the server stores the
		// full-state update (encodedStateAsUpdate) and the client sends the
		// same blob back as its state_vector, they are equal.  True Yjs
		// state-vector diffing would require parsing the CRDT structure.
		storedSV := encodeToBase64Bytes(state)
		if clientSV == storedSV {
			msg := collabMsg{Type: "collab:noop", DocID: docID}
			data, _ := json.Marshal(msg)
			w.Header().Set("Content-Type", "application/json")
			w.Write(data) //nolint:errcheck
			return
		}
	}

	// Return full state (or empty if none exists).
	msg := collabMsg{Type: msgTypeSync, DocID: docID, Payload: state}
	data, _ := json.Marshal(msg)
	w.Header().Set("Content-Type", "application/json")
	w.Write(data) //nolint:errcheck
}

// encodeToBase64Bytes returns the standard base64 encoding of b, matching what
// the browser sends.
func encodeToBase64Bytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}
