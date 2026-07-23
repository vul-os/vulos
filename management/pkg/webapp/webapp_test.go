package webapp_test

import (
	"context"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/webapp"
)

// storeFactory returns a fresh Store for testing.
type storeFactory func(t *testing.T) webapp.Store

func memFactory(_ *testing.T) webapp.Store {
	return webapp.NewMemStore()
}

func sqlFactory(t *testing.T) webapp.Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := webapp.OpenSQLStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("webapp.OpenSQLStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func runSuite(t *testing.T, f storeFactory) {
	t.Helper()

	t.Run("ConnectAndGet", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()

		req := webapp.ConnectDomainRequest{
			AccountID: "acct-1",
			ULID:      "01H1ULID001",
			Domain:    "myapp.example.com",
			AppKind:   webapp.AppKindNative,
		}
		got, err := st.ConnectDomain(ctx, req)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		if got.Domain != "myapp.example.com" {
			t.Fatalf("domain mismatch: %s", got.Domain)
		}
		if got.CacheTTLSec != webapp.DefaultCacheTTLSec {
			t.Fatalf("default TTL not applied: %d", got.CacheTTLSec)
		}
		if got.Published {
			t.Fatal("should not be published initially")
		}
		if got.NSDelegated {
			t.Fatal("should not have NS delegation initially")
		}
		if got.TLSProvisioned {
			t.Fatal("should not have TLS initially")
		}

		fetched, err := st.GetDomain(ctx, req.ULID, req.Domain)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if fetched.AccountID != "acct-1" {
			t.Fatalf("account_id mismatch")
		}
	})

	t.Run("ConnectDuplicate", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()

		req := webapp.ConnectDomainRequest{
			AccountID: "acct-1",
			ULID:      "01H1ULID001",
			Domain:    "dup.example.com",
		}
		if _, err := st.ConnectDomain(ctx, req); err != nil {
			t.Fatalf("first connect: %v", err)
		}
		_, err := st.ConnectDomain(ctx, req)
		if err != webapp.ErrDomainAlreadyExists {
			t.Fatalf("want ErrDomainAlreadyExists, got %v", err)
		}
	})

	t.Run("ConnectInvalidInput", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()
		_, err := st.ConnectDomain(ctx, webapp.ConnectDomainRequest{})
		if err != webapp.ErrInvalidInput {
			t.Fatalf("want ErrInvalidInput for empty req, got %v", err)
		}
	})

	t.Run("GetNotFound", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()
		_, err := st.GetDomain(ctx, "noulid", "nope.com")
		if err != webapp.ErrDomainNotFound {
			t.Fatalf("want ErrDomainNotFound, got %v", err)
		}
	})

	t.Run("ListDomains", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()

		for _, dom := range []string{"z.example.com", "a.example.com", "m.example.com"} {
			_, err := st.ConnectDomain(ctx, webapp.ConnectDomainRequest{
				AccountID: "acct-2",
				ULID:      "01H1ULID002",
				Domain:    dom,
			})
			if err != nil {
				t.Fatalf("connect %s: %v", dom, err)
			}
		}
		list, err := st.ListDomains(ctx, "01H1ULID002")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) != 3 {
			t.Fatalf("want 3, got %d", len(list))
		}
		// Should be sorted alphabetically.
		if list[0].Domain != "a.example.com" {
			t.Fatalf("want first=a.example.com, got %s", list[0].Domain)
		}
	})

	t.Run("ListDomainsByAccount", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()
		_, _ = st.ConnectDomain(ctx, webapp.ConnectDomainRequest{
			AccountID: "acct-x",
			ULID:      "01H1ULIDX01",
			Domain:    "x1.example.com",
		})
		_, _ = st.ConnectDomain(ctx, webapp.ConnectDomainRequest{
			AccountID: "acct-x",
			ULID:      "01H1ULIDX02",
			Domain:    "x2.example.com",
		})
		_, _ = st.ConnectDomain(ctx, webapp.ConnectDomainRequest{
			AccountID: "acct-y",
			ULID:      "01H1ULIDY01",
			Domain:    "y1.example.com",
		})
		list, err := st.ListDomainsByAccount(ctx, "acct-x")
		if err != nil {
			t.Fatalf("list by account: %v", err)
		}
		if len(list) != 2 {
			t.Fatalf("want 2 for acct-x, got %d", len(list))
		}
	})

	t.Run("DeleteDomain", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()

		_, err := st.ConnectDomain(ctx, webapp.ConnectDomainRequest{
			AccountID: "acct-3",
			ULID:      "01H1ULID003",
			Domain:    "del.example.com",
		})
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		if err := st.DeleteDomain(ctx, "01H1ULID003", "del.example.com"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		_, err = st.GetDomain(ctx, "01H1ULID003", "del.example.com")
		if err != webapp.ErrDomainNotFound {
			t.Fatalf("want ErrDomainNotFound after delete, got %v", err)
		}
		// Delete again → ErrDomainNotFound
		if err := st.DeleteDomain(ctx, "01H1ULID003", "del.example.com"); err != webapp.ErrDomainNotFound {
			t.Fatalf("want ErrDomainNotFound on second delete, got %v", err)
		}
	})

	t.Run("SetNSDelegated", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()

		_, _ = st.ConnectDomain(ctx, webapp.ConnectDomainRequest{
			AccountID: "acct-4",
			ULID:      "01H1ULID004",
			Domain:    "ns.example.com",
		})
		if err := st.SetNSDelegated(ctx, "01H1ULID004", "ns.example.com", true); err != nil {
			t.Fatalf("set ns delegated: %v", err)
		}
		d, _ := st.GetDomain(ctx, "01H1ULID004", "ns.example.com")
		if !d.NSDelegated {
			t.Fatal("ns_delegated should be true")
		}
		if d.NSVerifiedAt == nil {
			t.Fatal("ns_verified_at should be set")
		}

		// Revoke
		if err := st.SetNSDelegated(ctx, "01H1ULID004", "ns.example.com", false); err != nil {
			t.Fatalf("revoke ns: %v", err)
		}
		d, _ = st.GetDomain(ctx, "01H1ULID004", "ns.example.com")
		if d.NSDelegated {
			t.Fatal("ns_delegated should be false after revoke")
		}
	})

	t.Run("SetTLSProvisioned", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()

		_, _ = st.ConnectDomain(ctx, webapp.ConnectDomainRequest{
			AccountID: "acct-5",
			ULID:      "01H1ULID005",
			Domain:    "tls.example.com",
		})
		if err := st.SetTLSProvisioned(ctx, "01H1ULID005", "tls.example.com", true); err != nil {
			t.Fatalf("set tls: %v", err)
		}
		d, _ := st.GetDomain(ctx, "01H1ULID005", "tls.example.com")
		if !d.TLSProvisioned {
			t.Fatal("tls_provisioned should be true")
		}
		if d.TLSIssuedAt == nil {
			t.Fatal("tls_issued_at should be set")
		}
	})

	t.Run("SetPublished", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()

		_, _ = st.ConnectDomain(ctx, webapp.ConnectDomainRequest{
			AccountID: "acct-6",
			ULID:      "01H1ULID006",
			Domain:    "pub.example.com",
		})
		if err := st.SetPublished(ctx, "01H1ULID006", "pub.example.com", true); err != nil {
			t.Fatalf("set published: %v", err)
		}
		d, _ := st.GetDomain(ctx, "01H1ULID006", "pub.example.com")
		if !d.Published {
			t.Fatal("published should be true")
		}

		// Unpublish
		if err := st.SetPublished(ctx, "01H1ULID006", "pub.example.com", false); err != nil {
			t.Fatalf("unpublish: %v", err)
		}
		d, _ = st.GetDomain(ctx, "01H1ULID006", "pub.example.com")
		if d.Published {
			t.Fatal("published should be false after unpublish")
		}
	})

	t.Run("SetPublishedNotFound", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()
		err := st.SetPublished(ctx, "noexist", "noexist.com", true)
		if err != webapp.ErrDomainNotFound {
			t.Fatalf("want ErrDomainNotFound, got %v", err)
		}
	})

	t.Run("OCIAppKind", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()
		_, err := st.ConnectDomain(ctx, webapp.ConnectDomainRequest{
			AccountID: "acct-7",
			ULID:      "01H1ULID007",
			Domain:    "container.example.com",
			AppKind:   webapp.AppKindOCI,
		})
		if err != nil {
			t.Fatalf("connect oci: %v", err)
		}
		d, err := st.GetDomain(ctx, "01H1ULID007", "container.example.com")
		if err != nil {
			t.Fatalf("get oci: %v", err)
		}
		if d.AppKind != webapp.AppKindOCI {
			t.Fatalf("want oci, got %s", d.AppKind)
		}
	})
}

func TestMemStore(t *testing.T) {
	runSuite(t, memFactory)
}

func TestSQLStore(t *testing.T) {
	runSuite(t, sqlFactory)
}
