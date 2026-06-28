// Package gpuhost implements the BYO GPU streaming-host slice (STREAM-BYO-01).
//
// On a self-host box equipped with a GPU, gpuhost runs a low-latency WebRTC
// streaming server (the external "streamer" binary — Sunshine / Moonlight
// host or any other NVENC+WebRTC daemon) and registers the box with the
// Vulos fabric so a remote client can discover it and negotiate a session.
//
// What this package does:
//
//   - Generates the external streamer's config file (config.go).
//   - Supervises the external streamer process: launches it, watches stderr,
//     restarts on failure with backoff, drains cleanly on Stop (supervisor.go).
//   - Registers the box as a streaming HOST with the relay's HTTPS fabric
//     endpoint using the OS's existing fabric identity (Ed25519). Keeps the
//     registration alive with periodic heartbeats (fabric.go).
//   - Chooses P2P first; only falls back to the relay's TURN signal on
//     explicit P2P failure (signal.go).
//   - Exposes a small HTTP status surface so the cloud session orchestrator
//     (STREAM-SIGNAL-01) and local probes can read readiness (status.go).
//
// What this package does NOT do:
//
//   - It does NOT reimplement the streaming server. The wire/codec/NVENC path
//     belongs to the external binary; we only generate its config, launch it,
//     and watch it.
//   - It does NOT import vulos-relay's Go code. The relay's VULOS-STREAM/1
//     sub-protocol is reached via the HTTPS registration endpoint described
//     by fabricEndpoints (defined in fabric.go); the wire shape is a small,
//     stable JSON contract.
//   - It does NOT manage any user-level UI or app-store install of the
//     streamer; the operator installs the binary out-of-band.
//
// Opt-in: gpuhost is off by default. The OS server creates a Service only
// when Enabled returns true — either VULOS_GPU_HOST=1 in the environment, or
// the Config.Enabled flag set by the dashboard. Most boxes don't have a GPU
// and never instantiate this package.
package gpuhost

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// Service is the box-side gpuhost runtime: supervisor + fabric registration +
// status surface. One Service per box; safe for concurrent use.
type Service struct {
	cfg Config

	// fabric is the registration client (HTTPS-backed in production, a stub in
	// tests). Constructed in New from cfg.RelayBaseURL + cfg.Identity.
	fabric fabricClient

	// supervisor runs the external streamer binary. Nil until Start.
	sup *Supervisor

	// chooser implements the P2P-preferred / relay-fallback decision.
	chooser *PathChooser

	mu      sync.Mutex
	started bool
	state   State
	lastErr error
	stopHB  context.CancelFunc // heartbeat cancel
	ready   bool
}

// State is the coarse readiness state exposed via the status endpoint.
type State string

const (
	StateStopped      State = "stopped"
	StateStarting     State = "starting"
	StateReady        State = "ready"
	StateDegraded     State = "degraded" // supervisor or fabric in error, but still trying
	StateUnregistered State = "unregistered"
)

// Enabled reports whether gpuhost is opted-in for this process.
//
// Truthy means: VULOS_GPU_HOST is set to "1", "true", or "yes" (case-insensitive).
// The dashboard may persist its own setting; that's the caller's concern.
// gpuhost itself only consults the env so a one-line wiring is possible from
// the server bootstrap.
func Enabled() bool {
	v := os.Getenv("VULOS_GPU_HOST")
	switch v {
	case "1", "true", "TRUE", "True", "yes", "YES", "Yes":
		return true
	}
	return false
}

// New constructs a Service from cfg. It does NOT start the supervisor or
// contact the fabric; call Start for that. New returns an error if the config
// is structurally invalid (missing identity, missing streamer path).
func New(cfg Config) (*Service, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cfg.applyDefaults()

	svc := &Service{
		cfg:     cfg,
		state:   StateStopped,
		chooser: NewPathChooser(cfg.P2PProbeTimeout),
	}

	// fabricClient is overridable by tests via cfg.fabricOverride.
	if cfg.fabricOverride != nil {
		svc.fabric = cfg.fabricOverride
	} else {
		svc.fabric = newHTTPFabricClient(cfg.RelayBaseURL, cfg.Identity, cfg.HTTPClient)
	}

	return svc, nil
}

// Start launches the external streamer (under the supervisor) and registers
// the box with the fabric. It returns once the supervisor has spawned the
// process and the first registration POST has succeeded (or, if the fabric
// is unreachable, Start still returns nil but the Service enters
// StateUnregistered and keeps retrying — the supervisor remains running so
// LAN clients can still connect).
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("gpuhost: already started")
	}
	s.started = true
	s.state = StateStarting
	s.mu.Unlock()

	// Write streamer config to disk first so the process has it on first read.
	confPath, err := s.cfg.writeStreamerConfig()
	if err != nil {
		s.fail(fmt.Errorf("write streamer config: %w", err))
		return err
	}

	sup := NewSupervisor(s.cfg.StreamerBinary, append(s.cfg.StreamerArgs, "--config", confPath), s.cfg.RestartBackoff)
	s.mu.Lock()
	s.sup = sup
	s.mu.Unlock()
	if err := sup.Start(ctx); err != nil {
		s.fail(fmt.Errorf("start supervisor: %w", err))
		return err
	}

	// First registration is best-effort; surface success/failure into state.
	regCtx, cancel := context.WithTimeout(ctx, s.cfg.RegisterTimeout)
	err = s.fabric.Register(regCtx, s.registration())
	cancel()
	s.mu.Lock()
	if err != nil {
		s.state = StateUnregistered
		s.lastErr = err
		log.Printf("[gpuhost] initial fabric registration failed: %v (will retry)", err)
	} else {
		s.ready = true
		s.state = StateReady
	}
	s.mu.Unlock()

	// Spin up the heartbeat loop; it re-registers and probes liveness so the
	// fabric prunes us on crash. The loop owns its own context so Stop can cut
	// it independently of ctx.
	hbCtx, hbCancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.stopHB = hbCancel
	s.mu.Unlock()
	go s.heartbeatLoop(hbCtx)

	return nil
}

// Stop drains the supervisor and deregisters from the fabric. It is safe to
// call even if Start failed partway. Returns the first error encountered;
// individual sub-shutdowns always run.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = false
	stopHB := s.stopHB
	sup := s.sup
	s.stopHB = nil
	s.sup = nil
	s.state = StateStopped
	s.ready = false
	s.mu.Unlock()

	var firstErr error
	if stopHB != nil {
		stopHB()
	}
	if sup != nil {
		if err := sup.Stop(ctx); err != nil {
			firstErr = err
		}
	}
	// Best-effort deregister: a missed deregister is not an error condition
	// for the operator (the fabric expires stale hosts on its own).
	if err := s.fabric.Deregister(ctx, s.cfg.Identity.HostID); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// State returns the current coarse readiness state.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// LastError returns the last non-nil error the Service recorded (registration,
// supervisor crash, config write). It is informational; callers should not
// retry based on it directly.
func (s *Service) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Ready reports whether the box is currently registered with the fabric AND
// the streamer process is alive.
func (s *Service) Ready() bool {
	s.mu.Lock()
	sup := s.sup
	ready := s.ready
	s.mu.Unlock()
	if !ready {
		return false
	}
	if sup == nil || !sup.Alive() {
		return false
	}
	return true
}

// fail records err and sets state to degraded under the lock.
func (s *Service) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err
	s.state = StateDegraded
	s.ready = false
}

// registration is the snapshot the fabric receives at register/heartbeat time.
func (s *Service) registration() HostRegistration {
	return HostRegistration{
		HostID:       s.cfg.Identity.HostID,
		PublicKeyB64: s.cfg.Identity.PublicKeyB64,
		Domain:       s.cfg.Identity.Domain,
		Hostname:     s.cfg.AdvertiseHostname,
		Port:         s.cfg.AdvertisePort,
		Capabilities: HostCapabilities{
			Codec:       s.cfg.Codec,
			MaxBitrate:  s.cfg.MaxBitrateKbps,
			GPUVendor:   s.cfg.GPUVendor,
			HasHardware: s.cfg.HasHardwareEncoder,
		},
	}
}

// heartbeatLoop re-registers periodically. If the fabric is unreachable, the
// loop transitions us to StateUnregistered (or StateDegraded if the supervisor
// is also unhealthy) and keeps trying.
func (s *Service) heartbeatLoop(ctx context.Context) {
	interval := s.cfg.HeartbeatInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tickHeartbeat(ctx)
		}
	}
}

// tickHeartbeat performs one registration attempt and updates state.
func (s *Service) tickHeartbeat(ctx context.Context) {
	hbCtx, cancel := context.WithTimeout(ctx, s.cfg.RegisterTimeout)
	defer cancel()
	err := s.fabric.Heartbeat(hbCtx, s.registration())

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastErr = err
		s.ready = false
		if s.sup != nil && s.sup.Alive() {
			s.state = StateUnregistered
		} else {
			s.state = StateDegraded
		}
		return
	}
	if s.sup != nil && s.sup.Alive() {
		s.ready = true
		s.state = StateReady
	} else {
		s.ready = false
		s.state = StateDegraded
	}
}

// PathChooserForTesting exposes the internal chooser to package tests.
func (s *Service) PathChooserForTesting() *PathChooser { return s.chooser }

// StatusHandler returns an http.Handler that serves the JSON status endpoint
// (see status.go). Wired by the OS server under /api/gpuhost/status.
func (s *Service) StatusHandler() http.Handler { return statusHandler{svc: s} }
