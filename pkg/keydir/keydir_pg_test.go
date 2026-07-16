package keydir_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/keydir"
)

func openPGKeydirStore(t *testing.T) *keydir.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("keydir_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := keydir.OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("keydir.OpenSQLStore (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS keydir_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_KeydirUpsertAndGet(t *testing.T) {
	st := openPGKeydirStore(t)
	ctx := context.Background()
	e := keydir.Entry{
		AccountID:     "pg_acc1",
		VumailAddress: "pg_acc1@vulos.org",
		PublicKeyB64:  "dGVzdGtleWZvcnBvc3RncmVzdGVzdA==",
		Discoverable:  true,
		State:         "active",
	}
	upserted, err := st.Upsert(ctx, e)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if upserted.AccountID != "pg_acc1" {
		t.Errorf("AccountID mismatch: %v", upserted.AccountID)
	}
	got, err := st.GetByAddress(ctx, "pg_acc1@vulos.org")
	if err != nil {
		t.Fatalf("GetByAddress: %v", err)
	}
	if got.PublicKeyB64 != "dGVzdGtleWZvcnBvc3RncmVzdGVzdA==" {
		t.Errorf("PublicKeyB64 mismatch: %v", got.PublicKeyB64)
	}
}

func TestPG_KeydirRevoke(t *testing.T) {
	st := openPGKeydirStore(t)
	ctx := context.Background()
	e := keydir.Entry{
		AccountID:     "pg_revoke",
		VumailAddress: "pg_revoke@vulos.org",
		PublicKeyB64:  "cmV2b2tla2V5dGVzdA==",
		Discoverable:  true,
	}
	if _, err := st.Upsert(ctx, e); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := st.Revoke(ctx, "pg_revoke"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got, err := st.GetByAccount(ctx, "pg_revoke")
	if err != nil {
		t.Fatalf("GetByAccount after revoke: %v", err)
	}
	if got.State != "revoked" {
		t.Errorf("State: want revoked, got %q", got.State)
	}
}
