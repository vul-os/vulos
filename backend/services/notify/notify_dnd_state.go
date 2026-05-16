package notify

// notify_dnd_state.go — NOTIF-02: do-not-disturb state holder.
//
// This file owns ONLY the thread-safe state container plus the Service
// accessors NOTIF-05/06 rely on (DND() and SetStore()). The DND modes,
// schedule evaluation, suppression decision, and HTTP endpoints are NOT
// here — those are NOTIF-05/06's notify_dnd.go / routes_notify.go.

import (
	"sync"
	"time"
)

// DND mode values.
const (
	DNDOff      = "off"      // notifications flow normally
	DNDPriority = "priority" // only high/critical pass (policy lives in NOTIF-05)
	DNDTotal    = "total"    // everything suppressed (policy lives in NOTIF-05)
)

// DNDWindow is a recurring quiet-hours window. Start/End are "HH:MM" 24h
// strings; Days holds weekday numbers (0=Sunday … 6=Saturday).
type DNDWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Days  []int  `json:"days"`
}

// dndState is the thread-safe do-not-disturb state container.
type dndState struct {
	mu       sync.RWMutex
	mode     string      // off | priority | total
	until    time.Time   // optional auto-expiry of a manual override
	schedule []DNDWindow // recurring quiet-hours windows
}

// Get returns a consistent snapshot of the DND state. The returned schedule
// slice is a copy, so callers may freely retain or mutate it.
func (d *dndState) Get() (mode string, until time.Time, schedule []DNDWindow) {
	if d == nil {
		return DNDOff, time.Time{}, nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	mode = d.mode
	if mode == "" {
		mode = DNDOff
	}
	until = d.until
	if len(d.schedule) > 0 {
		schedule = make([]DNDWindow, len(d.schedule))
		copy(schedule, d.schedule)
	}
	return mode, until, schedule
}

// Set replaces the DND state. An empty mode normalises to "off". The
// schedule slice is copied defensively.
func (d *dndState) Set(mode string, until time.Time, schedule []DNDWindow) {
	if d == nil {
		return
	}
	if mode == "" {
		mode = DNDOff
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	d.mode = mode
	d.until = until
	if len(schedule) > 0 {
		d.schedule = make([]DNDWindow, len(schedule))
		copy(d.schedule, schedule)
	} else {
		d.schedule = nil
	}
}

// DND returns the Service's do-not-disturb state holder, lazily creating it
// (mode "off") on first use. Never returns nil. This is the accessor
// NOTIF-05/06 depends on per the freeze contract.
func (s *Service) DND() *dndState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dnd == nil {
		s.dnd = &dndState{mode: DNDOff}
	}
	return s.dnd
}

// SetStore attaches a persistent Store to the Service. Once set,
// SendNotification additively persists each notification via the store.
// Passing nil reverts the Service to memory-only behaviour. This is the
// accessor NOTIF-05/06 depends on per the freeze contract.
func (s *Service) SetStore(store *Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = store
}
