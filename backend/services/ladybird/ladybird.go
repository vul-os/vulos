// Package ladybird is an experimental spike for the Ladybird headless browser engine.
//
// STATUS: EXPERIMENTAL — feature-flag gated, off by default.
//
// # Why Ladybird?
//
// Chromium idles at 300-500 MB and copies frames three times
// (compositor → X11 shared memory → GStreamer capture). Ladybird's headless
// WebContent renderer targets a direct offscreen framebuffer path that could
// yield a near zero-copy pipeline into GStreamer appsrc — encoding only on
// dirty frames instead of polling a virtual display.
//
// See roadmap/future/LADYBIRD-BROWSER.md for the full analysis and TODO list.
//
// # Current state (spike / 2026-05-16)
//
// Ladybird is targeting a late-2026 alpha; the headless FramebufferBackend API
// is not yet stable and web-compat limits production use until 2027+. This
// package deliberately does *not* wire itself into main.go or modify any
// existing service. It exists to:
//
//  1. Define the integration seam (same stream.Pool / LaunchOpts pattern as
//     webbrowser/chrome.go) so the eventual migration is a small diff.
//  2. Validate that the binary detection and fallback paths compile and behave
//     correctly before the engine is available.
//  3. Expose EngineStatus() for a future /api/browser/status response field.
//
// # Wiring (deliberate follow-up)
//
// The orchestrator (main.go) is NOT modified by this package. To enable:
//  1. Set env var VULOS_LADYBIRD_ENGINE=1 at runtime.
//  2. Ensure a `ladybird` binary is on $PATH (or LADYBIRD_BIN points to it).
//  3. In main.go, instantiate ladybird.New(pool) and call Start().
//     Swap or run alongside webbrowser.Service — that toggle lives in Settings
//     and is a deliberate follow-up task.
//
// # Findings from the spike
//
//   - Ladybird's headless mode currently surfaces via `WebContent --headless` and
//     `RequestServer` sub-processes. The stable interface for piping a raw
//     framebuffer out of LibWeb is still in flux (tracked in TODO list above).
//   - Input injection: LibWeb accepts synthetic events via an IPC socket; X11 is
//     not required, which removes the xdotool / X11 round-trip from the latency
//     budget.
//   - The stream.Pool LaunchOpts seam fits naturally: Command=ladybird binary,
//     Args=URL + headless flags, Width/Height/FPS unchanged. GStreamer's appsrc
//     will need to replace ximagesrc once LibWeb's FramebufferBackend is stable.
//   - Docker image: removing Chromium (~400 MB) + Xvfb + X11 libs is a major win
//     that is blocked on Ladybird web-compat reaching production readiness.
package ladybird

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"vulos/backend/services/stream"
)

const (
	// EnvFeatureFlag is the environment variable that enables the Ladybird engine.
	// Set to "1" or "true" to opt in. Default is off.
	//
	// Example:
	//   VULOS_LADYBIRD_ENGINE=1 ./vulos-backend
	EnvFeatureFlag = "VULOS_LADYBIRD_ENGINE"

	// EnvBinaryPath allows overriding the ladybird binary location.
	// If unset, the service searches $PATH for "ladybird" and "WebContent".
	EnvBinaryPath = "LADYBIRD_BIN"

	sessionID = "ladybird"
	width     = 1280
	height    = 720
	fps       = 30
)

// EngineState describes the current status of the Ladybird engine integration.
type EngineState string

const (
	// EngineDisabled means the feature flag is off (default). No binary is
	// searched, no process is started.
	EngineDisabled EngineState = "disabled"

	// EngineMissing means the flag is on but no ladybird binary was found.
	// The service falls back cleanly; the Chrome engine continues to operate.
	EngineMissing EngineState = "missing"

	// EngineReady means the binary was found and the session is running.
	EngineReady EngineState = "ready"

	// EngineError means launch was attempted but failed.
	EngineError EngineState = "error"
)

// Status is returned by EngineStatus for a future /api/browser/status field.
type Status struct {
	// Engine is always "ladybird".
	Engine string `json:"engine"`
	// State is the current lifecycle state.
	State EngineState `json:"state"`
	// BinPath is the resolved binary path, empty when not found.
	BinPath string `json:"bin_path,omitempty"`
	// Message carries a human-readable explanation (e.g. fallback reason).
	Message string `json:"message,omitempty"`
	// SessionID is the stream.Pool session identifier when running.
	SessionID string `json:"session_id,omitempty"`
}

// Service is the experimental Ladybird engine manager.
// It follows the same structure as webbrowser.Service so the eventual
// migration to wiring it into main.go is a minimal diff.
//
// Instantiate with New(pool) and call Start(ctx, port). If the feature flag is
// off or the binary is absent the call returns immediately without error.
type Service struct {
	mu      sync.Mutex
	pool    *stream.Pool
	sess    *stream.Session
	state   EngineState
	binPath string
	lastErr string
}

// New creates a new (dormant) Ladybird service backed by the given stream pool.
// No binary search or process launch occurs until Start is called.
func New(pool *stream.Pool) *Service {
	return &Service{
		pool:  pool,
		state: EngineDisabled,
	}
}

// Start attempts to launch the Ladybird headless engine if the feature flag is
// enabled and a suitable binary is available.
//
// When the flag is off (default), Start is a no-op and returns nil — this is
// the intended production behaviour until Ladybird reaches web-compat parity.
//
// When the flag is on but the binary is absent, Start logs a clear fallback
// message and returns nil — callers may continue with the Chrome engine.
//
// The port parameter is reserved for future use (e.g. debug HTTP interface).
func (s *Service) Start(ctx context.Context, _ int) error {
	if !featureFlagEnabled() {
		s.mu.Lock()
		s.state = EngineDisabled
		s.mu.Unlock()
		log.Printf("[ladybird] engine disabled (set %s=1 to enable)", EnvFeatureFlag)
		return nil
	}

	bin := resolveBinary()
	s.mu.Lock()
	s.binPath = bin
	s.mu.Unlock()

	if bin == "" {
		s.mu.Lock()
		s.state = EngineMissing
		s.mu.Unlock()
		// Clean fallback: log context so operators understand the gap and
		// know what binary to install when Ladybird's alpha ships.
		log.Printf("[ladybird] binary not found — falling back to Chromium engine. "+
			"Install a Ladybird headless binary and set %s=/path/to/ladybird to enable. "+
			"Blocked on Ladybird alpha (targeting late 2026); see roadmap/future/LADYBIRD-BROWSER.md",
			EnvBinaryPath)
		return nil
	}

	return s.launch(ctx, bin)
}

func (s *Service) launch(ctx context.Context, bin string) error {
	// NOTE (spike): The headless flags below are placeholders.
	// Ladybird's stable CLI interface for framebuffer output is not yet defined.
	// Tracked in roadmap/future/LADYBIRD-BROWSER.md → "Track Ladybird headless mode API".
	//
	// When the API stabilises the args will need to enable the FramebufferBackend
	// and disable the X11 backend entirely. For now we use --headless as a best
	// guess based on WebContent process behaviour observed in WPT test runs.
	sess, err := s.pool.Launch(stream.LaunchOpts{
		ID:      sessionID,
		Name:    "Ladybird",
		Command: bin,
		Args: []string{
			// Headless mode — renders to an offscreen framebuffer.
			// NOTE: exact flag may change; monitor upstream CLI changes.
			"--headless",
			// Starting URL; mirrors chrome.go behaviour.
			"https://google.com",
		},
		Width:   width,
		Height:  height,
		FPS:     fps,
		Restart: false, // Restart disabled until the API is stable.
	})
	if err != nil {
		s.mu.Lock()
		s.state = EngineError
		s.lastErr = err.Error()
		s.mu.Unlock()
		// Return the error so the caller decides whether to fall back.
		return fmt.Errorf("[ladybird] stream launch failed: %w", err)
	}

	s.mu.Lock()
	s.sess = sess
	s.state = EngineReady
	s.mu.Unlock()
	log.Printf("[ladybird] session started (id=%s, display=%s, encoder=%s)",
		sess.ID, sess.Display, sess.Encoder)
	return nil
}

// EngineStatus returns a snapshot of the engine's current state.
// Intended for inclusion in a future /api/browser/status response alongside
// the Chromium status fields in webbrowser.Service.RegisterHandlers.
func (s *Service) EngineStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := Status{
		Engine:  "ladybird",
		State:   s.state,
		BinPath: s.binPath,
	}

	switch s.state {
	case EngineDisabled:
		st.Message = fmt.Sprintf("feature flag off — set %s=1 to enable", EnvFeatureFlag)
	case EngineMissing:
		st.Message = fmt.Sprintf("binary not found; set %s=/path/to/ladybird", EnvBinaryPath)
	case EngineReady:
		if s.sess != nil {
			st.SessionID = s.sess.ID
		}
	case EngineError:
		st.Message = s.lastErr
	}

	return st
}

// Stop shuts down the Ladybird session if one is running.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == EngineReady {
		s.pool.Stop(sessionID)
		s.state = EngineDisabled
		s.sess = nil
	}
}

// Running reports whether a Ladybird session is active.
func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == EngineReady
}

// WaitReady polls until the session is running or the deadline expires.
func (s *Service) WaitReady(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s.Running() {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// featureFlagEnabled returns true when VULOS_LADYBIRD_ENGINE is "1" or "true".
func featureFlagEnabled() bool {
	v := os.Getenv(EnvFeatureFlag)
	return v == "1" || v == "true"
}

// resolveBinary searches for the Ladybird headless binary.
// Checks LADYBIRD_BIN env var first, then common binary names on $PATH.
//
// Known binary names observed in upstream Ladybird builds:
//   - "ladybird" — expected stable name for the headless runner
//   - "WebContent" — the internal renderer process; not a public CLI
//
// When the stable CLI lands (tracked in the roadmap), this list should be
// updated to match the canonical binary name from the distribution package.
func resolveBinary() string {
	// Explicit override takes priority.
	if p := os.Getenv(EnvBinaryPath); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		log.Printf("[ladybird] %s=%q not found on disk — falling back to PATH search", EnvBinaryPath, p)
	}

	// Search PATH for known binary names.
	for _, name := range []string{"ladybird", "WebContent"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}
