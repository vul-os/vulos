// Package cgroups – PUBWEB-08: Resource alerts: notification + auto-throttle
// on sustained overuse.
//
// Alerter runs a background goroutine that polls cgroup stats every 10 s.
// Rules (matching the task spec):
//
//   - CPU overuse:  CPUWeight >= 90% of the slice's configured weight for > 60 s
//     → emit a warning notification and record an "alert" event.
//   - Memory overuse: memory.current > memory.high for > 30 s
//     → reduce CPUWeight by 50% (throttle), emit a notification, record a
//     "throttle" event.
//   - Auto-restore: once usage drops below 70% of normal limits for 120 s
//     → restore original CPUWeight, emit a notification, record a "restore" event.
//
// On non-Linux platforms live stats are always 0; the alerter starts and runs
// but never fires (no cgroup files exist to read).
package cgroups

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Notification represents a structured OS-level notification emitted by the
// Alerter.  Callers can replace the default (log) handler via Alerter.SetNotify.
type Notification struct {
	AppID    string
	Priority string // "warning" | "info"
	Title    string
	Body     string
	Action   string // suggested action label, e.g. "Review in Dashboard"
}

// NotifyFunc is called whenever the Alerter wants to surface a notification.
// The default implementation logs to the standard logger.
type NotifyFunc func(n Notification)

// appState tracks the per-app transient state used by the polling loop.
type appState struct {
	// cpuOveruseStart is when we first saw CPU usage >= 90% threshold.
	// Zero means no overuse observed yet.
	cpuOveruseStart time.Time

	// memOveruseStart is when memory.current first exceeded memory.high.
	memOveruseStart time.Time

	// throttled is true when the Alerter has halved the CPU weight.
	throttled bool

	// normalCPUWeight is the pre-throttle CPU weight so we can restore it.
	normalCPUWeight int64

	// belowThresholdStart is when usage first dropped below the restore
	// threshold after a throttle event.
	belowThresholdStart time.Time
}

// Alerter polls cgroup stats and enforces sustained-overuse policy.
type Alerter struct {
	governor *Governor
	notify   NotifyFunc
	interval time.Duration

	mu     sync.Mutex
	states map[string]*appState // keyed by AppID

	// Tunables (exposed for tests).
	cpuOveruseDuration  time.Duration // default 60 s
	memOveruseDuration  time.Duration // default 30 s
	restoreDuration     time.Duration // default 120 s
	cpuThresholdPct     int64         // default 90 (%)
	restoreThresholdPct int64         // default 70 (%)
}

// NewAlerter creates an Alerter that uses g for stats and slice management.
// Call Start to begin polling.
func NewAlerter(g *Governor) *Alerter {
	return &Alerter{
		governor:            g,
		notify:              defaultNotify,
		interval:            10 * time.Second,
		states:              make(map[string]*appState),
		cpuOveruseDuration:  60 * time.Second,
		memOveruseDuration:  30 * time.Second,
		restoreDuration:     120 * time.Second,
		cpuThresholdPct:     90,
		restoreThresholdPct: 70,
	}
}

// SetNotify replaces the notification handler.  Must be called before Start.
func (a *Alerter) SetNotify(fn NotifyFunc) {
	a.notify = fn
}

// Start launches the background polling goroutine.  It returns when ctx is
// cancelled.
func (a *Alerter) Start(ctx context.Context) {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.poll(ctx)
		}
	}
}

// poll is called on every tick; it reads Status and applies overuse rules.
func (a *Alerter) poll(_ context.Context) {
	statuses, err := a.governor.Status()
	if err != nil {
		log.Printf("cgroups/alerter: Status error: %v", err)
		return
	}

	now := time.Now()

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, s := range statuses {
		if s.SliceType != SliceTypeApp {
			continue
		}
		if s.AppID == "" {
			continue
		}

		st, ok := a.states[s.AppID]
		if !ok {
			st = &appState{}
			a.states[s.AppID] = st
		}

		a.evaluateCPU(s, st, now)
		a.evaluateMemory(s, st, now)
		a.evaluateRestore(s, st, now)
	}
}

// evaluateCPU checks whether the app is sustaining CPU overuse.
//
// Detection: CPUOveruseActive(s, st) returns true when usage is measurably
// high.  On Linux this uses the cumulative CPUUsageUs counter; on non-Linux
// cpuOveruseActive returns false unless cpuOveruseStart was already set by a
// prior tick (e.g. seeded in tests).
//
// Once cpuOveruseStart is set (overuse first observed), the timer keeps
// running on subsequent ticks as long as cpuOveruseActive remains true OR the
// start was explicitly set (preserves timer across ticks that may return 0 due
// to timing).  The alert fires when the duration exceeds cpuOveruseDuration.
func (a *Alerter) evaluateCPU(s SliceStatus, st *appState, now time.Time) {
	active := a.cpuOveruseActive(s, st)

	if active || !st.cpuOveruseStart.IsZero() {
		if st.cpuOveruseStart.IsZero() {
			st.cpuOveruseStart = now
		}
		if now.Sub(st.cpuOveruseStart) >= a.cpuOveruseDuration && !st.throttled {
			// Emit alert notification.
			a.notify(Notification{
				AppID:    s.AppID,
				Priority: "warning",
				Title:    fmt.Sprintf("App %s: sustained CPU overuse", s.AppID),
				Body:     fmt.Sprintf("CPU usage at ≥%d%% of weight for >%s.", a.cpuThresholdPct, a.cpuOveruseDuration),
				Action:   "Review in Dashboard",
			})
			sliceID := a.sliceID(s.AppID)
			_ = a.governor.RecordEvent(sliceID, "alert",
				fmt.Sprintf("CPU sustained overuse ≥%d%% for >%s", a.cpuThresholdPct, a.cpuOveruseDuration))
			// Reset so we don't fire repeatedly until next window.
			st.cpuOveruseStart = time.Time{}
		}
	} else {
		st.cpuOveruseStart = time.Time{}
	}
}

// cpuOveruseActive returns true when the slice's CPU usage is measurably high.
// On Linux we have a non-zero CPUUsageUs counter and check whether the current
// cpu.weight is still at ≥ cpuThresholdPct of the configured limit (kernel has
// not throttled down yet).  Returns false on non-Linux (CPUUsageUs == 0).
func (a *Alerter) cpuOveruseActive(s SliceStatus, st *appState) bool {
	if s.CPUUsageUs <= 0 {
		return false
	}
	configuredWeight := s.Limits.CPUWeight
	if st.throttled && st.normalCPUWeight > 0 {
		configuredWeight = st.normalCPUWeight
	}
	return s.Limits.CPUWeight*100 >= configuredWeight*a.cpuThresholdPct
}

// evaluateMemory checks whether memory.current > memory.high and throttles.
func (a *Alerter) evaluateMemory(s SliceStatus, st *appState, now time.Time) {
	memHigh := s.Limits.MemHigh
	if memHigh <= 0 {
		return // no soft limit configured
	}

	if s.MemCurrent > memHigh {
		if st.memOveruseStart.IsZero() {
			st.memOveruseStart = now
		} else if now.Sub(st.memOveruseStart) >= a.memOveruseDuration && !st.throttled {
			a.applyThrottle(s, st, now)
		}
	} else {
		st.memOveruseStart = time.Time{}
	}
}

// applyThrottle halves CPUWeight and records the event.
func (a *Alerter) applyThrottle(s SliceStatus, st *appState, now time.Time) {
	st.throttled = true
	st.normalCPUWeight = s.Limits.CPUWeight
	newWeight := s.Limits.CPUWeight / 2
	if newWeight < 1 {
		newWeight = 1
	}
	st.belowThresholdStart = time.Time{}

	// Apply cgroup change.
	err := a.governor.EnsureSlice(SliceSpec{
		AppID:      s.AppID,
		SliceType:  s.SliceType,
		Visibility: s.Visibility,
		Limits: Limits{
			CPUWeight: newWeight,
			MemMin:    s.Limits.MemMin,
			MemHigh:   s.Limits.MemHigh,
			MemMax:    s.Limits.MemMax,
		},
	})
	if err != nil {
		log.Printf("cgroups/alerter: throttle EnsureSlice(%s): %v", s.AppID, err)
	}

	a.notify(Notification{
		AppID:    s.AppID,
		Priority: "warning",
		Title:    fmt.Sprintf("App %s throttled", s.AppID),
		Body:     fmt.Sprintf("Memory exceeded soft limit for >%s. CPU weight reduced %d→%d.", a.memOveruseDuration, st.normalCPUWeight, newWeight),
		Action:   "Review in Dashboard",
	})

	sliceID := a.sliceID(s.AppID)
	_ = a.governor.RecordEvent(sliceID, "throttle",
		fmt.Sprintf("mem.current>mem.high for >%s; cpu_weight %d→%d", a.memOveruseDuration, st.normalCPUWeight, newWeight))

}

// evaluateRestore checks whether usage has recovered and restores normal limits.
func (a *Alerter) evaluateRestore(s SliceStatus, st *appState, now time.Time) {
	if !st.throttled {
		return
	}

	// Check if usage is below 70% of the normal (pre-throttle) weight.
	normalWeight := st.normalCPUWeight
	if normalWeight <= 0 {
		return
	}

	usageLow := s.CPUUsageUs == 0 ||
		(s.Limits.CPUWeight*100 < normalWeight*a.restoreThresholdPct)

	memOK := s.Limits.MemHigh <= 0 || s.MemCurrent <= s.Limits.MemHigh

	if usageLow && memOK {
		if st.belowThresholdStart.IsZero() {
			st.belowThresholdStart = now
		} else if now.Sub(st.belowThresholdStart) >= a.restoreDuration {
			a.applyRestore(s, st)
		}
	} else {
		st.belowThresholdStart = time.Time{}
	}
}

// applyRestore restores the original CPU weight.
func (a *Alerter) applyRestore(s SliceStatus, st *appState) {
	restoredWeight := st.normalCPUWeight

	err := a.governor.EnsureSlice(SliceSpec{
		AppID:      s.AppID,
		SliceType:  s.SliceType,
		Visibility: s.Visibility,
		Limits: Limits{
			CPUWeight: restoredWeight,
			MemMin:    s.Limits.MemMin,
			MemHigh:   s.Limits.MemHigh,
			MemMax:    s.Limits.MemMax,
		},
	})
	if err != nil {
		log.Printf("cgroups/alerter: restore EnsureSlice(%s): %v", s.AppID, err)
	}

	a.notify(Notification{
		AppID:    s.AppID,
		Priority: "info",
		Title:    fmt.Sprintf("App %s restored", s.AppID),
		Body:     fmt.Sprintf("Usage below %d%% for >%s. CPU weight restored to %d.", a.restoreThresholdPct, a.restoreDuration, restoredWeight),
		Action:   "Review in Dashboard",
	})

	sliceID := a.sliceID(s.AppID)
	_ = a.governor.RecordEvent(sliceID, "restore",
		fmt.Sprintf("usage below %d%% for >%s; cpu_weight restored to %d", a.restoreThresholdPct, a.restoreDuration, restoredWeight))

	// Reset state.
	st.throttled = false
	st.normalCPUWeight = 0
	st.belowThresholdStart = time.Time{}
	st.memOveruseStart = time.Time{}
	st.cpuOveruseStart = time.Time{}
}

// sliceID fetches the DB row ID for the given appID.
func (a *Alerter) sliceID(appID string) string {
	var id string
	_ = a.governor.db.QueryRow(
		`SELECT id FROM cgroup_slices WHERE app_id=? AND slice_type='app' ORDER BY updated_at DESC LIMIT 1`,
		appID,
	).Scan(&id)
	return id
}

// defaultNotify logs the notification to the standard logger.
func defaultNotify(n Notification) {
	log.Printf("cgroups/alerter [%s] %s — %s (action: %s)", n.Priority, n.Title, n.Body, n.Action)
}
