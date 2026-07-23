package orgadmin_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/orgadmin"
)

func openPGTestStore(t *testing.T) *orgadmin.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	db, err := cpdb.Open("orgadmin_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := orgadmin.OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("orgadmin.OpenSQLStore: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS orgadmin_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_BackupMode(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	// Test GetBackupMode returns central default for unknown tenant
	resp, err := st.GetBackupMode(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("GetBackupMode: %v", err)
	}
	if resp.Mode != orgadmin.BackupModeCentral {
		t.Errorf("want central, got %q", resp.Mode)
	}
	// Test SetBackupMode
	if err := st.SetBackupMode(ctx, "tenant-1", orgadmin.BackupModeRequest{Mode: "local"}); err != nil {
		t.Fatalf("SetBackupMode: %v", err)
	}
	resp, err = st.GetBackupMode(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("GetBackupMode after set: %v", err)
	}
	if resp.Mode != "local" {
		t.Errorf("want local, got %q", resp.Mode)
	}
}

func TestPG_CreateInvite(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	inv, err := st.CreateInvite(ctx, "tenant-1", "alice@example.com", "member", "Alice")
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if inv.Email != "alice@example.com" {
		t.Errorf("email mismatch")
	}
}

// TestPG_ProductStatus exercises the per-org product-status store on Postgres
// (dual-dialect proof for PRODUCTS-ADMIN-01): upsert + read + tenant scoping.
func TestPG_ProductStatus(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	// Unknown tenant → empty map.
	m, err := st.ProductStatuses(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("ProductStatuses (empty): %v", err)
	}
	if len(m) != 0 {
		t.Errorf("want empty, got %v", m)
	}
	// Upsert then update (same PK).
	if err := st.SetProductStatus(ctx, "tenant-1", "mail", "off"); err != nil {
		t.Fatalf("SetProductStatus: %v", err)
	}
	if err := st.SetProductStatus(ctx, "tenant-1", "mail", "configured"); err != nil {
		t.Fatalf("SetProductStatus (update): %v", err)
	}
	m, err = st.ProductStatuses(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("ProductStatuses: %v", err)
	}
	if m["mail"] != "configured" {
		t.Errorf("want configured, got %q", m["mail"])
	}
	// Tenant scoping.
	other, err := st.ProductStatuses(ctx, "tenant-2")
	if err != nil {
		t.Fatalf("ProductStatuses (other): %v", err)
	}
	if len(other) != 0 {
		t.Errorf("cross-tenant leak: %v", other)
	}
}
