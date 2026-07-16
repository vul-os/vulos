package edge_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/edge"
)

func openPGEdgeStore(t *testing.T) *edge.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("edge_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := edge.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("edge.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS edge_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_Edge_UpsertNode(t *testing.T) {
	st := openPGEdgeStore(t)
	ctx := context.Background()
	n, err := st.UpsertNode(ctx, edge.Node{ID: "iad1", Region: "us-east", OriginURL: "https://origin.example.com"})
	if err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if n.ID != "iad1" {
		t.Errorf("id: %q", n.ID)
	}
}

func TestPG_Edge_DomainConfig(t *testing.T) {
	st := openPGEdgeStore(t)
	ctx := context.Background()
	cfg, err := st.SetDomainConfig(ctx, edge.DomainConfig{
		ULID: "01TESTULID", Domain: "test.example.com",
		CacheTTLSec: 300, RateLimitRPS: 100, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SetDomainConfig: %v", err)
	}
	if cfg.Domain != "test.example.com" {
		t.Errorf("domain: %q", cfg.Domain)
	}
}

func TestPG_Edge_BandwidthEvent(t *testing.T) {
	st := openPGEdgeStore(t)
	ctx := context.Background()
	ev, err := st.EmitBandwidthEvent(ctx, edge.BandwidthEvent{
		AccountID: "acc1", ULID: "01ULID", Domain: "d.example.com",
		BytesIn: 100, BytesOut: 200,
		PeriodStart: time.Now().Add(-time.Hour), PeriodEnd: time.Now(),
	})
	if err != nil {
		t.Fatalf("EmitBandwidthEvent: %v", err)
	}
	if ev.ID == 0 {
		t.Error("expected non-zero ID")
	}
}
