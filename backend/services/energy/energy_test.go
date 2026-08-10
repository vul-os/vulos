package energy

import (
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// DefaultConfig — mode-specific thresholds
// ---------------------------------------------------------------------------

func TestDefaultConfig_Performance(t *testing.T) {
	cfg := DefaultConfig(ModePerformance)

	if cfg.DimAfter != 10*time.Minute {
		t.Errorf("performance DimAfter: want 10m, got %v", cfg.DimAfter)
	}
	if cfg.ScreenOff != 30*time.Minute {
		t.Errorf("performance ScreenOff: want 30m, got %v", cfg.ScreenOff)
	}
	if cfg.SuspendAfter != 0 {
		t.Errorf("performance SuspendAfter: want 0 (never), got %v", cfg.SuspendAfter)
	}
	if cfg.AppIdle != 0 {
		t.Errorf("performance AppIdle: want 0 (never), got %v", cfg.AppIdle)
	}
}

func TestDefaultConfig_Balanced(t *testing.T) {
	cfg := DefaultConfig(ModeBalanced)

	if cfg.DimAfter != 2*time.Minute {
		t.Errorf("balanced DimAfter: want 2m, got %v", cfg.DimAfter)
	}
	if cfg.ScreenOff != 5*time.Minute {
		t.Errorf("balanced ScreenOff: want 5m, got %v", cfg.ScreenOff)
	}
	if cfg.SuspendAfter != 15*time.Minute {
		t.Errorf("balanced SuspendAfter: want 15m, got %v", cfg.SuspendAfter)
	}
	if cfg.AppIdle != 10*time.Minute {
		t.Errorf("balanced AppIdle: want 10m, got %v", cfg.AppIdle)
	}
}

func TestDefaultConfig_Saver(t *testing.T) {
	cfg := DefaultConfig(ModeSaver)

	if cfg.DimAfter != 30*time.Second {
		t.Errorf("saver DimAfter: want 30s, got %v", cfg.DimAfter)
	}
	if cfg.ScreenOff != 2*time.Minute {
		t.Errorf("saver ScreenOff: want 2m, got %v", cfg.ScreenOff)
	}
	if cfg.SuspendAfter != 5*time.Minute {
		t.Errorf("saver SuspendAfter: want 5m, got %v", cfg.SuspendAfter)
	}
	if cfg.AppIdle != 3*time.Minute {
		t.Errorf("saver AppIdle: want 3m, got %v", cfg.AppIdle)
	}
}

// Ensure the default (unknown) mode falls through to the balanced branch.
func TestDefaultConfig_UnknownFallsToBalanced(t *testing.T) {
	cfg := DefaultConfig(Mode("unknown"))
	balanced := DefaultConfig(ModeBalanced)

	if cfg != balanced {
		t.Errorf("unknown mode should produce balanced config, got %+v", cfg)
	}
}

// ---------------------------------------------------------------------------
// NewManager — initial state
// ---------------------------------------------------------------------------

func TestNewManager_InitialState(t *testing.T) {
	m := NewManager(ModeBalanced)

	s := m.State()
	if s.Mode != ModeBalanced {
		t.Errorf("initial mode: want %s, got %s", ModeBalanced, s.Mode)
	}
	if !s.ScreenOn {
		t.Error("screen should be on at startup")
	}
	if s.ScreenDimmed {
		t.Error("screen should not be dimmed at startup")
	}
	if s.ScreenBrightness != 100 {
		t.Errorf("initial brightness: want 100, got %d", s.ScreenBrightness)
	}
	if s.SuspendReady {
		t.Error("SuspendReady should be false at startup")
	}
}

// ---------------------------------------------------------------------------
// Manager.SetMode — mode transitions
// ---------------------------------------------------------------------------

func TestManager_SetMode_PerformanceToSaver(t *testing.T) {
	m := NewManager(ModePerformance)
	m.SetMode(ModeSaver)

	s := m.State()
	if s.Mode != ModeSaver {
		t.Errorf("after SetMode(saver): want saver, got %s", s.Mode)
	}
	if m.AppIdleTimeout() != 3*time.Minute {
		t.Errorf("AppIdleTimeout after saver: want 3m, got %v", m.AppIdleTimeout())
	}
}

func TestManager_SetMode_SaverToPerformance(t *testing.T) {
	m := NewManager(ModeSaver)
	m.SetMode(ModePerformance)

	s := m.State()
	if s.Mode != ModePerformance {
		t.Errorf("after SetMode(performance): want performance, got %s", s.Mode)
	}
	if m.AppIdleTimeout() != 0 {
		t.Errorf("AppIdleTimeout after performance: want 0 (never), got %v", m.AppIdleTimeout())
	}
}

func TestManager_SetMode_Balanced(t *testing.T) {
	m := NewManager(ModePerformance)
	m.SetMode(ModeBalanced)

	s := m.State()
	if s.Mode != ModeBalanced {
		t.Errorf("after SetMode(balanced): want balanced, got %s", s.Mode)
	}
	if m.AppIdleTimeout() != 10*time.Minute {
		t.Errorf("AppIdleTimeout after balanced: want 10m, got %v", m.AppIdleTimeout())
	}
}

// ---------------------------------------------------------------------------
// Manager.AppIdleTimeout — per-mode values
// ---------------------------------------------------------------------------

func TestManager_AppIdleTimeout_AllModes(t *testing.T) {
	cases := []struct {
		mode Mode
		want time.Duration
	}{
		{ModePerformance, 0},
		{ModeBalanced, 10 * time.Minute},
		{ModeSaver, 3 * time.Minute},
	}

	for _, tc := range cases {
		m := NewManager(tc.mode)
		got := m.AppIdleTimeout()
		if got != tc.want {
			t.Errorf("AppIdleTimeout(%s): want %v, got %v", tc.mode, tc.want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Manager.ResetIdle — state machine: idle reset restores screen state
// ---------------------------------------------------------------------------

func TestManager_ResetIdle_RestoresBrightness(t *testing.T) {
	m := NewManager(ModeBalanced)

	// Manually force screen into dimmed state
	m.mu.Lock()
	m.state.ScreenDimmed = true
	m.state.ScreenBrightness = 30
	m.mu.Unlock()

	m.ResetIdle()

	s := m.State()
	if s.ScreenDimmed {
		t.Error("ScreenDimmed should be false after ResetIdle")
	}
	if s.ScreenBrightness != 100 {
		t.Errorf("ScreenBrightness should be 100 after ResetIdle, got %d", s.ScreenBrightness)
	}
	if s.SuspendReady {
		t.Error("SuspendReady should be false after ResetIdle")
	}
}

func TestManager_ResetIdle_WhenScreenOff(t *testing.T) {
	m := NewManager(ModeBalanced)

	// Manually force screen off
	m.mu.Lock()
	m.state.ScreenOn = false
	m.state.ScreenBrightness = 0
	m.mu.Unlock()

	m.ResetIdle()

	s := m.State()
	if !s.ScreenOn {
		t.Error("ScreenOn should be true after ResetIdle")
	}
	if s.ScreenBrightness != 100 {
		t.Errorf("ScreenBrightness should be 100 after ResetIdle, got %d", s.ScreenBrightness)
	}
}

// ---------------------------------------------------------------------------
// OnStateChange — callback is fired on mode change
// ---------------------------------------------------------------------------

func TestManager_OnStateChange_FiredOnSetMode(t *testing.T) {
	m := NewManager(ModeBalanced)

	var received []State
	m.OnStateChange(func(s State) {
		received = append(received, s)
	})

	m.SetMode(ModePerformance)
	m.SetMode(ModeSaver)

	if len(received) != 2 {
		t.Fatalf("expected 2 state-change callbacks, got %d", len(received))
	}
	if received[0].Mode != ModePerformance {
		t.Errorf("first callback mode: want performance, got %s", received[0].Mode)
	}
	if received[1].Mode != ModeSaver {
		t.Errorf("second callback mode: want saver, got %s", received[1].Mode)
	}
}

func TestManager_OnStateChange_FiredOnResetIdle(t *testing.T) {
	m := NewManager(ModeBalanced)

	callCount := 0
	m.OnStateChange(func(s State) { callCount++ })

	m.ResetIdle()

	if callCount != 1 {
		t.Errorf("expected 1 callback after ResetIdle, got %d", callCount)
	}
}

// ---------------------------------------------------------------------------
// fmtDuration helper
// ---------------------------------------------------------------------------

func TestFmtDuration_Zero(t *testing.T) {
	if got := fmtDuration(0); got != "never" {
		t.Errorf("fmtDuration(0): want \"never\", got %q", got)
	}
}

func TestFmtDuration_NonZero(t *testing.T) {
	if got := fmtDuration(5 * time.Minute); got != "5m0s" {
		t.Errorf("fmtDuration(5m): want \"5m0s\", got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Config.MarshalJSON — "never" for zero durations
// ---------------------------------------------------------------------------

func TestConfig_MarshalJSON_ZeroAreNever(t *testing.T) {
	cfg := DefaultConfig(ModePerformance) // SuspendAfter == 0, AppIdle == 0
	data, err := cfg.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON error: %v", err)
	}
	s := string(data)
	for _, marker := range []string{`"suspend_after":"never"`, `"app_idle":"never"`} {
		if !contains(s, marker) {
			t.Errorf("expected %s in JSON output, got: %s", marker, s)
		}
	}
}

func TestConfig_MarshalJSON_NonZeroContainsDuration(t *testing.T) {
	cfg := DefaultConfig(ModeBalanced)
	data, err := cfg.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON error: %v", err)
	}
	s := string(data)
	if !contains(s, `"dim_after":"2m0s"`) {
		t.Errorf("expected dim_after=2m0s in JSON, got: %s", s)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

// ---------------------------------------------------------------------------
// DPMS honesty — reported state must match what actually happened
// ---------------------------------------------------------------------------

// When screen power control is unavailable (wlopm missing, or a cage session
// that does not implement wlr-output-power-management-v1), the screen stays
// lit. The reported state must stay lit too. This used to fail silently:
// setDPMS discarded its error, so ScreenOn went false and /api/energy told a
// client the display was off while the user was looking at it.
func TestTick_ScreenOffFails_StateDoesNotClaimScreenIsOff(t *testing.T) {
	orig := dpmsSetter
	dpmsSetter = func(bool) error { return errors.New("no DPMS control: wlopm not found") }
	defer func() { dpmsSetter = orig }()

	m := NewManager(ModeSaver)
	m.mu.Lock()
	m.idleStart = time.Now().Add(-1 * time.Hour) // well past ScreenOff
	m.mu.Unlock()

	m.tick()

	if st := m.State(); !st.ScreenOn {
		t.Fatal("ScreenOn is false after the DPMS call failed — the state claims " +
			"a screen that nothing turned off is off")
	}
	if st := m.State(); st.ScreenBrightness == 0 {
		t.Errorf("ScreenBrightness = 0 after a failed screen-off, want non-zero")
	}
}

// The success path must still report the screen as off.
func TestTick_ScreenOffSucceeds_StateReportsOff(t *testing.T) {
	orig := dpmsSetter
	var gotOn bool
	var called bool
	dpmsSetter = func(on bool) error { called, gotOn = true, on; return nil }
	defer func() { dpmsSetter = orig }()

	m := NewManager(ModeSaver)
	m.mu.Lock()
	m.idleStart = time.Now().Add(-1 * time.Hour)
	m.mu.Unlock()

	m.tick()

	if !called {
		t.Fatal("expected a DPMS call")
	}
	if gotOn {
		t.Error("expected setDPMS(false) for screen-off")
	}
	if st := m.State(); st.ScreenOn {
		t.Error("ScreenOn is true after a successful screen-off")
	}
}
