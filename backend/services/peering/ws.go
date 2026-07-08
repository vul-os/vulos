// Package peering implements the real-time WebSocket multiplex channel that
// connects the browser to its own Vula server.  All real-time traffic that
// is not time-critical (messages, presence, collab events, server-push
// notifications and signaling hints) flows through a single connection that
// is tagged by a "channel" discriminator so many logical sub-streams can
// coexist without opening multiple WebSocket connections.
//
// Frame envelope
//
//	{ "channel": "message|signal|collab|notification|presence",
//	  "from":    "<userID>",   // set by the server when pushing
//	  "payload": <any JSON>  }
//
// Server-side usage
//
//	hub := peering.NewHub()
//	hub.RegisterHandlers(mux)            // wires GET /api/peering/stream
//	hub.Push(userID, peering.Frame{...}) // deliver to all of a user's tabs
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

// Channel discriminator values understood by the client hook.
const (
	ChannelMessage      = "message"
	ChannelSignal       = "signal"
	ChannelCollab       = "collab"
	ChannelNotification = "notification"
	ChannelPresence     = "presence"
)

// Frame is the envelope that wraps every multiplexed message.
type Frame struct {
	Channel string          `json:"channel"`
	From    string          `json:"from,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

// conn wraps a single WebSocket connection with a buffered send queue so
// that slow readers do not block the Hub.
type conn struct {
	ws     *websocket.Conn
	send   chan []byte
	userID string
	done   chan struct{}
	// closeDone guards done so it is closed at most once, even when both the
	// normal unregister path and Hub.Shutdown race to tear the connection down.
	closeDone sync.Once
}

// signalDone closes c.done exactly once, unblocking writePump. Idempotent and
// concurrency-safe.
func (c *conn) signalDone() {
	c.closeDone.Do(func() { close(c.done) })
}

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = 50 * time.Second // must be < pongWait
	maxMessageSize = 64 * 1024        // 64 KB inbound cap
	sendBufSize    = 256              // frames buffered per connection
)

// writePump serialises all sends; runs in its own goroutine.
func (c *conn) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.ws.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.ws.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

// Hub owns the per-user connection registry and fan-out logic.
type Hub struct {
	mu    sync.RWMutex
	users map[string]map[*conn]bool // userID → set of open connections
}

// NewHub creates an empty Hub ready for use.
func NewHub() *Hub {
	return &Hub{
		users: make(map[string]map[*conn]bool),
	}
}

// register adds a connection to the Hub under the given userID.
func (h *Hub) register(c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.users[c.userID] == nil {
		h.users[c.userID] = make(map[*conn]bool)
	}
	h.users[c.userID][c] = true
}

// unregister removes a connection from the Hub.
func (h *Hub) unregister(c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if conns, ok := h.users[c.userID]; ok {
		delete(conns, c)
		if len(conns) == 0 {
			delete(h.users, c.userID)
		}
	}
	c.signalDone()
}

// Push encodes frame and delivers it to every open connection for userID.
// It is safe to call from any goroutine.  Connections with a full send buffer
// are dropped (slow consumer protection).
func (h *Hub) Push(userID string, frame Frame) {
	data, err := json.Marshal(frame)
	if err != nil {
		log.Printf("[peering] Push marshal error: %v", err)
		return
	}

	h.mu.RLock()
	conns := h.users[userID]
	h.mu.RUnlock()

	for c := range conns {
		select {
		case c.send <- data:
		default:
			// Buffer full — drop and let the connection die on its own.
			log.Printf("[peering] send buffer full for user %s, dropping frame", userID)
		}
	}
}

// Broadcast delivers frame to every connected user.
func (h *Hub) Broadcast(frame Frame) {
	data, err := json.Marshal(frame)
	if err != nil {
		log.Printf("[peering] Broadcast marshal error: %v", err)
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, conns := range h.users {
		for c := range conns {
			select {
			case c.send <- data:
			default:
			}
		}
	}
}

// ConnectedUsers returns the list of userIDs with at least one open connection.
func (h *Hub) ConnectedUsers() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.users))
	for id := range h.users {
		ids = append(ids, id)
	}
	return ids
}

// Shutdown force-drains every live peering WebSocket. Hijacked WS conns are not
// tracked by http.Server.Shutdown (universal Go behavior), so without this their
// readPump goroutines (blocked in ws.ReadMessage) would linger past shutdown.
//
// For each open connection we send a close frame via its send channel (picked up
// by writePump), signal writePump to stop via c.done, and close the underlying
// ws — which unblocks readPump's ReadMessage so it returns, breaks the read loop,
// and lets streamHandler unregister and return. We snapshot the connection set
// under the lock and act outside it so a slow write cannot stall shutdown.
//
// Safe to call multiple times and concurrently with normal unregister: c.done is
// closed at most once here (guarded by the users-map membership snapshot), and
// unregister's own close(c.done) only runs for conns still registered — a conn we
// drain here is removed from the map first, so the two paths never double-close.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	var conns []*conn
	for uid, set := range h.users {
		for c := range set {
			conns = append(conns, c)
		}
		delete(h.users, uid)
	}
	h.mu.Unlock()

	closeFrame := websocket.FormatCloseMessage(websocket.CloseServiceRestart, "server shutting down")
	for _, c := range conns {
		// Best-effort close frame through the serialized writePump.
		select {
		case c.send <- closeFrame:
		default:
		}
		// Stop writePump and unblock readPump.
		c.signalDone()
		_ = c.ws.Close()
	}
}

// RegisterHandlers wires the peering endpoints into mux.
//
//	GET /api/peering/stream  — WebSocket upgrade; requires X-User-ID header
//	                           (set by the auth middleware in front of the mux)
func (h *Hub) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/peering/stream", h.streamHandler)
}

// streamHandler upgrades to WebSocket and runs the read/write pumps.
func (h *Hub) streamHandler(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	ws, err := wsutil.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[peering] upgrade: %v", err)
		return
	}

	c := &conn{
		ws:     ws,
		send:   make(chan []byte, sendBufSize),
		userID: userID,
		done:   make(chan struct{}),
	}

	h.register(c)
	log.Printf("[peering] user %s connected (%d total connections)", userID, h.connectionCount(userID))

	// Deliver a welcome presence frame so the client knows it's live.
	welcome, _ := json.Marshal(map[string]any{"status": "connected", "user_id": userID})
	c.send <- mustMarshal(Frame{Channel: ChannelPresence, From: "server", Payload: welcome})

	go c.writePump()

	// readPump — keeps the connection alive and processes inbound frames.
	// Inbound frames from the browser are currently echoed back on the same
	// channel (useful for collab / signal relay between the user's own tabs).
	ws.SetReadLimit(maxMessageSize)
	ws.SetReadDeadline(time.Now().Add(pongWait))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}
		// Parse inbound frame and relay it back to the same user's other tabs.
		var f Frame
		if jsonErr := json.Unmarshal(msg, &f); jsonErr == nil && f.Channel != "" {
			f.From = userID
			h.Push(userID, f)
		}
	}

	h.unregister(c)
	log.Printf("[peering] user %s disconnected", userID)
}

// connectionCount returns the number of open connections for a user (must be
// called with at least an RLock held, or while the lock is not needed).
func (h *Hub) connectionCount(userID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.users[userID])
}

// mustMarshal encodes v and panics if it fails (only used for static data).
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
