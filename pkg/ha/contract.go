// Package ha implements active-passive failover for the control plane
// (RISK-SPOF-01).
//
// # Goal
//
// The control plane (CP) is a single point of failure for hosted customers.
// This package gives the CP two things:
//
//  1. Lease-based leader election so a second CP replica can promote when
//     the active replica stops heartbeating. Pure-Go modernc/sqlite (NEVER
//     CGO) — the lease lives in a small dedicated SQLite DB co-located with
//     the rest of CP state under CP_DB_DIR. The DB file is intended to live
//     on shared/replicated storage between peers (Fly volume snapshot, or
//     LiteFS-replicated file in a future iteration). For dev / single-region
//     prod, the lease DB is just local and the HA package is opt-in.
//
//  2. A cross-peer heartbeat + health snapshot so peers know each other's
//     status and can take over.
//
// # Mutual exclusion
//
// At any instant at most one peer holds the lease for a given key. The
// invariant is enforced inside SQLite, atomically:
//
//   - Acquire succeeds iff the existing row has expired (renewed_at + ttl <
//     now) OR no row exists.
//   - Renew succeeds iff the caller is the current holder AND the row has
//     not expired (so a stale holder cannot resurrect a lease another peer
//     has already stolen).
//   - On any handoff (Acquire by a peer that was NOT the previous holder)
//     generation is incremented by 1. Generation is monotonic and visible
//     to clients so they can detect a leader change.
//
// # Graceful degradation
//
// Even with HA, the CP may be fully down (network partition, double-fault).
// Boxes are designed to keep serving from cached state — see
// internal/ha/DEGRADATION.md for the contract and the per-endpoint list of
// "needed at request time" vs. "defer/queue when CP down".
package ha

import (
	"context"
	"errors"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Defaults
// ─────────────────────────────────────────────────────────────────────────────

const (
	// DefaultLeaseTTL is the maximum time a leader may go without renewing
	// before another peer is allowed to steal the lease.
	DefaultLeaseTTL = 15 * time.Second

	// DefaultHeartbeatInterval is the cadence at which the leader renews
	// its lease. Must be < DefaultLeaseTTL/2 to be safe under jitter.
	DefaultHeartbeatInterval = 5 * time.Second

	// DefaultAcquireInterval is the cadence at which a standby peer attempts
	// to acquire (steal) the lease when it observes the leader is stale.
	DefaultAcquireInterval = 3 * time.Second

	// DefaultLeaseKey is the conventional lease-key for the CP leader role.
	DefaultLeaseKey = "cp-leader"
)

// Role constants reported by peers via PeerHeartbeat.Role.
const (
	RoleLeader  = "leader"
	RoleStandby = "standby"
	RoleUnknown = "unknown"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────────

var (
	// ErrNotLeader is returned by RenewLease when the caller is not the
	// current lease holder (someone else stole the lease).
	ErrNotLeader = errors.New("ha: caller is not the current lease holder")

	// ErrLeaseHeld is returned by TryAcquire when the lease is currently
	// valid and held by a DIFFERENT peer.
	ErrLeaseHeld = errors.New("ha: lease is currently held by another peer")

	// ErrLeaseNotFound is returned when no lease row exists for a key.
	ErrLeaseNotFound = errors.New("ha: lease not found")

	// ErrPeerNotFound is returned by GetPeer when no peer row exists.
	ErrPeerNotFound = errors.New("ha: peer not found")
)

// ─────────────────────────────────────────────────────────────────────────────
// Domain types
// ─────────────────────────────────────────────────────────────────────────────

// Lease is the persisted leadership record for one lease key.
//
// Mutual-exclusion invariant: at any wall-clock instant, at most one Lease row
// has (HolderID, Generation) considered "live" — i.e. ExpiresAt > now. Renew
// extends ExpiresAt; Acquire by a NEW holder bumps Generation by exactly 1.
type Lease struct {
	Key        string    `json:"key"`
	HolderID   string    `json:"holder_id"`
	AcquiredAt time.Time `json:"acquired_at"`
	RenewedAt  time.Time `json:"renewed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Generation int64     `json:"generation"`
}

// IsLive reports whether the lease is still valid relative to now.
func (l Lease) IsLive(now time.Time) bool {
	return now.Before(l.ExpiresAt)
}

// PeerHeartbeat is a snapshot of one peer's most recent self-reported liveness.
type PeerHeartbeat struct {
	PeerID         string    `json:"peer_id"`
	Address        string    `json:"address,omitempty"`
	LastHeartbeat  time.Time `json:"last_heartbeat"`
	Role           string    `json:"role"`
	GenerationSeen int64     `json:"generation_seen"`
}

// LeaderMarker is an append-only fact written by the current leader. The
// failover tests use markers to prove "no data loss for the lease state":
// markers written by the old leader before failover are still readable by
// the new leader after failover.
type LeaderMarker struct {
	ID         int64     `json:"id"`
	LeaseKey   string    `json:"lease_key"`
	Generation int64     `json:"generation"`
	HolderID   string    `json:"holder_id"`
	Marker     string    `json:"marker"`
	WrittenAt  time.Time `json:"written_at"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Store interface
// ─────────────────────────────────────────────────────────────────────────────

// Store persists HA state: leases, peer heartbeats, and leader markers.
//
// Concrete implementations:
//   - SQLStore: pure-Go modernc.org/sqlite, WAL, single-writer.
//   - MemStore: in-process map for tests; same semantics as SQLStore.
//
// All methods are safe for concurrent use.
type Store interface {
	// ─── Leases ────────────────────────────────────────────────────────────

	// TryAcquire attempts to take the lease for key on behalf of holderID.
	//
	// Semantics (atomic):
	//   - If no row exists for key       → insert (gen=1) and succeed.
	//   - If row exists and is expired   → overwrite; if previous holder
	//                                       differs, bump generation by 1;
	//                                       otherwise keep generation (the
	//                                       same peer recovered its own
	//                                       expired lease).
	//   - If row exists and is live and  → succeed without bumping
	//     held by the same holderID         generation (idempotent re-acquire).
	//   - If row exists and is live and  → return ErrLeaseHeld.
	//     held by a different holderID
	//
	// On success the returned Lease has the new RenewedAt/ExpiresAt.
	TryAcquire(ctx context.Context, key, holderID string, ttl time.Duration) (Lease, error)

	// RenewLease extends the lease deadline for the current holder.
	// Returns ErrNotLeader if the caller is not the current holder, or if
	// the lease has already expired (i.e. someone else may steal it).
	RenewLease(ctx context.Context, key, holderID string, ttl time.Duration) (Lease, error)

	// ReleaseLease deletes the lease row iff holderID is the current holder.
	// Used for graceful step-down on shutdown. No error if the lease does
	// not exist or is held by another peer (best-effort).
	ReleaseLease(ctx context.Context, key, holderID string) error

	// GetLease returns the current lease record for key (live or expired).
	// Returns ErrLeaseNotFound if no row exists.
	GetLease(ctx context.Context, key string) (Lease, error)

	// ─── Peer heartbeats ──────────────────────────────────────────────────

	// PutPeerHeartbeat upserts the heartbeat for one peer.
	PutPeerHeartbeat(ctx context.Context, hb PeerHeartbeat) error

	// GetPeer returns one peer's heartbeat. Returns ErrPeerNotFound if absent.
	GetPeer(ctx context.Context, peerID string) (PeerHeartbeat, error)

	// ListPeers returns all peer heartbeats ordered by peer_id.
	ListPeers(ctx context.Context) ([]PeerHeartbeat, error)

	// ─── Leader markers ───────────────────────────────────────────────────

	// WriteMarker appends a leader-written marker. The caller passes the
	// generation it believes it is operating under; the store does NOT
	// re-verify leadership here — call sites should call RenewLease first
	// in production. Used by tests to prove durability across failover.
	WriteMarker(ctx context.Context, m LeaderMarker) (LeaderMarker, error)

	// ListMarkers returns all markers for a lease key in ID order.
	ListMarkers(ctx context.Context, leaseKey string) ([]LeaderMarker, error)

	// Close closes the underlying DB. No-op for MemStore.
	Close() error
}
