package servingpool

import (
	"context"
	"testing"
)

func TestAutoscaler_EmitsScaleUpUnderLoad(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	sched := NewScheduler(store, Config{ScaleUpAt: 0.7, ScaleDownAt: 0.2})
	for _, id := range []string{"n1", "n2"} {
		if err := sched.AddNode(ctx, Node{ID: id, Health: HealthHealthy, Capacity: 100, LoadScore: 0.9}); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	// Bump load via SetNodeLoad (AddNode wrote 0 initially after Heartbeat
	// helper; ensure load is set high).
	if err := store.SetNodeLoad(ctx, "n1", 0.95); err != nil {
		t.Fatalf("set load: %v", err)
	}
	if err := store.SetNodeLoad(ctx, "n2", 0.95); err != nil {
		t.Fatalf("set load: %v", err)
	}
	as := NewAutoscaler(sched, store, Config{ScaleUpAt: 0.7, ScaleDownAt: 0.2})
	sig, err := as.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if sig.Action != ActionScaleUp {
		t.Fatalf("expected scale_up signal, got %q (reason=%s, load=%.2f)", sig.Action, sig.Reason, sig.LoadScore)
	}
	// Persisted?
	recent, err := store.RecentSignals(ctx, 5)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) == 0 || recent[0].Action != ActionScaleUp {
		t.Fatalf("expected persisted scale_up signal, got %+v", recent)
	}
}

func TestAutoscaler_EmitsScaleDownWhenIdle(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	sched := NewScheduler(store, Config{ScaleUpAt: 0.8, ScaleDownAt: 0.4})
	for _, id := range []string{"n1", "n2", "n3"} {
		if err := sched.AddNode(ctx, Node{ID: id, Health: HealthHealthy, Capacity: 100, LoadScore: 0.05}); err != nil {
			t.Fatalf("add: %v", err)
		}
		_ = store.SetNodeLoad(ctx, id, 0.05)
	}
	as := NewAutoscaler(sched, store, Config{ScaleUpAt: 0.8, ScaleDownAt: 0.4})
	sig, err := as.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if sig.Action != ActionScaleDown {
		t.Fatalf("expected scale_down, got %q (reason=%s)", sig.Action, sig.Reason)
	}
}

func TestAutoscaler_HoldsWhenInBounds(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	sched := NewScheduler(store, Config{})
	for _, id := range []string{"a", "b"} {
		if err := sched.AddNode(ctx, Node{ID: id, Health: HealthHealthy, Capacity: 100}); err != nil {
			t.Fatalf("add: %v", err)
		}
		_ = store.SetNodeLoad(ctx, id, 0.5)
	}
	as := NewAutoscaler(sched, store, Config{})
	sig, err := as.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if sig.Action != ActionHold {
		t.Fatalf("expected hold, got %q (reason=%s)", sig.Action, sig.Reason)
	}
}

func TestAutoscaler_ScaleUpWhenLeasesButNoHealthyNodes(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	// Insert a lease pointing at a phantom node so CountLeases > 0 but no
	// healthy nodes exist.
	if err := store.UpsertLease(ctx, Lease{TenantID: "t1", NodeID: "missing"}); err != nil {
		t.Fatalf("upsert lease: %v", err)
	}
	sched := NewScheduler(store, Config{})
	_ = sched.Refresh(ctx)
	as := NewAutoscaler(sched, store, Config{})
	sig, err := as.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if sig.Action != ActionScaleUp {
		t.Fatalf("expected scale_up for orphan leases, got %q (reason=%s)", sig.Action, sig.Reason)
	}
}
