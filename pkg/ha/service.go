package ha

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Config tunes the Manager loop. Zero-values are replaced with defaults.
type Config struct {
	// PeerID identifies this CP replica. Must be globally unique among peers
	// (typically hostname or a random ULID written to a file at first boot).
	PeerID string

	// Address is this peer's reachable URL — published in heartbeats so other
	// peers can probe it (e.g. for an HTTP health check). Optional.
	Address string

	// LeaseKey is the logical name of the leader role. Defaults to
	// DefaultLeaseKey ("cp-leader").
	LeaseKey string

	// LeaseTTL is the lease lifetime. Defaults to DefaultLeaseTTL.
	LeaseTTL time.Duration

	// HeartbeatInterval is the renew cadence while leader. Defaults to
	// DefaultHeartbeatInterval. Must be < LeaseTTL/2.
	HeartbeatInterval time.Duration

	// AcquireInterval is the polling cadence while standby. Defaults to
	// DefaultAcquireInterval.
	AcquireInterval time.Duration

	// Logf is the package logger. Defaults to a no-op.
	Logf func(format string, args ...any)
}

func (c *Config) applyDefaults() {
	if c.LeaseKey == "" {
		c.LeaseKey = DefaultLeaseKey
	}
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = DefaultLeaseTTL
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if c.AcquireInterval <= 0 {
		c.AcquireInterval = DefaultAcquireInterval
	}
	if c.HeartbeatInterval >= c.LeaseTTL {
		c.HeartbeatInterval = c.LeaseTTL / 3
		if c.HeartbeatInterval <= 0 {
			c.HeartbeatInterval = time.Second
		}
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
}

// State is the live status the Manager publishes.
type State struct {
	IsLeader   bool      `json:"is_leader"`
	Generation int64     `json:"generation"`
	HolderID   string    `json:"holder_id"`
	ExpiresAt  time.Time `json:"expires_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Manager drives the lease loop for one CP replica. It is safe to call
// IsLeader / Snapshot from request handlers; Run blocks until ctx cancels.
type Manager struct {
	store Store
	cfg   Config

	mu       sync.RWMutex
	state    State
	stateGen atomic.Int64 // mirrors state.Generation for lock-free readers
	stateLdr atomic.Bool  // mirrors state.IsLeader for lock-free readers

	// OnPromote is invoked once each time this peer transitions standby→leader.
	// Optional; useful for hooks like "start the bookkeeping cron only on the
	// leader". The callback runs inline; keep it cheap (or fan-out in a goroutine).
	OnPromote func(gen int64)

	// OnDemote is invoked once each time this peer transitions leader→standby.
	// Optional.
	OnDemote func()
}

// NewManager constructs a Manager. The first call to Run starts the loop.
func NewManager(store Store, cfg Config) *Manager {
	cfg.applyDefaults()
	if cfg.PeerID == "" {
		// Best-effort fallback; production wiring should always pass a real ID.
		cfg.PeerID = fmt.Sprintf("peer-%d", time.Now().UnixNano())
	}
	return &Manager{store: store, cfg: cfg}
}

// IsLeader is a fast, lock-free check suitable for hot paths.
func (m *Manager) IsLeader() bool { return m.stateLdr.Load() }

// Generation returns the current lease generation as observed by this peer.
func (m *Manager) Generation() int64 { return m.stateGen.Load() }

// Snapshot returns a copy of the latest published state.
func (m *Manager) Snapshot() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// PeerID returns the configured peer identifier.
func (m *Manager) PeerID() string { return m.cfg.PeerID }

// LeaseKey returns the configured lease key.
func (m *Manager) LeaseKey() string { return m.cfg.LeaseKey }

// Tick runs one iteration of the lease loop. Exposed for tests so the
// scheduler can be advanced deterministically without timers.
//
// Behaviour:
//   - If we currently hold the lease: try to RenewLease. If renew fails
//     with ErrNotLeader, demote and fall through to the standby branch.
//   - If we don't hold the lease: try TryAcquire. ErrLeaseHeld is benign —
//     it just means another peer is healthy.
//
// In both branches we publish a peer heartbeat so observers see us alive.
func (m *Manager) Tick(ctx context.Context) error {
	wasLeader := m.IsLeader()

	if wasLeader {
		l, err := m.store.RenewLease(ctx, m.cfg.LeaseKey, m.cfg.PeerID, m.cfg.LeaseTTL)
		switch {
		case err == nil:
			m.publish(l, true)
			m.heartbeat(ctx, l.Generation, RoleLeader)
			return nil
		case errors.Is(err, ErrNotLeader):
			m.cfg.Logf("[ha] lost leadership for %q (peer=%s): %v", m.cfg.LeaseKey, m.cfg.PeerID, err)
			m.demote()
			// Fall through to the acquire branch in the next tick.
			return nil
		default:
			return fmt.Errorf("ha: renew: %w", err)
		}
	}

	l, err := m.store.TryAcquire(ctx, m.cfg.LeaseKey, m.cfg.PeerID, m.cfg.LeaseTTL)
	switch {
	case err == nil:
		justPromoted := l.HolderID == m.cfg.PeerID && !wasLeader
		m.publish(l, l.HolderID == m.cfg.PeerID)
		role := RoleStandby
		if l.HolderID == m.cfg.PeerID {
			role = RoleLeader
		}
		m.heartbeat(ctx, l.Generation, role)
		if justPromoted && m.OnPromote != nil {
			m.OnPromote(l.Generation)
		}
		return nil
	case errors.Is(err, ErrLeaseHeld):
		// Another peer holds it — read the row so we publish the right
		// generation to observers.
		if cur, gerr := m.store.GetLease(ctx, m.cfg.LeaseKey); gerr == nil {
			m.publish(cur, false)
			m.heartbeat(ctx, cur.Generation, RoleStandby)
		}
		return nil
	default:
		return fmt.Errorf("ha: acquire: %w", err)
	}
}

// Run executes Tick on the appropriate cadence until ctx is cancelled.
// On exit, the manager releases the lease (if held) so that a peer can
// take over immediately instead of waiting for TTL expiry.
func (m *Manager) Run(ctx context.Context) error {
	m.cfg.Logf("[ha] manager started (peer=%s lease=%s ttl=%s heartbeat=%s acquire=%s)",
		m.cfg.PeerID, m.cfg.LeaseKey, m.cfg.LeaseTTL, m.cfg.HeartbeatInterval, m.cfg.AcquireInterval)

	// One Tick immediately so a freshly started single peer becomes leader
	// without waiting an AcquireInterval.
	if err := m.Tick(ctx); err != nil {
		m.cfg.Logf("[ha] initial tick: %v", err)
	}

	for {
		// Pick cadence based on current role.
		var d time.Duration
		if m.IsLeader() {
			d = m.cfg.HeartbeatInterval
		} else {
			d = m.cfg.AcquireInterval
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			m.shutdown()
			return ctx.Err()
		case <-t.C:
			if err := m.Tick(ctx); err != nil {
				m.cfg.Logf("[ha] tick: %v", err)
			}
		}
	}
}

func (m *Manager) shutdown() {
	if m.IsLeader() {
		// Best-effort step-down so the peer doesn't have to wait TTL.
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := m.store.ReleaseLease(releaseCtx, m.cfg.LeaseKey, m.cfg.PeerID); err != nil {
			m.cfg.Logf("[ha] release on shutdown: %v", err)
		} else {
			m.cfg.Logf("[ha] released lease on shutdown (peer=%s)", m.cfg.PeerID)
		}
		m.demote()
	}
}

func (m *Manager) publish(l Lease, isLeader bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	wasLeader := m.state.IsLeader
	m.state = State{
		IsLeader:   isLeader,
		Generation: l.Generation,
		HolderID:   l.HolderID,
		ExpiresAt:  l.ExpiresAt,
		UpdatedAt:  time.Now().UTC(),
	}
	m.stateGen.Store(l.Generation)
	m.stateLdr.Store(isLeader)
	if wasLeader && !isLeader && m.OnDemote != nil {
		m.OnDemote()
	}
}

func (m *Manager) demote() {
	m.mu.Lock()
	wasLeader := m.state.IsLeader
	m.state.IsLeader = false
	m.stateLdr.Store(false)
	m.mu.Unlock()
	if wasLeader && m.OnDemote != nil {
		m.OnDemote()
	}
}

func (m *Manager) heartbeat(ctx context.Context, gen int64, role string) {
	hb := PeerHeartbeat{
		PeerID:         m.cfg.PeerID,
		Address:        m.cfg.Address,
		LastHeartbeat:  time.Now().UTC(),
		Role:           role,
		GenerationSeen: gen,
	}
	if err := m.store.PutPeerHeartbeat(ctx, hb); err != nil {
		m.cfg.Logf("[ha] put peer heartbeat: %v", err)
	}
}
