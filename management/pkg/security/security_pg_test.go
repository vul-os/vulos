// security_pg_test.go — Postgres integration tests for the security store.
//
// These tests are skipped unless VULOS_TEST_POSTGRES is set to a valid
// Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/security/...
//
// In CI the cp-build-test-pg job sets this env var and runs against a real
// postgres:16 service container, verifying that both the SQLite and Postgres
// paths pass the same behavioural contract.
package security

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

// openPGTestStore opens a security store backed by Postgres, using the
// "security_pgtest" schema to avoid collisions with production data.
func openPGTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := pgDSN(t)

	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("security_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}

	st, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("security.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		// Best-effort: drop test schema so the next run starts clean.
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS security_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

// ─── PG: IP block + IsBlocked ────────────────────────────────────────────────

func TestPG_IsBlockedAfterBlock(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	const ip = "203.0.113.7"
	if st.IsBlocked(ctx, ip) {
		t.Fatal("ip should not be blocked before BlockIP")
	}
	if err := st.BlockIP(ctx, ip, "pg test block"); err != nil {
		t.Fatalf("BlockIP: %v", err)
	}
	if !st.IsBlocked(ctx, ip) {
		t.Error("ip should be blocked after BlockIP")
	}

	// BlockIP is idempotent (ON CONFLICT DO UPDATE) — a second call must not error.
	if err := st.BlockIP(ctx, ip, "pg test block 2"); err != nil {
		t.Fatalf("BlockIP (second): %v", err)
	}
	if !st.IsBlocked(ctx, ip) {
		t.Error("ip should remain blocked after re-block")
	}
}

// ─── PG: honeypot seeding idempotency (ON CONFLICT DO NOTHING) ────────────────

func TestPG_SeedHoneypotAccountsIdempotent(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.SeedHoneypotAccounts(ctx, 5); err != nil {
		t.Fatalf("SeedHoneypotAccounts: %v", err)
	}
	// Re-seeding the same count must be a no-op and must not error
	// (relies on ON CONFLICT(email) DO NOTHING for any overlap).
	if err := st.SeedHoneypotAccounts(ctx, 5); err != nil {
		t.Fatalf("SeedHoneypotAccounts (repeat): %v", err)
	}

	// The seeded honeypot emails must be recognised.
	if !st.IsHoneypotAccount(ctx, honeypotEmail(0)) {
		t.Errorf("expected %s to be a honeypot account", honeypotEmail(0))
	}
}
