package servingpool

import (
	"context"
	"testing"
)

// TestDrainCandidates_OrdersMostIdleFirst verifies the scale-in drain order:
// fewest tenant leases first, then least load, then node id.
func TestDrainCandidates_OrdersMostIdleFirst(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	sched := NewScheduler(store, Config{})

	for _, id := range []string{"n-busy", "n-idle", "n-mid"} {
		if err := sched.AddNode(ctx, Node{ID: id, Health: HealthHealthy, Capacity: 100}); err != nil {
			t.Fatalf("AddNode %s: %v", id, err)
		}
	}
	// Give n-busy 2 leases, n-mid 1 lease, n-idle 0 leases.
	mustLease(t, store, "t1", "n-busy")
	mustLease(t, store, "t2", "n-busy")
	mustLease(t, store, "t3", "n-mid")

	got, err := sched.DrainCandidates(ctx)
	if err != nil {
		t.Fatalf("DrainCandidates: %v", err)
	}
	want := []string{"n-idle", "n-mid", "n-busy"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: got %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}

// TestDrainCandidates_LoadTiebreak verifies that with equal lease counts the
// least-loaded node ranks first.
func TestDrainCandidates_LoadTiebreak(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	sched := NewScheduler(store, Config{})

	if err := sched.AddNode(ctx, Node{ID: "hot", Health: HealthHealthy, Capacity: 100, LoadScore: 0.9}); err != nil {
		t.Fatal(err)
	}
	if err := sched.AddNode(ctx, Node{ID: "cold", Health: HealthHealthy, Capacity: 100, LoadScore: 0.1}); err != nil {
		t.Fatal(err)
	}
	_ = store.SetNodeLoad(ctx, "hot", 0.9)
	_ = store.SetNodeLoad(ctx, "cold", 0.1)
	// Both have zero leases → load decides; cold (least loaded) first.

	got, err := sched.DrainCandidates(ctx)
	if err != nil {
		t.Fatalf("DrainCandidates: %v", err)
	}
	if len(got) != 2 || got[0] != "cold" {
		t.Fatalf("got %v, want [cold hot]", got)
	}
}

// TestDrainCandidates_EmptyPool returns an empty slice (nothing to drain).
func TestDrainCandidates_EmptyPool(t *testing.T) {
	sched := NewScheduler(NewMemStore(), Config{})
	got, err := sched.DrainCandidates(context.Background())
	if err != nil {
		t.Fatalf("DrainCandidates: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty pool got %v, want []", got)
	}
}

func mustLease(t *testing.T, store Store, tenant, node string) {
	t.Helper()
	if err := store.UpsertLease(context.Background(), Lease{TenantID: tenant, NodeID: node}); err != nil {
		t.Fatalf("UpsertLease %s→%s: %v", tenant, node, err)
	}
}
