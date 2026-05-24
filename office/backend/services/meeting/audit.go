package meeting

import (
	"fmt"
	"sync"
	"time"
)

// JoinAuditEvent records every join attempt — admitted or denied.
// All fields are immutable once written.
type JoinAuditEvent struct {
	RoomID     string    `json:"room_id"`
	AccountID  string    `json:"account_id,omitempty"` // empty for anonymous
	IP         string    `json:"ip"`
	UserAgent  string    `json:"user_agent"`
	Action     string    `json:"action"` // "admitted" | "denied" | "waiting" | "expired"
	AcceptedBy string    `json:"accepted_by,omitempty"`
	At         time.Time `json:"at"`
}

// AuditLog is a simple append-only in-memory log (per-process).
// In production this would be persisted to the meeting audit table.
type AuditLog struct {
	mu     sync.RWMutex
	events []*JoinAuditEvent
}

var globalAuditLog = &AuditLog{}

// GlobalAuditLog returns the process-wide audit log.
func GlobalAuditLog() *AuditLog { return globalAuditLog }

// Append adds an event to the log. The log is append-only; no update or delete
// path is exposed.
func (a *AuditLog) Append(ev *JoinAuditEvent) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
}

// ListByRoom returns all events for a given room_id (chronological order).
func (a *AuditLog) ListByRoom(roomID string) []*JoinAuditEvent {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var out []*JoinAuditEvent
	for _, ev := range a.events {
		if ev.RoomID == roomID {
			out = append(out, ev)
		}
	}
	return out
}

// ListByAccountID returns all events for a given account_id (chronological order).
func (a *AuditLog) ListByAccountID(accountID string) []*JoinAuditEvent {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var out []*JoinAuditEvent
	for _, ev := range a.events {
		if ev.AccountID == accountID {
			out = append(out, ev)
		}
	}
	return out
}

// Len returns total number of events (for testing).
func (a *AuditLog) Len() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.events)
}

// String provides a human-readable summary of the last N events.
func (a *AuditLog) String(n int) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	start := 0
	if len(a.events) > n {
		start = len(a.events) - n
	}
	out := ""
	for _, ev := range a.events[start:] {
		out += fmt.Sprintf("[%s] room=%s acct=%s ip=%s action=%s acceptedBy=%s\n",
			ev.At.Format(time.RFC3339), ev.RoomID, ev.AccountID, ev.IP, ev.Action, ev.AcceptedBy)
	}
	return out
}
