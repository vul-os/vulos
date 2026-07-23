package routing_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/routing"
)

func openPGTestStore(t *testing.T) *routing.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("routing_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := routing.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("routing.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS routing_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_Routing_Enroll(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	b, err := st.Enroll(ctx, "01TESTULID0000000000000000", "acc1", routing.ModeFabric)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if b.AccountID != "acc1" {
		t.Errorf("account: %q", b.AccountID)
	}
}

func TestPG_Routing_GetBinding(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	_, err := st.Enroll(ctx, "01TESTULID0000000000000001", "acc2", routing.ModeFabric)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	b, err := st.GetBinding(ctx, "01TESTULID0000000000000001")
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if b.ULID != "01TESTULID0000000000000001" {
		t.Errorf("ulid: %q", b.ULID)
	}
}

func TestPG_Routing_UpsertPop(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	p := routing.PoP{ID: "iad1", Region: "us-east", IP: "1.2.3.4", Lat: 38.9, Lon: -77.0, Healthy: true}
	if err := st.UpsertPoP(ctx, p); err != nil {
		t.Fatalf("UpsertPoP: %v", err)
	}
	pops, err := st.HealthyPoPs(ctx)
	if err != nil {
		t.Fatalf("HealthyPoPs: %v", err)
	}
	if len(pops) == 0 {
		t.Fatal("expected at least one pop")
	}
}

func TestPG_Routing_Vanity(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	if err := st.ClaimVanity(ctx, "testvanity", "01ULID00000000000000000000", "acc1"); err != nil {
		t.Fatalf("ClaimVanity: %v", err)
	}
	v, err := st.ResolveVanity(ctx, "testvanity")
	if err != nil {
		t.Fatalf("ResolveVanity: %v", err)
	}
	if v.ULID != "01ULID00000000000000000000" {
		t.Errorf("ulid: %q", v.ULID)
	}
}
