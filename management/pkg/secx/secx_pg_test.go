// secx_pg_test.go — Postgres integration tests for the secx SQLStore.
//
// These tests are skipped unless VULOS_TEST_POSTGRES is set to a valid
// Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/secx/...
//
// In CI the cp-build-test-pg job sets this env var and runs against a real
// postgres:16 service container, verifying that both the SQLite and Postgres
// paths pass the same behavioural contract.
package secx_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/secx"
)

// pgDSN returns the Postgres DSN or skips the test.
func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	return dsn
}

// openPGTestStore opens a secx SQLStore backed by Postgres, using the
// "secx_pgtest" schema to avoid collisions with production data.
func openPGTestStore(t *testing.T) *secx.SQLStore {
	t.Helper()
	dsn := pgDSN(t)

	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("secx_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}

	st, err := secx.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("secx.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		// Best-effort: drop test schema so the next run starts clean.
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS secx_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

// ─── PG: full behavioural contract ───────────────────────────────────────────

func TestPG_SetGetRoundtrip(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	av := secx.AppVisibility{
		AppID:      "pg-app",
		ULID:       "01J0000000000000000000PG01",
		AccountID:  "pg-acc-1",
		Visibility: secx.VisPrivate,
	}
	audit := secx.AuditEntry{
		AppID: av.AppID, ULID: av.ULID, AccountID: "pg-acc-1",
		OldVis: "", NewVis: string(secx.VisPrivate), IP: "127.0.0.1",
	}
	if err := st.Set(ctx, av, audit); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := st.Get(ctx, av.AppID, av.ULID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Visibility != secx.VisPrivate {
		t.Errorf("visibility: want private, got %s", got.Visibility)
	}
	if got.AccountID != "pg-acc-1" {
		t.Errorf("account: want pg-acc-1, got %s", got.AccountID)
	}
}

func TestPG_GetUnknownApp(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	_, err := st.Get(ctx, "pg-nope", "01J0000000000000000000PG02")
	if err != secx.ErrUnknownApp {
		t.Errorf("want ErrUnknownApp, got %v", err)
	}
}

func TestPG_SetUpdatesExisting(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	av := secx.AppVisibility{
		AppID: "pg-upd", ULID: "01J0000000000000000000PG03",
		AccountID: "pg-acc-2", Visibility: secx.VisPrivate,
	}
	if err := st.Set(ctx, av, secx.AuditEntry{
		AppID: av.AppID, ULID: av.ULID, AccountID: "pg-acc-2", OldVis: "", NewVis: "private",
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	av.Visibility = secx.VisLocal
	if err := st.Set(ctx, av, secx.AuditEntry{
		AppID: av.AppID, ULID: av.ULID, AccountID: "pg-acc-2", OldVis: "private", NewVis: "local",
	}); err != nil {
		t.Fatalf("Set update: %v", err)
	}

	got, err := st.Get(ctx, av.AppID, av.ULID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Visibility != secx.VisLocal {
		t.Errorf("want local, got %s", got.Visibility)
	}
}

func TestPG_ListByULID(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	const ulid = "01J0000000000000000000PG04"

	apps := []string{"alpha", "beta", "gamma"}
	for _, app := range apps {
		if err := st.Set(ctx, secx.AppVisibility{
			AppID: app, ULID: ulid, AccountID: "pg-acc-3", Visibility: secx.VisPrivate,
		}, secx.AuditEntry{
			AppID: app, ULID: ulid, AccountID: "pg-acc-3", OldVis: "", NewVis: "private",
		}); err != nil {
			t.Fatalf("Set %s: %v", app, err)
		}
	}

	list, err := st.ListByULID(ctx, ulid)
	if err != nil {
		t.Fatalf("ListByULID: %v", err)
	}
	if len(list) != len(apps) {
		t.Errorf("want %d records, got %d", len(apps), len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i].AppID < list[i-1].AppID {
			t.Errorf("not sorted by app_id at index %d", i)
		}
	}
}

func TestPG_AuditLog(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	const appID = "pg-audit"
	const ulid = "01J0000000000000000000PG05"

	for i, vis := range []secx.Visibility{secx.VisPrivate, secx.VisLocal, secx.VisPublic} {
		oldVis := ""
		if i > 0 {
			oldVis = string([]secx.Visibility{secx.VisPrivate, secx.VisLocal}[i-1])
		}
		if err := st.Set(ctx, secx.AppVisibility{
			AppID: appID, ULID: ulid, AccountID: "pg-acc-4", Visibility: vis,
		}, secx.AuditEntry{
			AppID: appID, ULID: ulid, AccountID: "pg-acc-4",
			OldVis: oldVis, NewVis: string(vis), IP: "10.0.0.1",
		}); err != nil {
			t.Fatalf("Set[%d]: %v", i, err)
		}
	}

	log, err := st.AuditLog(ctx, appID, ulid, 0)
	if err != nil {
		t.Fatalf("AuditLog: %v", err)
	}
	if len(log) != 3 {
		t.Errorf("want 3 audit rows, got %d", len(log))
	}
	if len(log) > 0 && log[0].NewVis != "public" {
		t.Errorf("want newest NewVis=public, got %s", log[0].NewVis)
	}
}
