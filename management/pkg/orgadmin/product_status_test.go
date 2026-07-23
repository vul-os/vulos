package orgadmin

// product_status_test.go — PRODUCTS-ADMIN-01 service authz + store round-trip.
//
// Covers the critical authz path (owner/admin may set, member → 403), input
// validation (unknown status → 400), persistence + tenant-scoping (cross-org
// isolation), and the dual-dialect store (MemStore + SQLite; Postgres in
// store_pg_test.go via the shared openPGTestStore skip-guard).

import (
	"context"
	"errors"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// newProductSvc builds a Service whose caller resolves to `role`, backed by an
// in-memory product-status store.
func newProductSvc(role string) *Service {
	svc := NewService(nil, nil, nil, nil, nil, NewMemStore(), nil)
	svc.Roles = fixedRoleResolver{role: role}
	svc.Products = NewMemStore()
	return svc
}

// TestSetProductStatus_RBAC verifies the owner/admin gate: owner + admin may set
// a product's status; billing-admin + member (and unknown roles) are refused
// with ErrForbidden. This is the load-bearing authz assertion.
func TestSetProductStatus_RBAC(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		role      string
		wantAllow bool
	}{
		{"owner", true},
		{"admin", true},
		{"billing-admin", false},
		{"member", false},
		{"", false},         // no membership → least privilege
		{"nonsense", false}, // unknown role → member → denied
	}
	for _, c := range cases {
		svc := newProductSvc(c.role)
		err := svc.SetProductStatus(ctx, "org-1", "caller-1", "mail", "off")
		if c.wantAllow && err != nil {
			t.Errorf("role %q: want allow, got %v", c.role, err)
		}
		if !c.wantAllow && !errors.Is(err, ErrForbidden) {
			t.Errorf("role %q: want ErrForbidden, got %v", c.role, err)
		}
	}
}

// TestSetProductStatus_InvalidStatus verifies a status outside the closed set is
// rejected with ErrInvalidInput even for an authorized caller (validation runs
// after authz, before the write).
func TestSetProductStatus_InvalidStatus(t *testing.T) {
	ctx := context.Background()
	svc := newProductSvc("owner")
	for _, bad := range []string{"enabled", "ON", "", "Available", "deleted"} {
		if err := svc.SetProductStatus(ctx, "org-1", "caller-1", "mail", bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("status %q: want ErrInvalidInput, got %v", bad, err)
		}
	}
	for _, ok := range []string{"configured", "available", "off"} {
		if err := svc.SetProductStatus(ctx, "org-1", "caller-1", "mail", ok); err != nil {
			t.Errorf("status %q: want nil, got %v", ok, err)
		}
	}
}

// TestSetProductStatus_NoStore verifies a missing product store fails closed:
// an authorized, valid write reports ErrNotFound rather than silently accepting.
func TestSetProductStatus_NoStore(t *testing.T) {
	ctx := context.Background()
	svc := NewService(nil, nil, nil, nil, nil, NewMemStore(), nil)
	svc.Roles = fixedRoleResolver{role: "owner"}
	// svc.Products left nil.
	if err := svc.SetProductStatus(ctx, "org-1", "caller-1", "mail", "off"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no store: want ErrNotFound, got %v", err)
	}
}

// TestProductStatus_PersistAndScope verifies a set is (a) reflected on read and
// (b) scoped to the tenant — a second org sees no override (cross-org isolation).
func TestProductStatus_PersistAndScope(t *testing.T) {
	ctx := context.Background()
	svc := newProductSvc("owner")

	if err := svc.SetProductStatus(ctx, "org-1", "caller-1", "mail", "off"); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Idempotent re-set (same value) must still succeed.
	if err := svc.SetProductStatus(ctx, "org-1", "caller-1", "mail", "off"); err != nil {
		t.Fatalf("idempotent set: %v", err)
	}

	got, err := svc.ProductStatuses(ctx, "org-1")
	if err != nil {
		t.Fatalf("read org-1: %v", err)
	}
	if got["mail"] != "off" {
		t.Errorf("org-1 mail = %q, want off", got["mail"])
	}

	// A different tenant must NOT see org-1's override.
	other, err := svc.ProductStatuses(ctx, "org-2")
	if err != nil {
		t.Fatalf("read org-2: %v", err)
	}
	if _, present := other["mail"]; present {
		t.Errorf("cross-org leak: org-2 sees mail override %v", other)
	}
}

// TestProductStatusStore_MemAndSQLite exercises the same round-trip against BOTH
// dialect-backed stores the wiring can select: the in-memory reference and the
// SQLite cpdb store. (Postgres is covered in store_pg_test.go behind the
// VULOS_TEST_POSTGRES skip-guard, using the identical assertions.)
func TestProductStatusStore_MemAndSQLite(t *testing.T) {
	ctx := context.Background()

	sqlite := func() ProductStatusStore {
		db, err := cpdb.OpenSQLiteDSN(":memory:")
		if err != nil {
			t.Fatalf("cpdb open: %v", err)
		}
		st, err := OpenSQLStore(db)
		if err != nil {
			db.Close()
			t.Fatalf("OpenSQLStore: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	}

	stores := map[string]ProductStatusStore{
		"mem":    NewMemStore(),
		"sqlite": sqlite(),
	}
	for name, st := range stores {
		t.Run(name, func(t *testing.T) {
			// Unknown tenant → empty map, no error.
			m, err := st.ProductStatuses(ctx, "org-x")
			if err != nil {
				t.Fatalf("empty read: %v", err)
			}
			if len(m) != 0 {
				t.Errorf("empty read = %v, want {}", m)
			}
			// Upsert two products for org-1.
			if err := st.SetProductStatus(ctx, "org-1", "mail", "off"); err != nil {
				t.Fatalf("set mail: %v", err)
			}
			if err := st.SetProductStatus(ctx, "org-1", "office", "configured"); err != nil {
				t.Fatalf("set office: %v", err)
			}
			// Update mail (upsert path).
			if err := st.SetProductStatus(ctx, "org-1", "mail", "available"); err != nil {
				t.Fatalf("update mail: %v", err)
			}
			got, err := st.ProductStatuses(ctx, "org-1")
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if got["mail"] != "available" || got["office"] != "configured" {
				t.Errorf("read = %v, want mail=available office=configured", got)
			}
			// Tenant scoping.
			other, err := st.ProductStatuses(ctx, "org-2")
			if err != nil {
				t.Fatalf("read org-2: %v", err)
			}
			if len(other) != 0 {
				t.Errorf("org-2 = %v, want {} (scoped)", other)
			}
		})
	}
}
