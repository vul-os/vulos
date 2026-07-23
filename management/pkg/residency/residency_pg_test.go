// residency_pg_test.go — Postgres integration tests for the residency store.
//
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/residency/...
package residency_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/residency"
)

func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	return dsn
}

func openPGTestStore(t *testing.T) residency.Store {
	t.Helper()
	dsn := pgDSN(t)
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("residency_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := residency.OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("residency.OpenSQLStore (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS residency_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_GetRegion_UnknownOrg(t *testing.T) {
	st := openPGTestStore(t)
	_, err := st.GetRegion(context.Background(), "pg-org-missing")
	if !errors.Is(err, residency.ErrUnknownOrg) {
		t.Errorf("expected ErrUnknownOrg, got %v", err)
	}
}

func TestPG_SetAndGetRegion(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.SetRegion(ctx, "pg-org-1", "eu-west-1"); err != nil {
		t.Fatalf("SetRegion: %v", err)
	}
	got, err := st.GetRegion(ctx, "pg-org-1")
	if err != nil {
		t.Fatalf("GetRegion: %v", err)
	}
	if got.Region != "eu-west-1" {
		t.Errorf("region = %q, want eu-west-1", got.Region)
	}
}

func TestPG_SetRegion_Update(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.SetRegion(ctx, "pg-org-2", "us-east-1"); err != nil {
		t.Fatalf("first SetRegion: %v", err)
	}
	// IMMUTABILITY (item 11): changing the region after first set is rejected.
	if err := st.SetRegion(ctx, "pg-org-2", "af-south-1"); !errors.Is(err, residency.ErrRegionLocked) {
		t.Fatalf("change SetRegion: want ErrRegionLocked, got %v", err)
	}
	got, err := st.GetRegion(ctx, "pg-org-2")
	if err != nil {
		t.Fatalf("GetRegion: %v", err)
	}
	if got.Region != "us-east-1" {
		t.Errorf("region = %q, want us-east-1 (immutable)", got.Region)
	}
}

func TestPG_SetRegion_Invalid(t *testing.T) {
	st := openPGTestStore(t)
	err := st.SetRegion(context.Background(), "pg-org-3", "mars-north-99")
	if !errors.Is(err, residency.ErrInvalidRegion) {
		t.Errorf("expected ErrInvalidRegion, got %v", err)
	}
}
