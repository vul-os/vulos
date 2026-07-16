// store_pg_test.go — Postgres integration tests for the servingpool store.
//
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/servingpool/... -run TestPG_
package servingpool

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openPGTestStore(t *testing.T) *SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("servingpool_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	s, err := OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSQLStore (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS servingpool_pgtest CASCADE`)
		_ = s.Close()
	})
	return s
}

func TestPG_NodeCRUD(t *testing.T) {
	ctx := context.Background()
	s := openPGTestStore(t)

	n := Node{ID: "pg-node-1", Region: "eu-west", Capacity: 500, Health: HealthHealthy}
	if err := s.RegisterNode(ctx, n); err != nil {
		t.Fatalf("RegisterNode: %v", err)
	}
	got, err := s.GetNode(ctx, "pg-node-1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Region != "eu-west" || got.Capacity != 500 || got.Health != HealthHealthy {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	if err := s.SetNodeHealth(ctx, "pg-node-1", HealthDegraded); err != nil {
		t.Fatalf("SetNodeHealth: %v", err)
	}
	got, _ = s.GetNode(ctx, "pg-node-1")
	if got.Health != HealthDegraded {
		t.Errorf("health not updated: %q", got.Health)
	}

	if err := s.SetNodeLoad(ctx, "pg-node-1", 0.75); err != nil {
		t.Fatalf("SetNodeLoad: %v", err)
	}
	got, _ = s.GetNode(ctx, "pg-node-1")
	if got.LoadScore < 0.74 || got.LoadScore > 0.76 {
		t.Errorf("load not updated: %.3f", got.LoadScore)
	}

	when := time.Now().UTC()
	if err := s.Heartbeat(ctx, "pg-node-1", when); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	got, _ = s.GetNode(ctx, "pg-node-1")
	if got.LastHeartbeat == nil {
		t.Error("heartbeat not stored")
	}

	if err := s.RemoveNode(ctx, "pg-node-1"); err != nil {
		t.Fatalf("RemoveNode: %v", err)
	}
	if _, err := s.GetNode(ctx, "pg-node-1"); err != ErrNodeNotFound {
		t.Errorf("expected ErrNodeNotFound, got %v", err)
	}
}

func TestPG_LeaseGenerationBump(t *testing.T) {
	ctx := context.Background()
	s := openPGTestStore(t)

	if err := s.UpsertLease(ctx, Lease{TenantID: "pg-t1", NodeID: "pg-n1"}); err != nil {
		t.Fatalf("UpsertLease: %v", err)
	}
	first, _ := s.GetLease(ctx, "pg-t1")
	if first.Generation != 1 {
		t.Fatalf("first gen = %d, want 1", first.Generation)
	}

	// Same node — gen stays.
	if err := s.UpsertLease(ctx, Lease{TenantID: "pg-t1", NodeID: "pg-n1"}); err != nil {
		t.Fatalf("UpsertLease same: %v", err)
	}
	same, _ := s.GetLease(ctx, "pg-t1")
	if same.Generation != 1 {
		t.Fatalf("gen bumped on same-node upsert: %d", same.Generation)
	}

	// Different node — gen bumps.
	if err := s.UpsertLease(ctx, Lease{TenantID: "pg-t1", NodeID: "pg-n2"}); err != nil {
		t.Fatalf("UpsertLease move: %v", err)
	}
	moved, _ := s.GetLease(ctx, "pg-t1")
	if moved.Generation != 2 {
		t.Fatalf("gen after move = %d, want 2", moved.Generation)
	}
}

func TestPG_AutoscaleSignals(t *testing.T) {
	ctx := context.Background()
	s := openPGTestStore(t)

	for i := 0; i < 4; i++ {
		if err := s.EmitSignal(ctx, AutoscaleSignal{
			Scope:     "global",
			Action:    ActionScaleUp,
			Reason:    fmt.Sprintf("pg-signal-%d", i),
			LoadScore: float64(i) * 0.2,
		}); err != nil {
			t.Fatalf("EmitSignal[%d]: %v", i, err)
		}
	}

	sigs, err := s.RecentSignals(ctx, 10)
	if err != nil {
		t.Fatalf("RecentSignals: %v", err)
	}
	if len(sigs) != 4 {
		t.Errorf("RecentSignals len = %d, want 4", len(sigs))
	}
	// Returned descending by id.
	if sigs[0].Reason != "pg-signal-3" {
		t.Errorf("expected newest first, got %q", sigs[0].Reason)
	}
}

func TestPG_TenantPins(t *testing.T) {
	ctx := context.Background()
	s := openPGTestStore(t)

	pin := TenantPin{TenantID: "pg-tenant-1", NodeID: "pg-dedicated-node", Reason: "enterprise SLA"}
	if err := s.UpsertTenantPin(ctx, pin); err != nil {
		t.Fatalf("UpsertTenantPin: %v", err)
	}

	got, err := s.GetTenantPin(ctx, "pg-tenant-1")
	if err != nil {
		t.Fatalf("GetTenantPin: %v", err)
	}
	if got.NodeID != "pg-dedicated-node" || got.Reason != "enterprise SLA" {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	pins, err := s.ListTenantPins(ctx)
	if err != nil {
		t.Fatalf("ListTenantPins: %v", err)
	}
	if len(pins) != 1 {
		t.Errorf("ListTenantPins len = %d, want 1", len(pins))
	}

	if err := s.DeleteTenantPin(ctx, "pg-tenant-1"); err != nil {
		t.Fatalf("DeleteTenantPin: %v", err)
	}
	if _, err := s.GetTenantPin(ctx, "pg-tenant-1"); err != ErrPinNotFound {
		t.Errorf("expected ErrPinNotFound, got %v", err)
	}
}

func TestPG_VDISlices(t *testing.T) {
	ctx := context.Background()
	s := openPGTestStore(t)

	// Register a node to satisfy FK if any.
	_ = s.RegisterNode(ctx, Node{ID: "pg-vdi-node", Health: HealthHealthy})

	v := VDISlice{
		UserID:    "pg-user-1",
		AccountID: "pg-acct-1",
		TenantID:  "pg-tenant-vdi",
		HostKind:  VDIHostPool,
		NodeID:    "pg-vdi-node",
	}
	if err := s.UpsertVDISlice(ctx, v); err != nil {
		t.Fatalf("UpsertVDISlice: %v", err)
	}

	got, err := s.GetVDISlice(ctx, "pg-user-1")
	if err != nil {
		t.Fatalf("GetVDISlice: %v", err)
	}
	if got.NodeID != "pg-vdi-node" || got.HostKind != VDIHostPool {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	slices, err := s.ListVDISlices(ctx)
	if err != nil {
		t.Fatalf("ListVDISlices: %v", err)
	}
	if len(slices) != 1 {
		t.Errorf("ListVDISlices len = %d, want 1", len(slices))
	}

	if err := s.DeleteVDISlice(ctx, "pg-user-1"); err != nil {
		t.Fatalf("DeleteVDISlice: %v", err)
	}
	if _, err := s.GetVDISlice(ctx, "pg-user-1"); err != ErrVDINotFound {
		t.Errorf("expected ErrVDINotFound, got %v", err)
	}
}
