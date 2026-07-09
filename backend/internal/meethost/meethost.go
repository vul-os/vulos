// Package meethost implements the BYO / self-host SFU-host slice (Vulos Meet SFU
// Phase 2).
//
// On a box that wants to run BIG calls on its OWN infrastructure, meethost
// registers the box's SFU with the Vulos relay fabric so the allocation step can
// route an escalated (past-the-mesh-cap) call to it. It is the SFU analogue of
// gpuhost (the GPU streaming-host slice) and is a near-copy of it: same
// Enabled()+New()+Start()+heartbeat shape, same HTTPS registration contract, and
// the relay verifies the advertised endpoint with the SAME SSRF-guarded
// directprobe verifier it already runs for tunnel fast-paths.
//
// What this package does:
//
//   - Registers the box as an available SFU host with the relay's /api/meet/host/*
//     registry and keeps it alive with periodic heartbeats (fabric.go).
//   - OPTIONALLY supervises an external SFU worker binary (a co-located vulos-meet
//     LiveKit server) when Config.WorkerBinary is set. When it is EMPTY, the SFU is
//     assumed to run IN-PROCESS in the OS binary (the Pion SFU under
//     backend/services/peering/sfu) and meethost only registers + heartbeats.
//   - Exposes a small HTTP status surface (status.go) for dashboards/probes.
//
// What this package does NOT do:
//
//   - It does NOT implement an SFU. The media engine is the in-process Pion SFU or
//     the external vulos-meet binary; meethost only advertises + supervises.
//   - It does NOT import vulos-relay's Go code; the registry is reached over the
//     stable HTTPS/JSON contract in fabric.go.
//   - E2EE: none is needed here. A self-host / BYO SFU is the operator's own media
//     node — there is no third party to be blind to (SFU.md §4, LOCKED). So
//     Capabilities.HasE2EE is false by default and recording/transcription run on
//     the operator's own box.
//
// Opt-in: meethost is off by default. The OS server creates a Service only when
// Enabled() returns true (VULOS_SFU_HOST=1) or the dashboard flag is set. Behavior
// is unchanged for a box that does not opt in.
package meethost

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"vulos/backend/internal/gpuhost"
)

// Service is the box-side meethost runtime: optional worker supervisor + fabric
// registration + status surface. One Service per box; safe for concurrent use.
type Service struct {
	cfg Config

	// fabric is the registration client (HTTPS in production, a stub in tests).
	fabric fabricClient

	// sup supervises an external SFU worker binary. Nil when the SFU runs
	// in-process (Config.WorkerBinary empty) — the common self-host case.
	sup *gpuhost.Supervisor

	mu      sync.Mutex
	started bool
	state   State
	lastErr error
	stopHB  context.CancelFunc // heartbeat cancel
	ready   bool
	inProc  bool // true when there is no external worker (SFU in-process)
}

// State is the coarse readiness state exposed via the status endpoint.
type State string

const (
	StateStopped      State = "stopped"
	StateStarting     State = "starting"
	StateReady        State = "ready"
	StateDegraded     State = "degraded"
	StateUnregistered State = "unregistered"
)

// Enabled reports whether meethost is opted-in for this process. Truthy means
// VULOS_SFU_HOST is set to "1"/"true"/"yes" (case-insensitive).
func Enabled() bool {
	switch os.Getenv("VULOS_SFU_HOST") {
	case "1", "true", "TRUE", "True", "yes", "YES", "Yes":
		return true
	}
	return false
}

// New constructs a Service from cfg. It does NOT start the supervisor or contact
// the relay; call Start for that. New returns an error if cfg is invalid.
func New(cfg Config) (*Service, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cfg.applyDefaults()

	svc := &Service{
		cfg:    cfg,
		state:  StateStopped,
		inProc: cfg.WorkerBinary == "",
	}
	if cfg.fabricOverride != nil {
		svc.fabric = cfg.fabricOverride
	} else {
		svc.fabric = newHTTPFabricClient(cfg.RelayBaseURL, cfg.Token, cfg.HTTPClient)
	}
	return svc, nil
}

// Start supervises the external SFU worker (if any) and registers the box with the
// relay. It returns once the first registration attempt completes; a failed
// registration is non-fatal (StateUnregistered, keeps retrying) so the co-located
// SFU still serves LAN/direct clients.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("meethost: already started")
	}
	s.started = true
	s.state = StateStarting
	s.mu.Unlock()

	// Supervise the external worker only when one is configured.
	if s.cfg.WorkerBinary != "" {
		sup := gpuhost.NewSupervisor(s.cfg.WorkerBinary, s.cfg.WorkerArgs, s.cfg.RestartBackoff)
		s.mu.Lock()
		s.sup = sup
		s.mu.Unlock()
		if err := sup.Start(ctx); err != nil {
			s.fail(fmt.Errorf("start SFU worker: %w", err))
			return err
		}
	}

	// First registration is best-effort; surface success/failure into state.
	regCtx, cancel := context.WithTimeout(ctx, s.cfg.RegisterTimeout)
	err := s.fabric.Register(regCtx, s.registration())
	cancel()
	s.mu.Lock()
	if err != nil {
		s.state = StateUnregistered
		s.lastErr = err
		log.Printf("[meethost] initial registration failed: %v (will retry)", err)
	} else {
		s.ready = true
		s.state = StateReady
	}
	s.mu.Unlock()

	// Heartbeat loop keeps the registry entry alive so the relay prunes us on crash.
	hbCtx, hbCancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.stopHB = hbCancel
	s.mu.Unlock()
	go s.heartbeatLoop(hbCtx)

	return nil
}

// Stop drains the supervisor (if any) and deregisters from the relay. Safe to call
// even if Start failed partway. Returns the first error; sub-shutdowns always run.
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
	// Best-effort deregister: a missed one is not an error (the relay TTL-prunes).
	if err := s.fabric.Deregister(ctx, s.registration()); err != nil && firstErr == nil {
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

// LastError returns the last non-nil error recorded (registration / supervisor).
func (s *Service) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Ready reports whether the box is registered AND (if an external worker is
// configured) the worker process is alive. With an in-process SFU there is no
// worker to check, so readiness is just "registered".
func (s *Service) Ready() bool {
	s.mu.Lock()
	sup := s.sup
	ready := s.ready
	inProc := s.inProc
	s.mu.Unlock()
	if !ready {
		return false
	}
	if inProc {
		return true
	}
	return sup != nil && sup.Alive()
}

func (s *Service) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err
	s.state = StateDegraded
	s.ready = false
}

// registration is the snapshot the relay receives at register/heartbeat time.
func (s *Service) registration() HostRegistration {
	return HostRegistration{
		HostID:       s.cfg.Identity.HostID,
		PublicKeyB64: s.cfg.Identity.PublicKeyB64,
		Domain:       s.cfg.Identity.Domain,
		Name:         s.cfg.Name,
		Endpoint:     s.cfg.Endpoint,
		Capabilities: s.cfg.Capabilities,
	}
}

// heartbeatLoop re-POSTs periodically. On relay unreachability it transitions to
// StateUnregistered (or StateDegraded if an external worker is also unhealthy) and
// keeps trying. A relay 404 (host pruned) is handled by re-registering.
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

// tickHeartbeat performs one heartbeat and updates state. If the heartbeat fails
// (e.g. the relay pruned us on a prior miss, returning 404), it re-registers so
// the endpoint is re-verified and the host re-appears in the registry.
func (s *Service) tickHeartbeat(ctx context.Context) {
	hbCtx, cancel := context.WithTimeout(ctx, s.cfg.RegisterTimeout)
	defer cancel()
	err := s.fabric.Heartbeat(hbCtx, s.registration())
	if err != nil {
		// Heartbeat failed — try a full re-register (covers a TTL-pruned host that
		// needs re-verification). Best-effort; state reflects the outcome below.
		rerr := s.fabric.Register(hbCtx, s.registration())
		if rerr == nil {
			err = nil
		} else {
			err = rerr
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	workerOK := s.inProc || (s.sup != nil && s.sup.Alive())
	if err != nil {
		s.lastErr = err
		s.ready = false
		if workerOK {
			s.state = StateUnregistered
		} else {
			s.state = StateDegraded
		}
		return
	}
	if workerOK {
		s.ready = true
		s.state = StateReady
	} else {
		s.ready = false
		s.state = StateDegraded
	}
}

// StatusHandler returns an http.Handler serving the JSON status endpoint (see
// status.go). Wired by the OS server under /api/meethost/status.
func (s *Service) StatusHandler() http.Handler { return statusHandler{svc: s} }
