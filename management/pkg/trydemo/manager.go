package trydemo

import (
	"context"
	"log"
	"sync"
	"time"
)

// Manager wires Store + DemoMachines + Queue + CostGuard. Created once at
// startup, passed into the routes_trydemo.go register function.
// All exported methods are safe for concurrent use.
type Manager struct {
	cfg   Config
	store Store
	demo  DemoMachines
	queue Queue
	cg    CostGuard

	// machineWarm tracks whether the demo instance is currently up.
	machineWarm bool
	machineMu   sync.RWMutex

	// lastViewerAt: last time we had at least one viewer (for scale-to-zero).
	lastViewerAt   time.Time
	lastViewerAtMu sync.Mutex

	cancelFn context.CancelFunc
}

// NewManager creates and starts a Manager. Background tickers start immediately.
func NewManager(s Store, dm DemoMachines, cfg Config) *Manager {
	cg := newCostGuard(s, cfg.MonthlyCostCapUSD)
	q := newQueue(cfg, s, cg)

	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		cfg:          cfg,
		store:        s,
		demo:         dm,
		queue:        q,
		cg:           cg,
		lastViewerAt: time.Now(),
		cancelFn:     cancel,
	}

	// Poll demo instance status once at startup.
	go m.syncMachineStatus(ctx)

	go m.tickerCostGuard(ctx)
	go m.tickerDriverDeadline(ctx)
	go m.tickerPromote(ctx)
	go m.tickerScaleToZero(ctx)

	return m
}

// Close stops all background goroutines and is idempotent.
func (m *Manager) Close() error {
	m.cancelFn()
	return nil
}

// MachineWarm returns whether the demo instance is currently started.
func (m *Manager) MachineWarm() bool {
	m.machineMu.RLock()
	defer m.machineMu.RUnlock()
	return m.machineWarm
}

// setMachineWarm updates the warm flag.
func (m *Manager) setMachineWarm(v bool) {
	m.machineMu.Lock()
	defer m.machineMu.Unlock()
	m.machineWarm = v
}

// ensureMachineStarted starts the demo instance if it is not running.
func (m *Manager) ensureMachineStarted(ctx context.Context) {
	if m.MachineWarm() {
		return
	}
	if err := m.demo.Start(ctx); err != nil {
		log.Printf("[trydemo] machine start: %v", err)
		return
	}
	m.setMachineWarm(true)
	_ = m.store.LogEvent(ctx, "machine_start", "", "", "", time.Now())
}

// syncMachineStatus polls the demo instance Status once to sync the warm flag.
func (m *Manager) syncMachineStatus(ctx context.Context) {
	status, err := m.demo.Status(ctx)
	if err != nil {
		log.Printf("[trydemo] initial machine status: %v", err)
		return
	}
	m.setMachineWarm(status.Started)
}

// tickerCostGuard runs every 60s: if the machine is warm, accumulates
// compute minutes and estimated egress into the monthly cost accumulator.
func (m *Manager) tickerCostGuard(ctx context.Context) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !m.MachineWarm() {
				continue
			}
			snap := m.queue.Snapshot()
			viewers := snap.SpectatorCount
			if snap.DriverActive {
				viewers++
			}
			// Estimate: viewers × 1.5 Mbps × 60s = bytes per tick
			egressBytes := int64(viewers) * int64(1.5*1024*1024/8*60)
			computeMin := 1.0 // 1 machine-minute per tick
			if err := m.cg.Tick(ctx, computeMin, egressBytes); err != nil {
				log.Printf("[trydemo] costguard tick: %v", err)
			}
		}
	}
}

// tickerDriverDeadline runs every 1s and evicts the driver on session timeout
// or idle-kill.
func (m *Manager) tickerDriverDeadline(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			q := m.queue.(*inMemQueue)
			q.mu.Lock()

			// Check session timeout.
			if tok, expired := q.driverExpired(); expired {
				_ = q.removeLocked(ctx, tok, "session_timeout")
				q.mu.Unlock()
				_ = m.store.LogEvent(ctx, "driver_release", "", tok, "session_timeout", time.Now())
				// Reset the demo instance between drivers.
				go func() {
					if err := m.demo.Restart(ctx); err != nil {
						log.Printf("[trydemo] machine restart after timeout: %v", err)
					}
					_ = m.store.LogEvent(ctx, "reset", "", "", "session_timeout", time.Now())
				}()
				continue
			}

			// Check idle-kill.
			if tok, idle := q.driverIdle(); idle {
				_ = q.removeLocked(ctx, tok, "idle_kill")
				q.mu.Unlock()
				_ = m.store.LogEvent(ctx, "driver_idle_kill", "", tok, "idle_kill", time.Now())
				go func() {
					if err := m.demo.Restart(ctx); err != nil {
						log.Printf("[trydemo] machine restart after idle: %v", err)
					}
					_ = m.store.LogEvent(ctx, "reset", "", "", "idle_kill", time.Now())
				}()
				continue
			}

			q.mu.Unlock()
		}
	}
}

// tickerPromote runs every 500ms and promotes the queue head when the driver
// seat is empty.
func (m *Manager) tickerPromote(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := m.queue.Promote(ctx); err != nil {
				log.Printf("[trydemo] promote: %v", err)
			}
		}
	}
}

// tickerScaleToZero runs every 60s and stops the demo instance if there have
// been no viewers for IdleStopSecs.
func (m *Manager) tickerScaleToZero(ctx context.Context) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			q := m.queue.(*inMemQueue)
			q.mu.Lock()
			viewers := q.viewerCount()
			q.mu.Unlock()

			m.lastViewerAtMu.Lock()
			if viewers > 0 {
				m.lastViewerAt = time.Now()
			}
			idle := time.Since(m.lastViewerAt)
			m.lastViewerAtMu.Unlock()

			if viewers == 0 && idle >= time.Duration(m.cfg.IdleStopSecs)*time.Second {
				if m.MachineWarm() {
					if err := m.demo.Stop(ctx); err != nil {
						log.Printf("[trydemo] machine stop (scale-to-zero): %v", err)
					} else {
						m.setMachineWarm(false)
						_ = m.store.LogEvent(ctx, "machine_stop", "", "", "scale_to_zero", time.Now())
						log.Printf("[trydemo] machine stopped (scale-to-zero, idle for %.0fs)", idle.Seconds())
					}
				}
			}
		}
	}
}

// QueueRef exposes the queue for handlers.
func (m *Manager) QueueRef() Queue { return m.queue }

// CostGuardRef exposes the cost guard for handlers.
func (m *Manager) CostGuardRef() CostGuard { return m.cg }
