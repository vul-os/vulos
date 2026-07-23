package servingpool

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// newTestScheduler returns a scheduler backed by MemStore with N nodes already
// added and marked healthy.
func newTestScheduler(t *testing.T, nodeIDs ...string) (*Scheduler, Store) {
	t.Helper()
	ctx := context.Background()
	store := NewMemStore()
	sched := NewScheduler(store, Config{LeaseTTL: 2 * time.Second})
	for _, id := range nodeIDs {
		if err := sched.AddNode(ctx, Node{ID: id, Capacity: 100, Health: HealthHealthy}); err != nil {
			t.Fatalf("AddNode(%s): %v", id, err)
		}
	}
	return sched, store
}

func TestScheduler_AssignConsistentHashLease(t *testing.T) {
	ctx := context.Background()
	sched, store := newTestScheduler(t, "node-a", "node-b", "node-c")

	l1, err := sched.Assign(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if l1.NodeID == "" {
		t.Fatalf("expected non-empty node assignment")
	}
	if l1.Generation != 1 {
		t.Fatalf("first assignment generation = %d, want 1", l1.Generation)
	}
	// Re-assign within TTL — same lease, same node.
	l2, err := sched.Assign(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("re-assign: %v", err)
	}
	if l2.NodeID != l1.NodeID {
		t.Fatalf("lease moved within TTL: %q → %q", l1.NodeID, l2.NodeID)
	}
	// Stored in the store?
	got, err := store.GetLease(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if got.NodeID != l1.NodeID {
		t.Fatalf("store lease node mismatch: %q vs %q", got.NodeID, l1.NodeID)
	}
}

func TestScheduler_AssignFailsWhenNoHealthyNodes(t *testing.T) {
	ctx := context.Background()
	sched, _ := newTestScheduler(t /* no nodes */)
	if _, err := sched.Assign(ctx, "tenant-x"); err != ErrNoHealthyNodes {
		t.Fatalf("expected ErrNoHealthyNodes, got %v", err)
	}
}

func TestScheduler_ManyTenantsPerNode(t *testing.T) {
	ctx := context.Background()
	sched, store := newTestScheduler(t, "node-a", "node-b")
	const N = 500
	for i := 0; i < N; i++ {
		_, err := sched.Assign(ctx, fmt.Sprintf("tenant-%04d", i))
		if err != nil {
			t.Fatalf("assign %d: %v", i, err)
		}
	}
	a, _ := store.ListLeasesByNode(ctx, "node-a")
	b, _ := store.ListLeasesByNode(ctx, "node-b")
	if len(a)+len(b) != N {
		t.Fatalf("expected %d total leases, got a=%d b=%d", N, len(a), len(b))
	}
	// Each node must host many tenants (the whole point of the pool).
	if len(a) < 50 || len(b) < 50 {
		t.Fatalf("uneven multi-tenancy: a=%d b=%d (each should be >=50)", len(a), len(b))
	}
}

func TestScheduler_LeaseHandoffOnNodeAdd(t *testing.T) {
	ctx := context.Background()
	sched, _ := newTestScheduler(t, "node-a", "node-b")
	const N = 200
	// Snapshot initial assignments.
	initial := make(map[string]string)
	for i := 0; i < N; i++ {
		tid := fmt.Sprintf("t-%03d", i)
		l, err := sched.Assign(ctx, tid)
		if err != nil {
			t.Fatalf("assign: %v", err)
		}
		initial[tid] = l.NodeID
	}
	// Add a new node — the ring rebalances.
	if err := sched.AddNode(ctx, Node{ID: "node-c", Health: HealthHealthy, Capacity: 100}); err != nil {
		t.Fatalf("add node-c: %v", err)
	}
	// Force re-assignment by expiring leases (set TTL very short and renew).
	time.Sleep(2100 * time.Millisecond)
	movedToC := 0
	for tid := range initial {
		l, err := sched.Assign(ctx, tid)
		if err != nil {
			t.Fatalf("re-assign: %v", err)
		}
		if l.NodeID == "node-c" {
			movedToC++
		}
	}
	if movedToC == 0 {
		t.Fatalf("no tenants migrated to node-c after handoff")
	}
}

func TestScheduler_LeaseHandoffOnNodeRemove(t *testing.T) {
	ctx := context.Background()
	sched, store := newTestScheduler(t, "node-a", "node-b", "node-c")
	const N = 150
	for i := 0; i < N; i++ {
		if _, err := sched.Assign(ctx, fmt.Sprintf("t-%03d", i)); err != nil {
			t.Fatalf("assign: %v", err)
		}
	}
	// Count tenants on node-c so we know there is work to migrate.
	cLeases, _ := store.ListLeasesByNode(ctx, "node-c")
	if len(cLeases) == 0 {
		t.Skipf("node-c had no tenants; test inconclusive")
	}

	if err := sched.RemoveNode(ctx, "node-c"); err != nil {
		t.Fatalf("remove node-c: %v", err)
	}

	// After removal the leases for the removed node must have been deleted;
	// a fresh Assign for those tenants must give a new (a or b) node.
	for _, l := range cLeases {
		got, err := sched.Assign(ctx, l.TenantID)
		if err != nil {
			t.Fatalf("re-assign after remove: %v", err)
		}
		if got.NodeID == "node-c" {
			t.Fatalf("tenant %s still on removed node-c", l.TenantID)
		}
		if got.NodeID != "node-a" && got.NodeID != "node-b" {
			t.Fatalf("unexpected new owner %q", got.NodeID)
		}
	}
}

func TestScheduler_HealthGatedAssignment(t *testing.T) {
	ctx := context.Background()
	sched, _ := newTestScheduler(t, "node-a", "node-b")

	// Sicken node-a — only node-b should receive NEW assignments.
	if err := sched.MarkHealth(ctx, "node-a", HealthSick); err != nil {
		t.Fatalf("mark sick: %v", err)
	}
	if sched.HealthyNodeCount() != 1 {
		t.Fatalf("expected 1 healthy node, got %d", sched.HealthyNodeCount())
	}
	for i := 0; i < 50; i++ {
		l, err := sched.Assign(ctx, fmt.Sprintf("fresh-%d", i))
		if err != nil {
			t.Fatalf("assign: %v", err)
		}
		if l.NodeID != "node-b" {
			t.Fatalf("sick node received assignment: %q", l.NodeID)
		}
	}
}

func TestScheduler_DrainOnHealthChange(t *testing.T) {
	ctx := context.Background()
	sched, store := newTestScheduler(t, "node-a", "node-b")
	// Pre-load 100 tenants.
	const N = 100
	for i := 0; i < N; i++ {
		if _, err := sched.Assign(ctx, fmt.Sprintf("t-%03d", i)); err != nil {
			t.Fatalf("assign: %v", err)
		}
	}
	preA, _ := store.ListLeasesByNode(ctx, "node-a")
	if len(preA) == 0 {
		t.Skipf("node-a empty; test inconclusive")
	}
	// Snapshot which tenants were originally on node-a — only these should
	// have their generation bumped after the drain handoff.
	migrated := make(map[string]bool, len(preA))
	for _, l := range preA {
		migrated[l.TenantID] = true
	}

	// Drain node-a.
	if err := sched.MarkHealth(ctx, "node-a", HealthDraining); err != nil {
		t.Fatalf("drain: %v", err)
	}
	postA, _ := store.ListLeasesByNode(ctx, "node-a")
	if len(postA) != 0 {
		t.Fatalf("expected node-a to be drained, still has %d leases", len(postA))
	}
	// All tenants must now live on node-b.
	postB, _ := store.ListLeasesByNode(ctx, "node-b")
	if len(postB) != N {
		t.Fatalf("expected node-b to host all %d tenants after drain, has %d", N, len(postB))
	}
	// Generation should have bumped for migrated tenants. Tenants that were
	// originally on node-b retain gen=1.
	bumped := 0
	for _, l := range postB {
		if migrated[l.TenantID] {
			if l.Generation < 2 {
				t.Fatalf("expected generation>=2 on migrated lease %s, got %d", l.TenantID, l.Generation)
			}
			bumped++
		}
	}
	if bumped != len(preA) {
		t.Fatalf("expected %d migrated leases bumped, got %d", len(preA), bumped)
	}
}

func TestScheduler_LookupNodeStable(t *testing.T) {
	sched, _ := newTestScheduler(t, "node-a", "node-b", "node-c")
	owner := sched.LookupNode("tenant-foo")
	if owner == "" {
		t.Fatalf("expected a hash owner")
	}
	for i := 0; i < 20; i++ {
		if got := sched.LookupNode("tenant-foo"); got != owner {
			t.Fatalf("LookupNode unstable: %q vs %q", got, owner)
		}
	}
}
