package servingpool

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemStore is an in-memory Store used by tests + as a fallback when the SQL
// store cannot be opened. Semantically identical to SQLStore.
type MemStore struct {
	mu      sync.Mutex
	nodes   map[string]Node
	leases  map[string]Lease
	signals []AutoscaleSignal
	nextID  int64
	// CLOUD-DEDICATED-01.
	pins map[string]TenantPin
	vdis map[string]VDISlice
}

// NewMemStore returns an empty in-memory Store.
func NewMemStore() *MemStore {
	return &MemStore{
		nodes:  make(map[string]Node),
		leases: make(map[string]Lease),
		pins:   make(map[string]TenantPin),
		vdis:   make(map[string]VDISlice),
	}
}

var _ Store = (*MemStore)(nil)

func (m *MemStore) Close() error { return nil }

func (m *MemStore) RegisterNode(_ context.Context, n Node) error {
	if n.ID == "" {
		return ErrNodeNotFound
	}
	if n.Capacity <= 0 {
		n.Capacity = DefaultNodeCapacity
	}
	if n.Health == "" {
		n.Health = HealthUnknown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, ok := m.nodes[n.ID]
	if ok {
		// Preserve registered_at on update.
		n.RegisteredAt = prev.RegisteredAt
		if n.LastHeartbeat == nil {
			n.LastHeartbeat = prev.LastHeartbeat
		}
	} else if n.RegisteredAt.IsZero() {
		n.RegisteredAt = nowUTC()
	}
	n.UpdatedAt = nowUTC()
	m.nodes[n.ID] = n
	return nil
}

func (m *MemStore) RemoveNode(_ context.Context, nodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodes[nodeID]; !ok {
		return ErrNodeNotFound
	}
	delete(m.nodes, nodeID)
	return nil
}

func (m *MemStore) GetNode(_ context.Context, nodeID string) (Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[nodeID]
	if !ok {
		return Node{}, ErrNodeNotFound
	}
	return n, nil
}

func (m *MemStore) ListNodes(_ context.Context) ([]Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Node, 0, len(m.nodes))
	for _, n := range m.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemStore) SetNodeHealth(_ context.Context, nodeID, health string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[nodeID]
	if !ok {
		return ErrNodeNotFound
	}
	n.Health = health
	n.UpdatedAt = nowUTC()
	m.nodes[nodeID] = n
	return nil
}

func (m *MemStore) SetNodeLoad(_ context.Context, nodeID string, load float64) error {
	if load < 0 {
		load = 0
	}
	if load > 1 {
		load = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[nodeID]
	if !ok {
		return ErrNodeNotFound
	}
	n.LoadScore = load
	n.UpdatedAt = nowUTC()
	m.nodes[nodeID] = n
	return nil
}

func (m *MemStore) Heartbeat(_ context.Context, nodeID string, when time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[nodeID]
	if !ok {
		return ErrNodeNotFound
	}
	t := when.UTC()
	n.LastHeartbeat = &t
	n.UpdatedAt = nowUTC()
	m.nodes[nodeID] = n
	return nil
}

func (m *MemStore) UpsertLease(_ context.Context, l Lease) error {
	if l.TenantID == "" || l.NodeID == "" {
		return ErrLeaseNotFound
	}
	if l.AcquiredAt.IsZero() {
		l.AcquiredAt = nowUTC()
	}
	if l.RenewedAt.IsZero() {
		l.RenewedAt = nowUTC()
	}
	if l.ExpiresAt.IsZero() {
		l.ExpiresAt = nowUTC().Add(DefaultLeaseTTL)
	}
	if l.Generation <= 0 {
		l.Generation = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, ok := m.leases[l.TenantID]; ok {
		if prev.NodeID != l.NodeID {
			l.Generation = prev.Generation + 1
			l.AcquiredAt = nowUTC()
		} else {
			l.Generation = prev.Generation
			l.AcquiredAt = prev.AcquiredAt
		}
	}
	m.leases[l.TenantID] = l
	return nil
}

func (m *MemStore) GetLease(_ context.Context, tenantID string) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.leases[tenantID]
	if !ok {
		return Lease{}, ErrLeaseNotFound
	}
	return l, nil
}

func (m *MemStore) ListLeasesByNode(_ context.Context, nodeID string) ([]Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Lease
	for _, l := range m.leases {
		if l.NodeID == nodeID {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TenantID < out[j].TenantID })
	return out, nil
}

func (m *MemStore) DeleteLease(_ context.Context, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.leases, tenantID)
	return nil
}

func (m *MemStore) CountLeases(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.leases), nil
}

func (m *MemStore) EmitSignal(_ context.Context, sig AutoscaleSignal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	sig.ID = m.nextID
	if sig.EmittedAt.IsZero() {
		sig.EmittedAt = nowUTC()
	}
	if sig.Scope == "" {
		sig.Scope = "global"
	}
	if sig.Action == "" {
		sig.Action = ActionHold
	}
	m.signals = append(m.signals, sig)
	return nil
}

// ----- Tenant pins (CLOUD-DEDICATED-01) -----

func (m *MemStore) UpsertTenantPin(_ context.Context, p TenantPin) error {
	if p.TenantID == "" || p.NodeID == "" {
		return ErrPinNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := nowUTC()
	if prev, ok := m.pins[p.TenantID]; ok {
		p.CreatedAt = prev.CreatedAt
	} else if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	m.pins[p.TenantID] = p
	return nil
}

func (m *MemStore) GetTenantPin(_ context.Context, tenantID string) (TenantPin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pins[tenantID]
	if !ok {
		return TenantPin{}, ErrPinNotFound
	}
	return p, nil
}

func (m *MemStore) DeleteTenantPin(_ context.Context, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pins[tenantID]; !ok {
		return ErrPinNotFound
	}
	delete(m.pins, tenantID)
	return nil
}

func (m *MemStore) ListTenantPins(_ context.Context) ([]TenantPin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TenantPin, 0, len(m.pins))
	for _, p := range m.pins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TenantID < out[j].TenantID })
	return out, nil
}

// ----- VDI slices (CLOUD-DEDICATED-01) -----

func (m *MemStore) UpsertVDISlice(_ context.Context, v VDISlice) error {
	if v.UserID == "" || v.NodeID == "" {
		return ErrVDINotFound
	}
	if v.HostKind != VDIHostPool && v.HostKind != VDIHostDedicated {
		return ErrInvalidHostKind
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := nowUTC()
	if prev, ok := m.vdis[v.UserID]; ok {
		v.ScheduledAt = prev.ScheduledAt
	} else if v.ScheduledAt.IsZero() {
		v.ScheduledAt = now
	}
	v.UpdatedAt = now
	m.vdis[v.UserID] = v
	return nil
}

func (m *MemStore) GetVDISlice(_ context.Context, userID string) (VDISlice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.vdis[userID]
	if !ok {
		return VDISlice{}, ErrVDINotFound
	}
	return v, nil
}

func (m *MemStore) DeleteVDISlice(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.vdis[userID]; !ok {
		return ErrVDINotFound
	}
	delete(m.vdis, userID)
	return nil
}

func (m *MemStore) ListVDISlices(_ context.Context) ([]VDISlice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]VDISlice, 0, len(m.vdis))
	for _, v := range m.vdis {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

func (m *MemStore) RecentSignals(_ context.Context, n int) ([]AutoscaleSignal, error) {
	if n <= 0 {
		n = 50
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AutoscaleSignal, 0, n)
	for i := len(m.signals) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, m.signals[i])
	}
	return out, nil
}
