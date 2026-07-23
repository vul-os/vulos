package customdomain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// runStoreParity exercises the same sequence of operations against any Store
// implementation so SQLStore and MemStore stay behaviourally identical.
func runStoreParity(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()

	// Unknown domain → ErrNotFound.
	if _, err := s.Get(ctx, "ghost.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(unknown) err = %v, want ErrNotFound", err)
	}

	verifiedAt := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	d := CustomDomain{
		Domain:      "mail.acme.example",
		TenantID:    "tenant-a",
		BundleNode:  "vulos-node-1",
		VerifyToken: "vulos-verify-abc",
		VerifyState: StateVerified,
		ACMEStatus:  "pending",
		VerifiedAt:  &verifiedAt,
		LastError:   "",
	}
	if err := s.Upsert(ctx, d); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := s.Get(ctx, "mail.acme.example")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TenantID != "tenant-a" || got.VerifyState != StateVerified || got.VerifyToken != "vulos-verify-abc" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.VerifiedAt == nil || !got.VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("VerifiedAt roundtrip mismatch: %v", got.VerifiedAt)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not stamped: created=%v updated=%v", got.CreatedAt, got.UpdatedAt)
	}

	// Case-insensitive Get.
	if _, err := s.Get(ctx, "MAIL.ACME.EXAMPLE"); err != nil {
		t.Fatalf("case-insensitive Get: %v", err)
	}

	// Upsert again (state transition) — must replace, not duplicate.
	d.VerifyState = StateAttached
	d.CreatedAt = got.CreatedAt // preserve created across update
	if err := s.Upsert(ctx, d); err != nil {
		t.Fatalf("Upsert(update): %v", err)
	}
	got2, _ := s.Get(ctx, "mail.acme.example")
	if got2.VerifyState != StateAttached {
		t.Fatalf("update state = %q, want attached", got2.VerifyState)
	}

	// Second tenant + domain for list scoping.
	if err := s.Upsert(ctx, CustomDomain{
		Domain: "two.acme.example", TenantID: "tenant-a",
		VerifyToken: "t2", VerifyState: StatePending,
	}); err != nil {
		t.Fatalf("Upsert(two): %v", err)
	}
	if err := s.Upsert(ctx, CustomDomain{
		Domain: "other.beta.example", TenantID: "tenant-b",
		VerifyToken: "t3", VerifyState: StatePending,
	}); err != nil {
		t.Fatalf("Upsert(other): %v", err)
	}

	listA, err := s.ListByTenant(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(listA) != 2 {
		t.Fatalf("tenant-a list = %d, want 2", len(listA))
	}
	listB, _ := s.ListByTenant(ctx, "tenant-b")
	if len(listB) != 1 {
		t.Fatalf("tenant-b list = %d, want 1", len(listB))
	}
	if listEmpty, _ := s.ListByTenant(ctx, "tenant-none"); len(listEmpty) != 0 {
		t.Fatalf("unknown tenant list = %d, want 0", len(listEmpty))
	}

	// Empty-domain Upsert rejected.
	if err := s.Upsert(ctx, CustomDomain{Domain: ""}); !errors.Is(err, ErrInvalidDomain) {
		t.Fatalf("empty-domain Upsert err = %v, want ErrInvalidDomain", err)
	}

	// Delete + verify gone (idempotent).
	if err := s.Delete(ctx, "mail.acme.example"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "mail.acme.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete err = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "mail.acme.example"); err != nil {
		t.Fatalf("idempotent Delete: %v", err)
	}
}

func TestSQLStore_Roundtrip(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	s, err := OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSQLStore: %v", err)
	}
	defer s.Close()
	runStoreParity(t, s)
}

func TestSQLStore_TempFile(t *testing.T) {
	dsn := "file:" + t.TempDir() + "/customdomain.db?_pragma=journal_mode(WAL)"
	db, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	s, err := OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSQLStore(temp): %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if err := s.Upsert(ctx, CustomDomain{
		Domain: "persist.example", TenantID: "t", VerifyToken: "tok", VerifyState: StatePending,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.Get(ctx, "persist.example")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TenantID != "t" {
		t.Fatalf("tenant = %q, want t", got.TenantID)
	}
}

func TestMemStore_Parity(t *testing.T) {
	runStoreParity(t, NewMemStore())
}
