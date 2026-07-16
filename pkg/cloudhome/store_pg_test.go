// store_pg_test.go — NEON-PGTEST: Postgres-backed coverage for the cpdb
// SQLStore cloud-home identity store. Skips unless VULOS_TEST_POSTGRES points
// at a reachable Postgres cluster (same gate as every other *_pg_test.go).
package cloudhome

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// openPGSQLStore returns a fresh Postgres-schema-backed SQLStore, or skips.
func openPGSQLStore(t *testing.T) *SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("cloudhome_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	s, err := OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSQLStore: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS cloudhome_pgtest CASCADE`)
		_ = s.Close()
	})
	return s
}

// TestPG_CloudHome_InsertGet round-trips a record and asserts the unique-
// violation + not-found paths behave identically on Postgres (IsUniqueViolation
// must recognise SQLSTATE 23505, and Rebind must number the placeholders).
func TestPG_CloudHome_InsertGet(t *testing.T) {
	ctx := context.Background()
	s := openPGSQLStore(t)

	r := record{
		AccountID:    "acct-pg-1",
		VulaID:       "vula-pg-1",
		PublicKeyB64: "cHVia2V5",
		EncPrivKey:   "ZW5jcHJpdg==",
		KEKVersion:   1,
		Server:       "cp.vulos.org",
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.Insert(ctx, r); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Duplicate account → ErrExists (unique violation surfaced portably).
	if err := s.Insert(ctx, r); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate Insert → want ErrExists, got %v", err)
	}

	got, err := s.GetByAccount(ctx, "acct-pg-1")
	if err != nil {
		t.Fatalf("GetByAccount: %v", err)
	}
	if got.VulaID != "vula-pg-1" || got.Server != "cp.vulos.org" || got.KEKVersion != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	byVula, err := s.GetByVulaID(ctx, "vula-pg-1")
	if err != nil || byVula.AccountID != "acct-pg-1" {
		t.Fatalf("GetByVulaID: acct=%q err=%v", byVula.AccountID, err)
	}

	// Unknown → ErrNotFound (no oracle).
	if _, err := s.GetByAccount(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown account → want ErrNotFound, got %v", err)
	}
}

// TestPG_CloudHome_UpdateEncKeyAndRevocation exercises the update + revocation
// paths (parameterised UPDATE + upsert-style revocation put/get).
func TestPG_CloudHome_UpdateEncKeyAndRevocation(t *testing.T) {
	ctx := context.Background()
	s := openPGSQLStore(t)

	if err := s.Insert(ctx, record{
		AccountID: "acct-pg-2", VulaID: "vula-pg-2", PublicKeyB64: "cGs=",
		EncPrivKey: "ZW5j", KEKVersion: 1, Server: "cp.vulos.org", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := s.UpdateEncKey(ctx, "acct-pg-2", "cm90YXRlZA==", 2); err != nil {
		t.Fatalf("UpdateEncKey: %v", err)
	}
	got, err := s.GetByAccount(ctx, "acct-pg-2")
	if err != nil || got.EncPrivKey != "cm90YXRlZA==" || got.KEKVersion != 2 {
		t.Fatalf("after UpdateEncKey: %+v err=%v", got, err)
	}

	// Revocation put/get.
	if err := s.PutRevocation(ctx, "vula-pg-2", "acct-pg-2", `{"cert":"x"}`, time.Now().UTC()); err != nil {
		t.Fatalf("PutRevocation: %v", err)
	}
	certJSON, ok, err := s.GetRevocation(ctx, "vula-pg-2")
	if err != nil || !ok || certJSON == "" {
		t.Fatalf("GetRevocation: json=%q ok=%v err=%v", certJSON, ok, err)
	}
	if _, ok, _ := s.GetRevocation(ctx, "vula-unknown"); ok {
		t.Fatalf("GetRevocation unknown → ok=true, want false")
	}
}
