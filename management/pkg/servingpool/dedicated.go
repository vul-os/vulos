// dedicated.go — CLOUD-DEDICATED-01: dedicated single-tenant pinning + per-user
// VDI slice scheduling. Extends the existing pooled scheduler — does NOT
// replace it.
//
// Two add-ons land here:
//
//  1. PinTenant(tenantID, nodeID) locks a tenant to a specific bundle node;
//     UnpinTenant(tenantID) reverts to the consistent-hash placement. The
//     existing Scheduler.Assign honours the pin (skips the ring), so any
//     caller that already uses Assign automatically gets dedicated routing
//     for pinned tenants. Existing pooled behaviour is unchanged when no pin
//     is present.
//
//  2. ScheduleVDISlice(userID, hostKind) provisions a per-user full-desktop
//     (cage/labwc) VDI attachment. hostKind ∈ {"pool", "dedicated"}:
//     - "pool"      → pick a healthy node via the existing scheduler
//     - "dedicated" → require an existing pin on the user's tenant; fail
//     with ErrDedicatedRequiresPin otherwise.
//     One attachment row per user_id — re-scheduling overwrites the slice.
//
// Billing for these SKUs lives next to the GPU SKU catalogue in
// internal/gpusku/dedicated_sku.go and flows through the same wallet-meter
// pattern (no new payment surface).
//
// INVARIANTS (frozen):
//   - Pure-Go modernc.org/sqlite — NEVER CGO.
//   - No Go-module dependency on vulos-mail / vulos-relay / vulos-office.
//   - Opt-in: cloud control plane wires this only when CP_DEDICATED_ENABLE=1.
package servingpool

import (
	"context"
	"errors"
	"fmt"
)

// DedicatedManager exposes the dedicated-node + VDI scheduling APIs. It is a
// thin orchestration layer over the existing Scheduler + Store — most of the
// behaviour (pin-aware Assign) is already inside Scheduler.Assign.
//
// Construct with NewDedicatedManager(sched). Safe for concurrent use; both
// the store and the scheduler are.
type DedicatedManager struct {
	sched *Scheduler
	store Store
}

// NewDedicatedManager returns a manager bound to sched. The scheduler's Store
// is reused so pins + VDI slices live alongside leases in the same DB.
func NewDedicatedManager(sched *Scheduler) *DedicatedManager {
	return &DedicatedManager{sched: sched, store: sched.store}
}

// PinTenant binds tenantID to nodeID. After this call:
//   - Scheduler.Assign(tenantID) returns nodeID regardless of consistent-hash;
//   - drainNode skips the tenant on health-change handoffs.
//
// nodeID must be a registered node (we validate via GetNode). A re-pin to a
// different node is allowed and bumps the lease generation on next Assign.
// reason is free-form (e.g. "enterprise SLA", "isolation").
func (d *DedicatedManager) PinTenant(ctx context.Context, tenantID, nodeID, reason string) (TenantPin, error) {
	if tenantID == "" || nodeID == "" {
		return TenantPin{}, errors.New("servingpool: tenant_id and node_id required")
	}
	if _, err := d.store.GetNode(ctx, nodeID); err != nil {
		return TenantPin{}, fmt.Errorf("servingpool: pin tenant %q: %w", tenantID, err)
	}
	pin := TenantPin{
		TenantID: tenantID,
		NodeID:   nodeID,
		Reason:   reason,
	}
	if err := d.store.UpsertTenantPin(ctx, pin); err != nil {
		return TenantPin{}, err
	}
	// Eagerly refresh the lease so callers see the pin take effect immediately
	// (without waiting for the existing lease to expire).
	if _, err := d.sched.assignToNode(ctx, tenantID, nodeID); err != nil {
		return TenantPin{}, fmt.Errorf("servingpool: pin refresh lease: %w", err)
	}
	return d.store.GetTenantPin(ctx, tenantID)
}

// UnpinTenant removes the pin for tenantID. Returns ErrPinNotFound if no pin
// exists. The next Assign reverts to the consistent-hash ring (the existing
// lease is left in place; it will migrate on its next expiry / handoff).
func (d *DedicatedManager) UnpinTenant(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return errors.New("servingpool: tenant id required")
	}
	return d.store.DeleteTenantPin(ctx, tenantID)
}

// GetPin returns the current pin for tenantID, or ErrPinNotFound.
func (d *DedicatedManager) GetPin(ctx context.Context, tenantID string) (TenantPin, error) {
	return d.store.GetTenantPin(ctx, tenantID)
}

// ListPins returns all current pins (sorted by tenant_id). Useful for dashboards.
func (d *DedicatedManager) ListPins(ctx context.Context) ([]TenantPin, error) {
	return d.store.ListTenantPins(ctx)
}

// ScheduleVDISlice provisions (or re-provisions) a per-user VDI attachment.
//
//   - hostKind == VDIHostPool       → pick a healthy node via Scheduler.Assign
//     keyed on tenantID (so the user lands on
//     the same bundle node as their tenant's
//     pooled placement).
//   - hostKind == VDIHostDedicated  → require an existing pin on tenantID and
//     attach the slice to the pinned node. If
//     no pin exists, returns ErrDedicatedRequiresPin.
//
// One slice per userID — re-scheduling overwrites the previous row. The
// returned VDISlice reflects the persisted state (NodeID resolved).
func (d *DedicatedManager) ScheduleVDISlice(
	ctx context.Context,
	userID, accountID, tenantID, hostKind string,
) (VDISlice, error) {
	if userID == "" || tenantID == "" {
		return VDISlice{}, errors.New("servingpool: user_id and tenant_id required")
	}
	switch hostKind {
	case VDIHostPool, VDIHostDedicated:
		// ok
	default:
		return VDISlice{}, ErrInvalidHostKind
	}

	var nodeID string
	switch hostKind {
	case VDIHostDedicated:
		pin, err := d.store.GetTenantPin(ctx, tenantID)
		if err != nil {
			if errors.Is(err, ErrPinNotFound) {
				return VDISlice{}, ErrDedicatedRequiresPin
			}
			return VDISlice{}, err
		}
		nodeID = pin.NodeID
	case VDIHostPool:
		// Reuse the existing scheduler so the VDI slice co-locates with the
		// tenant's pooled placement. This also honours pins transparently —
		// a pinned tenant whose user opts into "pool" still lands on the pin.
		lease, err := d.sched.Assign(ctx, tenantID)
		if err != nil {
			return VDISlice{}, fmt.Errorf("servingpool: schedule vdi (pool): %w", err)
		}
		nodeID = lease.NodeID
	}

	slice := VDISlice{
		UserID:    userID,
		AccountID: accountID,
		TenantID:  tenantID,
		HostKind:  hostKind,
		NodeID:    nodeID,
	}
	if err := d.store.UpsertVDISlice(ctx, slice); err != nil {
		return VDISlice{}, err
	}
	return d.store.GetVDISlice(ctx, userID)
}

// GetVDISlice returns the current slice for userID, or ErrVDINotFound.
func (d *DedicatedManager) GetVDISlice(ctx context.Context, userID string) (VDISlice, error) {
	return d.store.GetVDISlice(ctx, userID)
}

// ReleaseVDISlice deletes the slice for userID. Returns ErrVDINotFound if no
// row exists. Billing for the slice stops on the next meter tick after this.
func (d *DedicatedManager) ReleaseVDISlice(ctx context.Context, userID string) error {
	if userID == "" {
		return errors.New("servingpool: user id required")
	}
	return d.store.DeleteVDISlice(ctx, userID)
}

// ListVDISlices returns all current slices (sorted by user_id).
func (d *DedicatedManager) ListVDISlices(ctx context.Context) ([]VDISlice, error) {
	return d.store.ListVDISlices(ctx)
}
