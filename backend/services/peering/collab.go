// Package peering implements Vulos' peer-to-peer collaboration layer.
// collab.go handles Yjs CRDT document sync and awareness over the
// "collab" WebSocket channel, plus server-to-server relay of opaque
// binary update blobs.
package peering

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"vulos/backend/internal/wsutil"
)

// ─────────────────────────────────────────
// Message types exchanged on the WS channel
// ─────────────────────────────────────────

const (
	// msgTypeUpdate carries a raw Yjs binary update blob.
	msgTypeUpdate = "collab:update"
	// msgTypeAwareness carries an opaque awareness state blob.
	msgTypeAwareness = "collab:awareness"
	// msgTypeSync requests the persisted doc state for catch-up.
	msgTypeSync = "collab:sync"
)

// docIDPattern is the allowlist for collaborative document IDs.
// Only lowercase hex characters (UUID-style) and hyphens are accepted.
// This prevents path traversal when docID is embedded in filesystem paths
// (e.g. <dir>/<docID>.yjs, <dir>/<docID>.snapshots/).
var docIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]{0,126}$`)

// validateDocID returns an error if docID is empty or contains characters
// that could be used for path traversal.
func validateDocID(docID string) error {
	if !docIDPattern.MatchString(docID) {
		return fmt.Errorf("invalid doc_id: must match [a-z0-9][a-z0-9-]{0,126}")
	}
	return nil
}

// collabMsg is the envelope exchanged on the "collab" WebSocket channel.
// Payload is opaque binary (base64-encoded in JSON); the server never
// inspects nor modifies it — it relays it verbatim.
type collabMsg struct {
	// Type is one of msgTypeUpdate | msgTypeAwareness | msgTypeSync.
	Type string `json:"type"`
	// DocID identifies the collaborative document.
	DocID string `json:"doc_id"`
	// Payload is the raw Yjs binary blob encoded as base64.
	Payload []byte `json:"payload"`
}

// ─────────────────────────────────────────
// Per-document session hub
// ─────────────────────────────────────────

// collabClient is a single connected browser peer for one document.
// send carries JSON-encoded collabMsg frames to write to the connection.
// A nil value is the ping sentinel — the write pump sends a WebSocket
// ping control frame when it dequeues nil, keeping the connection alive
// without a separate goroutine (which would race on concurrent writes).
type collabClient struct {
	conn   *websocket.Conn
	send   chan []byte // buffered outbound message queue; nil = ping sentinel
	docID  string
	peerID string // optional: client-supplied peer identifier
}

// collabRoom manages all connected clients for a single document.
// It is goroutine-safe.
type collabRoom struct {
	mu      sync.RWMutex
	clients map[*collabClient]struct{}
}

func newCollabRoom() *collabRoom {
	return &collabRoom{clients: make(map[*collabClient]struct{})}
}

// join registers a client in the room.
func (r *collabRoom) join(c *collabClient) {
	r.mu.Lock()
	r.clients[c] = struct{}{}
	r.mu.Unlock()
}

// leave removes a client from the room.
// The send channel is NOT closed here; the write pump stops via the
// done channel that the read pump closes on exit.
func (r *collabRoom) leave(c *collabClient) {
	r.mu.Lock()
	delete(r.clients, c)
	r.mu.Unlock()
}

// broadcast sends raw bytes to every client except the origin.
func (r *collabRoom) broadcast(origin *collabClient, data []byte) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for c := range r.clients {
		if c == origin {
			continue
		}
		select {
		case c.send <- data:
		default:
			// slow consumer — drop rather than block the broadcaster
			log.Printf("[peering/collab] slow consumer on doc %s — frame dropped", origin.docID)
		}
	}
}

// count returns the number of connected clients.
func (r *collabRoom) count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.clients)
}

// ─────────────────────────────────────────
// CollabStore — top-level service
// ─────────────────────────────────────────

// CollabStore manages document persistence and the per-document WebSocket rooms.
// Yjs binary document states are stored at:
//
//	~/.vulos/peering/collab/<doc-id>.yjs      (full CRDT state)
//	~/.vulos/peering/collab/<doc-id>.meta.json (title, type, owner, shared-with)
type CollabStore struct {
	dir string // e.g. ~/.vulos/peering/collab

	mu    sync.Mutex
	rooms map[string]*collabRoom // doc_id → room
}

// NewCollabStore creates (or opens) the collab storage directory.
func NewCollabStore(dir string) (*CollabStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &CollabStore{
		dir:   dir,
		rooms: make(map[string]*collabRoom),
	}, nil
}

// room returns (or lazily creates) the room for docID.
func (s *CollabStore) room(docID string) *collabRoom {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rooms[docID]; ok {
		return r
	}
	r := newCollabRoom()
	s.rooms[docID] = r
	return r
}

// persistUpdate atomically appends (merges) a Yjs update blob to the
// on-disk document state. For simplicity we store the most-recent
// complete state snapshot; a production system would use a proper Yjs
// persistence layer.  We keep the last blob written as the canonical
// state so that catch-up clients receive it via msgTypeSync.
//
// The update blob is opaque binary — the server never parses it.
func (s *CollabStore) persistUpdate(docID string, payload []byte) error {
	path := filepath.Join(s.dir, docID+".yjs")
	// O_CREATE|O_WRONLY|O_TRUNC: overwrite with latest full state blob.
	// Callers should send Yjs encodedStateAsUpdate(doc) snapshots, not diffs,
	// for persistence; diffs are fine for live relay only.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(payload)
	return err
}

// loadState returns the persisted Yjs state for docID, or nil if none exists.
func (s *CollabStore) loadState(docID string) ([]byte, error) {
	path := filepath.Join(s.dir, docID+".yjs")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

// DocMeta holds document metadata stored alongside the Yjs binary.
type DocMeta struct {
	DocID      string    `json:"doc_id"`
	Title      string    `json:"title"`
	DocType    string    `json:"doc_type"` // "doc","sheet","slide","note","code"
	Owner      string    `json:"owner"`
	SharedWith []string  `json:"shared_with"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// UpsertMeta writes or updates the metadata for a document.
func (s *CollabStore) UpsertMeta(m DocMeta) error {
	m.UpdatedAt = time.Now().UTC()
	path := filepath.Join(s.dir, m.DocID+".meta.json")
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// GetMeta reads the metadata for a document.
func (s *CollabStore) GetMeta(docID string) (*DocMeta, error) {
	path := filepath.Join(s.dir, docID+".meta.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m DocMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ListDocs returns metadata for all known collaborative documents.
func (s *CollabStore) ListDocs() ([]*DocMeta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var docs []*DocMeta
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		base := e.Name()[:len(e.Name())-len(".meta.json")]
		// only *.meta.json files
		if len(e.Name()) < len(".meta.json") || e.Name()[len(e.Name())-len(".meta.json"):] != ".meta.json" {
			continue
		}
		m, err := s.GetMeta(base)
		if err != nil || m == nil {
			continue
		}
		docs = append(docs, m)
	}
	return docs, nil
}

// DeleteDoc removes the Yjs state and metadata for a document.
func (s *CollabStore) DeleteDoc(docID string) error {
	os.Remove(filepath.Join(s.dir, docID+".yjs"))
	os.Remove(filepath.Join(s.dir, docID+".meta.json"))
	// Purge snapshot history (PEER-33).
	s.DeleteDocHistory(docID)
	// evict live room
	s.mu.Lock()
	delete(s.rooms, docID)
	s.mu.Unlock()
	return nil
}

// ─────────────────────────────────────────
// HTTP handlers — RegisterCollabHandlers
// ─────────────────────────────────────────

// RegisterCollabHandlers wires all collab HTTP endpoints onto mux.
//
//	WS  /api/peering/collab/{doc_id}/sync         — real-time sync + awareness
//	GET /api/peering/collab/documents              — list all collab docs
//	GET /api/peering/collab/{doc_id}               — doc state + meta
//	DELETE /api/peering/collab/{doc_id}            — remove doc
//	POST /api/peering/collab/share                 — share a doc (upsert meta)
//	POST /api/peering/inbound/collab-update        — S2S relay: receive CRDT update
//	GET  /api/peering/inbound/collab-sync          — S2S relay: catch-up state
func RegisterCollabHandlers(mux *http.ServeMux, store *CollabStore) {
	// WebSocket — real-time sync + awareness
	mux.HandleFunc("/api/peering/collab/{doc_id}/sync", store.handleCollabWS)

	// REST
	mux.HandleFunc("GET /api/peering/collab/documents", store.handleListDocs)
	mux.HandleFunc("GET /api/peering/collab/{doc_id}", store.handleGetDoc)
	mux.HandleFunc("DELETE /api/peering/collab/{doc_id}", store.handleDeleteDoc)
	mux.HandleFunc("POST /api/peering/collab/share", store.handleShare)

	// Server-to-server inbound.
	// HandleInboundCollabUpdate reads the verified envelope from the request
	// context (set by InboundMiddleware) rather than from the request body,
	// which InboundMiddleware has already consumed. handleInboundUpdate (the
	// plain variant) is retained internally for direct HTTP calls that bypass
	// the middleware (e.g. the non-signed catch-up path used in tests).
	mux.HandleFunc("POST /api/peering/inbound/collab-update", store.HandleInboundCollabUpdate)
	mux.HandleFunc("GET /api/peering/inbound/collab-sync", store.handleInboundSync)
}

// handleCollabWS upgrades the connection and runs the read/write pumps.
func (s *CollabStore) handleCollabWS(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("doc_id")
	if err := validateDocID(docID); err != nil {
		http.Error(w, `{"error":"invalid doc_id"}`, http.StatusBadRequest)
		return
	}

	conn, err := wsutil.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[peering/collab] ws upgrade error for doc %s: %v", docID, err)
		return
	}

	client := &collabClient{
		conn:  conn,
		send:  make(chan []byte, 64),
		docID: docID,
	}

	room := s.room(docID)
	room.join(client)
	log.Printf("[peering/collab] client joined doc %s (room size=%d)", docID, room.count())

	// Enqueue catch-up state so the write pump delivers it (no direct conn
	// write here — all writes must go through the single write pump goroutine
	// to avoid concurrent-write races on the WebSocket connection).
	if state, err := s.loadState(docID); err == nil && len(state) > 0 {
		syncMsg := collabMsg{Type: msgTypeSync, DocID: docID, Payload: state}
		if data, err := json.Marshal(syncMsg); err == nil {
			client.send <- data
		}
	}

	// done is closed when the read pump exits to signal the write pump to stop.
	done := make(chan struct{})

	// Write pump — the SOLE goroutine that writes to conn.
	// A nil value in the send channel is the ping sentinel.
	go func() {
		pingTick := time.NewTicker(30 * time.Second)
		defer func() {
			pingTick.Stop()
			conn.Close()
		}()
		for {
			select {
			case <-done:
				return
			case <-pingTick.C:
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case data, ok := <-client.send:
				if !ok {
					return
				}
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
					return
				}
			}
		}
	}()

	// Read pump — runs in the calling goroutine; signals done on exit.
	defer func() {
		close(done)
		room.leave(client)
		conn.Close()
		log.Printf("[peering/collab] client left doc %s (room size=%d)", docID, room.count())
	}()

	conn.SetReadLimit(4 << 20) // 4 MiB max frame
	conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[peering/collab] read error doc %s: %v", docID, err)
			}
			break
		}
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))

		var msg collabMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("[peering/collab] malformed message on doc %s: %v", docID, err)
			continue
		}
		if msg.DocID != docID {
			log.Printf("[peering/collab] doc_id mismatch: got %q want %q", msg.DocID, docID)
			continue
		}

		switch msg.Type {
		case msgTypeUpdate:
			// Persist the update blob as the latest doc state, then broadcast.
			if err := s.persistUpdate(docID, msg.Payload); err != nil {
				log.Printf("[peering/collab] persist error doc %s: %v", docID, err)
			}
			// Append to snapshot ring-buffer for time-travel history (PEER-33).
			s.AppendSnapshot(docID, msg.Payload)
			room.broadcast(client, raw)

		case msgTypeAwareness:
			// Awareness is ephemeral — relay only, do not persist.
			room.broadcast(client, raw)

		case msgTypeSync:
			// Client is requesting the current persisted state.
			state, err := s.loadState(docID)
			if err != nil {
				log.Printf("[peering/collab] load state error doc %s: %v", docID, err)
				continue
			}
			if len(state) == 0 {
				continue
			}
			reply := collabMsg{Type: msgTypeSync, DocID: docID, Payload: state}
			replyData, _ := json.Marshal(reply)
			select {
			case client.send <- replyData:
			default:
			}

		default:
			log.Printf("[peering/collab] unknown message type %q on doc %s", msg.Type, docID)
		}
	}
}

// handleListDocs returns metadata for all known documents.
func (s *CollabStore) handleListDocs(w http.ResponseWriter, r *http.Request) {
	docs, err := s.ListDocs()
	if err != nil {
		writeCollabErr(w, http.StatusInternalServerError, "failed to list docs: "+err.Error())
		return
	}
	if docs == nil {
		docs = []*DocMeta{}
	}
	writeCollabJSON(w, docs)
}

// handleGetDoc returns the Yjs state and metadata for a single document.
func (s *CollabStore) handleGetDoc(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("doc_id")
	if err := validateDocID(docID); err != nil {
		writeCollabErr(w, http.StatusBadRequest, "invalid doc_id")
		return
	}

	meta, err := s.GetMeta(docID)
	if err != nil {
		writeCollabErr(w, http.StatusInternalServerError, "failed to read meta")
		return
	}

	state, err := s.loadState(docID)
	if err != nil {
		writeCollabErr(w, http.StatusInternalServerError, "failed to load state")
		return
	}

	writeCollabJSON(w, map[string]any{
		"doc_id":    docID,
		"meta":      meta,
		"has_state": len(state) > 0,
		"size":      len(state),
	})
}

// handleDeleteDoc removes a document from the store.
func (s *CollabStore) handleDeleteDoc(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("doc_id")
	if err := validateDocID(docID); err != nil {
		writeCollabErr(w, http.StatusBadRequest, "invalid doc_id")
		return
	}
	if err := s.DeleteDoc(docID); err != nil {
		writeCollabErr(w, http.StatusInternalServerError, "failed to delete doc")
		return
	}
	writeCollabJSON(w, map[string]string{"status": "deleted", "doc_id": docID})
}

// handleShare upserts document metadata (initiates or accepts a share).
func (s *CollabStore) handleShare(w http.ResponseWriter, r *http.Request) {
	var m DocMeta
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeCollabErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if m.DocID == "" {
		writeCollabErr(w, http.StatusBadRequest, "doc_id is required")
		return
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if err := s.UpsertMeta(m); err != nil {
		writeCollabErr(w, http.StatusInternalServerError, "failed to save meta")
		return
	}
	writeCollabJSON(w, m)
}

// ─────────────────────────────────────────
// Server-to-server (S2S) inbound handlers
// ─────────────────────────────────────────

// handleInboundUpdate receives a CRDT update blob from a peer server,
// persists it, and fans it out to all locally connected browser clients.
func (s *CollabStore) handleInboundUpdate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeCollabErr(w, http.StatusBadRequest, "failed to read body")
		return
	}

	var msg collabMsg
	if err := json.Unmarshal(body, &msg); err != nil {
		writeCollabErr(w, http.StatusBadRequest, "invalid collab message")
		return
	}
	if msg.Type != msgTypeUpdate {
		writeCollabErr(w, http.StatusBadRequest, "expected collab:update message type")
		return
	}
	if msg.DocID == "" {
		writeCollabErr(w, http.StatusBadRequest, "doc_id is required")
		return
	}

	// Persist then fan out to local WS clients.
	if err := s.persistUpdate(msg.DocID, msg.Payload); err != nil {
		log.Printf("[peering/collab] inbound persist error doc %s: %v", msg.DocID, err)
	}
	// Snapshot for time-travel history (PEER-33).
	s.AppendSnapshot(msg.DocID, msg.Payload)

	room := s.room(msg.DocID)
	room.broadcast(nil, body)

	writeCollabJSON(w, map[string]string{"status": "ok", "doc_id": msg.DocID})
}

// handleInboundSync responds to a catch-up request from a peer server.
// The caller passes ?doc_id=<id>; we return the current persisted state.
func (s *CollabStore) handleInboundSync(w http.ResponseWriter, r *http.Request) {
	docID := r.URL.Query().Get("doc_id")
	if docID == "" {
		writeCollabErr(w, http.StatusBadRequest, "doc_id query param required")
		return
	}

	state, err := s.loadState(docID)
	if err != nil {
		writeCollabErr(w, http.StatusInternalServerError, "failed to load state")
		return
	}

	msg := collabMsg{Type: msgTypeSync, DocID: docID, Payload: state}
	data, _ := json.Marshal(msg)
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// ─────────────────────────────────────────
// Signed S2S inbound handler (envelope-aware)
// ─────────────────────────────────────────

// HandleInboundCollabUpdate implements POST /api/peering/inbound/collab-update
// when the route is wrapped with InboundMiddleware.
//
// InboundMiddleware has already:
//  1. Verified the envelope's Ed25519 signature (401 on failure).
//  2. Confirmed the sender is in the approved contacts list (403 on failure).
//
// This handler:
//  1. Reads the pre-verified *Envelope from the request context.
//  2. Decodes the collab-update payload (doc_id + ops base64 blob).
//  3. Persists the CRDT update blob to disk.
//  4. Fans the update out to all locally connected browser clients for the doc.
//
// The method is exported so the orchestrator can register it on an
// InboundMiddleware-wrapped sub-mux (same pattern as HandleInboundMessage in
// messages.go) without touching inbound.go.
func (s *CollabStore) HandleInboundCollabUpdate(w http.ResponseWriter, r *http.Request) {
	env, ok := r.Context().Value(EnvelopeKey).(*Envelope)
	if !ok || env == nil {
		// Fallback: try the plain JSON path used by internal tests or when the
		// route is called without InboundMiddleware.
		s.handleInboundUpdate(w, r)
		return
	}

	// Decode the payload embedded in the envelope.
	var body struct {
		DocID string `json:"doc_id"`
		Ops   string `json:"ops,omitempty"` // base64-encoded CRDT ops blob
	}
	if err := json.Unmarshal(env.Payload, &body); err != nil {
		writeCollabErr(w, http.StatusBadRequest, "invalid collab-update envelope payload: "+err.Error())
		return
	}
	if body.DocID == "" {
		writeCollabErr(w, http.StatusBadRequest, "doc_id is required in collab-update payload")
		return
	}

	// Decode the base64 ops blob (may be absent for a sync-request-only message).
	var opsBytes []byte
	if body.Ops != "" {
		var decErr error
		opsBytes, decErr = base64.StdEncoding.DecodeString(body.Ops)
		if decErr != nil {
			writeCollabErr(w, http.StatusBadRequest, "invalid base64 in collab-update ops: "+decErr.Error())
			return
		}
	}

	// Persist then fan out to local WS clients.
	if len(opsBytes) > 0 {
		if err := s.persistUpdate(body.DocID, opsBytes); err != nil {
			log.Printf("[peering/collab] inbound S2S persist error doc %s from %s: %v",
				body.DocID, env.From, err)
		}
		// Snapshot for time-travel history (PEER-33).
		s.AppendSnapshot(body.DocID, opsBytes)

		// Re-encode as a collab:update frame for broadcast to local WS clients.
		broadcastMsg := collabMsg{Type: msgTypeUpdate, DocID: body.DocID, Payload: opsBytes}
		broadcastData, _ := json.Marshal(broadcastMsg)
		room := s.room(body.DocID)
		room.broadcast(nil, broadcastData)
	}

	log.Printf("[peering/collab] inbound S2S update for doc %s from %s (ops=%d bytes)",
		body.DocID, env.From, len(opsBytes))
	writeCollabJSON(w, map[string]string{"status": "ok", "doc_id": body.DocID})
}

// ─────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────

func writeCollabJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeCollabErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
