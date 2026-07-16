package ha_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/ha"
)

func openPGHAStore(t *testing.T) *ha.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("ha_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := ha.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("ha.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS ha_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_HA_TryAcquire(t *testing.T) {
	st := openPGHAStore(t)
	ctx := context.Background()
	lease, err := st.TryAcquire(ctx, "cp-leader", "peer-1", 30*time.Second)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if lease.HolderID != "peer-1" {
		t.Errorf("holder: %q", lease.HolderID)
	}
	if lease.Generation != 1 {
		t.Errorf("gen: %d", lease.Generation)
	}
}

func TestPG_HA_LeaseHeld(t *testing.T) {
	st := openPGHAStore(t)
	ctx := context.Background()
	if _, err := st.TryAcquire(ctx, "lock1", "peer-a", 30*time.Second); err != nil {
		t.Fatalf("%v", err)
	}
	_, err := st.TryAcquire(ctx, "lock1", "peer-b", 30*time.Second)
	if !errors.Is(err, ha.ErrLeaseHeld) {
		t.Errorf("expected ErrLeaseHeld, got %v", err)
	}
}

func TestPG_HA_RenewLease(t *testing.T) {
	st := openPGHAStore(t)
	ctx := context.Background()
	if _, err := st.TryAcquire(ctx, "lock2", "peer-1", 30*time.Second); err != nil {
		t.Fatalf("%v", err)
	}
	l, err := st.RenewLease(ctx, "lock2", "peer-1", 30*time.Second)
	if err != nil {
		t.Fatalf("RenewLease: %v", err)
	}
	if l.HolderID != "peer-1" {
		t.Errorf("holder: %q", l.HolderID)
	}
}

func TestPG_HA_WriteMarker(t *testing.T) {
	st := openPGHAStore(t)
	ctx := context.Background()
	m, err := st.WriteMarker(ctx, ha.LeaderMarker{LeaseKey: "lock3", Generation: 1, HolderID: "peer-1", Marker: "test-marker"})
	if err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}
	if m.ID == 0 {
		t.Error("expected non-zero ID")
	}
	markers, err := st.ListMarkers(ctx, "lock3")
	if err != nil {
		t.Fatalf("ListMarkers: %v", err)
	}
	if len(markers) == 0 {
		t.Fatal("expected markers")
	}
}

func TestPG_HA_PeerHeartbeat(t *testing.T) {
	st := openPGHAStore(t)
	ctx := context.Background()
	hb := ha.PeerHeartbeat{PeerID: "pg-peer-1", Address: "https://peer1.example", Role: "standby", GenerationSeen: 0}
	if err := st.PutPeerHeartbeat(ctx, hb); err != nil {
		t.Fatalf("PutPeerHeartbeat: %v", err)
	}
	got, err := st.GetPeer(ctx, "pg-peer-1")
	if err != nil {
		t.Fatalf("GetPeer: %v", err)
	}
	if got.PeerID != "pg-peer-1" {
		t.Errorf("peer: %q", got.PeerID)
	}
}
