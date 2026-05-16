package notify

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"vulos/backend/internal/wsutil"
)

// Level is the notification urgency (legacy, kept for backward compat).
type Level string

const (
	LevelInfo    Level = "info"
	LevelWarning Level = "warning"
	LevelUrgent  Level = "urgent"
)

// Type is the notification category per the NOTIFICATIONS spec.
type Type string

const (
	TypePresence Type = "presence"
	TypeEvent    Type = "event"
	TypeCall     Type = "call"
	TypeAlert    Type = "alert"
	TypeAction   Type = "action"
)

// Priority is the delivery urgency per the NOTIFICATIONS spec.
type Priority string

const (
	PriorityLow      Priority = "low"
	PriorityNormal   Priority = "normal"
	PriorityHigh     Priority = "high"
	PriorityCritical Priority = "critical"
)

// levelToPriority maps legacy Level values to the new Priority enum.
func levelToPriority(l Level) Priority {
	switch l {
	case LevelUrgent:
		return PriorityHigh
	case LevelWarning:
		return PriorityNormal
	default: // LevelInfo and anything unrecognised
		return PriorityNormal
	}
}

// newUUIDv7 generates a UUIDv7 string (time-ordered).
// The google/uuid package v1.6+ supports UUIDv7.
func newUUIDv7() string {
	id, err := uuid.NewV7()
	if err != nil {
		// Fallback: use a v4 UUID on the rare chance the clock is broken.
		return uuid.New().String()
	}
	return id.String()
}

// Notification is a single alert.
type Notification struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Body      any       `json:"body"`
	Level     Level     `json:"level"`
	Source    string    `json:"source"`           // "system", "ai", app ID
	Action    string    `json:"action,omitempty"` // URL or action ID (legacy)
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`

	// Structured fields (NOTIF-01)
	Type     Type     `json:"type"`
	Subtype  string   `json:"subtype"`
	Priority Priority `json:"priority"`
	TTL      int      `json:"ttl"` // seconds; 0 = no expiry
}

// IsExpired reports whether the notification has passed its TTL.
// Notifications with TTL == 0 never expire.
func (n *Notification) IsExpired() bool {
	if n.TTL == 0 {
		return false
	}
	return time.Now().After(n.CreatedAt.Add(time.Duration(n.TTL) * time.Second))
}

// clampPriority enforces the rule from the spec: only call.incoming may carry
// "critical"; any other type is silently downgraded to "high".
func clampPriority(notifType Type, p Priority) Priority {
	if p == PriorityCritical && notifType != TypeCall {
		return PriorityHigh
	}
	return p
}

// fillDefaults sets sensible defaults on a Notification before storing it.
func fillDefaults(n *Notification) {
	if n.ID == "" {
		n.ID = newUUIDv7()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now()
	}
	if n.Priority == "" {
		n.Priority = PriorityNormal
	}
	if n.Type == "" {
		n.Type = TypeAlert
	}
	if n.Subtype == "" {
		n.Subtype = "system"
	}
	// Enforce critical-only-for-calls rule.
	n.Priority = clampPriority(n.Type, n.Priority)
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

// SendNotification stores and broadcasts a fully-specified Notification.
// Missing fields (ID, CreatedAt, Priority, Type, Subtype) are filled with
// defaults before the notification is stored.
func (s *Service) SendNotification(n Notification) *Notification {
	fillDefaults(&n)

	s.mu.Lock()
	s.history = append(s.history, n)
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

	log.Printf("[notify] %s (%s/%s): %s", n.Priority, n.Type, n.Subtype, n.Title)
	return &n
}

// SendWithAction creates and broadcasts a notification with a clickable action URL.
// This is a thin legacy wrapper around SendNotification; callers are unchanged.
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
	// Re-broadcast with action field.
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
// This is a thin legacy wrapper; callers compile unchanged and always
// produce priority=normal (or high for LevelUrgent).
func (s *Service) Send(title, body string, level Level, source string) *Notification {
	return s.SendNotification(Notification{
		Title:    title,
		Body:     body,
		Level:    level,
		Source:   source,
		Priority: levelToPriority(level),
	})
}

// List returns notification history, newest first.
func (s *Service) List(limit int) []Notification {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.history) {
		limit = len(s.history)
	}
	// Return in reverse (newest first).
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
		if !n.Read {
			count++
		}
	}
	return count
}

// Clear removes all notifications.
func (s *Service) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = nil
}

// NotifyOnConflict emits a toast-level notification for a sync conflict file.
// Category "sync" is encoded in the Source field so the frontend can route it
// to the ConflictResolver view. The deep-link payload is the relative conflict path.
func (s *Service) NotifyOnConflict(path string) *Notification {
	return s.SendWithAction(
		"Sync conflict detected",
		"A conflict copy was created: "+path,
		LevelWarning,
		"sync",
		"/api/sync/conflicts",
	)
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

		// Block until disconnect.
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
