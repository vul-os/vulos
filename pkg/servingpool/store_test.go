package servingpool

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// openTestSQL opens a SQLStore on a fresh in-memory cpdb (SQLite) for testing.
func openTestSQL(t *testing.T) *SQLStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	s, err := OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSQLStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSQLStore_NodeCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTestSQL(t)

	n := Node{ID: "node-1", Region: "jnb", Capacity: 200, Health: HealthHealthy}
	if err := s.RegisterNode(ctx, n); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := s.GetNode(ctx, "node-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Region != "jnb" || got.Capacity != 200 || got.Health != HealthHealthy {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if err := s.SetNodeHealth(ctx, "node-1", HealthSick); err != nil {
		t.Fatalf("set health: %v", err)
	}
	got, _ = s.GetNode(ctx, "node-1")
	if got.Health != HealthSick {
		t.Fatalf("health not updated: %q", got.Health)
	}
	if err := s.SetNodeLoad(ctx, "node-1", 0.42); err != nil {
		t.Fatalf("set load: %v", err)
	}
	got, _ = s.GetNode(ctx, "node-1")
	if got.LoadScore < 0.41 || got.LoadScore > 0.43 {
		t.Fatalf("load not updated: %.3f", got.LoadScore)
	}
	if err := s.Heartbeat(ctx, "node-1", time.Now()); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	got, _ = s.GetNode(ctx, "node-1")
	if got.LastHeartbeat == nil {
		t.Fatalf("heartbeat not stored")
	}
	if err := s.RemoveNode(ctx, "node-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := s.GetNode(ctx, "node-1"); err != ErrNodeNotFound {
		t.Fatalf("expected ErrNodeNotFound after remove, got %v", err)
	}
}

func TestSQLStore_LeaseGenerationBumpsOnNodeChange(t *testing.T) {
	ctx := context.Background()
	s := openTestSQL(t)

	if err := s.UpsertLease(ctx, Lease{TenantID: "t1", NodeID: "n1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	first, _ := s.GetLease(ctx, "t1")
	if first.Generation != 1 {
		t.Fatalf("first gen = %d, want 1", first.Generation)
	}
	// Re-upsert same node — gen unchanged.
	if err := s.UpsertLease(ctx, Lease{TenantID: "t1", NodeID: "n1"}); err != nil {
		t.Fatalf("upsert same: %v", err)
	}
	same, _ := s.GetLease(ctx, "t1")
	if same.Generation != 1 {
		t.Fatalf("gen bumped on same-node upsert: %d", same.Generation)
	}
	// Move to a different node — gen bumps.
	if err := s.UpsertLease(ctx, Lease{TenantID: "t1", NodeID: "n2"}); err != nil {
		t.Fatalf("upsert move: %v", err)
	}
	moved, _ := s.GetLease(ctx, "t1")
	if moved.Generation != 2 {
		t.Fatalf("gen after move = %d, want 2", moved.Generation)
	}
}

func TestSQLStore_AutoscaleSignalsAppend(t *testing.T) {
	ctx := context.Background()
	s := openTestSQL(t)

	for i := 0; i < 5; i++ {
		if err := s.EmitSignal(ctx, AutoscaleSignal{
			Scope:     "global",
			Action:    ActionHold,
			Reason:    fmt.Sprintf("test-%d", i),
			LoadScore: 0.5,
		}); err != nil {
			t.Fatalf("emit: %v", err)
		}
	}
	got, err := s.RecentSignals(ctx, 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 signals, got %d", len(got))
	}
	// Returned in descending order (most recent first).
	if got[0].Reason != "test-4" {
		t.Fatalf("expected most recent first, got %q", got[0].Reason)
	}
}

// TestSQLStore_PureGoSQLite is an explicit reminder that this package depends
// only on modernc.org/sqlite (no CGO). The build tags + go.sum lock keep it
// that way; this test just exercises the SQL path so a CGO regression would
// show up as a build failure under CGO_ENABLED=0.
func TestSQLStore_PureGoSQLite(t *testing.T) {
	ctx := context.Background()
	s := openTestSQL(t)
	if err := s.RegisterNode(ctx, Node{ID: "x", Health: HealthHealthy}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := s.UpsertLease(ctx, Lease{TenantID: "tx", NodeID: "x"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	n, err := s.CountLeases(ctx)
	if err != nil || n != 1 {
		t.Fatalf("count leases = %d, err=%v", n, err)
	}
}
