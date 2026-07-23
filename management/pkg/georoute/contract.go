// Package georoute implements multi-region geo-routing + cross-region
// convergence for the Vulos cloud control plane (CLOUD-REGION-01).
//
// # What this package owns
//
//   - Per-tenant home-region assignment (one row per tenant in pure-Go
//     modernc/sqlite at $CP_DB_DIR/georoute.db).
//   - HTTP 307 geo-redirect middleware: requests landing on a non-home region
//     are bounced to the tenant's home-region URL (the simpler shape that
//     fits the existing route handlers — see cmd/server/main.go — versus a
//     reverse-proxy which would couple cp to the bundle-node binary).
//   - Deterministic alternate-region fallback ring (Region.Ordinal + ring
//     order) used when the home region is unhealthy; failover ALWAYS picks
//     the same alt for the same (tenant, home) under steady state.
//   - Cross-region convergence trigger: on failover, fire the SyncTrigger
//     seam so the rendezvous (internal/syncrz) re-converges the tenant
//     between the old + new region. We do NOT reimplement sync — we call.
//
// # Why this design
//
// Self-host multi-location and cloud multi-region use the SAME mechanism:
// CRDT + bucket-lease + rendezvous + fabric-P2P. internal/multiloc owns the
// self-host shape (per-org locations table); this package owns the cloud
// shape (per-tenant home region). Identity + anchor inbox stay global-central
// (Decision D, unchanged) so we never persist or route those here.
//
// # Wire format (CP-GEOROUTE/1)
//
// The geo-route middleware reads X-Tenant-ID (or a configured extractor) +
// the request URL, looks up the home region, and either:
//
//   - passes the request through if it's already on the home region, or
//   - returns HTTP 307 Temporary Redirect to the home-region URL with the
//     same path + query (preserving method semantics for non-GET callers).
//
// 307 (not 302) is intentional: per RFC 7231 §6.4.7, 307 preserves the
// request method, so a POST geo-redirected from a non-home region still
// arrives at the home region as a POST.
//
// # Invariants (FROZEN)
//
//   - Pure-Go modernc.org/sqlite. Never CGO.
//   - No Go-module dep on vulos-mail / vulos-relay / vulos-office.
//   - Opt-in: CP_GEOROUTE_ENABLE=1. Default = no-op (single-region
//     deployments are unchanged).
//   - The SyncTrigger seam is narrow on purpose: we MUST NOT depend on
//     internal/syncrz at the contract layer — that would create an import
//     cycle risk and force a transport choice on the package. main.go wires
//     a concrete adapter at startup.
package georoute

import (
	"context"
	"errors"
	"time"
)

// WireProto is the protocol tag the redirect middleware tags responses with
// (X-Georoute header). Bump on a breaking change.
const WireProto = "CP-GEOROUTE/1"

// Sentinel errors.
var (
	// ErrUnknownTenant is returned by HomeRegion when no row exists.
	ErrUnknownTenant = errors.New("georoute: unknown tenant")
	// ErrInvalidInput is returned for blank tenant_id / region.
	ErrInvalidInput = errors.New("georoute: invalid input")
	// ErrNoRegions is returned when no regions are configured (Ring is empty).
	ErrNoRegions = errors.New("georoute: no regions configured")
	// ErrRegionLocked is returned by Assign when a tenant already has a DIFFERENT
	// home region. home_region is immutable after first set (REGION-SSOT-01): free
	// re-assignment across the several region stores caused split-brain. A
	// deliberate failover/migration must go through ForceAssign / Service
	// .ReassignFailover, which is gated behind an explicit failover flag.
	ErrRegionLocked = errors.New("georoute: home_region is immutable after first set")
)

// Assignment is one (tenant, home_region) row.
type Assignment struct {
	TenantID   string
	HomeRegion string
	AssignedAt time.Time
}

// Store is the persistence contract. Both SQLStore and MemStore implement
// this interface so handlers / Service depend only on the interface (matches
// internal/multiloc + internal/servingpool style).
type Store interface {
	// Assign sets (tenantID → region) on FIRST set only. Re-assigning the SAME
	// region is an idempotent no-op; attempting to CHANGE an existing tenant's
	// region returns ErrRegionLocked (REGION-SSOT-01 immutability). A deliberate
	// failover/migration must use ForceAssign instead.
	Assign(ctx context.Context, tenantID, region string) (Assignment, error)

	// ForceAssign overwrites a tenant's home region unconditionally. It is the
	// EXPLICIT failover/migration path the immutable Assign intentionally forbids;
	// callers must gate it (Service.ReassignFailover). assigned_at advances.
	ForceAssign(ctx context.Context, tenantID, region string) (Assignment, error)

	// HomeRegion returns the home region for tenantID. Returns ErrUnknownTenant
	// when no row exists.
	HomeRegion(ctx context.Context, tenantID string) (string, error)

	// List returns all assignments (used by ops / dashboards / reconciler).
	List(ctx context.Context) ([]Assignment, error)

	// Close releases the underlying DB.
	Close() error
}

// SyncTrigger is the narrow seam over the central rendezvous that this
// package calls on failover to trigger cross-region convergence. The
// concrete adapter is wired in main.go and bridges to internal/syncrz —
// see georoute_wire in cmd/server/main.go.
//
// We pass (tenantID, syncKey) — the same shape syncrz uses — and a "reason"
// for observability. The adapter is responsible for whatever transport call
// the central bucket needs (a no-op cursor advance is sufficient — the box
// pulls on the next cycle).
type SyncTrigger interface {
	Trigger(ctx context.Context, tenantID, syncKey, reason string) error
}

// NoopSyncTrigger is the no-op default used when CP_GEOROUTE_ENABLE is unset
// OR when no syncrz is wired. Failover still routes; just no proactive
// convergence call is made.
type NoopSyncTrigger struct{}

// Trigger satisfies SyncTrigger with a no-op.
func (NoopSyncTrigger) Trigger(_ context.Context, _, _, _ string) error { return nil }

// HealthProbe reports whether a region is currently healthy. The concrete
// impl wraps either an internal/ha health check, a multiloc-style probe, or
// a static map in tests. AlternateRegion uses this to skip sick regions in
// the ring lookup.
type HealthProbe interface {
	IsHealthy(ctx context.Context, region string) bool
}

// StaticHealth is a deterministic HealthProbe for tests + static configs.
type StaticHealth map[string]bool

// IsHealthy reports the map value; missing keys default to true (assume
// healthy unless explicitly marked sick — matches multiloc default).
func (s StaticHealth) IsHealthy(_ context.Context, region string) bool {
	if v, ok := s[region]; ok {
		return v
	}
	return true
}

// CountingSyncTrigger is a test helper that records calls. Useful for the
// "failover triggers sync" unit test without pulling in syncrz.
type CountingSyncTrigger struct {
	Calls []SyncTriggerCall
}

// SyncTriggerCall is one observation recorded by CountingSyncTrigger.
type SyncTriggerCall struct {
	TenantID string
	SyncKey  string
	Reason   string
	At       time.Time
}

// Trigger records the call and returns nil.
func (c *CountingSyncTrigger) Trigger(_ context.Context, tenantID, syncKey, reason string) error {
	c.Calls = append(c.Calls, SyncTriggerCall{
		TenantID: tenantID, SyncKey: syncKey, Reason: reason, At: time.Now().UTC(),
	})
	return nil
}
