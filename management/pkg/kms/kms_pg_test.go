// kms_pg_test.go — Postgres integration tests for the kms SQLStore.
//
// These tests are skipped unless VULOS_TEST_POSTGRES is set to a valid
// Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/kms/...
//
// In CI the cp-build-test-pg job sets this env var and runs against a real
// postgres:16 service container, verifying that both the SQLite and Postgres
// paths pass the same behavioural contract.
package kms

import (
	"bytes"
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

// openPGTestStore opens a kms SQLStore backed by Postgres, using the
// "kms_pgtest" schema to avoid collisions with production data.
func openPGTestStore(t *testing.T) *SQLStore {
	t.Helper()
	dsn := pgDSN(t)

	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("kms_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}

	s, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("kms.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		// Best-effort: drop test schema so the next run starts clean.
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS kms_pgtest CASCADE`)
		_ = s.Close()
	})
	return s
}

func TestPG_ConfigRoundTrip(t *testing.T) {
	s := openPGTestStore(t)
	ctx := context.Background()

	cfg := Config{
		AccountID:            "pg-acct",
		Tier:                 "pro",
		Kind:                 KindSymmetric,
		EncryptedKeyMaterial: "deadbeef",
		KEKVersion:           1,
	}
	if err := s.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	got, err := s.GetConfig(ctx, "pg-acct")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.AccountID != "pg-acct" || got.Tier != "pro" {
		t.Fatalf("unexpected config: %+v", got)
	}
	if got.EncryptedKeyMaterial != "deadbeef" {
		t.Fatalf("ekm: got %q, want deadbeef", got.EncryptedKeyMaterial)
	}
}

func TestPG_DEKRoundTrip(t *testing.T) {
	s := openPGTestStore(t)
	ctx := context.Background()

	d := DEKRecord{
		ID:         "pg-dek-1",
		AccountID:  "pg-acct",
		ObjectRef:  "bucket/key",
		WrappedDEK: []byte("wrappedpgblob"),
		KEKVersion: 1,
	}
	if err := s.PutDEK(ctx, d); err != nil {
		t.Fatalf("PutDEK: %v", err)
	}
	got, err := s.GetDEK(ctx, "pg-dek-1")
	if err != nil {
		t.Fatalf("GetDEK: %v", err)
	}
	if got.AccountID != "pg-acct" || !bytes.Equal(got.WrappedDEK, d.WrappedDEK) {
		t.Fatalf("unexpected DEK: %+v", got)
	}
}
