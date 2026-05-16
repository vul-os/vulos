// Package telephony exposes a Vulos telephony service backed by ModemManager.
// It provides:
//   - GET  /api/telephony/status  — lists all modems (empty when none present)
//   - GET  /api/telephony/ws      — WebSocket hub for real-time modem events
//
// The service is intentionally free of SMS/voice logic (those are follow-up
// tasks). New() and RegisterHandlers() are the orchestrator's entry points;
// main.go wiring is handled separately.
package telephony

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"vulos/backend/internal/wsutil"
)

// StatusResponse is the JSON body for GET /api/telephony/status.
type StatusResponse struct {
	Modems    []Modem `json:"modems"`    // empty slice, never null
	DBusOK    bool    `json:"dbus_ok"`   // false when ModemManager is unreachable
	Timestamp int64   `json:"timestamp"` // unix seconds
}

// Event is a JSON message pushed to WebSocket clients.
type Event struct {
	Type string      `json:"type"` // "modems_updated", "ping"
	Data interface{} `json:"data"`
}

// Service is the telephony service instance.
type Service struct {
	mm      *mmClient
	mu      sync.RWMutex
	clients map[*websocket.Conn]bool
}

// New creates and initialises the telephony service. It probes ModemManager
// over D-Bus; if the bus is absent the service starts in no-modem mode and
// every status call returns an empty modem list without crashing.
func New() *Service {
	return &Service{
		mm:      newMMClient(),
		clients: make(map[*websocket.Conn]bool),
	}
}

// RegisterHandlers mounts the telephony endpoints onto mux. The orchestrator
// (main.go) calls this after creating the service.
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/api/telephony/status", s.handleStatus)
	mux.HandleFunc("/api/telephony/ws", s.handleWS)
	log.Printf("[telephony] registered /api/telephony/status and /api/telephony/ws")
}

// Start launches background polling of modem state and pushes updates to all
// connected WebSocket clients. It runs until ctx is cancelled.
func (s *Service) Start() {
	go s.pollLoop()
}

// pollLoop periodically fetches modem state and broadcasts to WS clients.
func (s *Service) pollLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		modems := s.mm.ListModems()
		ev := Event{Type: "modems_updated", Data: modems}
		data, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		s.broadcast(data)
	}
}

// broadcast sends a message to every connected WebSocket client, pruning dead
// connections silently.
func (s *Service) broadcast(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
			c.Close()
			delete(s.clients, c)
		}
	}
}

// handleStatus serves GET /api/telephony/status.
// Always returns 200 — an empty modem list is valid (no modem present).
func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	modems := s.mm.ListModems()
	resp := StatusResponse{
		Modems:    modems,
		DBusOK:    s.mm.available,
		Timestamp: time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("[telephony] status encode: %v", err)
	}
}

// handleWS upgrades to WebSocket and keeps the connection open, registering
// it as a subscriber for modem events.
func (s *Service) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := wsutil.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[telephony] ws upgrade: %v", err)
		return
	}
	defer ws.Close()

	// Send current modem state immediately on connect.
	modems := s.mm.ListModems()
	initial := Event{Type: "modems_updated", Data: modems}
	if data, err := json.Marshal(initial); err == nil {
		ws.WriteMessage(websocket.TextMessage, data)
	}

	s.mu.Lock()
	s.clients[ws] = true
	s.mu.Unlock()
	log.Printf("[telephony] ws client connected (total %d)", s.clientCount())

	// Keep connection alive; read loop drains pings/pongs from the client.
	ws.SetReadDeadline(time.Time{}) // no deadline — server-push pattern
	for {
		_, _, err := ws.ReadMessage()
		if err != nil {
			break
		}
	}

	s.mu.Lock()
	delete(s.clients, ws)
	s.mu.Unlock()
	log.Printf("[telephony] ws client disconnected (total %d)", s.clientCount())
}

// clientCount returns the current number of connected WS clients (caller must
// NOT hold the lock).
func (s *Service) clientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}
