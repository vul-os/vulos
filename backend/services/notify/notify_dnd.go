// Package notify — NOTIF-05: Do Not Disturb manager.
//
// Design choice: self-contained, independent of NOTIF-02's dndState / Store.
// State is persisted to ~/.vulos/db/dnd.json (0600, atomic write).
// No edits to notify.go; no dependency on NOTIF-02 symbols.
package notify

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DNDMode controls how DND suppresses notifications.
type DNDMode string

const (
	DNDModeOff      DNDMode = "off"
	DNDModePriority DNDMode = "priority" // only Urgent/Critical pass through
	DNDModeTotal    DNDMode = "total"    // suppress all
)

// Priority classifies a notification for DND priority-pass decisions.
type Priority string

const (
	PriorityNormal   Priority = "normal"
	PriorityUrgent   Priority = "urgent"
	PriorityCritical Priority = "critical"
)

// ScheduleWindow is a recurring weekly DND window.
type ScheduleWindow struct {
	// Weekdays is a bitmask: 0=Sun,1=Mon,...,6=Sat  (bit 0 = Sunday)
	Weekdays  uint8  `json:"weekdays"`
	StartHHMM string `json:"start"` // "HH:MM" 24-h
	EndHHMM   string `json:"end"`   // "HH:MM" 24-h (may wrap midnight)
}

// dndPersist is the JSON-serialised state written to disk.
type dndPersist struct {
	Mode     DNDMode          `json:"mode"`
	Until    time.Time        `json:"until,omitempty"` // zero = no expiry
	Schedule []ScheduleWindow `json:"schedule,omitempty"`
}

// DNDManager manages Do Not Disturb state with optional schedule windows
// and time-limited activations. All methods are goroutine-safe.
type DNDManager struct {
	mu       sync.RWMutex
	path     string // ~/.vulos/db/dnd.json
	mode     DNDMode
	until    time.Time        // zero means no expiry
	schedule []ScheduleWindow // weekly recurrence
}

// NewDNDManager loads (or creates) the DND state file at path.
// path should be the full path to dnd.json.
func NewDNDManager(path string) *DNDManager {
	m := &DNDManager{
		path: path,
		mode: DNDModeOff,
	}
	m.load()
	return m
}

// ---- Persistence -------------------------------------------------------

func (m *DNDManager) load() {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return // file doesn't exist yet — fine
	}
	var p dndPersist
	if err := json.Unmarshal(data, &p); err != nil {
		log.Printf("[dnd] corrupt state file, resetting: %v", err)
		return
	}
	m.mode = p.Mode
	m.until = p.Until
	m.schedule = p.Schedule
}

func (m *DNDManager) save() {
	p := dndPersist{
		Mode:     m.mode,
		Until:    m.until,
		Schedule: m.schedule,
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		log.Printf("[dnd] marshal error: %v", err)
		return
	}
	// Atomic write: temp file + rename
	dir := filepath.Dir(m.path)
	os.MkdirAll(dir, 0700)
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		log.Printf("[dnd] write error: %v", err)
		return
	}
	if err := os.Rename(tmp, m.path); err != nil {
		os.Remove(tmp)
		log.Printf("[dnd] rename error: %v", err)
	}
}

// ---- State accessors ----------------------------------------------------

// Mode returns the current DND mode (resolving schedule windows in real time).
func (m *DNDManager) Mode() DNDMode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.effectiveMode(time.Now())
}

// effectiveMode is the internal (un-locked) decision — must be called with at least
// a read-lock held.
func (m *DNDManager) effectiveMode(now time.Time) DNDMode {
	// If a timed override is active, prefer it.
	if m.mode != DNDModeOff && !m.until.IsZero() {
		if now.Before(m.until) {
			return m.mode
		}
		// expired — treat as off (state will be cleaned on next write)
	}
	if m.mode != DNDModeOff && m.until.IsZero() {
		return m.mode // permanent override
	}
	// Check schedule windows
	for _, w := range m.schedule {
		if inWindow(now, w) {
			return DNDModeTotal
		}
	}
	return DNDModeOff
}

// Status returns a snapshot of DND state for API responses.
func (m *DNDManager) Status() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := time.Now()
	eff := m.effectiveMode(now)

	var until *time.Time
	if !m.until.IsZero() {
		u := m.until
		until = &u
	}
	return map[string]interface{}{
		"mode":           string(m.mode),
		"effective_mode": string(eff),
		"until":          until,
		"active":         eff != DNDModeOff,
		"schedule":       m.schedule,
	}
}

// ---- Mutators -----------------------------------------------------------

// Set activates or deactivates DND.
// until is optional — zero time means permanent until manually cleared.
func (m *DNDManager) Set(mode DNDMode, until time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mode = mode
	m.until = until
	m.save()
}

// SetSchedule replaces the weekly schedule windows.
func (m *DNDManager) SetSchedule(schedule []ScheduleWindow) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schedule = schedule
	m.save()
}

// Clear disables DND (mode=off, removes expiry but preserves schedule).
func (m *DNDManager) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mode = DNDModeOff
	m.until = time.Time{}
	m.save()
}

// ---- Suppression decision ----------------------------------------------

// Suppressed returns true when the notification should be withheld.
//
//   - DNDModeOff:      never suppresses
//   - DNDModePriority: only lets PriorityUrgent / PriorityCritical through;
//     maps to notify.LevelUrgent when used alongside notify.Level
//   - DNDModeTotal:    suppresses everything
func (m *DNDManager) Suppressed(level Level, priority Priority) bool {
	eff := m.Mode()
	switch eff {
	case DNDModeOff:
		return false
	case DNDModePriority:
		// Urgent/Critical pass through
		if priority == PriorityUrgent || priority == PriorityCritical {
			return false
		}
		if level == LevelUrgent {
			return false
		}
		return true
	case DNDModeTotal:
		return true
	}
	return false
}

// ---- Schedule helpers ---------------------------------------------------

// inWindow returns true when now falls inside the recurrence window.
func inWindow(now time.Time, w ScheduleWindow) bool {
	// Check weekday bit
	wd := now.Weekday() // 0=Sun
	if w.Weekdays != 0 && (w.Weekdays>>uint(wd))&1 == 0 {
		return false
	}

	startMin, okS := parseHHMM(w.StartHHMM)
	endMin, okE := parseHHMM(w.EndHHMM)
	if !okS || !okE {
		return false
	}

	nowMin := now.Hour()*60 + now.Minute()

	if endMin > startMin {
		// Normal range (e.g. 22:00–23:00)
		return nowMin >= startMin && nowMin < endMin
	}
	// Wraps midnight (e.g. 22:00–06:00)
	return nowMin >= startMin || nowMin < endMin
}

func parseHHMM(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	mn := int(s[3]-'0')*10 + int(s[4]-'0')
	if h < 0 || h > 23 || mn < 0 || mn > 59 {
		return 0, false
	}
	return h*60 + mn, true
}
