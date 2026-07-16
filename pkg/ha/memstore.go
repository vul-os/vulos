package ha

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemStore is an in-process Store implementation used by tests. The semantics
// match SQLStore byte-for-byte for the lease state machine; production wiring
// uses SQLStore exclusively.
type MemStore struct {
	mu      sync.Mutex
	leases  map[string]Lease
	peers   map[string]PeerHeartbeat
	markers []LeaderMarker
	nextID  int64
	// now is a swappable clock for tests.
	now func() time.Time
}

// NewMemStore returns a fresh in-process store.
func NewMemStore() *MemStore {
	return &MemStore{
		leases: map[string]Lease{},
		peers:  map[string]PeerHeartbeat{},
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// SetClock installs a deterministic clock for tests.
func (m *MemStore) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if now != nil {
		m.now = now
	}
}

var _ Store = (*MemStore)(nil)

func (m *MemStore) TryAcquire(_ context.Context, key, holderID string, ttl time.Duration) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}

	existing, ok := m.leases[key]
	switch {
	case !ok:
		// Brand-new lease.
		l := Lease{
			Key:        key,
			HolderID:   holderID,
			AcquiredAt: now,
			RenewedAt:  now,
			ExpiresAt:  now.Add(ttl),
			Generation: 1,
		}
		m.leases[key] = l
		return l, nil

	case existing.IsLive(now) && existing.HolderID == holderID:
		// Same holder re-acquiring its own live lease — idempotent renew.
		existing.RenewedAt = now
		existing.ExpiresAt = now.Add(ttl)
		m.leases[key] = existing
		return existing, nil

	case existing.IsLive(now) && existing.HolderID != holderID:
		// Live and held by someone else.
		return Lease{}, ErrLeaseHeld

	default:
		// Expired. New holder steals → bump generation. Same holder recovering
		// its own expired lease → keep generation (no handoff occurred).
		gen := existing.Generation
		acquired := existing.AcquiredAt
		if existing.HolderID != holderID {
			gen++
			acquired = now
		}
		l := Lease{
			Key:        key,
			HolderID:   holderID,
			AcquiredAt: acquired,
			RenewedAt:  now,
			ExpiresAt:  now.Add(ttl),
			Generation: gen,
		}
		m.leases[key] = l
		return l, nil
	}
}

func (m *MemStore) RenewLease(_ context.Context, key, holderID string, ttl time.Duration) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	existing, ok := m.leases[key]
	if !ok {
		return Lease{}, ErrNotLeader
	}
	if existing.HolderID != holderID {
		return Lease{}, ErrNotLeader
	}
	if !existing.IsLive(now) {
		// Already expired — caller must re-Acquire.
		return Lease{}, ErrNotLeader
	}
	existing.RenewedAt = now
	existing.ExpiresAt = now.Add(ttl)
	m.leases[key] = existing
	return existing, nil
}

func (m *MemStore) ReleaseLease(_ context.Context, key, holderID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.leases[key]
	if !ok {
		return nil
	}
	if existing.HolderID != holderID {
		return nil
	}
	// Mark the lease expired (rather than deleting) so the generation
	// counter survives the handoff: a graceful step-down is still a
	// leadership transition from the clients' perspective.
	now := m.now().UTC()
	existing.ExpiresAt = now
	existing.RenewedAt = now
	m.leases[key] = existing
	return nil
}

func (m *MemStore) GetLease(_ context.Context, key string) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.leases[key]
	if !ok {
		return Lease{}, ErrLeaseNotFound
	}
	return l, nil
}

func (m *MemStore) PutPeerHeartbeat(_ context.Context, hb PeerHeartbeat) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if hb.LastHeartbeat.IsZero() {
		hb.LastHeartbeat = m.now().UTC()
	}
	if hb.Role == "" {
		hb.Role = RoleUnknown
	}
	// generation_seen is monotonic per peer: never regress.
	if existing, ok := m.peers[hb.PeerID]; ok {
		if existing.GenerationSeen > hb.GenerationSeen {
			hb.GenerationSeen = existing.GenerationSeen
		}
	}
	m.peers[hb.PeerID] = hb
	return nil
}

func (m *MemStore) GetPeer(_ context.Context, peerID string) (PeerHeartbeat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hb, ok := m.peers[peerID]
	if !ok {
		return PeerHeartbeat{}, ErrPeerNotFound
	}
	return hb, nil
}

func (m *MemStore) ListPeers(_ context.Context) ([]PeerHeartbeat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PeerHeartbeat, 0, len(m.peers))
	for _, hb := range m.peers {
		out = append(out, hb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PeerID < out[j].PeerID })
	return out, nil
}

func (m *MemStore) WriteMarker(_ context.Context, mk LeaderMarker) (LeaderMarker, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	mk.ID = m.nextID
	if mk.WrittenAt.IsZero() {
		mk.WrittenAt = m.now().UTC()
	}
	m.markers = append(m.markers, mk)
	return mk, nil
}

func (m *MemStore) ListMarkers(_ context.Context, leaseKey string) ([]LeaderMarker, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []LeaderMarker
	for _, mk := range m.markers {
		if mk.LeaseKey == leaseKey {
			out = append(out, mk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemStore) Close() error { return nil }
