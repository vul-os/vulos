// Package servingpool implements a generic bundle-node serving pool and
// tenant-affinity scheduler (originally specced as CLOUD-POOL-01). It is LIVE
// and WIRED today — but not for mail. Current callers: hosted-OS-instance
// placement (internal/orgadmin's InstanceLister, the autoscaler) and Meet's
// dedicated-node pinning (internal/meetalloc via servingpool.DedicatedManager).
//
// MAIL-TOPOLOGY-FENCE-01 (2026-07-11): CLOUD-POOL-01 was originally envisioned
// as THE mail cloud topology — a fleet of bundle nodes serving many mail
// tenants per node via this package's consistent-hash + lease scheduler (see
// roadmap/MAIL.md's history). That specific use is GONE: after the box-federated
// pivot (2026-07-15) mail is box-level (the OS box runs lilmail directly) and the
// central CP mail plane was removed, so nothing schedules a mail tenant through
// this pool. This package itself is
// NOT deprecated — only its mail-pooling INTENT is; keep using it for
// compute/Meet placement. Revisit mail-pooling only if a single central-app
// fleet later needs load-based sharding beyond horizontal scaling behind the
// reverse proxy.
//
// This package owns:
//   - a tenant→node scheduler (consistent-hash + lease, pure-Go modernc/sqlite),
//   - many-tenants-per-node placement with health-gated assignment (sick/
//     draining nodes are skipped and their tenants reassigned on renew),
//   - graceful lease handoff on node add/remove,
//   - an autoscale-by-load signal an external autoscaler can consume.
//
// INVARIANTS:
//   - Pure-Go SQLite (modernc.org/sqlite); CGO_ENABLED=0 must build clean.
//   - No Go-module dependency on vulos-mail / vulos-relay / vulos-office. We
//     define our own consistent-hash + lease primitives so the OSS binary is
//     never imported into the cloud control plane.
package servingpool

import (
	"context"
	"errors"
	"time"
)

// Health values for a bundle node.
const (
	HealthUnknown  = "unknown"
	HealthHealthy  = "healthy"
	HealthDegraded = "degraded"
	HealthSick     = "sick"
	HealthDraining = "draining"
)

// Autoscale actions emitted on a signal.
const (
	ActionScaleUp   = "scale_up"
	ActionScaleDown = "scale_down"
	ActionHold      = "hold"
)

// Defaults for lease duration and autoscale thresholds. Tunable via Config.
const (
	DefaultLeaseTTL          = 60 * time.Second
	DefaultScaleUpLoad       = 0.80 // fleet >80% loaded → scale_up
	DefaultScaleDownLoad     = 0.30 // fleet <30% loaded → scale_down
	DefaultNodeCapacity      = 1000
	DefaultVirtualNodeWeight = 64 // virtual nodes per real node on the hash ring
)

// VDI host placement modes for ScheduleVDISlice (CLOUD-DEDICATED-01).
const (
	VDIHostPool      = "pool"      // pick a healthy node via the scheduler
	VDIHostDedicated = "dedicated" // require an existing tenant pin
)

// Errors.
var (
	ErrNoHealthyNodes = errors.New("servingpool: no healthy nodes available")
	ErrNodeNotFound   = errors.New("servingpool: node not found")
	ErrLeaseNotFound  = errors.New("servingpool: lease not found")
	// CLOUD-DEDICATED-01.
	ErrPinNotFound          = errors.New("servingpool: tenant pin not found")
	ErrVDINotFound          = errors.New("servingpool: vdi slice not found")
	ErrInvalidHostKind      = errors.New("servingpool: invalid vdi host kind (want 'pool' or 'dedicated')")
	ErrDedicatedRequiresPin = errors.New("servingpool: dedicated VDI requires an existing tenant pin")
)

// Node is a registered bundle-node in the fleet.
type Node struct {
	ID            string
	Region        string
	Capacity      int
	Health        string
	LastHeartbeat *time.Time
	LoadScore     float64 // 0..1 normalised
	RegisteredAt  time.Time
	UpdatedAt     time.Time
}

// Lease is the current tenant→node assignment.
type Lease struct {
	TenantID   string
	NodeID     string
	AcquiredAt time.Time
	RenewedAt  time.Time
	ExpiresAt  time.Time
	Generation int
}

// AutoscaleSignal is a single observation the autoscaler can read.
type AutoscaleSignal struct {
	ID        int64
	EmittedAt time.Time
	Scope     string  // "global" or node_id
	Action    string  // scale_up|scale_down|hold
	Reason    string  // human-readable
	LoadScore float64 // 0..1
}

// TenantPin pins a tenant to a specific bundle node — the scheduler honours
// the pin and bypasses consistent-hash for that tenant (CLOUD-DEDICATED-01).
type TenantPin struct {
	TenantID  string
	NodeID    string
	Reason    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// VDISlice is a per-user full-desktop (cage/labwc) attachment. One row per
// user_id; re-scheduling overwrites the previous slice (CLOUD-DEDICATED-01).
type VDISlice struct {
	UserID      string
	AccountID   string
	TenantID    string
	HostKind    string // VDIHostPool | VDIHostDedicated
	NodeID      string
	ScheduledAt time.Time
	UpdatedAt   time.Time
}

// PoolStats summarise current fleet state. Read by the autoscaler / dashboards.
type PoolStats struct {
	TotalNodes    int
	HealthyNodes  int
	DrainingNodes int
	SickNodes     int
	TotalLeases   int
	TotalCapacity int
	// FleetLoad is sum(load_score) / count(healthy nodes), 0..1. Zero when
	// no healthy nodes.
	FleetLoad float64
}

// Store is the persistence interface for the serving pool. Implemented by
// SQLStore (pure-Go modernc/sqlite) and MemStore (in-memory, for tests).
type Store interface {
	// Nodes
	RegisterNode(ctx context.Context, n Node) error
	RemoveNode(ctx context.Context, nodeID string) error
	GetNode(ctx context.Context, nodeID string) (Node, error)
	ListNodes(ctx context.Context) ([]Node, error)
	SetNodeHealth(ctx context.Context, nodeID, health string) error
	SetNodeLoad(ctx context.Context, nodeID string, load float64) error
	Heartbeat(ctx context.Context, nodeID string, when time.Time) error

	// Leases
	UpsertLease(ctx context.Context, l Lease) error
	GetLease(ctx context.Context, tenantID string) (Lease, error)
	ListLeasesByNode(ctx context.Context, nodeID string) ([]Lease, error)
	DeleteLease(ctx context.Context, tenantID string) error
	CountLeases(ctx context.Context) (int, error)

	// Autoscale signals
	EmitSignal(ctx context.Context, sig AutoscaleSignal) error
	RecentSignals(ctx context.Context, n int) ([]AutoscaleSignal, error)

	// Tenant pins + VDI slices (CLOUD-DEDICATED-01). Implementations MUST be
	// idempotent on (tenant_id) / (user_id) — re-pinning / re-scheduling the
	// same key is an update, not an error.
	UpsertTenantPin(ctx context.Context, p TenantPin) error
	GetTenantPin(ctx context.Context, tenantID string) (TenantPin, error)
	DeleteTenantPin(ctx context.Context, tenantID string) error
	ListTenantPins(ctx context.Context) ([]TenantPin, error)

	UpsertVDISlice(ctx context.Context, v VDISlice) error
	GetVDISlice(ctx context.Context, userID string) (VDISlice, error)
	DeleteVDISlice(ctx context.Context, userID string) error
	ListVDISlices(ctx context.Context) ([]VDISlice, error)

	Close() error
}
