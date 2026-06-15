package anchorinbox_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"vulos/backend/services/anchorinbox"
)

// openTestStore creates a Store backed by a temp SQLite file that is cleaned up
// after the test.
func openTestStore(t *testing.T) *anchorinbox.AnchorStore {
	t.Helper()
	dir := t.TempDir()
	s, err := anchorinbox.Open(filepath.Join(dir, "anchor.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// ---------------------------------------------------------------------------
// TestAnchorProvisionCreatesRecord
// ---------------------------------------------------------------------------

func TestAnchorProvisionCreatesRecord(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const accountID = "user-abc-123"

	st, err := s.Provision(ctx, accountID)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if st.AccountID != accountID {
		t.Errorf("AccountID: got %q want %q", st.AccountID, accountID)
	}
	if st.Status != "provisioned" {
		t.Errorf("Status: got %q want %q", st.Status, "provisioned")
	}
	if st.BucketPrefix == "" {
		t.Error("BucketPrefix: must not be empty")
	}
	if st.ProvisionedAt.IsZero() {
		t.Error("ProvisionedAt: must not be zero")
	}
}

// ---------------------------------------------------------------------------
// TestAnchorProvisionIdempotent
// ---------------------------------------------------------------------------

func TestAnchorProvisionIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const accountID = "user-idempotent"

	st1, err := s.Provision(ctx, accountID)
	if err != nil {
		t.Fatalf("first Provision: %v", err)
	}

	st2, err := s.Provision(ctx, accountID)
	if err != nil {
		t.Fatalf("second Provision: %v", err)
	}

	if st1.BucketPrefix != st2.BucketPrefix {
		t.Errorf("BucketPrefix changed on second provision: %q -> %q", st1.BucketPrefix, st2.BucketPrefix)
	}
	if st1.Status != st2.Status {
		t.Errorf("Status changed on second provision: %q -> %q", st1.Status, st2.Status)
	}
}

// ---------------------------------------------------------------------------
// TestAnchorStatusNotFound
// ---------------------------------------------------------------------------

func TestAnchorStatusNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, err := s.Status(ctx, "unknown-account-xyz")
	if err == nil {
		t.Fatal("Status for unknown account: expected error, got nil")
	}
	if !errors.Is(err, anchorinbox.ErrNotFound) {
		t.Errorf("Status error: got %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// TestAnchorCapacityDefault
// ---------------------------------------------------------------------------

func TestAnchorCapacityDefault(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const accountID = "user-capacity-check"

	st, err := s.Provision(ctx, accountID)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	const want = anchorinbox.DefaultCapacityBytes // 1 GiB
	if st.CapacityBytes != want {
		t.Errorf("CapacityBytes: got %d want %d", st.CapacityBytes, want)
	}
	if st.UsedBytes != 0 {
		t.Errorf("UsedBytes: got %d want 0", st.UsedBytes)
	}
}

// ---------------------------------------------------------------------------
// TestAnchorStatusAfterProvision
// ---------------------------------------------------------------------------

func TestAnchorStatusAfterProvision(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const accountID = "user-status-check"

	if _, err := s.Provision(ctx, accountID); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	st, err := s.Status(ctx, accountID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.AccountID != accountID {
		t.Errorf("AccountID: got %q want %q", st.AccountID, accountID)
	}
	if st.Status != "provisioned" {
		t.Errorf("Status: got %q want %q", st.Status, "provisioned")
	}
	if st.CapacityBytes != anchorinbox.DefaultCapacityBytes {
		t.Errorf("CapacityBytes: got %d want %d", st.CapacityBytes, anchorinbox.DefaultCapacityBytes)
	}
}
