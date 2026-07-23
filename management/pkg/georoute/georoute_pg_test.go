package georoute_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/georoute"
)

func openPGGeoTestStore(t *testing.T) *georoute.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("georoute_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := georoute.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("georoute.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS georoute_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_GeoRoute_Assign(t *testing.T) {
	st := openPGGeoTestStore(t)
	ctx := context.Background()
	a, err := st.Assign(ctx, "tenant1", "eu-west")
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if a.HomeRegion != "eu-west" {
		t.Errorf("region: %q", a.HomeRegion)
	}
}

func TestPG_GeoRoute_HomeRegion(t *testing.T) {
	st := openPGGeoTestStore(t)
	ctx := context.Background()
	if _, err := st.Assign(ctx, "tenant2", "us-east"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	r, err := st.HomeRegion(ctx, "tenant2")
	if err != nil {
		t.Fatalf("HomeRegion: %v", err)
	}
	if r != "us-east" {
		t.Errorf("region: %q", r)
	}
}

func TestPG_GeoRoute_List(t *testing.T) {
	st := openPGGeoTestStore(t)
	ctx := context.Background()
	if _, err := st.Assign(ctx, "t3", "eu-west"); err != nil {
		t.Fatalf("%v", err)
	}
	list, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("expected non-empty list")
	}
}
