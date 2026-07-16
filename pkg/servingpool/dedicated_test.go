package servingpool

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// newDedicatedTestSetup wires a Scheduler + DedicatedManager backed by MemStore
// with two healthy bundle nodes. Lease TTL is short so handoff-style assertions
// don't depend on timer fuzz.
func newDedicatedTestSetup(t *testing.T) (*Scheduler, *DedicatedManager, Store) {
	t.Helper()
	ctx := context.Background()
	store := NewMemStore()
	sched := NewScheduler(store, Config{LeaseTTL: 2 * time.Second})
	for _, id := range []string{"node-a", "node-b"} {
		if err := sched.AddNode(ctx, Node{ID: id, Capacity: 100, Health: HealthHealthy}); err != nil {
			t.Fatalf("AddNode(%s): %v", id, err)
		}
	}
	return sched, NewDedicatedManager(sched), store
}

func TestDedicated_PinTenant_SchedulerHonoursPin(t *testing.T) {
	ctx := context.Background()
	sched, mgr, _ := newDedicatedTestSetup(t)

	// Establish a "wrong" baseline: tenant currently lives on whatever the
	// ring picks. We pin to the OTHER node and confirm Assign returns the pin.
	baseline, err := sched.Assign(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("baseline assign: %v", err)
	}
	target := "node-a"
	if baseline.NodeID == "node-a" {
		target = "node-b"
	}
	pin, err := mgr.PinTenant(ctx, "tenant-1", target, "enterprise SLA")
	if err != nil {
		t.Fatalf("PinTenant: %v", err)
	}
	if pin.NodeID != target {
		t.Fatalf("pin stored wrong node: %q want %q", pin.NodeID, target)
	}

	// Every subsequent Assign must return the pinned node, no matter the hash.
	for i := 0; i < 25; i++ {
		got, err := sched.Assign(ctx, "tenant-1")
		if err != nil {
			t.Fatalf("Assign #%d: %v", i, err)
		}
		if got.NodeID != target {
			t.Fatalf("Assign #%d ignored pin: got %q want %q", i, got.NodeID, target)
		}
	}
}

func TestDedicated_PinRequiresExistingNode(t *testing.T) {
	ctx := context.Background()
	_, mgr, _ := newDedicatedTestSetup(t)
	if _, err := mgr.PinTenant(ctx, "tenant-x", "ghost-node", ""); err == nil {
		t.Fatal("expected PinTenant to error on unknown node, got nil")
	} else if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("expected ErrNodeNotFound, got %v", err)
	}
}

func TestDedicated_UnpinRevertsToHash(t *testing.T) {
	ctx := context.Background()
	sched, mgr, _ := newDedicatedTestSetup(t)

	// Pin to whichever node is NOT the hash owner so the contrast is visible.
	hashOwner := sched.LookupNode("tenant-2")
	target := "node-a"
	if hashOwner == "node-a" {
		target = "node-b"
	}
	if _, err := mgr.PinTenant(ctx, "tenant-2", target, ""); err != nil {
		t.Fatalf("pin: %v", err)
	}
	pinned, _ := sched.Assign(ctx, "tenant-2")
	if pinned.NodeID != target {
		t.Fatalf("pin not honoured: %q want %q", pinned.NodeID, target)
	}

	// Unpin — the next Assign after lease expiry must return to the hash owner.
	if err := mgr.UnpinTenant(ctx, "tenant-2"); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	// Force re-evaluation by expiring the lease (TTL = 2s above).
	time.Sleep(2100 * time.Millisecond)
	post, err := sched.Assign(ctx, "tenant-2")
	if err != nil {
		t.Fatalf("post-unpin assign: %v", err)
	}
	if post.NodeID != hashOwner {
		t.Fatalf("unpin did not revert to hash owner: got %q want %q", post.NodeID, hashOwner)
	}
}

func TestDedicated_UnpinUnknownErrors(t *testing.T) {
	ctx := context.Background()
	_, mgr, _ := newDedicatedTestSetup(t)
	if err := mgr.UnpinTenant(ctx, "no-such-tenant"); !errors.Is(err, ErrPinNotFound) {
		t.Fatalf("expected ErrPinNotFound, got %v", err)
	}
}

func TestDedicated_PinStickyOnNodeDrain(t *testing.T) {
	// A pinned tenant must NOT be migrated when its node is drained — pin is
	// stronger than the drain handoff. Pooled tenants still move.
	ctx := context.Background()
	sched, mgr, store := newDedicatedTestSetup(t)

	// Pool a non-pinned tenant onto node-a (we'll force it there by removing b
	// temporarily — but simpler: drain b later instead; arrange the pin on a).
	if _, err := mgr.PinTenant(ctx, "pinned-tenant", "node-a", "isolation"); err != nil {
		t.Fatalf("pin: %v", err)
	}
	// Confirm the pinned tenant's lease lives on node-a.
	if l, _ := sched.Assign(ctx, "pinned-tenant"); l.NodeID != "node-a" {
		t.Fatalf("pre-drain pinned lease on %q, want node-a", l.NodeID)
	}

	// Drain node-a. Pooled tenants would migrate; the pinned one stays.
	if err := sched.MarkHealth(ctx, "node-a", HealthDraining); err != nil {
		t.Fatalf("drain: %v", err)
	}
	leases, _ := store.ListLeasesByNode(ctx, "node-a")
	found := false
	for _, l := range leases {
		if l.TenantID == "pinned-tenant" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pinned tenant was migrated off draining node — pin must be sticky")
	}
	// And Assign still returns the pin even when the node is non-healthy.
	got, err := sched.Assign(ctx, "pinned-tenant")
	if err != nil {
		t.Fatalf("post-drain assign: %v", err)
	}
	if got.NodeID != "node-a" {
		t.Fatalf("pin not honoured on drained node: got %q", got.NodeID)
	}
}

func TestDedicated_VDIPoolSchedulesOntoHealthyNode(t *testing.T) {
	ctx := context.Background()
	_, mgr, _ := newDedicatedTestSetup(t)

	slice, err := mgr.ScheduleVDISlice(ctx, "user-1", "acct-1", "tenant-1", VDIHostPool)
	if err != nil {
		t.Fatalf("ScheduleVDISlice(pool): %v", err)
	}
	if slice.NodeID != "node-a" && slice.NodeID != "node-b" {
		t.Fatalf("expected slice on a healthy node, got %q", slice.NodeID)
	}
	if slice.HostKind != VDIHostPool {
		t.Fatalf("host_kind = %q, want %q", slice.HostKind, VDIHostPool)
	}
	// Round-trip via the manager.
	got, err := mgr.GetVDISlice(ctx, "user-1")
	if err != nil || got.NodeID != slice.NodeID {
		t.Fatalf("Get round-trip mismatch: got=%+v err=%v", got, err)
	}
}

func TestDedicated_VDIDedicatedRequiresPin(t *testing.T) {
	ctx := context.Background()
	_, mgr, _ := newDedicatedTestSetup(t)

	// No pin yet → dedicated VDI must fail.
	_, err := mgr.ScheduleVDISlice(ctx, "user-2", "acct-2", "tenant-2", VDIHostDedicated)
	if !errors.Is(err, ErrDedicatedRequiresPin) {
		t.Fatalf("expected ErrDedicatedRequiresPin, got %v", err)
	}

	// Pin the tenant then retry — must succeed and land on the pinned node.
	if _, err := mgr.PinTenant(ctx, "tenant-2", "node-b", ""); err != nil {
		t.Fatalf("pin: %v", err)
	}
	slice, err := mgr.ScheduleVDISlice(ctx, "user-2", "acct-2", "tenant-2", VDIHostDedicated)
	if err != nil {
		t.Fatalf("ScheduleVDISlice(dedicated): %v", err)
	}
	if slice.NodeID != "node-b" {
		t.Fatalf("dedicated slice on wrong node: got %q want node-b", slice.NodeID)
	}
	if slice.HostKind != VDIHostDedicated {
		t.Fatalf("host_kind = %q, want %q", slice.HostKind, VDIHostDedicated)
	}
}

func TestDedicated_VDIInvalidHostKindErrors(t *testing.T) {
	ctx := context.Background()
	_, mgr, _ := newDedicatedTestSetup(t)
	if _, err := mgr.ScheduleVDISlice(ctx, "u", "a", "t", "bogus"); !errors.Is(err, ErrInvalidHostKind) {
		t.Fatalf("expected ErrInvalidHostKind, got %v", err)
	}
}

func TestDedicated_VDIReleaseDeletes(t *testing.T) {
	ctx := context.Background()
	_, mgr, _ := newDedicatedTestSetup(t)
	if _, err := mgr.ScheduleVDISlice(ctx, "user-3", "acct", "tenant", VDIHostPool); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if err := mgr.ReleaseVDISlice(ctx, "user-3"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := mgr.GetVDISlice(ctx, "user-3"); !errors.Is(err, ErrVDINotFound) {
		t.Fatalf("expected ErrVDINotFound after release, got %v", err)
	}
}

func TestDedicated_ListPinsAndSlices(t *testing.T) {
	ctx := context.Background()
	_, mgr, _ := newDedicatedTestSetup(t)
	for i := 0; i < 3; i++ {
		tid := fmt.Sprintf("tenant-%d", i)
		nid := "node-a"
		if i%2 == 1 {
			nid = "node-b"
		}
		if _, err := mgr.PinTenant(ctx, tid, nid, ""); err != nil {
			t.Fatalf("pin %d: %v", i, err)
		}
		if _, err := mgr.ScheduleVDISlice(ctx, fmt.Sprintf("u-%d", i), "a", tid, VDIHostDedicated); err != nil {
			t.Fatalf("schedule %d: %v", i, err)
		}
	}
	pins, err := mgr.ListPins(ctx)
	if err != nil || len(pins) != 3 {
		t.Fatalf("ListPins len=%d err=%v", len(pins), err)
	}
	slices, err := mgr.ListVDISlices(ctx)
	if err != nil || len(slices) != 3 {
		t.Fatalf("ListVDISlices len=%d err=%v", len(slices), err)
	}
}

// TestDedicated_SQLStorePersistsPinsAndSlices smoke-tests the pure-Go SQLite
// backend so a CGO regression or migration mistake fails loudly here.
func TestDedicated_SQLStorePersistsPinsAndSlices(t *testing.T) {
	ctx := context.Background()
	s := openTestSQL(t)
	if err := s.RegisterNode(ctx, Node{ID: "n1", Health: HealthHealthy}); err != nil {
		t.Fatalf("register: %v", err)
	}
	pin := TenantPin{TenantID: "t1", NodeID: "n1", Reason: "test"}
	if err := s.UpsertTenantPin(ctx, pin); err != nil {
		t.Fatalf("upsert pin: %v", err)
	}
	got, err := s.GetTenantPin(ctx, "t1")
	if err != nil || got.NodeID != "n1" || got.Reason != "test" {
		t.Fatalf("get pin mismatch: %+v err=%v", got, err)
	}
	// Re-pin updates node + bumps updated_at; created_at preserved.
	pin.NodeID = "n2"
	pin.Reason = "updated"
	_ = s.RegisterNode(ctx, Node{ID: "n2", Health: HealthHealthy})
	if err := s.UpsertTenantPin(ctx, pin); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, _ = s.GetTenantPin(ctx, "t1")
	if got.NodeID != "n2" || got.Reason != "updated" {
		t.Fatalf("re-pin not applied: %+v", got)
	}
	if err := s.DeleteTenantPin(ctx, "t1"); err != nil {
		t.Fatalf("delete pin: %v", err)
	}
	if _, err := s.GetTenantPin(ctx, "t1"); !errors.Is(err, ErrPinNotFound) {
		t.Fatalf("expected ErrPinNotFound after delete, got %v", err)
	}

	// VDI slice round-trip.
	slice := VDISlice{
		UserID:    "u1",
		AccountID: "a1",
		TenantID:  "t1",
		HostKind:  VDIHostPool,
		NodeID:    "n1",
	}
	if err := s.UpsertVDISlice(ctx, slice); err != nil {
		t.Fatalf("upsert slice: %v", err)
	}
	gv, err := s.GetVDISlice(ctx, "u1")
	if err != nil || gv.NodeID != "n1" || gv.HostKind != VDIHostPool {
		t.Fatalf("get slice mismatch: %+v err=%v", gv, err)
	}
	if err := s.DeleteVDISlice(ctx, "u1"); err != nil {
		t.Fatalf("delete slice: %v", err)
	}
	if _, err := s.GetVDISlice(ctx, "u1"); !errors.Is(err, ErrVDINotFound) {
		t.Fatalf("expected ErrVDINotFound after delete, got %v", err)
	}
}

func TestDedicated_InvalidHostKindAtStoreLayer(t *testing.T) {
	ctx := context.Background()
	s := openTestSQL(t)
	_ = s.RegisterNode(ctx, Node{ID: "n1", Health: HealthHealthy})
	err := s.UpsertVDISlice(ctx, VDISlice{
		UserID: "u1", AccountID: "a", TenantID: "t",
		HostKind: "bogus", NodeID: "n1",
	})
	if !errors.Is(err, ErrInvalidHostKind) {
		t.Fatalf("expected ErrInvalidHostKind, got %v", err)
	}
}
