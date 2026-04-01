package notify

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"vulos/backend/internal/wsutil"
)

// Level is the notification urgency.
type Level string

const (
	LevelInfo    Level = "info"
	LevelWarning Level = "warning"
	LevelUrgent  Level = "urgent"
)

// Notification is a single alert.
type Notification struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Level     Level     `json:"level"`
	Source    string    `json:"source"`  // "system", "ai", app ID
	Action   string    `json:"action,omitempty"` // URL or action ID
	Read     bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

// Service manages notifications and streams them to connected clients.
type Service struct {
	mu      sync.RWMutex
	history []Notification
	clients map[*websocket.Conn]bool
	maxHist int
}

func New() *Service {
	return &Service{
		clients: make(map[*websocket.Conn]bool),
		maxHist: 200,
	}
}

// SendWithAction creates and broadcasts a notification with a clickable action URL.
func (s *Service) SendWithAction(title, body string, level Level, source, action string) *Notification {
	n := s.Send(title, body, level, source)
	s.mu.Lock()
	for i := range s.history {
		if s.history[i].ID == n.ID {
			s.history[i].Action = action
			break
		}
	}
	s.mu.Unlock()
	// Re-broadcast with action field
	n.Action = action
	data, _ := json.Marshal(n)
	s.mu.RLock()
	for c := range s.clients {
		c.WriteMessage(websocket.TextMessage, data)
	}
	s.mu.RUnlock()
	return n
}

// Send creates and broadcasts a notification.
func (s *Service) Send(title, body string, level Level, source string) *Notification {
	n := &Notification{
		ID:        time.Now().Format("20060102150405.000000"),
		Title:     title,
		Body:      body,
		Level:     level,
		Source:    source,
		CreatedAt: time.Now(),
	}

	s.mu.Lock()
	s.history = append(s.history, *n)
	if len(s.history) > s.maxHist {
		s.history = s.history[len(s.history)-s.maxHist:]
	}
	clients := make([]*websocket.Conn, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	data, _ := json.Marshal(n)
	for _, c := range clients {
		c.WriteMessage(websocket.TextMessage, data)
	}

	log.Printf("[notify] %s: %s — %s", level, title, body)
	return n
}

// List returns notification history, newest first.
func (s *Service) List(limit int) []Notification {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.history) {
		limit = len(s.history)
	}
	// Return in reverse (newest first)
	result := make([]Notification, limit)
	for i := 0; i < limit; i++ {
		result[i] = s.history[len(s.history)-1-i]
	}
	return result
}

// MarkRead marks a notification as read.
func (s *Service) MarkRead(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.history {
		if s.history[i].ID == id {
			s.history[i].Read = true
			return
		}
	}
}

// MarkAllRead marks all notifications as read.
func (s *Service) MarkAllRead() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.history {
		s.history[i].Read = true
	}
}

// UnreadCount returns the number of unread notifications.
func (s *Service) UnreadCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, n := range s.history {
		if !n.Read { count++ }
	}
	return count
}

// Clear removes all notifications.
func (s *Service) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = nil
}

// Handler returns an HTTP handler that upgrades to WebSocket for live notification streaming.
// Connect via: ws://host:port/api/notifications/stream
func (s *Service) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws, err := wsutil.Upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[notify] websocket upgrade: %v", err)
			return
		}
		defer ws.Close()

		s.mu.Lock()
		s.clients[ws] = true
		s.mu.Unlock()

		log.Printf("[notify] client connected")

		// Block until disconnect
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				break
			}
		}

		s.mu.Lock()
		delete(s.clients, ws)
		s.mu.Unlock()
		log.Printf("[notify] client disconnected")
	}
}
