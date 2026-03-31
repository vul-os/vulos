package energy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mode defines the power profile.
type Mode string

const (
	ModePerformance Mode = "performance"
	ModeBalanced    Mode = "balanced"
	ModeSaver       Mode = "saver"
)

// State is the full energy management state exposed to the frontend.
type State struct {
	Mode             Mode      `json:"mode"`
	ScreenOn         bool      `json:"screen_on"`
	ScreenDimmed     bool      `json:"screen_dimmed"`
	ScreenBrightness int       `json:"screen_brightness"` // 0-100
	IdleSince        time.Time `json:"idle_since"`
	IdleDuration     string    `json:"idle_duration"`
	BatteryPercent   int       `json:"battery_percent"`
	BatteryCharging  bool      `json:"battery_charging"`
	CPUGovernor      string    `json:"cpu_governor"`
	SuspendReady     bool      `json:"suspend_ready"`
}

// Config holds energy policy thresholds.
type Config struct {
	DimAfter     time.Duration `json:"dim_after"`     // dim screen after idle
	ScreenOff    time.Duration `json:"screen_off"`    // turn off screen after idle
	SuspendAfter time.Duration `json:"suspend_after"` // suspend system after idle
	AppIdle      time.Duration `json:"app_idle"`      // kill idle apps after this
}

func DefaultConfig(mode Mode) Config {
	switch mode {
	case ModePerformance:
		return Config{
			DimAfter: 10 * time.Minute, ScreenOff: 30 * time.Minute,
			SuspendAfter: 0, AppIdle: 0, // never
		}
	case ModeSaver:
		return Config{
			DimAfter: 30 * time.Second, ScreenOff: 2 * time.Minute,
			SuspendAfter: 5 * time.Minute, AppIdle: 3 * time.Minute,
		}
	default: // balanced
		return Config{
			DimAfter: 2 * time.Minute, ScreenOff: 5 * time.Minute,
			SuspendAfter: 15 * time.Minute, AppIdle: 10 * time.Minute,
		}
	}
}

// Manager handles display, CPU governor, idle tracking, and suspend.
type Manager struct {
	mu        sync.Mutex
	mode      Mode
	cfg       Config
	state     State
	idleStart time.Time
	listeners []func(State) // state change callbacks
}

func NewManager(mode Mode) *Manager {
	cfg := DefaultConfig(mode)
	now := time.Now()
	m := &Manager{
		mode:      mode,
		cfg:       cfg,
		idleStart: now,
		state: State{
			Mode: mode, ScreenOn: true, ScreenBrightness: 100,
			IdleSince: now, CPUGovernor: "schedutil",
		},
	}
	return m
}

// SetMode changes the power profile.
func (m *Manager) SetMode(mode Mode) {
	m.mu.Lock()
	m.mode = mode
	m.cfg = DefaultConfig(mode)
	m.state.Mode = mode
	m.mu.Unlock()

	m.applyCPUGovernor()
	m.notify()
	log.Printf("[energy] mode changed to %s", mode)
}

// ResetIdle should be called on any user interaction (input, touch, etc).
func (m *Manager) ResetIdle() {
	m.mu.Lock()
	wasOff := !m.state.ScreenOn
	wasDimmed := m.state.ScreenDimmed
	m.idleStart = time.Now()
	m.state.IdleSince = m.idleStart
	m.state.ScreenOn = true
	m.state.ScreenDimmed = false
	m.state.ScreenBrightness = 100
	m.state.SuspendReady = false
	m.mu.Unlock()

	if wasOff {
		setBrightness(100)
		setDPMS(true)
		log.Printf("[energy] screen woke up")
	} else if wasDimmed {
		setBrightness(100)
	}
	m.notify()
}

// Run is the main energy loop — checks idle state and acts.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Apply CPU governor on start
	m.applyCPUGovernor()
	m.readBattery()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick()
		}
	}
}

func (m *Manager) tick() {
	m.mu.Lock()
	idle := time.Since(m.idleStart)
	m.state.IdleDuration = idle.Truncate(time.Second).String()
	cfg := m.cfg
	changed := false
	m.mu.Unlock()

	// Read battery periodically
	m.readBattery()

	// Dim screen
	if cfg.DimAfter > 0 && idle > cfg.DimAfter {
		m.mu.Lock()
		if !m.state.ScreenDimmed && m.state.ScreenOn {
			m.state.ScreenDimmed = true
			m.state.ScreenBrightness = 30
			changed = true
		}
		m.mu.Unlock()
		if changed {
			setBrightness(30)
			log.Printf("[energy] screen dimmed (idle %s)", idle.Truncate(time.Second))
		}
	}

	// Screen off
	if cfg.ScreenOff > 0 && idle > cfg.ScreenOff {
		m.mu.Lock()
		if m.state.ScreenOn {
			m.state.ScreenOn = false
			m.state.ScreenBrightness = 0
			changed = true
		}
		m.mu.Unlock()
		if changed {
			setDPMS(false)
			log.Printf("[energy] screen off (idle %s)", idle.Truncate(time.Second))
		}
	}

	// Suspend readiness
	if cfg.SuspendAfter > 0 && idle > cfg.SuspendAfter {
		m.mu.Lock()
		if !m.state.SuspendReady {
			m.state.SuspendReady = true
			changed = true
		}
		m.mu.Unlock()
		if changed {
			log.Printf("[energy] suspend ready (idle %s) — writing to /sys/power/state", idle.Truncate(time.Second))
			suspend()
		}
	}

	if changed {
		m.notify()
	}
}

// State returns the current energy state.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state
	s.IdleDuration = time.Since(m.idleStart).Truncate(time.Second).String()
	return s
}

// AppIdleTimeout returns the configured idle timeout for apps (for traffic monitor).
func (m *Manager) AppIdleTimeout() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.AppIdle
}

// OnStateChange registers a callback for state changes.
func (m *Manager) OnStateChange(fn func(State)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listeners = append(m.listeners, fn)
}

func (m *Manager) notify() {
	m.mu.Lock()
	s := m.state
	fns := m.listeners
	m.mu.Unlock()
	for _, fn := range fns {
		fn(s)
	}
}

func (m *Manager) readBattery() {
	pct, charging := readBatteryStatus()
	m.mu.Lock()
	m.state.BatteryPercent = pct
	m.state.BatteryCharging = charging
	m.mu.Unlock()

	// Auto-switch to saver at low battery
	if pct > 0 && pct <= 15 && !charging {
		m.mu.Lock()
		if m.mode != ModeSaver {
			m.mu.Unlock()
			m.SetMode(ModeSaver)
			log.Printf("[energy] auto-switched to saver (battery %d%%)", pct)
			return
		}
		m.mu.Unlock()
	}
}

func (m *Manager) applyCPUGovernor() {
	var gov string
	switch m.mode {
	case ModePerformance:
		gov = "performance"
	case ModeSaver:
		gov = "powersave"
	default:
		gov = "schedutil"
	}

	m.mu.Lock()
	m.state.CPUGovernor = gov
	m.mu.Unlock()

	// Apply to all CPU cores
	matches, _ := filepath.Glob("/sys/devices/system/cpu/cpu*/cpufreq/scaling_governor")
	for _, path := range matches {
		os.WriteFile(path, []byte(gov), 0644)
	}
}

// --- System interactions (Linux specific) ---

func setBrightness(percent int) {
	// Try backlight via sysfs
	matches, _ := filepath.Glob("/sys/class/backlight/*/max_brightness")
	for _, maxPath := range matches {
		data, err := os.ReadFile(maxPath)
		if err != nil {
			continue
		}
		maxVal, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if maxVal == 0 {
			continue
		}
		target := maxVal * percent / 100
		brightPath := filepath.Join(filepath.Dir(maxPath), "brightness")
		os.WriteFile(brightPath, []byte(strconv.Itoa(target)), 0644)
	}
}

func setDPMS(on bool) {
	// For Wayland/Cage — toggle via wlopm if available
	state := "off"
	if on {
		state = "on"
	}
	exec.Command("wlopm", "--"+state, "*").Run()
}

func suspend() {
	os.WriteFile("/sys/power/state", []byte("mem"), 0644)
}

func readBatteryStatus() (percent int, charging bool) {
	base := "/sys/class/power_supply"
	entries, err := os.ReadDir(base)
	if err != nil {
		return -1, false
	}
	for _, e := range entries {
		typePath := filepath.Join(base, e.Name(), "type")
		data, err := os.ReadFile(typePath)
		if err != nil || strings.TrimSpace(string(data)) != "Battery" {
			continue
		}

		capPath := filepath.Join(base, e.Name(), "capacity")
		if capData, err := os.ReadFile(capPath); err == nil {
			percent, _ = strconv.Atoi(strings.TrimSpace(string(capData)))
		}

		statusPath := filepath.Join(base, e.Name(), "status")
		if statusData, err := os.ReadFile(statusPath); err == nil {
			s := strings.TrimSpace(string(statusData))
			charging = s == "Charging" || s == "Full"
		}
		return
	}
	return -1, false
}

// MarshalJSON for Config.
func (c Config) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		DimAfter     string `json:"dim_after"`
		ScreenOff    string `json:"screen_off"`
		SuspendAfter string `json:"suspend_after"`
		AppIdle      string `json:"app_idle"`
	}{
		DimAfter:     fmtDuration(c.DimAfter),
		ScreenOff:    fmtDuration(c.ScreenOff),
		SuspendAfter: fmtDuration(c.SuspendAfter),
		AppIdle:      fmtDuration(c.AppIdle),
	})
}

func fmtDuration(d time.Duration) string {
	if d == 0 {
		return "never"
	}
	return d.String()
}

func init() {
	_ = fmt.Sprintf // keep import
}
