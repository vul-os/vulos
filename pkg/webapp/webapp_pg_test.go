package webapp_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/webapp"
)

func openPGTestStore(t *testing.T) webapp.Store {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	db, err := cpdb.Open("webapp_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := webapp.OpenSQLStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("webapp.OpenSQLStore (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS webapp_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_ConnectAndGet(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	req := webapp.ConnectDomainRequest{
		AccountID: "pg-acct-1",
		ULID:      "01PG1ULID001",
		Domain:    "pg.example.com",
		AppKind:   webapp.AppKindNative,
	}
	got, err := st.ConnectDomain(ctx, req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got.Domain != "pg.example.com" {
		t.Fatalf("domain mismatch: %s", got.Domain)
	}
	if got.CacheTTLSec != webapp.DefaultCacheTTLSec {
		t.Fatalf("default TTL not applied: %d", got.CacheTTLSec)
	}

	fetched, err := st.GetDomain(ctx, req.ULID, req.Domain)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched.AccountID != "pg-acct-1" {
		t.Fatalf("account_id mismatch: %s", fetched.AccountID)
	}
}

func TestPG_DuplicateDomain(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	req := webapp.ConnectDomainRequest{
		AccountID: "pg-acct-2",
		ULID:      "01PG1ULID002",
		Domain:    "dup-pg.example.com",
	}
	if _, err := st.ConnectDomain(ctx, req); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	_, err := st.ConnectDomain(ctx, req)
	if err != webapp.ErrDomainAlreadyExists {
		t.Fatalf("want ErrDomainAlreadyExists, got %v", err)
	}
}

func TestPG_DeleteDomain(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	req := webapp.ConnectDomainRequest{
		AccountID: "pg-acct-3",
		ULID:      "01PG1ULID003",
		Domain:    "del-pg.example.com",
	}
	if _, err := st.ConnectDomain(ctx, req); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := st.DeleteDomain(ctx, req.ULID, req.Domain); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := st.GetDomain(ctx, req.ULID, req.Domain)
	if err != webapp.ErrDomainNotFound {
		t.Fatalf("want ErrDomainNotFound after delete, got %v", err)
	}
}

func TestPG_SetPublished(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	req := webapp.ConnectDomainRequest{
		AccountID: "pg-acct-4",
		ULID:      "01PG1ULID004",
		Domain:    "pub-pg.example.com",
	}
	if _, err := st.ConnectDomain(ctx, req); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := st.SetPublished(ctx, req.ULID, req.Domain, true); err != nil {
		t.Fatalf("set published: %v", err)
	}
	d, err := st.GetDomain(ctx, req.ULID, req.Domain)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !d.Published {
		t.Fatal("published should be true")
	}
	if err := st.SetPublished(ctx, req.ULID, req.Domain, false); err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	d, _ = st.GetDomain(ctx, req.ULID, req.Domain)
	if d.Published {
		t.Fatal("published should be false after unpublish")
	}
}

func TestPG_ListDomains(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	ulid := "01PG1ULID005"
	for _, dom := range []string{"z-pg.example.com", "a-pg.example.com", "m-pg.example.com"} {
		if _, err := st.ConnectDomain(ctx, webapp.ConnectDomainRequest{
			AccountID: "pg-acct-5",
			ULID:      ulid,
			Domain:    dom,
		}); err != nil {
			t.Fatalf("connect %s: %v", dom, err)
		}
	}
	list, err := st.ListDomains(ctx, ulid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("want 3, got %d", len(list))
	}
	if list[0].Domain != "a-pg.example.com" {
		t.Fatalf("want first=a-pg.example.com, got %s", list[0].Domain)
	}
}
