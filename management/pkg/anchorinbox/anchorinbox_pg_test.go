package anchorinbox_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/anchorinbox"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openPGAnchorStore(t *testing.T) *anchorinbox.Store {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("anchorinbox_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := anchorinbox.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("anchorinbox.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS anchorinbox_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_AnchorInbox_Provision(t *testing.T) {
	st := openPGAnchorStore(t)
	ctx := context.Background()

	inbox, err := st.Provision(ctx, "acc-pg1", "pghandle1")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if inbox.Address != "pghandle1@vulos.org" {
		t.Errorf("address: %q", inbox.Address)
	}
	if inbox.AccountID != "acc-pg1" {
		t.Errorf("account_id: %q", inbox.AccountID)
	}
	if inbox.QuotaMB != anchorinbox.DefaultQuotaMB {
		t.Errorf("quota_mb: %d", inbox.QuotaMB)
	}
}

func TestPG_AnchorInbox_IdempotentProvision(t *testing.T) {
	st := openPGAnchorStore(t)
	ctx := context.Background()

	if _, err := st.Provision(ctx, "acc-pg2", "pghandle2"); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err := st.Provision(ctx, "acc-pg2", "pghandle2")
	if !errors.Is(err, anchorinbox.ErrAlreadyProvisioned) {
		t.Errorf("expected ErrAlreadyProvisioned, got %v", err)
	}
}

func TestPG_AnchorInbox_Get(t *testing.T) {
	st := openPGAnchorStore(t)
	ctx := context.Background()

	if _, err := st.Provision(ctx, "acc-pg3", "pghandle3"); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	inbox, err := st.Get(ctx, "acc-pg3")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if inbox.AccountID != "acc-pg3" {
		t.Errorf("account: %q", inbox.AccountID)
	}
	if inbox.Address != "pghandle3@vulos.org" {
		t.Errorf("address: %q", inbox.Address)
	}
}

func TestPG_AnchorInbox_GetByAddress(t *testing.T) {
	st := openPGAnchorStore(t)
	ctx := context.Background()

	if _, err := st.Provision(ctx, "acc-pg4", "pghandle4"); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	inbox, err := st.GetByAddress(ctx, "pghandle4@vulos.org")
	if err != nil {
		t.Fatalf("GetByAddress: %v", err)
	}
	if inbox.AccountID != "acc-pg4" {
		t.Errorf("account: %q", inbox.AccountID)
	}
}

func TestPG_AnchorInbox_GetNotFound(t *testing.T) {
	st := openPGAnchorStore(t)
	_, err := st.Get(context.Background(), "no-such-account")
	if !errors.Is(err, anchorinbox.ErrNotFound) {
		t.Errorf("got %v; want ErrNotFound", err)
	}
}
