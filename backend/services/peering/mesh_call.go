// mesh_call.go — WebSocket signaling room for full-mesh group calls (PEER-26).
//
// # Architecture
//
//	Browser A → GET /api/peering/call/signal?room=<roomId>&peer=<peerId>
//	Browser B → GET /api/peering/call/signal?room=<roomId>&peer=<peerId>
//	Browser C → GET /api/peering/call/signal?room=<roomId>&peer=<peerId>
//
//	Each browser connects its own Vula server's signaling endpoint.
//	The server forms a room keyed by roomId and routes JSON frames between
//	participants.
//
// # Message routing
//
//	Frames with a "to" field → unicast to the named peer in the room.
//	Frames without a "to" field → broadcast to all other peers.
//
//	Message types (mirrors useMeshCall.js):
//	  { type: "join",      from, roomId }                         → broadcast
//	  { type: "offer",     from, to, roomId, sdp }               → unicast
//	  { type: "answer",    from, to, roomId, sdp }               → unicast
//	  { type: "candidate", from, to, roomId, candidate }         → unicast
//	  { type: "leave",     from, roomId }                        → broadcast
//	  { type: "bw",        from, roomId, upload_kbps, … }        → broadcast
//
// # Capacity
//
//	Maximum MESH_ROOM_MAX_PEERS (4) connections per room.  A 5th joiner
//	receives a single JSON error frame and the connection is closed.
//
// # Routes registered by RegisterMeshCallHandlers
//
//	GET /api/peering/call/signal   — WebSocket upgrade; query params: room, peer
package peering

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"vulos/backend/internal/wsutil"

	"github.com/gorilla/websocket"
)

// meshRoomMaxPeers is the maximum simultaneous participants in a mesh room.
// Above 4 the O(N²) RTCPeerConnection count becomes impractical for browsers.
const meshRoomMaxPeers = 4

// meshWriteWait is the deadline for a single WebSocket write.
const meshWriteWait = 10 * time.Second

// meshPongWait is how long we wait for a pong before considering the connection dead.
const meshPongWait = 60 * time.Second

// meshPingPeriod controls how often we send ping frames (must be < meshPongWait).
const meshPingPeriod = 50 * time.Second

// ─── Wire types ───────────────────────────────────────────────────────────────

// meshFrame is the generic signaling frame exchanged between browsers.
// The server does not interpret the payload beyond routing.
type meshFrame struct {
	Type   string `json:"type"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	RoomID string `json:"roomId,omitempty"`
	// The rest of the fields are arbitrary and forwarded verbatim.
	// We use json.RawMessage so we can re-serialise the whole frame unchanged.
	raw json.RawMessage // original bytes — used for forwarding
}

// meshGenericFrame is used for forwarding the raw frame bytes and for injecting
// error frames before closing.
type meshGenericFrame map[string]json.RawMessage

// ─── meshPeer ─────────────────────────────────────────────────────────────────

// meshPeer represents one connected browser in a room.
type meshPeer struct {
	id   string // Vula peer ID (from ?peer= query param)
	ws   *websocket.Conn
	send chan []byte
	done chan struct{}
}

// write delivers a raw JSON frame to this peer.  Non-blocking: frames are
// queued in the buffered send channel; full buffers are dropped (slow consumer).
func (p *meshPeer) write(data []byte) {
	select {
	case p.send <- data:
	default:
		log.Printf("[mesh] send buffer full for peer %s, dropping frame", p.id)
	}
}

// writePump serialises all writes; runs in its own goroutine.
func (p *meshPeer) writePump() {
	ticker := time.NewTicker(meshPingPeriod)
	defer func() {
		ticker.Stop()
		p.ws.Close()
	}()
	for {
		select {
		case msg, ok := <-p.send:
			p.ws.SetWriteDeadline(time.Now().Add(meshWriteWait))
			if !ok {
				p.ws.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := p.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			p.ws.SetWriteDeadline(time.Now().Add(meshWriteWait))
			if err := p.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-p.done:
			return
		}
	}
}

// ─── meshRoom ─────────────────────────────────────────────────────────────────

// meshRoom manages the set of peers in a single signaling room.
type meshRoom struct {
	mu    sync.RWMutex
	peers map[string]*meshPeer // peerId → peer
}

func newMeshRoom() *meshRoom {
	return &meshRoom{peers: make(map[string]*meshPeer)}
}

// add registers a peer.  Returns false if the room is at capacity.
func (r *meshRoom) add(p *meshPeer) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.peers) >= meshRoomMaxPeers {
		return false
	}
	r.peers[p.id] = p
	return true
}

// remove removes a peer and returns the peer that was removed (nil if absent).
func (r *meshRoom) remove(peerID string) *meshPeer {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.peers[peerID]
	delete(r.peers, peerID)
	return p
}

// size returns the current participant count.
func (r *meshRoom) size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.peers)
}

// get returns the named peer, or nil.
func (r *meshRoom) get(peerID string) *meshPeer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.peers[peerID]
}

// broadcast sends data to all peers except exclude (nil to send to all).
func (r *meshRoom) broadcast(exclude *meshPeer, data []byte) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.peers {
		if exclude != nil && p.id == exclude.id {
			continue
		}
		p.write(data)
	}
}

// unicast delivers data to a single named peer.
func (r *meshRoom) unicast(toID string, data []byte) {
	p := r.get(toID)
	if p != nil {
		p.write(data)
	}
}

// ─── MeshSignalingHub ─────────────────────────────────────────────────────────

// MeshSignalingHub manages all mesh signaling rooms.
// It is safe for concurrent use.
type MeshSignalingHub struct {
	mu    sync.RWMutex
	rooms map[string]*meshRoom // roomId → room
}

// NewMeshSignalingHub creates an empty hub.
func NewMeshSignalingHub() *MeshSignalingHub {
	return &MeshSignalingHub{rooms: make(map[string]*meshRoom)}
}

// room returns (or lazily creates) the room for roomID.
func (h *MeshSignalingHub) room(roomID string) *meshRoom {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r, ok := h.rooms[roomID]; ok {
		return r
	}
	r := newMeshRoom()
	h.rooms[roomID] = r
	return r
}

// evictIfEmpty removes a room from the registry if it has no remaining peers.
func (h *MeshSignalingHub) evictIfEmpty(roomID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.rooms[roomID]
	if ok && r.size() == 0 {
		delete(h.rooms, roomID)
	}
}

// RegisterMeshCallHandlers wires the mesh signaling WebSocket route onto mux.
//
//	GET /api/peering/call/signal   — WebSocket upgrade for mesh signaling
//	                                 query params: room (required), peer (required)
func RegisterMeshCallHandlers(mux *http.ServeMux, hub *MeshSignalingHub) {
	mux.HandleFunc("GET /api/peering/call/signal", hub.handleMeshSignal)
}

// handleMeshSignal upgrades the connection and runs the signaling pump for one peer.
func (h *MeshSignalingHub) handleMeshSignal(w http.ResponseWriter, r *http.Request) {
	roomID := r.URL.Query().Get("room")
	peerID := r.URL.Query().Get("peer")
	if roomID == "" || peerID == "" {
		http.Error(w, `{"error":"room and peer query params are required"}`, http.StatusBadRequest)
		return
	}

	ws, err := wsutil.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[mesh] upgrade: %v", err)
		return
	}

	room := h.room(roomID)

	peer := &meshPeer{
		id:   peerID,
		ws:   ws,
		send: make(chan []byte, 256),
		done: make(chan struct{}),
	}

	if !room.add(peer) {
		// Room full — send error and close.
		errMsg, _ := json.Marshal(map[string]string{
			"type":  "error",
			"error": "room is full (max 4 participants)",
		})
		ws.SetWriteDeadline(time.Now().Add(meshWriteWait))
		ws.WriteMessage(websocket.TextMessage, errMsg) //nolint:errcheck
		ws.Close()
		log.Printf("[mesh] room %s is full, rejected peer %s", roomID, peerID)
		return
	}

	log.Printf("[mesh] peer %s joined room %s (size=%d)", peerID, roomID, room.size())

	go peer.writePump()

	// Read pump — handle inbound frames and route them.
	ws.SetReadLimit(64 * 1024)
	ws.SetReadDeadline(time.Now().Add(meshPongWait))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(meshPongWait))
		return nil
	})

	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}

		// Parse just enough to route (type, from, to).
		var f struct {
			Type string `json:"type"`
			From string `json:"from"`
			To   string `json:"to"`
		}
		if jsonErr := json.Unmarshal(msg, &f); jsonErr != nil {
			continue
		}

		// Stamp the from field with the authenticated peer ID so a misbehaving
		// client cannot spoof another participant.
		if f.From != peerID {
			var raw meshGenericFrame
			if jsonErr := json.Unmarshal(msg, &raw); jsonErr == nil {
				fromJSON, _ := json.Marshal(peerID)
				raw["from"] = json.RawMessage(fromJSON)
				if stamped, mErr := json.Marshal(raw); mErr == nil {
					msg = stamped
				}
			}
		}

		switch f.Type {
		case "join", "leave", "bw":
			// Broadcast to all other room members.
			room.broadcast(peer, msg)

		case "offer", "answer", "candidate":
			// Unicast to the named recipient.
			if f.To != "" {
				room.unicast(f.To, msg)
			}

		default:
			// Unknown frame type — broadcast to everyone else.
			room.broadcast(peer, msg)
		}
	}

	// Peer disconnected — clean up.
	room.remove(peerID)
	close(peer.done)
	close(peer.send)
	log.Printf("[mesh] peer %s left room %s (size=%d)", peerID, roomID, room.size())

	// Synthesise a leave frame so remaining peers remove this participant.
	leaveMsg, _ := json.Marshal(map[string]any{
		"type":   "leave",
		"from":   peerID,
		"roomId": roomID,
	})
	room.broadcast(nil, leaveMsg)

	h.evictIfEmpty(roomID)
}
