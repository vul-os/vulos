// secrets_pg_test.go — Postgres integration tests for the secrets Manager.
//
// These tests are skipped unless VULOS_TEST_POSTGRES is set to a valid
// Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/secrets/...
//
// In CI the cp-build-test-pg job sets this env var and runs against a real
// postgres:16 service container, verifying that both the SQLite and Postgres
// paths pass the same behavioural contract (including BYTEA round-trips of the
// AES-256-GCM encrypted secret blobs).
package secrets_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/secrets"
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

// openPGManager opens a secrets Manager backed by Postgres, using the
// "secrets_pgtest" schema to avoid collisions with production data.
func openPGManager(t *testing.T) *secrets.Manager {
	t.Helper()
	dsn := pgDSN(t)

	// A real master key so the encrypted-at-rest path (BYTEA columns) is
	// exercised against Postgres.
	t.Setenv("VULOS_SECRET_KMS_KEY",
		"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("secrets_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	m, err := secrets.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("secrets.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		// Best-effort: drop test schema so the next run starts clean.
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS secrets_pgtest CASCADE`)
		_ = m.Close()
	})
	return m
}

// ─── PG: rotate / current / verify roundtrip ─────────────────────────────────

func TestPG_RotateCurrentVerify(t *testing.T) {
	m := openPGManager(t)
	ctx := context.Background()

	if err := m.Rotate(ctx, "SESSION_SECRET"); err != nil {
		t.Fatalf("first Rotate: %v", err)
	}
	first := m.Current("SESSION_SECRET")
	if len(first) == 0 {
		t.Fatal("current should be non-empty after rotation")
	}
	if !m.Verify("SESSION_SECRET", first) {
		t.Error("Verify: current value should be accepted")
	}

	// Second rotation: current changes, previous retained.
	if err := m.Rotate(ctx, "SESSION_SECRET"); err != nil {
		t.Fatalf("second Rotate: %v", err)
	}
	second := m.Current("SESSION_SECRET")
	if string(first) == string(second) {
		t.Error("current did not change after second rotation")
	}
	// Previous (first) still valid within the default grace window.
	if !m.Verify("SESSION_SECRET", first) {
		t.Error("Verify: previous value should be accepted within grace window")
	}
	if !m.Verify("SESSION_SECRET", second) {
		t.Error("Verify: current value should be accepted")
	}
}

// ─── PG: encrypted-at-rest BYTEA round-trip across handles ────────────────────

func TestPG_EncryptedAtRestRoundTrip(t *testing.T) {
	m1 := openPGManager(t)
	ctx := context.Background()

	if err := m1.Rotate(ctx, "JWT_SIGNING_KEY"); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	value1 := m1.Current("JWT_SIGNING_KEY")

	// Open a second handle against the same schema; values must reload
	// identically (decrypts the BYTEA-stored ciphertext).
	t.Setenv("DATABASE_URL", os.Getenv("VULOS_TEST_POSTGRES"))
	t.Setenv("VULOS_DATABASE_URL", "")
	db2, err := cpdb.Open("secrets_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open m2: %v", err)
	}
	defer db2.Close()
	m2, err := secrets.Open(db2)
	if err != nil {
		t.Fatalf("secrets.Open m2: %v", err)
	}

	value2 := m2.Current("JWT_SIGNING_KEY")
	if string(value1) != string(value2) {
		t.Errorf("round-trip mismatch: persisted %q, reloaded %q", value1, value2)
	}
}

// ─── PG: unknown name returns ErrUnknownSecret ───────────────────────────────

func TestPG_UnknownName(t *testing.T) {
	m := openPGManager(t)
	ctx := context.Background()

	if err := m.Rotate(ctx, "NONEXISTENT_SECRET"); err == nil {
		t.Error("expected error for unknown secret name")
	}
}
