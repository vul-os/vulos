// audit_pg_test.go — Postgres integration tests for the audit log.
//
// These tests are skipped unless VULOS_TEST_POSTGRES is set to a valid
// Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/audit/...
//
// In CI the cp-build-test-pg job sets this env var and runs against a real
// postgres:16 service container, verifying that both the SQLite and Postgres
// paths pass the same behavioural contract (hash chain + Query roundtrip).
package audit_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/audit"
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

// openPGTestLog opens an audit log backed by Postgres, using the
// "audit_pgtest" schema to avoid collisions with production data.
func openPGTestLog(t *testing.T, sink audit.BucketSink) *audit.Log {
	t.Helper()
	dsn := pgDSN(t)

	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("audit_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}

	l, err := audit.Open(db, sink, "test-audit-bucket")
	if err != nil {
		db.Close()
		t.Fatalf("audit.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		// Best-effort: drop test schema so the next run starts clean.
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS audit_pgtest CASCADE`)
		_ = l.Close()
	})
	return l
}

// ─── PG: Record + hash chain ─────────────────────────────────────────────────

func TestPG_Record_HashChain(t *testing.T) {
	l := openPGTestLog(t, nil)
	ctx := context.Background()

	e1, err := l.Record(ctx, audit.KindLogin, "pg-acc1", "user@example.com", nil)
	if err != nil {
		t.Fatalf("Record e1: %v", err)
	}
	e2, err := l.Record(ctx, audit.KindBucketWrite, "pg-acc1", "user@example.com",
		map[string]string{"bucket": "my-bucket"})
	if err != nil {
		t.Fatalf("Record e2: %v", err)
	}
	e3, err := l.Record(ctx, audit.KindLogout, "pg-acc1", "user@example.com", nil)
	if err != nil {
		t.Fatalf("Record e3: %v", err)
	}

	// Seq must be assigned (RETURNING seq via BIGSERIAL).
	if e1.Seq <= 0 || e2.Seq <= e1.Seq || e3.Seq <= e2.Seq {
		t.Errorf("seq not monotonically increasing: %d, %d, %d", e1.Seq, e2.Seq, e3.Seq)
	}

	// First entry's PrevHash is the zero hash.
	if e1.PrevHash != strings.Repeat("0", 64) {
		t.Errorf("e1 PrevHash not zero-hash: %q", e1.PrevHash)
	}
	// Chain links: e2.PrevHash == e1.Hash, e3.PrevHash == e2.Hash.
	if e2.PrevHash != e1.Hash {
		t.Errorf("e2.PrevHash %q != e1.Hash %q", e2.PrevHash, e1.Hash)
	}
	if e3.PrevHash != e2.Hash {
		t.Errorf("e3.PrevHash %q != e2.Hash %q", e3.PrevHash, e2.Hash)
	}
}

// ─── PG: Query roundtrip ─────────────────────────────────────────────────────

func TestPG_Query_Roundtrip(t *testing.T) {
	l := openPGTestLog(t, nil)
	ctx := context.Background()

	if _, err := l.Record(ctx, audit.KindLogin, "pg-q1", "a@example.com", nil); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := l.Record(ctx, audit.KindBucketWrite, "pg-q1", "a@example.com",
		map[string]string{"k": "v"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := l.Record(ctx, audit.KindLogin, "pg-q2", "b@example.com", nil); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Filter by account.
	byAcct, err := l.Query(ctx, audit.QueryOptions{AccountID: "pg-q1"})
	if err != nil {
		t.Fatalf("Query by account: %v", err)
	}
	if len(byAcct) != 2 {
		t.Errorf("want 2 entries for pg-q1, got %d", len(byAcct))
	}
	for i := 1; i < len(byAcct); i++ {
		if byAcct[i].Seq <= byAcct[i-1].Seq {
			t.Errorf("entries not in ascending seq order at index %d", i)
		}
	}

	// Filter by kind.
	byKind, err := l.Query(ctx, audit.QueryOptions{Kind: audit.KindLogin})
	if err != nil {
		t.Fatalf("Query by kind: %v", err)
	}
	if len(byKind) != 2 {
		t.Errorf("want 2 login entries, got %d", len(byKind))
	}

	// Roundtrip fidelity: detail map survives the DB round-trip.
	for _, e := range byAcct {
		if e.Kind == audit.KindBucketWrite && e.Detail["k"] != "v" {
			t.Errorf("detail not preserved: %v", e.Detail)
		}
	}
}
