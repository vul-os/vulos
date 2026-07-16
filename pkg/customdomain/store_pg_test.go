// store_pg_test.go — Postgres integration tests for the customdomain store.
//
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/customdomain/...
package customdomain_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/customdomain"
)

func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	return dsn
}

func openPGTestStore(t *testing.T) customdomain.Store {
	t.Helper()
	dsn := pgDSN(t)
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("customdomain_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := customdomain.OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("customdomain.OpenSQLStore (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS customdomain_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_UpsertAndGet(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	d := customdomain.CustomDomain{
		Domain:      "pg-mail.acme.example",
		TenantID:    "pg-tenant-a",
		VerifyToken: "pg-tok-abc",
		VerifyState: customdomain.StatePending,
	}
	if err := st.Upsert(ctx, d); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := st.Get(ctx, "pg-mail.acme.example")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TenantID != "pg-tenant-a" {
		t.Errorf("tenant = %q, want pg-tenant-a", got.TenantID)
	}
}

func TestPG_GetNotFound(t *testing.T) {
	st := openPGTestStore(t)
	_, err := st.Get(context.Background(), "pg-ghost.example")
	if !errors.Is(err, customdomain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestPG_ListByTenant(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	for _, dom := range []string{"pg-d1.example", "pg-d2.example"} {
		if err := st.Upsert(ctx, customdomain.CustomDomain{
			Domain: dom, TenantID: "pg-tenant-b",
			VerifyToken: "tok", VerifyState: customdomain.StatePending,
		}); err != nil {
			t.Fatalf("Upsert %s: %v", dom, err)
		}
	}
	list, err := st.ListByTenant(ctx, "pg-tenant-b")
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("list len = %d, want 2", len(list))
	}
}

func TestPG_Delete(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.Upsert(ctx, customdomain.CustomDomain{
		Domain: "pg-del.example", TenantID: "pg-tenant-c",
		VerifyToken: "tok", VerifyState: customdomain.StatePending,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := st.Delete(ctx, "pg-del.example"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Get(ctx, "pg-del.example"); !errors.Is(err, customdomain.ErrNotFound) {
		t.Errorf("after delete: got %v, want ErrNotFound", err)
	}
}
