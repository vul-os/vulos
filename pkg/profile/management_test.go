package profile

import (
	"context"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ─────────────────────────────────────────────────────────────────────────────
// MemManagementStore conformance tests
// ─────────────────────────────────────────────────────────────────────────────

const mgmtTestULID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestMemManagementStore_UpsertAndList(t *testing.T) {
	t.Parallel()
	st := NewMemManagementStore()
	ctx := context.Background()

	// Empty initially.
	entries, err := st.ListProfiles(ctx, mgmtTestULID)
	if err != nil {
		t.Fatalf("ListProfiles empty: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}

	// Upsert two profiles.
	if err := st.UpsertProfile(ctx, mgmtTestULID, "alice"); err != nil {
		t.Fatalf("UpsertProfile alice: %v", err)
	}
	if err := st.UpsertProfile(ctx, mgmtTestULID, "bob"); err != nil {
		t.Fatalf("UpsertProfile bob: %v", err)
	}

	entries, err = st.ListProfiles(ctx, mgmtTestULID)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Should be sorted: alice, bob.
	if entries[0].ProfileName != "alice" || entries[1].ProfileName != "bob" {
		t.Fatalf("wrong order: %v", entries)
	}
}

func TestMemManagementStore_UpsertIdempotent(t *testing.T) {
	t.Parallel()
	st := NewMemManagementStore()
	ctx := context.Background()

	// Upsert same profile twice.
	if err := st.UpsertProfile(ctx, mgmtTestULID, "alice"); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := st.UpsertProfile(ctx, mgmtTestULID, "alice"); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	entries, _ := st.ListProfiles(ctx, mgmtTestULID)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after double upsert, got %d", len(entries))
	}
}

func TestMemManagementStore_Rename(t *testing.T) {
	t.Parallel()
	st := NewMemManagementStore()
	ctx := context.Background()

	_ = st.UpsertProfile(ctx, mgmtTestULID, "alice")
	_ = st.UpsertProfile(ctx, mgmtTestULID, "charlie")

	if err := st.RenameProfile(ctx, mgmtTestULID, "alice", "alice2"); err != nil {
		t.Fatalf("RenameProfile: %v", err)
	}

	entries, _ := st.ListProfiles(ctx, mgmtTestULID)
	found := map[string]bool{}
	for _, e := range entries {
		found[e.ProfileName] = true
	}
	if found["alice"] {
		t.Fatal("old name 'alice' should not exist after rename")
	}
	if !found["alice2"] {
		t.Fatal("new name 'alice2' should exist after rename")
	}
	if !found["charlie"] {
		t.Fatal("'charlie' should still exist")
	}
}

func TestMemManagementStore_RenameNonExistentOldName(t *testing.T) {
	// Rename when oldName doesn't exist should just insert newName.
	t.Parallel()
	st := NewMemManagementStore()
	ctx := context.Background()

	// No existing profiles.
	if err := st.RenameProfile(ctx, mgmtTestULID, "ghost", "ghost2"); err != nil {
		t.Fatalf("RenameProfile nonexistent: %v", err)
	}

	entries, _ := st.ListProfiles(ctx, mgmtTestULID)
	if len(entries) != 1 || entries[0].ProfileName != "ghost2" {
		t.Fatalf("expected ghost2, got: %v", entries)
	}
}

func TestMemManagementStore_AuditLog(t *testing.T) {
	t.Parallel()
	st := NewMemManagementStore()
	ctx := context.Background()

	// Log two audit entries for the ULID.
	if err := st.LogAudit(ctx, AuditEntry{
		AccountID:   "ACCOUNT1",
		ActorID:     "ACTOR1",
		ULID:        mgmtTestULID,
		ProfileName: "alice",
		Action:      string(UpdateTypeResetPassword),
	}); err != nil {
		t.Fatalf("LogAudit reset: %v", err)
	}
	if err := st.LogAudit(ctx, AuditEntry{
		AccountID:   "ACCOUNT1",
		ActorID:     "ACTOR1",
		ULID:        mgmtTestULID,
		ProfileName: "alice",
		Action:      string(UpdateTypeRename),
	}); err != nil {
		t.Fatalf("LogAudit rename: %v", err)
	}

	rows, err := st.AuditLog(ctx, mgmtTestULID, 0)
	if err != nil {
		t.Fatalf("AuditLog: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 audit rows, got %d", len(rows))
	}
	// Most recent first: rename is the last logged, should appear first.
	if rows[0].Action != string(UpdateTypeRename) {
		t.Fatalf("first row should be rename (most recent), got %q", rows[0].Action)
	}
}

func TestMemManagementStore_AuditLogLimit(t *testing.T) {
	t.Parallel()
	st := NewMemManagementStore()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_ = st.LogAudit(ctx, AuditEntry{
			AccountID:   "ACCOUNT1",
			ActorID:     "ACTOR1",
			ULID:        mgmtTestULID,
			ProfileName: "alice",
			Action:      string(UpdateTypeResetPassword),
		})
	}

	rows, err := st.AuditLog(ctx, mgmtTestULID, 3)
	if err != nil {
		t.Fatalf("AuditLog limited: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows with limit=3, got %d", len(rows))
	}
}

func TestMemManagementStore_AuditLogIsolation(t *testing.T) {
	// Audit rows for a different ULID should not appear.
	t.Parallel()
	st := NewMemManagementStore()
	ctx := context.Background()

	const otherULID = "01BX5ZZKBKACTAV9WEVGEMMVS0"

	_ = st.LogAudit(ctx, AuditEntry{
		AccountID:   "ACCOUNT1",
		ActorID:     "ACTOR1",
		ULID:        mgmtTestULID,
		ProfileName: "alice",
		Action:      string(UpdateTypeResetPassword),
	})
	_ = st.LogAudit(ctx, AuditEntry{
		AccountID:   "ACCOUNT2",
		ActorID:     "ACTOR2",
		ULID:        otherULID,
		ProfileName: "carol",
		Action:      string(UpdateTypeRename),
	})

	rows, _ := st.AuditLog(ctx, mgmtTestULID, 0)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row for mgmtTestULID, got %d", len(rows))
	}
	if rows[0].ULID != mgmtTestULID {
		t.Fatalf("wrong ULID in audit row: %q", rows[0].ULID)
	}
}

func TestMemManagementStore_Close(t *testing.T) {
	t.Parallel()
	st := NewMemManagementStore()
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SQLManagementStore conformance tests
// ─────────────────────────────────────────────────────────────────────────────

func openSQLMgmtStore(t *testing.T) *SQLManagementStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := OpenSQLManagementStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSQLManagementStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSQLManagementStore_UpsertAndList(t *testing.T) {
	st := openSQLMgmtStore(t)
	ctx := context.Background()

	if err := st.UpsertProfile(ctx, mgmtTestULID, "alice"); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}
	if err := st.UpsertProfile(ctx, mgmtTestULID, "bob"); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}

	entries, err := st.ListProfiles(ctx, mgmtTestULID)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

func TestSQLManagementStore_Rename(t *testing.T) {
	st := openSQLMgmtStore(t)
	ctx := context.Background()

	_ = st.UpsertProfile(ctx, mgmtTestULID, "alice")
	if err := st.RenameProfile(ctx, mgmtTestULID, "alice", "alice2"); err != nil {
		t.Fatalf("RenameProfile: %v", err)
	}

	entries, _ := st.ListProfiles(ctx, mgmtTestULID)
	if len(entries) != 1 || entries[0].ProfileName != "alice2" {
		t.Fatalf("expected alice2, got %v", entries)
	}
}

func TestSQLManagementStore_AuditLog(t *testing.T) {
	st := openSQLMgmtStore(t)
	ctx := context.Background()

	_ = st.LogAudit(ctx, AuditEntry{
		AccountID:   "ACCOUNT1",
		ActorID:     "ACTOR1",
		ULID:        mgmtTestULID,
		ProfileName: "alice",
		Action:      string(UpdateTypeResetPassword),
	})
	_ = st.LogAudit(ctx, AuditEntry{
		AccountID:   "ACCOUNT1",
		ActorID:     "ACTOR1",
		ULID:        mgmtTestULID,
		ProfileName: "alice",
		Action:      string(UpdateTypeRename),
	})

	rows, err := st.AuditLog(ctx, mgmtTestULID, 0)
	if err != nil {
		t.Fatalf("AuditLog: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 audit rows, got %d", len(rows))
	}
}
