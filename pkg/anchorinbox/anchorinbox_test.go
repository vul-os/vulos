package anchorinbox_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/vul-os/vulos-management/pkg/anchorinbox"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openTestStore(t *testing.T) *anchorinbox.Store {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "anchor.db") + "?_pragma=journal_mode(WAL)"
	db, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := anchorinbox.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("anchorinbox.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestProvisionAtCreation verifies that a new account gets an anchor inbox with
// the expected address and default quota.
func TestProvisionAtCreation(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	inbox, err := st.Provision(ctx, "acc-001", "alice")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if inbox.AccountID != "acc-001" {
		t.Errorf("AccountID = %q, want %q", inbox.AccountID, "acc-001")
	}
	if inbox.Address != "alice@vulos.org" {
		t.Errorf("Address = %q, want %q", inbox.Address, "alice@vulos.org")
	}
	if inbox.QuotaMB != anchorinbox.DefaultQuotaMB {
		t.Errorf("QuotaMB = %d, want %d", inbox.QuotaMB, anchorinbox.DefaultQuotaMB)
	}
	if inbox.ProvisionedAt == "" {
		t.Error("ProvisionedAt must not be empty")
	}
}

// TestIdempotentReprovision verifies that calling Provision a second time for the
// same account returns ErrAlreadyProvisioned and does not corrupt the existing row.
func TestIdempotentReprovision(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	if _, err := st.Provision(ctx, "acc-002", "bob"); err != nil {
		t.Fatalf("first Provision: %v", err)
	}

	// Second call must return ErrAlreadyProvisioned.
	_, err := st.Provision(ctx, "acc-002", "bob")
	if !errors.Is(err, anchorinbox.ErrAlreadyProvisioned) {
		t.Errorf("second Provision error = %v, want ErrAlreadyProvisioned", err)
	}

	// The existing row should still be intact.
	got, err := st.Get(ctx, "acc-002")
	if err != nil {
		t.Fatalf("Get after re-provision: %v", err)
	}
	if got.Address != "bob@vulos.org" {
		t.Errorf("Address after re-provision = %q, want %q", got.Address, "bob@vulos.org")
	}
}

// TestGetNotFound verifies that Get returns ErrNotFound for an unknown account.
func TestGetNotFound(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	_, err := st.Get(ctx, "no-such-account")
	if !errors.Is(err, anchorinbox.ErrNotFound) {
		t.Errorf("Get unknown account = %v, want ErrNotFound", err)
	}
}

// TestQuotaDefault verifies that the quota defaults to DefaultQuotaMB (1024 MB).
func TestQuotaDefault(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	inbox, err := st.Provision(ctx, "acc-003", "carol")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if inbox.QuotaMB != 1024 {
		t.Errorf("QuotaMB = %d, want 1024", inbox.QuotaMB)
	}

	// Verify via Get as well.
	got, err := st.Get(ctx, "acc-003")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.QuotaMB != 1024 {
		t.Errorf("QuotaMB via Get = %d, want 1024", got.QuotaMB)
	}
}

// TestCrossAccountIsolation verifies that multiple accounts each get their own
// anchor inbox and that Get is scoped to the correct account.
func TestCrossAccountIsolation(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	accounts := []struct {
		id     string
		handle string
	}{
		{"acc-010", "dave"},
		{"acc-011", "eve"},
		{"acc-012", "frank"},
	}

	for _, a := range accounts {
		if _, err := st.Provision(ctx, a.id, a.handle); err != nil {
			t.Fatalf("Provision %s: %v", a.id, err)
		}
	}

	for _, a := range accounts {
		got, err := st.Get(ctx, a.id)
		if err != nil {
			t.Fatalf("Get %s: %v", a.id, err)
		}
		want := a.handle + "@vulos.org"
		if got.Address != want {
			t.Errorf("account %s: Address = %q, want %q", a.id, got.Address, want)
		}
		if got.AccountID != a.id {
			t.Errorf("account %s: AccountID = %q, want %q", a.id, got.AccountID, a.id)
		}
	}
}

// TestGetByAddress verifies address-keyed lookup.
func TestGetByAddress(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	if _, err := st.Provision(ctx, "acc-020", "grace"); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	got, err := st.GetByAddress(ctx, "grace@vulos.org")
	if err != nil {
		t.Fatalf("GetByAddress: %v", err)
	}
	if got.AccountID != "acc-020" {
		t.Errorf("AccountID = %q, want %q", got.AccountID, "acc-020")
	}

	// Unknown address must return ErrNotFound.
	_, err = st.GetByAddress(ctx, "nobody@vulos.org")
	if !errors.Is(err, anchorinbox.ErrNotFound) {
		t.Errorf("GetByAddress unknown = %v, want ErrNotFound", err)
	}
}

// TestAddressTakenByOtherAccount verifies that two accounts cannot claim the
// same @vulos.org address.
func TestAddressTakenByOtherAccount(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	if _, err := st.Provision(ctx, "acc-030", "henry"); err != nil {
		t.Fatalf("first Provision: %v", err)
	}

	// Different accountID, same handle → address collision.
	_, err := st.Provision(ctx, "acc-031", "henry")
	if err == nil {
		t.Fatal("expected error for duplicate address, got nil")
	}
	// Must NOT be ErrAlreadyProvisioned (that is reserved for same account_id).
	if errors.Is(err, anchorinbox.ErrAlreadyProvisioned) {
		t.Error("got ErrAlreadyProvisioned, but want a different-account address-collision error")
	}
}
