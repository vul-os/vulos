// auditlog_pg_test.go — Postgres integration tests for the auditlog store.
//
// These tests are skipped unless VULOS_TEST_POSTGRES is set to a valid
// Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/auditlog/...
//
// In CI the cp-build-test-pg job sets this env var and runs against a real
// postgres:16 service container, verifying that both the SQLite and Postgres
// paths pass the same behavioural contract.
package auditlog_test

import (
	"context"
	"math"
	"os"
	"strconv"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auditlog"
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

// openPGTestLogger opens an auditlog logger backed by Postgres, using the
// "auditlog_pgtest" schema to avoid collisions with production data.
func openPGTestLogger(t *testing.T) (*auditlog.Logger, *cpdb.DB) {
	t.Helper()
	dsn := pgDSN(t)

	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("auditlog_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}

	l, err := auditlog.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("auditlog.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		// Best-effort: drop test schema so the next run starts clean.
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS auditlog_pgtest CASCADE`)
		_ = l.Close()
	})
	return l, db
}

// ─── PG: append + verify clean hash chain ────────────────────────────────────

func TestPG_AppendAndVerify_Clean(t *testing.T) {
	ctx := context.Background()
	l, _ := openPGTestLogger(t)

	for i := 0; i < 5; i++ {
		if err := l.Record(ctx, "admin@example.com", "pop.drain", "pop-"+strconv.Itoa(i),
			map[string]string{"region": "eu-west"}); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	if err := l.Verify(ctx, 0, math.MaxInt64); err != nil {
		t.Errorf("Verify: expected clean chain, got: %v", err)
	}
}

// ─── PG: detect tampered row (hash chain integrity) ──────────────────────────

func TestPG_Verify_DetectsTamperedRow(t *testing.T) {
	ctx := context.Background()
	l, db := openPGTestLogger(t)

	for i := 0; i < 3; i++ {
		if err := l.Record(ctx, "admin@example.com", "ota.release", "v1.0."+strconv.Itoa(i), nil); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	if err := l.Verify(ctx, 0, math.MaxInt64); err != nil {
		t.Fatalf("expected clean chain before tampering: %v", err)
	}

	// Mutate the entry_hash of the second row via raw SQL.
	if _, err := db.Exec(db.Rebind(
		`UPDATE auditlog_entries SET entry_hash = 'deadbeef'
		   WHERE seq = (SELECT MIN(seq) + 1 FROM auditlog_entries)`)); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	verr := l.Verify(ctx, 0, math.MaxInt64)
	if verr == nil {
		t.Fatal("expected Verify to detect tampered entry_hash, got nil")
	}
	if _, ok := verr.(*auditlog.VerifyError); !ok {
		t.Fatalf("expected *VerifyError, got %T: %v", verr, verr)
	}
	t.Logf("tamper detected: %v", verr)
}
