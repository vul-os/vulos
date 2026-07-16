package profile_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/profile"
)

func openPGManagementStore(t *testing.T) *profile.SQLManagementStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("profile_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := profile.OpenSQLManagementStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("profile.OpenSQLManagementStore (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS profile_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_ProfileUpsertAndList(t *testing.T) {
	st := openPGManagementStore(t)
	ctx := context.Background()
	if err := st.UpsertProfile(ctx, "ulid1", "admin"); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}
	profiles, err := st.ListProfiles(ctx, "ulid1")
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(profiles) != 1 || profiles[0].ProfileName != "admin" {
		t.Errorf("profiles mismatch: %v", profiles)
	}
}

func TestPG_ProfileRename(t *testing.T) {
	st := openPGManagementStore(t)
	ctx := context.Background()
	if err := st.UpsertProfile(ctx, "ulid2", "user"); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}
	if err := st.RenameProfile(ctx, "ulid2", "user", "admin"); err != nil {
		t.Fatalf("RenameProfile: %v", err)
	}
	profiles, err := st.ListProfiles(ctx, "ulid2")
	if err != nil {
		t.Fatalf("ListProfiles after rename: %v", err)
	}
	if len(profiles) != 1 || profiles[0].ProfileName != "admin" {
		t.Errorf("expected [admin], got %v", profiles)
	}
}

func TestPG_ProfileAuditLog(t *testing.T) {
	st := openPGManagementStore(t)
	ctx := context.Background()
	e := profile.AuditEntry{
		AccountID:   "acc1",
		ActorID:     "actor1",
		ULID:        "ulid3",
		ProfileName: "test",
		Action:      string(profile.UpdateTypeResetPassword),
	}
	if err := st.LogAudit(ctx, e); err != nil {
		t.Fatalf("LogAudit: %v", err)
	}
	entries, err := st.AuditLog(ctx, "ulid3", 10)
	if err != nil {
		t.Fatalf("AuditLog: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}
