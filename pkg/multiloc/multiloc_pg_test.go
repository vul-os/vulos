// multiloc_pg_test.go — Postgres integration tests for the multiloc store.
//
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/multiloc/...
package multiloc_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/multiloc"
)

func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	return dsn
}

func openPGTestStore(t *testing.T) multiloc.Store {
	t.Helper()
	dsn := pgDSN(t)
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("multiloc_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := multiloc.OpenStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("multiloc.OpenStore (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS multiloc_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_Enroll_NewLocation(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	req := multiloc.EnrollRequest{
		OrgID:      "pg-org-1",
		LocationID: "pg-loc-a",
		Name:       "EU-West-1",
		Endpoint:   "https://eu1.example.com",
	}
	loc, err := st.Enroll(ctx, req)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if loc.OrgID != req.OrgID {
		t.Errorf("OrgID = %q, want %q", loc.OrgID, req.OrgID)
	}
	if loc.Healthy {
		t.Error("newly enrolled location should not be healthy")
	}
}

func TestPG_Enroll_AlreadyEnrolled(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	req := multiloc.EnrollRequest{
		OrgID: "pg-org-dup", LocationID: "pg-loc-dup",
		Name: "Dup", Endpoint: "https://dup.example.com",
	}
	if _, err := st.Enroll(ctx, req); err != nil {
		t.Fatalf("first Enroll: %v", err)
	}
	_, err := st.Enroll(ctx, req)
	if err != multiloc.ErrAlreadyEnrolled {
		t.Errorf("got %v, want ErrAlreadyEnrolled", err)
	}
}

func TestPG_MarkHealth_AndPick(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"pg-h1", "pg-h2"} {
		if _, err := st.Enroll(ctx, multiloc.EnrollRequest{
			OrgID: "pg-org-health", LocationID: id,
			Name: id, Endpoint: "https://" + id + ".example.com",
		}); err != nil {
			t.Fatalf("Enroll %s: %v", id, err)
		}
	}
	if err := st.MarkHealth(ctx, "pg-org-health", "pg-h1", true); err != nil {
		t.Fatalf("MarkHealth: %v", err)
	}
	loc, err := st.PickLocation(ctx, "pg-org-health")
	if err != nil {
		t.Fatalf("PickLocation: %v", err)
	}
	if loc.LocationID != "pg-h1" {
		t.Errorf("got %q, want pg-h1", loc.LocationID)
	}
}

func TestPG_MarkHealth_NotFound(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	err := st.MarkHealth(ctx, "pg-org-x", "no-such-loc", true)
	if err != multiloc.ErrLocationNotFound {
		t.Errorf("got %v, want ErrLocationNotFound", err)
	}
}
