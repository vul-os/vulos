package telephony

import (
	"net/http"
	"strconv"
	"strings"
	"sync"

	"vulos/backend/internal/wsutil"

	"github.com/gorilla/websocket"
)

// wsHub fans telephony events (inbound SMS, call state) out to the phone app's
// open WebSocket clients. Each connection has its own write mutex — gorilla
// forbids concurrent writes to one conn — so this stays correct even if a second
// event source is added later.
type wsHub struct {
	mu    sync.Mutex
	conns map[*websocket.Conn]*sync.Mutex
}

func newWSHub() *wsHub { return &wsHub{conns: map[*websocket.Conn]*sync.Mutex{}} }

func (h *wsHub) add(c *websocket.Conn) {
	h.mu.Lock()
	h.conns[c] = &sync.Mutex{}
	h.mu.Unlock()
}

func (h *wsHub) remove(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
	_ = c.Close()
}

func (h *wsHub) broadcast(v any) {
	h.mu.Lock()
	type target struct {
		c  *websocket.Conn
		wm *sync.Mutex
	}
	targets := make([]target, 0, len(h.conns))
	for c, wm := range h.conns {
		targets = append(targets, target{c, wm})
	}
	h.mu.Unlock()
	for _, t := range targets {
		t.wm.Lock()
		err := t.c.WriteJSON(v)
		t.wm.Unlock()
		if err != nil {
			h.remove(t.c)
		}
	}
}

// serveWS upgrades a client (assumed already authorized by the caller) and holds
// it open, reading only to detect close. Events are pushed via broadcast().
func (h *wsHub) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := wsutil.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.add(conn)
	defer h.remove(conn)
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// serveWS on the Service gates the live SMS feed to the box owner (the SIM is the
// owner's line) before upgrading — so another account on a multi-user box can't
// open a socket and watch inbound OTPs.
func (s *Service) serveWS(w http.ResponseWriter, r *http.Request) {
	if !s.isOwner(r.Header.Get("X-User-ID")) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	s.hub.serveWS(w, r)
}

// ─── small helpers ────────────────────────────────────────────────────────────

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
