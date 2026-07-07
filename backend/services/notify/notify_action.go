// Package notify — NOTIF-06: Inline action registry + dispatch.
//
// Actions are named handlers registered by subsystems. A notification can
// carry a list of ActionSpec buttons (serialised in the JSON envelope), and
// the client POSTs /api/notifications/{id}/action?action_id=X to invoke one.
// Pure additive — no changes to notify.go.
package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ActionSpec describes a button that the notification UI should render.
type ActionSpec struct {
	ID    string `json:"id"`    // unique within the notification
	Label string `json:"label"` // display text
	Style string `json:"style"` // "default"|"primary"|"danger"|"ghost"
}

// ActionFunc is the handler invoked when a user clicks an action button.
// It receives the notification ID and action ID.
type ActionFunc func(notifID, actionID string) error

// ActionNotification is a Notification variant enriched with action specs.
// It embeds the base Notification so it is wire-compatible.
type ActionNotification struct {
	Notification
	Actions []ActionSpec `json:"actions,omitempty"`
}

// ActionRegistry maps action IDs to handlers and maintains a log of dispatches.
type ActionRegistry struct {
	mu       sync.RWMutex
	handlers map[string]ActionFunc
	log      []dispatchRecord
	maxLog   int
}

type dispatchRecord struct {
	NotifID  string    `json:"notif_id"`
	ActionID string    `json:"action_id"`
	At       time.Time `json:"at"`
	Err      string    `json:"error,omitempty"`
}

var ErrActionNotFound = errors.New("action not found")

// NewActionRegistry creates a registry with a 500-entry dispatch log.
func NewActionRegistry() *ActionRegistry {
	return &ActionRegistry{
		handlers: make(map[string]ActionFunc),
		maxLog:   500,
	}
}

// Register binds actionID → handler. Safe to call at any time; last-write wins.
func (r *ActionRegistry) Register(actionID string, fn ActionFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[actionID] = fn
}

// Unregister removes a handler.
func (r *ActionRegistry) Unregister(actionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.handlers, actionID)
}

// Dispatch invokes the handler for actionID and records the outcome.
func (r *ActionRegistry) Dispatch(notifID, actionID string) error {
	r.mu.RLock()
	fn, ok := r.handlers[actionID]
	r.mu.RUnlock()

	var err error
	if !ok {
		err = fmt.Errorf("%w: %q", ErrActionNotFound, actionID)
	} else {
		err = fn(notifID, actionID)
	}

	rec := dispatchRecord{
		NotifID:  notifID,
		ActionID: actionID,
		At:       time.Now(),
	}
	if err != nil {
		rec.Err = err.Error()
	}

	r.mu.Lock()
	r.log = append(r.log, rec)
	if len(r.log) > r.maxLog {
		r.log = r.log[len(r.log)-r.maxLog:]
	}
	r.mu.Unlock()

	return err
}

// Log returns the dispatch history (newest first).
func (r *ActionRegistry) Log(limit int) []dispatchRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if limit <= 0 || limit > len(r.log) {
		limit = len(r.log)
	}
	out := make([]dispatchRecord, limit)
	for i := 0; i < limit; i++ {
		out[i] = r.log[len(r.log)-1-i]
	}
	return out
}

// ---- Service integration helpers ----------------------------------------

// SendAction creates a ActionNotification notification and broadcasts it.
// svc is the base *Service; specs lists the action buttons.
func SendAction(svc *Service, title, body string, level Level, source string, specs []ActionSpec) *ActionNotification {
	n := svc.Send(title, body, level, source)
	ta := &ActionNotification{
		Notification: *n,
		Actions:      specs,
	}
	// Re-broadcast with action specs. Serialized per-connection + written outside
	// the lock via the shared broadcast helper (nil predicate = all clients),
	// which prevents concurrent-writer panics on a shared WS connection.
	data, _ := json.Marshal(ta)
	svc.broadcast(data, nil)
	return ta
}
