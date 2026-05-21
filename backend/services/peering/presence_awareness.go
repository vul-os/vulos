// presence_awareness.go — Presence/awareness channel on the peering/relay hot path (COLLAB-01).
//
// For apps declared with `concurrency: collaborative` (CONC-01), this file
// provides an ephemeral presence/awareness channel layered on top of the
// existing peering/relay infrastructure (PEER-30 WS multiplex + PEER-05 relay).
//
// Design constraints (from the task AC):
//
//   - Presence state is EPHEMERAL — never written to the bucket / disk.
//   - Awareness clears automatically on disconnect.
//   - The channel does NOT route through the lease primitive (LEASE-* / CONC-02);
//     exclusion is bucket-lease territory; this is real-time hot-path territory.
//   - Relay fallback: if the direct WS path is not available, awareness frames are
//     forwarded via the relay deposit mechanism (opaque encrypted blob, same as
//     PEER-38). The server never decrypts relay payloads.
//
// # Message types
//
//	presence:join    — a peer entered the shared app session
//	presence:leave   — a peer left (also emitted automatically on disconnect)
//	presence:state   — full presence state snapshot for a peer (cursor, selection, …)
//	presence:ping    — keepalive, triggers TTL reset on the server
//
// # Wire format (reuses the ws.go Frame envelope)
//
//	{ "channel": "presence",
//	  "from":    "<peerID>",
//	  "payload": {
//	      "type":    "presence:join|leave|state|ping",
//	      "app_id":  "<app identifier>",
//	      "peer_id": "<peer's Vula ID>",
//	      "data":    <opaque JSON — cursor/selection/etc; nil for join/leave/ping>
//	  }}
//
// # HTTP surface
//
//	WS  /api/peering/presence/{app_id}          — real-time presence channel
//	GET /api/peering/presence/{app_id}/peers    — current peer list (SSE-friendly JSON)
//	POST /api/peering/inbound/presence          — S2S relay: receive presence frame
//
// # Registration
//
// Call [RegisterPresenceHandlers] once at startup, alongside RegisterCollabHandlers,
// to wire all routes. The function creates a single [PresenceService] and registers
// all handlers on the supplied mux.
package peering

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"vulos/backend/internal/wsutil"
)

// ─────────────────────────────────────────
// Presence message type constants
// ─────────────────────────────────────────

const (
	// presenceMsgJoin is emitted when a peer connects to the presence channel.
	presenceMsgJoin = "presence:join"

	// presenceMsgLeave is emitted when a peer disconnects (server-side on behalf
	// of the departing client, so other peers learn of the departure immediately).
	presenceMsgLeave = "presence:leave"

	// presenceMsgState carries an opaque awareness blob: cursor position,
	// selection range, name, colour — whatever the app encodes.
	presenceMsgState = "presence:state"

	// presenceMsgPing is a keepalive from the client; the server resets the
	// peer's TTL and does NOT relay it (it is local only).
	presenceMsgPing = "presence:ping"
)

// presenceTTL is the duration after which a peer entry is evicted from the
// in-memory store if no ping / state message has been received.
const presenceTTL = 90 * time.Second

// ─────────────────────────────────────────
// Wire types
// ─────────────────────────────────────────

// presencePayload is the JSON payload nested inside a ws.go Frame
// (channel="presence").  It is intentionally separate from collabMsg so the
// two channels stay decoupled.
type presencePayload struct {
	// Type is one of the presenceMsg* constants.
	Type string `json:"type"`

	// AppID identifies the collaborative-app session (maps to collabRoom's docID
	// conceptually, but the presence channel is kept separate — it never touches
	// the Yjs state).
	AppID string `json:"app_id"`

	// PeerID is the Vula ID (or local user ID) of the peer that this frame
	// describes.  Set by the server on outbound frames; trusted on inbound only
	// if the connection is server-to-server (S2S relay path).
	PeerID string `json:"peer_id,omitempty"`

	// Data is opaque application-defined JSON.  Nil for join/leave/ping frames.
	Data json.RawMessage `json:"data,omitempty"`
}

// ─────────────────────────────────────────
// In-memory peer entry
// ─────────────────────────────────────────

// peerEntry holds the last-known awareness state for a connected peer.
type peerEntry struct {
	PeerID    string          `json:"peer_id"`
	Data      json.RawMessage `json:"data,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// ─────────────────────────────────────────
// presenceClient — one WS connection
// ─────────────────────────────────────────

// presenceClient is a single browser connection to one app's presence channel.
type presenceClient struct {
	conn   *websocket.Conn
	send   chan []byte
	appID  string
	peerID string
}

// ─────────────────────────────────────────
// presenceRoom — per-app session hub
// ─────────────────────────────────────────

// presenceRoom manages the connected clients and ephemeral peer state for a
// single collaborative-app session.  It is goroutine-safe.
type presenceRoom struct {
	mu      sync.RWMutex
	clients map[*presenceClient]struct{}
	peers   map[string]*peerEntry // peerID → last known state
}

func newPresenceRoom() *presenceRoom {
	return &presenceRoom{
		clients: make(map[*presenceClient]struct{}),
		peers:   make(map[string]*peerEntry),
	}
}

// join registers the client and upserts a bare peer entry.
func (r *presenceRoom) join(c *presenceClient) {
	r.mu.Lock()
	r.clients[c] = struct{}{}
	if _, ok := r.peers[c.peerID]; !ok {
		r.peers[c.peerID] = &peerEntry{
			PeerID:    c.peerID,
			UpdatedAt: time.Now().UTC(),
		}
	}
	r.mu.Unlock()
}

// leave removes the client and, if no other client shares the same peerID,
// evicts the peer entry so that presence clears on disconnect.
func (r *presenceRoom) leave(c *presenceClient) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.clients, c)

	// Count remaining connections for this peerID.
	remaining := 0
	for other := range r.clients {
		if other.peerID == c.peerID {
			remaining++
		}
	}
	if remaining == 0 {
		delete(r.peers, c.peerID)
	}
}

// upsertPeer updates (or inserts) the awareness data for peerID.
func (r *presenceRoom) upsertPeer(peerID string, data json.RawMessage) {
	r.mu.Lock()
	r.peers[peerID] = &peerEntry{
		PeerID:    peerID,
		Data:      data,
		UpdatedAt: time.Now().UTC(),
	}
	r.mu.Unlock()
}

// broadcast sends raw bytes to every connected client except the origin.
// A nil origin broadcasts to all clients (used for S2S relay frames).
func (r *presenceRoom) broadcast(origin *presenceClient, data []byte) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for c := range r.clients {
		if c == origin {
			continue
		}
		select {
		case c.send <- data:
		default:
			log.Printf("[peering/presence] slow consumer on app %s — frame dropped", c.appID)
		}
	}
}

// peerList returns a snapshot of all currently-present peers.
func (r *presenceRoom) peerList() []peerEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]peerEntry, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, *p)
	}
	return out
}

// evictStale removes peer entries whose UpdatedAt is older than presenceTTL.
// This is called lazily from the HTTP peer-list handler and from the
// background reaper (if started).
func (r *presenceRoom) evictStale() {
	cutoff := time.Now().Add(-presenceTTL)
	r.mu.Lock()
	for id, p := range r.peers {
		if p.UpdatedAt.Before(cutoff) {
			delete(r.peers, id)
		}
	}
	r.mu.Unlock()
}

// count returns the number of connected WS clients.
func (r *presenceRoom) count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.clients)
}

// ─────────────────────────────────────────
// PresenceService — top-level service
// ─────────────────────────────────────────

// PresenceService manages ephemeral presence/awareness state for all running
// collaborative-app sessions.  State is kept in memory only — it is never
// written to the bucket or to disk, satisfying the COLLAB-01 AC.
type PresenceService struct {
	mu    sync.Mutex
	rooms map[string]*presenceRoom // appID → room
}

// NewPresenceService creates a ready-to-use PresenceService.
func NewPresenceService() *PresenceService {
	return &PresenceService{
		rooms: make(map[string]*presenceRoom),
	}
}

// room returns (or lazily creates) the presence room for appID.
func (s *PresenceService) room(appID string) *presenceRoom {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rooms[appID]; ok {
		return r
	}
	r := newPresenceRoom()
	s.rooms[appID] = r
	return r
}

// ─────────────────────────────────────────
// RegisterPresenceHandlers
// ─────────────────────────────────────────

// RegisterPresenceHandlers wires all presence endpoints onto mux.
//
//	WS  /api/peering/presence/{app_id}          — real-time presence channel
//	GET /api/peering/presence/{app_id}/peers    — snapshot of current peers
//	POST /api/peering/inbound/presence          — S2S relay inbound frame
func RegisterPresenceHandlers(mux *http.ServeMux, svc *PresenceService) {
	mux.HandleFunc("/api/peering/presence/{app_id}", svc.handlePresenceWS)
	mux.HandleFunc("GET /api/peering/presence/{app_id}/peers", svc.handlePresencePeers)
	mux.HandleFunc("POST /api/peering/inbound/presence", svc.HandleInboundPresence)
}

// ─────────────────────────────────────────
// Handler: WS /api/peering/presence/{app_id}
// ─────────────────────────────────────────

// handlePresenceWS upgrades to WebSocket and runs the presence read/write pumps.
//
// The caller must supply a peerID via the "X-Peer-ID" header (or query param
// "peer_id"). This ID is never validated against a PKI on the WS path — it is
// trusted as-is because authentication is handled by the surrounding middleware
// layer (same assumption as collab.go and ws.go).
func (s *PresenceService) handlePresenceWS(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("app_id")
	if appID == "" {
		presenceWriteErr(w, http.StatusBadRequest, "missing app_id")
		return
	}

	// Accept peer ID from header or query param.
	peerID := r.Header.Get("X-Peer-ID")
	if peerID == "" {
		peerID = r.URL.Query().Get("peer_id")
	}
	if peerID == "" {
		presenceWriteErr(w, http.StatusBadRequest, "peer_id is required (X-Peer-ID header or ?peer_id=)")
		return
	}

	conn, err := wsutil.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[peering/presence] ws upgrade error for app %s: %v", appID, err)
		return
	}

	client := &presenceClient{
		conn:   conn,
		send:   make(chan []byte, 64),
		appID:  appID,
		peerID: peerID,
	}

	room := s.room(appID)
	room.join(client)
	log.Printf("[peering/presence] peer %s joined app %s (room size=%d)", peerID, appID, room.count())

	// Announce join to all other peers in the room.
	joinFrame := s.buildFrame(peerID, presenceMsgJoin, appID, nil)
	if data, err := json.Marshal(joinFrame); err == nil {
		room.broadcast(client, data)
	}

	// Send the current peer-list snapshot to the joining client so they
	// immediately know who else is present.
	room.evictStale()
	peers := room.peerList()
	for _, p := range peers {
		if p.PeerID == peerID {
			continue // skip self
		}
		stateFrame := s.buildFrame(p.PeerID, presenceMsgState, appID, p.Data)
		if data, err := json.Marshal(stateFrame); err == nil {
			select {
			case client.send <- data:
			default:
			}
		}
	}

	// done is closed when the read pump exits.
	done := make(chan struct{})

	// Write pump — sole goroutine writing to conn.
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

	// Read pump — runs in calling goroutine.
	defer func() {
		close(done)
		room.leave(client)
		conn.Close()

		// Announce departure to remaining peers.
		leaveFrame := s.buildFrame(peerID, presenceMsgLeave, appID, nil)
		if data, err := json.Marshal(leaveFrame); err == nil {
			room.broadcast(nil, data)
		}
		log.Printf("[peering/presence] peer %s left app %s (room size=%d)", peerID, appID, room.count())
	}()

	conn.SetReadLimit(256 * 1024) // 256 KiB max frame
	conn.SetReadDeadline(time.Now().Add(presenceTTL))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(presenceTTL))
		return nil
	})

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[peering/presence] read error peer %s app %s: %v", peerID, appID, err)
			}
			break
		}
		conn.SetReadDeadline(time.Now().Add(presenceTTL))

		var msg presencePayload
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("[peering/presence] malformed frame from peer %s: %v", peerID, err)
			continue
		}

		switch msg.Type {
		case presenceMsgState:
			// Update in-memory state (ephemeral only — never persisted).
			room.upsertPeer(peerID, msg.Data)

			// Relay to all other peers in the room.
			outFrame := s.buildFrame(peerID, presenceMsgState, appID, msg.Data)
			if data, err := json.Marshal(outFrame); err == nil {
				room.broadcast(client, data)
			}

		case presenceMsgPing:
			// Touch the peer TTL — no relay needed.
			room.upsertPeer(peerID, nil)

		default:
			log.Printf("[peering/presence] unknown message type %q from peer %s", msg.Type, peerID)
		}
	}
}

// ─────────────────────────────────────────
// Handler: GET /api/peering/presence/{app_id}/peers
// ─────────────────────────────────────────

// handlePresencePeers returns the current ephemeral peer list for an app.
// Stale entries (beyond presenceTTL) are evicted before responding.
func (s *PresenceService) handlePresencePeers(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("app_id")
	if appID == "" {
		presenceWriteErr(w, http.StatusBadRequest, "missing app_id")
		return
	}

	room := s.room(appID)
	room.evictStale()
	peers := room.peerList()
	if peers == nil {
		peers = []peerEntry{}
	}
	presenceWriteJSON(w, map[string]any{
		"app_id": appID,
		"peers":  peers,
		"count":  len(peers),
	})
}

// ─────────────────────────────────────────
// Handler: POST /api/peering/inbound/presence (S2S relay inbound)
// ─────────────────────────────────────────

// HandleInboundPresence receives an ephemeral presence frame from a peer server
// (relay fallback path) and fans it out to all locally connected browser clients
// for the named app session.
//
// The method is exported so the orchestrator can register it on an
// InboundMiddleware-wrapped sub-mux without touching inbound.go, following the
// same pattern as CollabStore.HandleInboundCollabUpdate.
//
// InboundMiddleware has already verified the envelope signature and confirmed
// the sender is an approved contact. This handler reads the pre-verified
// *Envelope from the request context when present; otherwise it falls back to
// plain JSON (used by internal tests and direct calls).
//
// Presence frames are NEVER persisted — they are fanned out in-memory only.
func (s *PresenceService) HandleInboundPresence(w http.ResponseWriter, r *http.Request) {
	// Try the signed-envelope path first.
	if env, ok := r.Context().Value(EnvelopeKey).(*Envelope); ok && env != nil {
		s.handleInboundPresenceEnvelope(w, r, env)
		return
	}
	// Plain JSON fallback (tests / unauthenticated internal calls).
	s.handleInboundPresencePlain(w, r)
}

// handleInboundPresenceEnvelope handles the signed-envelope variant.
func (s *PresenceService) handleInboundPresenceEnvelope(w http.ResponseWriter, r *http.Request, env *Envelope) {
	var body presencePayload
	if err := json.Unmarshal(env.Payload, &body); err != nil {
		presenceWriteErr(w, http.StatusBadRequest, "invalid presence payload in envelope: "+err.Error())
		return
	}
	if body.AppID == "" {
		presenceWriteErr(w, http.StatusBadRequest, "app_id is required")
		return
	}

	// Use the envelope sender as the canonical peer ID.
	peerID := env.From
	s.fanOutPresence(body.AppID, peerID, body.Type, body.Data)

	log.Printf("[peering/presence] inbound S2S presence type=%s app=%s from=%s", body.Type, body.AppID, env.From)
	presenceWriteJSON(w, map[string]string{"status": "ok", "app_id": body.AppID})
}

// handleInboundPresencePlain handles the plain-JSON (no envelope) variant.
func (s *PresenceService) handleInboundPresencePlain(w http.ResponseWriter, r *http.Request) {
	var body presencePayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		presenceWriteErr(w, http.StatusBadRequest, "invalid presence frame: "+err.Error())
		return
	}
	if body.AppID == "" {
		presenceWriteErr(w, http.StatusBadRequest, "app_id is required")
		return
	}
	peerID := body.PeerID
	if peerID == "" {
		presenceWriteErr(w, http.StatusBadRequest, "peer_id is required")
		return
	}

	s.fanOutPresence(body.AppID, peerID, body.Type, body.Data)
	presenceWriteJSON(w, map[string]string{"status": "ok", "app_id": body.AppID})
}

// fanOutPresence updates in-memory state (ephemeral only) and broadcasts a
// presence frame to all locally connected WS clients for appID.
func (s *PresenceService) fanOutPresence(appID, peerID, msgType string, data json.RawMessage) {
	room := s.room(appID)

	if msgType == presenceMsgState {
		room.upsertPeer(peerID, data)
	}

	outFrame := s.buildFrame(peerID, msgType, appID, data)
	frameData, err := json.Marshal(outFrame)
	if err != nil {
		log.Printf("[peering/presence] marshal error during fanOut: %v", err)
		return
	}
	room.broadcast(nil, frameData) // nil origin = send to everyone
}

// ─────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────

// buildFrame constructs a ws.go Frame with channel="presence" wrapping a
// presencePayload.  Keeping frame construction in one place ensures consistent
// wire format across all code paths.
func (s *PresenceService) buildFrame(peerID, msgType, appID string, data json.RawMessage) Frame {
	p := presencePayload{
		Type:   msgType,
		AppID:  appID,
		PeerID: peerID,
		Data:   data,
	}
	payload, _ := json.Marshal(p)
	return Frame{
		Channel: ChannelPresence,
		From:    peerID,
		Payload: json.RawMessage(payload),
	}
}

func presenceWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func presenceWriteErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
