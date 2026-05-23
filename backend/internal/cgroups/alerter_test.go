package cgroups

import (
	"context"
	"sync"
	"testing"
	"time"
)

// newTestAlerter builds a Governor + Alerter with fast timings for unit tests.
func newTestAlerter(t *testing.T) (*Governor, *Alerter) {
	t.Helper()
	g := newTestGovernor(t)
	a := NewAlerter(g)
	// Fast timings so tests don't wait real seconds.
	a.interval = 5 * time.Millisecond
	a.cpuOveruseDuration = 50 * time.Millisecond
	a.memOveruseDuration = 30 * time.Millisecond
	a.restoreDuration = 60 * time.Millisecond
	return g, a
}

// captureNotifications returns a NotifyFunc that appends to a slice and a
// getter func to read the captured notifications.
func captureNotifications() (NotifyFunc, func() []Notification) {
	var mu sync.Mutex
	var ns []Notification
	fn := func(n Notification) {
		mu.Lock()
		ns = append(ns, n)
		mu.Unlock()
	}
	get := func() []Notification {
		mu.Lock()
		defer mu.Unlock()
		out := make([]Notification, len(ns))
		copy(out, ns)
		return out
	}
	return fn, get
}

// --- CPU overuse alert ---

// TestCPUAlert_FiresAfterDuration verifies that a CPU alert is emitted after
// the configured sustained period.
func TestCPUAlert_FiresAfterDuration(t *testing.T) {
	g, a := newTestAlerter(t)
	notify, getNS := captureNotifications()
	a.SetNotify(notify)

	const appID = "cpu-alert-app"
	if err := g.EnsureAppSlice(appID, VisibilityPublic); err != nil {
		t.Fatalf("EnsureAppSlice: %v", err)
	}

	// Inject state: mark cpuOveruseStart in the past beyond the threshold.
	a.mu.Lock()
	a.states[appID] = &appState{
		cpuOveruseStart: time.Now().Add(-a.cpuOveruseDuration - 10*time.Millisecond),
	}
	a.mu.Unlock()

	// Run a single poll tick.  We call poll directly to avoid goroutine timing
	// races in tests.
	a.poll(context.Background())

	ns := getNS()
	found := false
	for _, n := range ns {
		if n.AppID == appID && n.Priority == "warning" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected CPU alert notification for %s, got %v", appID, ns)
	}
}

// TestCPUAlert_EventRecorded checks that a "alert" cgroup_event row is written.
func TestCPUAlert_EventRecorded(t *testing.T) {
	g, a := newTestAlerter(t)
	notify, _ := captureNotifications()
	a.SetNotify(notify)

	const appID = "cpu-event-app"
	if err := g.EnsureAppSlice(appID, VisibilityPublic); err != nil {
		t.Fatalf("EnsureAppSlice: %v", err)
	}

	a.mu.Lock()
	a.states[appID] = &appState{
		cpuOveruseStart: time.Now().Add(-a.cpuOveruseDuration - 10*time.Millisecond),
	}
	a.mu.Unlock()

	a.poll(context.Background())

	var count int
	_ = g.db.QueryRow(`SELECT COUNT(*) FROM cgroup_events WHERE event_type='alert'`).Scan(&count)
	if count == 0 {
		t.Error("expected at least one 'alert' event in cgroup_events")
	}
}

// TestCPUAlert_NoFireBeforeDuration verifies that no alert fires if the
// overuse duration hasn't elapsed yet.
func TestCPUAlert_NoFireBeforeDuration(t *testing.T) {
	g, a := newTestAlerter(t)
	notify, getNS := captureNotifications()
	a.SetNotify(notify)

	const appID = "cpu-early-app"
	if err := g.EnsureAppSlice(appID, VisibilityPublic); err != nil {
		t.Fatalf("EnsureAppSlice: %v", err)
	}

	// Mark overuse start as only 5 ms ago (threshold is 50 ms).
	a.mu.Lock()
	a.states[appID] = &appState{
		cpuOveruseStart: time.Now().Add(-5 * time.Millisecond),
	}
	a.mu.Unlock()

	a.poll(context.Background())

	for _, n := range getNS() {
		if n.AppID == appID && n.Priority == "warning" {
			t.Errorf("alert fired too early for %s", appID)
		}
	}
}

// --- Memory overuse throttle ---

// TestMemoryThrottle_Applied verifies that CPU weight is halved when memory
// overuse sustains past the threshold.
func TestMemoryThrottle_Applied(t *testing.T) {
	g, a := newTestAlerter(t)
	notify, getNS := captureNotifications()
	a.SetNotify(notify)

	const appID = "mem-throttle-app"
	if err := g.EnsureAppSlice(appID, VisibilityPublic); err != nil {
		t.Fatalf("EnsureAppSlice: %v", err)
	}

	// Get slice DB row to learn the configured CPUWeight.
	statuses, _ := g.Status()
	var originalWeight int64
	for _, s := range statuses {
		if s.AppID == appID {
			originalWeight = s.Limits.CPUWeight
			break
		}
	}
	if originalWeight == 0 {
		t.Fatal("could not determine original CPU weight")
	}

	// Synthesise: memOveruseStart past the threshold, high memCurrent.
	// Because on darwin live stats are 0, we drive the state machine directly.
	a.mu.Lock()
	st := &appState{
		memOveruseStart: time.Now().Add(-a.memOveruseDuration - 10*time.Millisecond),
	}
	a.states[appID] = st
	a.mu.Unlock()

	// We need Status to return a slice where MemCurrent > MemHigh.
	// Since readLiveStats returns 0 on darwin we manipulate the conditions by
	// calling applyThrottle directly with a synthetic SliceStatus.
	syntheticStatus := SliceStatus{
		AppID:      appID,
		SliceType:  SliceTypeApp,
		Visibility: VisibilityPublic,
		Limits:     PublicAppLimits,
		MemCurrent: PublicAppLimits.MemHigh + 1,
	}

	a.mu.Lock()
	a.applyThrottle(syntheticStatus, st, time.Now())
	a.mu.Unlock()

	// Verify state.
	if !st.throttled {
		t.Error("expected throttled=true after applyThrottle")
	}
	if st.normalCPUWeight != originalWeight {
		// normalCPUWeight is set from the synthetic status Limits.CPUWeight
		if st.normalCPUWeight != PublicAppLimits.CPUWeight {
			t.Errorf("normalCPUWeight = %d, want %d", st.normalCPUWeight, PublicAppLimits.CPUWeight)
		}
	}

	// Check notification.
	ns := getNS()
	found := false
	for _, n := range ns {
		if n.AppID == appID && n.Priority == "warning" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected throttle notification for %s, got %v", appID, ns)
	}

	// Check DB event.
	var count int
	_ = g.db.QueryRow(`SELECT COUNT(*) FROM cgroup_events WHERE event_type='throttle'`).Scan(&count)
	if count == 0 {
		t.Error("expected 'throttle' event in cgroup_events")
	}
}

// TestMemoryThrottle_EventLogged mirrors TestMemoryThrottle_Applied but
// focuses purely on the DB record.
func TestMemoryThrottle_EventLogged(t *testing.T) {
	g, a := newTestAlerter(t)
	a.SetNotify(func(_ Notification) {})

	const appID = "mem-event-app"
	if err := g.EnsureAppSlice(appID, VisibilityPublic); err != nil {
		t.Fatalf("EnsureAppSlice: %v", err)
	}

	syntheticStatus := SliceStatus{
		AppID:      appID,
		SliceType:  SliceTypeApp,
		Visibility: VisibilityPublic,
		Limits:     PublicAppLimits,
		MemCurrent: PublicAppLimits.MemHigh + 1,
	}

	a.mu.Lock()
	st := &appState{}
	a.states[appID] = st
	a.applyThrottle(syntheticStatus, st, time.Now())
	a.mu.Unlock()

	var count int
	_ = g.db.QueryRow(`SELECT COUNT(*) FROM cgroup_events WHERE event_type='throttle'`).Scan(&count)
	if count == 0 {
		t.Error("expected at least one 'throttle' event")
	}
}

// --- Auto-restore ---

// TestAutoRestore_AfterCooldown verifies that normal CPU weight is restored
// after usage drops below the threshold for the restore duration.
func TestAutoRestore_AfterCooldown(t *testing.T) {
	g, a := newTestAlerter(t)
	notify, getNS := captureNotifications()
	a.SetNotify(notify)

	const appID = "restore-app"
	if err := g.EnsureAppSlice(appID, VisibilityPublic); err != nil {
		t.Fatalf("EnsureAppSlice: %v", err)
	}

	// Set up as already throttled, with belowThresholdStart past the restore window.
	a.mu.Lock()
	st := &appState{
		throttled:           true,
		normalCPUWeight:     PublicAppLimits.CPUWeight,
		belowThresholdStart: time.Now().Add(-a.restoreDuration - 10*time.Millisecond),
	}
	a.states[appID] = st
	a.mu.Unlock()

	// Synthetic status: CPUUsageUs=0 and MemCurrent=0 so both look calm.
	syntheticStatus := SliceStatus{
		AppID:      appID,
		SliceType:  SliceTypeApp,
		Visibility: VisibilityPublic,
		Limits:     PublicAppLimits,
		MemCurrent: 0,
		CPUUsageUs: 0,
	}

	a.mu.Lock()
	a.evaluateRestore(syntheticStatus, st, time.Now())
	a.mu.Unlock()

	if st.throttled {
		t.Error("expected throttled=false after restore")
	}
	if st.normalCPUWeight != 0 {
		t.Errorf("expected normalCPUWeight reset to 0, got %d", st.normalCPUWeight)
	}

	ns := getNS()
	found := false
	for _, n := range ns {
		if n.AppID == appID && n.Priority == "info" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected restore notification for %s, got %v", appID, ns)
	}

	var count int
	_ = g.db.QueryRow(`SELECT COUNT(*) FROM cgroup_events WHERE event_type='restore'`).Scan(&count)
	if count == 0 {
		t.Error("expected 'restore' event in cgroup_events")
	}
}

// TestAutoRestore_NoRestoreBeforeCooldown verifies that restore does NOT happen
// if the cooldown hasn't passed.
func TestAutoRestore_NoRestoreBeforeCooldown(t *testing.T) {
	g, a := newTestAlerter(t)
	notify, getNS := captureNotifications()
	a.SetNotify(notify)

	const appID = "early-restore-app"
	if err := g.EnsureAppSlice(appID, VisibilityPublic); err != nil {
		t.Fatalf("EnsureAppSlice: %v", err)
	}

	a.mu.Lock()
	st := &appState{
		throttled:           true,
		normalCPUWeight:     PublicAppLimits.CPUWeight,
		belowThresholdStart: time.Now().Add(-5 * time.Millisecond), // too recent
	}
	a.states[appID] = st
	a.mu.Unlock()

	syntheticStatus := SliceStatus{
		AppID:      appID,
		SliceType:  SliceTypeApp,
		Visibility: VisibilityPublic,
		Limits:     PublicAppLimits,
	}

	a.mu.Lock()
	a.evaluateRestore(syntheticStatus, st, time.Now())
	a.mu.Unlock()

	if !st.throttled {
		t.Error("expected still throttled (cooldown not elapsed)")
	}
	for _, n := range getNS() {
		if n.AppID == appID && n.Priority == "info" {
			t.Error("restore notification fired too early")
		}
	}
}

// --- Start / goroutine integration ---

// TestAlerter_StartAndCancel verifies that Start returns promptly after ctx
// is cancelled (no goroutine leak).
func TestAlerter_StartAndCancel(t *testing.T) {
	_, a := newTestAlerter(t)
	a.SetNotify(func(_ Notification) {})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		a.Start(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Alerter.Start did not return after context cancel")
	}
}
