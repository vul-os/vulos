// compliance_pg_test.go — Postgres integration tests for the compliance store.
//
// These tests are skipped unless VULOS_TEST_POSTGRES is set to a valid
// Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/compliance/...
//
// In CI the cp-build-test-pg job sets this env var and runs against a real
// postgres:16 service container, verifying that both the SQLite and Postgres
// paths pass the same behavioural contract.
package compliance

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
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

// openPGTestStore opens a compliance store backed by Postgres, using the
// "compliance_pgtest" schema to avoid collisions with production data.
func openPGTestStore(t *testing.T) *SQLStore {
	t.Helper()
	dsn := pgDSN(t)

	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("compliance_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}

	st, err := Open(db)
	if err != nil {
		db.Close() //nolint:errcheck
		t.Fatalf("compliance.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		// Best-effort: drop test schema so the next run starts clean.
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS compliance_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

// ─── PG: Record + ListByAccount roundtrip ────────────────────────────────────

func TestPG_RecordAndList(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	r1, err := st.Record(ctx, "pg_acct1", KindExport, "")
	if err != nil {
		t.Fatalf("Record export: %v", err)
	}
	if r1.ID == "" || r1.Status != StatusReceived || r1.Kind != KindExport {
		t.Fatalf("unexpected request: %+v", r1)
	}
	if _, err := st.Record(ctx, "pg_acct1", KindErasure, "please delete"); err != nil {
		t.Fatalf("Record erasure: %v", err)
	}
	// Different account — must not leak across accounts.
	if _, err := st.Record(ctx, "pg_acct2", KindExport, ""); err != nil {
		t.Fatalf("Record other account: %v", err)
	}

	list, err := st.ListByAccount(ctx, "pg_acct1")
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 requests for pg_acct1, got %d", len(list))
	}

	other, err := st.ListByAccount(ctx, "pg_acct2")
	if err != nil {
		t.Fatalf("ListByAccount pg_acct2: %v", err)
	}
	if len(other) != 1 {
		t.Fatalf("want 1 request for pg_acct2, got %d", len(other))
	}
}

func TestPG_RecordRejectsUnknownKind(t *testing.T) {
	st := openPGTestStore(t)
	if _, err := st.Record(context.Background(), "pg_acct1", "obliterate", ""); err != ErrInvalidKind {
		t.Fatalf("want ErrInvalidKind, got %v", err)
	}
}
