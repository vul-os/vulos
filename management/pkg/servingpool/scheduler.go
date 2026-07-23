package servingpool

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Config tunes scheduler behaviour.
type Config struct {
	LeaseTTL          time.Duration
	VirtualNodeWeight int
	// ScaleUpAt: emit scale_up when fleet load > this (0..1). Default 0.80.
	ScaleUpAt float64
	// ScaleDownAt: emit scale_down when fleet load < this (0..1). Default 0.30.
	ScaleDownAt float64
}

func (c *Config) withDefaults() {
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = DefaultLeaseTTL
	}
	if c.VirtualNodeWeight <= 0 {
		c.VirtualNodeWeight = DefaultVirtualNodeWeight
	}
	if c.ScaleUpAt <= 0 {
		c.ScaleUpAt = DefaultScaleUpLoad
	}
	if c.ScaleDownAt <= 0 {
		c.ScaleDownAt = DefaultScaleDownLoad
	}
}

// Scheduler assigns tenants to nodes via consistent-hash + lease. It is safe
// for concurrent use. A single Scheduler manages many tenants per node.
type Scheduler struct {
	cfg   Config
	store Store
	ring  *HashRing
	mu    sync.RWMutex
	// healthy is the set of node IDs currently healthy/eligible for new work.
	// Built from store.ListNodes() on Refresh().
	healthy map[string]bool
}

// NewScheduler constructs a Scheduler. Callers must invoke Refresh(ctx) at
// least once before Assign / RenewLease to load nodes into the ring.
func NewScheduler(store Store, cfg Config) *Scheduler {
	cfg.withDefaults()
	return &Scheduler{
		cfg:     cfg,
		store:   store,
		ring:    NewHashRing(cfg.VirtualNodeWeight),
		healthy: make(map[string]bool),
	}
}

// Refresh syncs the in-memory hash ring + healthy set from the store. It is
// the only mutation point for the ring's node membership — Assign / Renew are
// read paths that may stamp a stale lease only if the hash ring is stale.
// Call this on:
//   - startup,
//   - node add/remove (RegisterNode / RemoveNode also call this),
//   - health transitions.
//
// Safe to call concurrently; refresh is idempotent.
func (s *Scheduler) Refresh(ctx context.Context) error {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return fmt.Errorf("servingpool: refresh: %w", err)
	}
	newRing := NewHashRing(s.cfg.VirtualNodeWeight)
	healthy := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if isEligible(n) {
			newRing.AddNode(n.ID)
			healthy[n.ID] = true
		}
	}
	s.mu.Lock()
	s.ring = newRing
	s.healthy = healthy
	s.mu.Unlock()
	return nil
}

// isEligible reports whether a node accepts NEW lease assignments.
// Healthy = accept new; Draining / Sick / Degraded = no new work (existing
// leases on draining/sick nodes are reassigned on lease renew).
func isEligible(n Node) bool {
	return n.Health == HealthHealthy
}

// AddNode registers a node and re-balances the ring. Existing leases are NOT
// pre-emptively moved; they migrate on next Renew via the consistent-hash
// lookup, giving a "graceful handoff" on scale-out.
func (s *Scheduler) AddNode(ctx context.Context, n Node) error {
	if n.Health == "" {
		n.Health = HealthHealthy
	}
	if err := s.store.RegisterNode(ctx, n); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

// RemoveNode drops a node from the pool. All leases for that node are deleted
// so subsequent Assigns hash to a new owner. This is the graceful handoff on
// scale-in / node failure.
func (s *Scheduler) RemoveNode(ctx context.Context, nodeID string) error {
	leases, err := s.store.ListLeasesByNode(ctx, nodeID)
	if err != nil {
		return err
	}
	for _, l := range leases {
		if err := s.store.DeleteLease(ctx, l.TenantID); err != nil {
			return err
		}
	}
	if err := s.store.RemoveNode(ctx, nodeID); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

// MarkHealth changes a node's health. Transitions to non-healthy drain the
// node (leases moved to the next eligible owner). Refresh is called so the
// ring reflects the new eligibility set.
func (s *Scheduler) MarkHealth(ctx context.Context, nodeID, health string) error {
	if err := s.store.SetNodeHealth(ctx, nodeID, health); err != nil {
		return err
	}
	if err := s.Refresh(ctx); err != nil {
		return err
	}
	// Drain: move existing leases off the node if it became ineligible.
	if health != HealthHealthy {
		if err := s.drainNode(ctx, nodeID); err != nil {
			return err
		}
	}
	return nil
}

// drainNode reassigns every lease currently on nodeID to the next eligible
// owner (per consistent-hash lookup). If no eligible owner exists, the lease
// is left in place (best-effort; ErrNoHealthyNodes only on Assign).
func (s *Scheduler) drainNode(ctx context.Context, nodeID string) error {
	leases, err := s.store.ListLeasesByNode(ctx, nodeID)
	if err != nil {
		return err
	}
	for _, l := range leases {
		// CLOUD-DEDICATED-01: pinned tenants must NOT be drained off their pin.
		// A pin is sticky regardless of node health.
		if _, err := s.store.GetTenantPin(ctx, l.TenantID); err == nil {
			continue
		}
		s.mu.RLock()
		next := s.ring.Lookup(l.TenantID)
		s.mu.RUnlock()
		if next == "" || next == nodeID {
			continue
		}
		l.NodeID = next
		l.RenewedAt = nowUTC()
		l.ExpiresAt = nowUTC().Add(s.cfg.LeaseTTL)
		// Generation bump happens in UpsertLease on node change.
		if err := s.store.UpsertLease(ctx, l); err != nil {
			return err
		}
	}
	return nil
}

// Assign returns the lease assignment for a tenant. If a fresh lease already
// exists pointing at an eligible node, it is returned as-is. Otherwise a new
// (or migrated) lease is written via consistent-hash + the lease TTL.
//
// Assignment is health-gated: only HealthHealthy nodes receive new assignments;
// existing leases on non-eligible nodes migrate on the next call.
//
// CLOUD-DEDICATED-01: if the tenant has a TenantPin, the pinned node REPLACES
// the consistent-hash lookup (regardless of the node's current health — a
// dedicated tenant follows its node, not the ring). Existing pooled behaviour
// is unchanged when no pin exists.
func (s *Scheduler) Assign(ctx context.Context, tenantID string) (Lease, error) {
	if tenantID == "" {
		return Lease{}, fmt.Errorf("servingpool: tenant id required")
	}

	// CLOUD-DEDICATED-01: honour an explicit pin before anything else. A pinned
	// tenant skips the ring; the pin IS the assignment.
	if pin, err := s.store.GetTenantPin(ctx, tenantID); err == nil {
		return s.assignToNode(ctx, tenantID, pin.NodeID)
	}

	// Hot path: existing lease, still fresh, on a still-eligible node.
	existing, err := s.store.GetLease(ctx, tenantID)
	if err == nil {
		s.mu.RLock()
		eligible := s.healthy[existing.NodeID]
		s.mu.RUnlock()
		if eligible && nowUTC().Before(existing.ExpiresAt) {
			return existing, nil
		}
	}

	// Look up the consistent-hash owner.
	s.mu.RLock()
	owner := s.ring.Lookup(tenantID)
	if owner == "" {
		s.mu.RUnlock()
		return Lease{}, ErrNoHealthyNodes
	}
	s.mu.RUnlock()

	return s.assignToNode(ctx, tenantID, owner)
}

// assignToNode writes (or refreshes) a lease for tenantID → nodeID without
// consulting the consistent-hash ring. Used by pinned-tenant assignment and as
// the common write tail of the ring-based path. Generation is bumped by the
// store on node change.
func (s *Scheduler) assignToNode(ctx context.Context, tenantID, nodeID string) (Lease, error) {
	now := nowUTC()
	l := Lease{
		TenantID:   tenantID,
		NodeID:     nodeID,
		AcquiredAt: now,
		RenewedAt:  now,
		ExpiresAt:  now.Add(s.cfg.LeaseTTL),
		Generation: 1,
	}
	if err := s.store.UpsertLease(ctx, l); err != nil {
		return Lease{}, err
	}
	return s.store.GetLease(ctx, tenantID)
}

// RenewLease extends the lease on its current node if that node is still
// eligible; otherwise it triggers a handoff (re-Assign).
func (s *Scheduler) RenewLease(ctx context.Context, tenantID string) (Lease, error) {
	cur, err := s.store.GetLease(ctx, tenantID)
	if err != nil {
		// No prior lease — assign a fresh one.
		return s.Assign(ctx, tenantID)
	}
	s.mu.RLock()
	eligible := s.healthy[cur.NodeID]
	s.mu.RUnlock()
	if !eligible {
		// Trigger handoff via Assign.
		return s.Assign(ctx, tenantID)
	}
	cur.RenewedAt = nowUTC()
	cur.ExpiresAt = nowUTC().Add(s.cfg.LeaseTTL)
	if err := s.store.UpsertLease(ctx, cur); err != nil {
		return Lease{}, err
	}
	return s.store.GetLease(ctx, tenantID)
}

// LookupNode returns the consistent-hash preferred node for a tenant without
// touching the lease table. Useful for routing previews.
func (s *Scheduler) LookupNode(tenantID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ring.Lookup(tenantID)
}

// HealthyNodeCount returns the number of currently eligible nodes.
func (s *Scheduler) HealthyNodeCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.healthy)
}

// NodeForTenant resolves the bundle node a tenant currently maps to and returns
// its full record (region, health, …). Resolution order, all read-only (no
// lease mutation):
//  1. an existing lease (the node the tenant is actually placed on), else
//  2. the consistent-hash preferred owner from the in-memory ring.
//
// Returns ("", Node{}, nil) when neither a lease nor a ring owner exists (e.g.
// no healthy nodes yet) so dashboards can render an empty Pooled list cleanly.
// A genuine store error (other than not-found) is surfaced.
func (s *Scheduler) NodeForTenant(ctx context.Context, tenantID string) (string, Node, error) {
	nodeID := ""
	if l, err := s.store.GetLease(ctx, tenantID); err == nil && l.NodeID != "" {
		nodeID = l.NodeID
	} else if err != nil && err != ErrLeaseNotFound {
		return "", Node{}, err
	}
	if nodeID == "" {
		nodeID = s.LookupNode(tenantID)
	}
	if nodeID == "" {
		return "", Node{}, nil
	}
	n, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		if err == ErrNodeNotFound {
			// Lease/ring points at a node no longer registered — report the id
			// without region/health rather than failing the whole instances tab.
			return nodeID, Node{ID: nodeID, Health: HealthUnknown}, nil
		}
		return "", Node{}, err
	}
	return nodeID, n, nil
}

// NodeByID returns the full node record for nodeID (region, health, …). Thin
// read-only pass-through to the store used by the dedicated-pin path in the
// org-admin instances tab. Returns ErrNodeNotFound when absent.
func (s *Scheduler) NodeByID(ctx context.Context, nodeID string) (Node, error) {
	return s.store.GetNode(ctx, nodeID)
}

// DrainCandidates returns the pool's node IDs ordered MOST-IDLE FIRST so a
// scale-in path drains the least-occupied node rather than an arbitrary one.
//
// Ordering key (ascending — index 0 is the best drain target):
//  1. active tenant-lease count (fewest first — draining an empty node evicts
//     no tenants),
//  2. node LoadScore (least loaded first), then
//  3. node ID (stable tiebreak for determinism).
//
// This is the "most-idle node" hook the autoscaler's pickDrainVictim references:
// the autoscaler intersects this ranked list with the live machine refs and
// drains the first match. A store error is surfaced; an empty pool returns an
// empty slice (the autoscaler then has nothing to drain).
func (s *Scheduler) DrainCandidates(ctx context.Context) ([]string, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	type cand struct {
		id     string
		leases int
		load   float64
	}
	cands := make([]cand, 0, len(nodes))
	for _, n := range nodes {
		leases, lerr := s.store.ListLeasesByNode(ctx, n.ID)
		if lerr != nil {
			return nil, lerr
		}
		cands = append(cands, cand{id: n.ID, leases: len(leases), load: n.LoadScore})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].leases != cands[j].leases {
			return cands[i].leases < cands[j].leases
		}
		if cands[i].load != cands[j].load {
			return cands[i].load < cands[j].load
		}
		return cands[i].id < cands[j].id
	})
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.id
	}
	return out, nil
}

// Stats compute current PoolStats from the store. Used by the autoscale loop
// and dashboards.
func (s *Scheduler) Stats(ctx context.Context) (PoolStats, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return PoolStats{}, err
	}
	leaseN, err := s.store.CountLeases(ctx)
	if err != nil {
		return PoolStats{}, err
	}
	st := PoolStats{TotalNodes: len(nodes), TotalLeases: leaseN}
	var loadSum float64
	var loadDen int
	for _, n := range nodes {
		st.TotalCapacity += n.Capacity
		switch n.Health {
		case HealthHealthy:
			st.HealthyNodes++
			loadSum += n.LoadScore
			loadDen++
		case HealthDraining:
			st.DrainingNodes++
		case HealthSick:
			st.SickNodes++
		}
	}
	if loadDen > 0 {
		st.FleetLoad = loadSum / float64(loadDen)
	}
	return st, nil
}
