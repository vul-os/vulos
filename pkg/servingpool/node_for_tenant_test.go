package servingpool

import (
	"context"
	"testing"
	"time"
)

// TestNodeForTenant_ResolvesRegionAndHealth verifies the read-only resolver
// returns the full node record (region + health) for the tenant's placement,
// covering both the consistent-hash preview (no lease) and the leased path.
func TestNodeForTenant_ResolvesRegionAndHealth(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	sched := NewScheduler(store, Config{LeaseTTL: 2 * time.Second})
	if err := sched.AddNode(ctx, Node{ID: "node-jhb", Region: "af-south-1", Capacity: 100, Health: HealthHealthy}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	// No lease yet → resolver falls back to the ring owner, still resolving the
	// node record for region/health.
	nodeID, n, err := sched.NodeForTenant(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("NodeForTenant (pre-lease): %v", err)
	}
	if nodeID != "node-jhb" {
		t.Fatalf("nodeID = %q, want node-jhb", nodeID)
	}
	if n.Region != "af-south-1" || n.Health != HealthHealthy {
		t.Fatalf("node = %+v, want region af-south-1 / healthy", n)
	}

	// After an explicit assign, the resolver reads the leased node.
	if _, err := sched.Assign(ctx, "tenant-1"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	nodeID, n, err = sched.NodeForTenant(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("NodeForTenant (post-lease): %v", err)
	}
	if nodeID != "node-jhb" || n.Region != "af-south-1" {
		t.Fatalf("post-lease node = %q %+v", nodeID, n)
	}
}

// TestNodeForTenant_EmptyWhenNoNodes returns ("", Node{}, nil) when the pool is
// empty so the dashboard renders an empty Pooled list rather than erroring.
func TestNodeForTenant_EmptyWhenNoNodes(t *testing.T) {
	ctx := context.Background()
	sched := NewScheduler(NewMemStore(), Config{})
	nodeID, _, err := sched.NodeForTenant(ctx, "tenant-x")
	if err != nil {
		t.Fatalf("NodeForTenant: %v", err)
	}
	if nodeID != "" {
		t.Fatalf("nodeID = %q, want empty", nodeID)
	}
}

// TestNodeByID_PassThrough confirms NodeByID surfaces the stored record and
// ErrNodeNotFound for an unknown id (used by the dedicated-pin path).
func TestNodeByID_PassThrough(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	sched := NewScheduler(store, Config{})
	if err := sched.AddNode(ctx, Node{ID: "ded-1", Region: "eu-west-1", Health: HealthHealthy, Capacity: 1}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	n, err := sched.NodeByID(ctx, "ded-1")
	if err != nil {
		t.Fatalf("NodeByID: %v", err)
	}
	if n.Region != "eu-west-1" {
		t.Fatalf("region = %q, want eu-west-1", n.Region)
	}
	if _, err := sched.NodeByID(ctx, "nope"); err != ErrNodeNotFound {
		t.Fatalf("NodeByID(unknown): want ErrNodeNotFound, got %v", err)
	}
}
