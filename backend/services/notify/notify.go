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

	// UserID is the OPTIONAL target user this notification is private to
	// (NOTIF-USER-SCOPE). Empty ⇒ a box-level/system notification, delivered and
	// listed to every connected client as before (DND toggles, sync conflicts,
	// host-browser-open, etc.). Non-empty ⇒ the notification carries a specific
	// user's private content (e.g. a fired reminder's text) and MUST only ever be
	// delivered to, and listed for, that same user — never leaked to another
	// account sharing the same multi-user box. Enforced by deliverableTo + the
	// per-connection user id the WS Handler records, and by ListForUser.
	UserID string `json:"user_id,omitempty"`
}

// deliverableTo reports whether a notification may be shown to the client
// authenticated as clientUID. A system/box notification (empty UserID) is shown
// to everyone; a user-private notification is shown ONLY to its target user, so
// one account's private content (a reminder's text) never leaks to another
// account on the same box.
func deliverableTo(n Notification, clientUID string) bool {
	return n.UserID == "" || n.UserID == clientUID
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

// notifyWriteWait bounds a single broadcast write to one client so a slow or
// dead reader cannot block a sender indefinitely.
const notifyWriteWait = 10 * time.Second

// wsClient is one live notification stream. wmu SERIALIZES writes to conn:
// gorilla/websocket permits only ONE concurrent writer per connection, so every
// broadcast must hold wmu while writing. Without it, two goroutines emitting to
// the same client at once (e.g. an HTTP handler and the reminder scheduler)
// panic with "concurrent write to websocket connection" — crashing the backend.
type wsClient struct {
	uid string
	wmu sync.Mutex
}

// Service manages notifications and streams them to connected clients.
type Service struct {
	mu      sync.RWMutex
	history []Notification
	// clients maps a live WS connection to the user id it authenticated as
	// (NOTIF-USER-SCOPE). The recorded id is used to route user-private
	// notifications to their owner only; an empty id (unauthenticated stream)
	// receives system/box notifications only.
	clients map[*websocket.Conn]*wsClient
	maxHist int

	// NOTIF-02 additive fields. Both are optional; the in-memory history
	// path is fully functional with either left nil.
	store *Store    // persistent on-disk store (set via SetStore); nil = memory-only
	dnd   *dndState // do-not-disturb state holder (lazily created by DND())

	// PUSH-CELL-01 additive field: the cell-side Web Push send-path. nil = no
	// Web Push (in-app WebSocket path unchanged). Set via SetPush.
	push *pushBinding
}

func New() *Service {
	return &Service{
		clients: make(map[*websocket.Conn]*wsClient),
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
	s.mu.Unlock()

	// Broadcast only to connections ALLOWED to see this notification: a
	// user-private notification (non-empty UserID) goes solely to its target
	// user's connections, never to another account on the same box.
	data, _ := json.Marshal(n)
	s.broadcast(data, func(uid string) bool { return deliverableTo(n, uid) })

	// NOTIF-02: additively persist to the on-disk store when configured.
	// The in-memory history path above is unchanged; this is nil-safe and
	// a no-op when no store has been attached via SetStore.
	if s.store != nil {
		s.store.Append(&n)
	}

	// PUSH-CELL-01: additively fan a Web Push out to the owner's browser
	// subscriptions (direct-to-vendor, E2E-encrypted). No-op unless SetPush has
	// attached a live binding; only owner-targeted notifications are pushed and
	// DND/prefs are respected inside. Runs async — never blocks this caller.
	s.maybeWebPush(n)

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
	// Re-broadcast with action field (respecting the same per-user routing).
	n.Action = action
	data, _ := json.Marshal(*n)
	s.broadcast(data, func(uid string) bool { return deliverableTo(*n, uid) })
	return n
}

// broadcast writes data as a text frame to every connected client the deliver
// predicate accepts. It snapshots the client set under the read lock, then
// writes OUTSIDE the lock (so a slow/dead client cannot stall registration,
// disconnect cleanup, or other senders) and serializes each connection's write
// with that connection's own wmu, bounded by a write deadline. deliver==nil
// delivers to every client.
func (s *Service) broadcast(data []byte, deliver func(uid string) bool) {
	type target struct {
		conn *websocket.Conn
		cl   *wsClient
	}
	s.mu.RLock()
	targets := make([]target, 0, len(s.clients))
	for c, cl := range s.clients {
		if deliver == nil || deliver(cl.uid) {
			targets = append(targets, target{c, cl})
		}
	}
	s.mu.RUnlock()

	for _, t := range targets {
		t.cl.wmu.Lock()
		_ = t.conn.SetWriteDeadline(time.Now().Add(notifyWriteWait))
		_ = t.conn.WriteMessage(websocket.TextMessage, data)
		t.cl.wmu.Unlock()
	}
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

// List returns notification history, newest first. It is UNSCOPED and must NOT
// be used to answer a per-user HTTP request (it would expose other accounts'
// private notifications). HTTP surfaces use ListForUser; List remains for
// box-level/internal callers and tests.
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

// ListForUser returns notification history VISIBLE to userID, newest first:
// box-level (untargeted) notifications plus this user's own private ones. It
// never returns another account's user-private notifications, so the per-user
// isolation the reminders store enforces is preserved all the way to the shell.
func (s *Service) ListForUser(userID string, limit int) []Notification {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Notification, 0, len(s.history))
	// Walk newest→oldest, keeping only visible entries up to limit.
	for i := len(s.history) - 1; i >= 0; i-- {
		if limit > 0 && len(result) >= limit {
			break
		}
		if deliverableTo(s.history[i], userID) {
			result = append(result, s.history[i])
		}
	}
	return result
}

// UnreadForUser counts unread notifications VISIBLE to userID (box-level plus
// this user's own private ones) — the scoped counterpart of UnreadCount, so the
// unread badge does not reveal another account's activity.
func (s *Service) UnreadForUser(userID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, n := range s.history {
		if !n.Read && deliverableTo(n, userID) {
			count++
		}
	}
	return count
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

// Shutdown force-drains every live notification WebSocket. Hijacked WS conns are
// NOT tracked by http.Server.Shutdown (universal Go behavior: Shutdown only waits
// on non-hijacked connections), so their blocking ReadMessage loops — and the
// reader goroutines running them — would otherwise linger past server shutdown.
//
// We send each connection a proper WebSocket close frame (best-effort, deadline-
// bounded) and then close the underlying conn. Closing unblocks the goroutine's
// ReadMessage, which returns an error, breaks the read loop, and lets the handler
// unregister the client and return — draining the goroutine.
//
// Safe to call multiple times: after the first call the client set is emptied, so
// subsequent calls are no-ops. The per-connection Handler still performs its own
// delete(s.clients, ws) on exit, which is idempotent.
func (s *Service) Shutdown() {
	s.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(s.clients))
	clients := make([]*wsClient, 0, len(s.clients))
	for c, cl := range s.clients {
		conns = append(conns, c)
		clients = append(clients, cl)
		// Deregister here rather than waiting for each Handler goroutine to do
		// it on its way out: those run asynchronously, so leaving it to them
		// makes "the set is empty once Shutdown returns" a race.
		delete(s.clients, c)
	}
	s.mu.Unlock()

	deadline := time.Now().Add(notifyWriteWait)
	closeFrame := websocket.FormatCloseMessage(websocket.CloseServiceRestart, "server shutting down")
	for i, c := range conns {
		// Serialize the close-frame write with any concurrent broadcast writer via
		// the per-connection wmu (gorilla permits only one writer at a time).
		clients[i].wmu.Lock()
		_ = c.SetWriteDeadline(deadline)
		_ = c.WriteMessage(websocket.CloseMessage, closeFrame)
		clients[i].wmu.Unlock()
		// Close the conn to unblock the reader goroutine's ReadMessage.
		_ = c.Close()
	}
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

		// Record the user this stream authenticated as (set by the auth
		// Middleware that wraps every route). User-private notifications are then
		// routed to this connection only when its id matches — a different account
		// on the same box never receives another user's private notification.
		// An empty id (e.g. an unauthenticated/system stream) sees box-level
		// notifications only.
		uid := r.Header.Get("X-User-ID")

		s.mu.Lock()
		s.clients[ws] = &wsClient{uid: uid}
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
