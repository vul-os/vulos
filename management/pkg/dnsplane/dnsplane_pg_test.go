package dnsplane_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/dnsplane"
)

func openPGDNSStore(t *testing.T) *dnsplane.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("dnsplane_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := dnsplane.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("dnsplane.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS dnsplane_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_DNS_Upsert(t *testing.T) {
	st := openPGDNSStore(t)
	ctx := context.Background()
	r, err := st.Upsert(ctx, dnsplane.Record{FQDN: "test.vulos.org", Type: "A", Value: "1.2.3.4", TTL: 60})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if r.State != "desired" {
		t.Errorf("state: %q", r.State)
	}
}

func TestPG_DNS_DesiredUnapplied(t *testing.T) {
	st := openPGDNSStore(t)
	ctx := context.Background()
	if _, err := st.Upsert(ctx, dnsplane.Record{FQDN: "a.vulos.org", Type: "A", Value: "1.2.3.4", TTL: 60}); err != nil {
		t.Fatalf("%v", err)
	}
	recs, err := st.DesiredUnapplied(ctx, 10)
	if err != nil {
		t.Fatalf("DesiredUnapplied: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("expected records")
	}
}

func TestPG_DNS_MarkApplied(t *testing.T) {
	st := openPGDNSStore(t)
	ctx := context.Background()
	r, err := st.Upsert(ctx, dnsplane.Record{FQDN: "b.vulos.org", Type: "A", Value: "5.6.7.8", TTL: 60})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := st.MarkApplied(ctx, r.ID, "cf-123"); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	n, _ := st.CountApplied(ctx)
	if n == 0 {
		t.Error("expected at least one applied")
	}
}
