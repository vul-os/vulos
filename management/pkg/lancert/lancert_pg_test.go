package lancert_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/lancert"
)

func openPGLancertStore(t *testing.T) *lancert.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("lancert_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := lancert.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("lancert.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS lancert_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_Lancert_UpsertLANIP(t *testing.T) {
	st := openPGLancertStore(t)
	ctx := context.Background()

	if err := st.UpsertLANIP(ctx, "box1", "box1.lan.vulos.org", "192.168.1.1", time.Now()); err != nil {
		t.Fatalf("UpsertLANIP: %v", err)
	}
	bs, err := st.Get(ctx, "box1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if bs.LANIP != "192.168.1.1" {
		t.Errorf("lanip: %q", bs.LANIP)
	}
	if bs.FQDN != "box1.lan.vulos.org" {
		t.Errorf("fqdn: %q", bs.FQDN)
	}

	// Update IP.
	if err := st.UpsertLANIP(ctx, "box1", "box1.lan.vulos.org", "192.168.1.2", time.Now()); err != nil {
		t.Fatalf("UpsertLANIP update: %v", err)
	}
	bs, err = st.Get(ctx, "box1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if bs.LANIP != "192.168.1.2" {
		t.Errorf("updated lanip: %q", bs.LANIP)
	}
}

func TestPG_Lancert_SaveCert(t *testing.T) {
	st := openPGLancertStore(t)
	ctx := context.Background()
	now := time.Now()

	// Box must exist before SaveCert (SaveCert uses UPSERT with created_at).
	if err := st.UpsertLANIP(ctx, "box2", "box2.lan.vulos.org", "10.0.0.2", now); err != nil {
		t.Fatalf("UpsertLANIP: %v", err)
	}

	c := lancert.BoxCert{
		BoxID:     "box2",
		FQDN:      "box2.lan.vulos.org",
		CertPEM:   "---CERT---",
		KeyPEM:    "---KEY---",
		NotBefore: now,
		NotAfter:  now.Add(90 * 24 * time.Hour),
	}
	if err := st.SaveCert(ctx, c, now); err != nil {
		t.Fatalf("SaveCert: %v", err)
	}
	bs, err := st.Get(ctx, "box2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if bs.CertPEM != "---CERT---" {
		t.Errorf("cert: %q", bs.CertPEM)
	}
	if bs.KeyPEM != "---KEY---" {
		t.Errorf("key: %q", bs.KeyPEM)
	}
}

func TestPG_Lancert_AppendLog(t *testing.T) {
	st := openPGLancertStore(t)
	ctx := context.Background()

	if err := st.UpsertLANIP(ctx, "box3", "box3.lan.vulos.org", "10.0.0.1", time.Now()); err != nil {
		t.Fatalf("UpsertLANIP: %v", err)
	}
	e := lancert.LogEntry{
		BoxID:      "box3",
		FQDN:       "box3.lan.vulos.org",
		Event:      "issued",
		Detail:     "ok",
		OccurredAt: time.Now(),
	}
	if err := st.AppendLog(ctx, e); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	logs, err := st.RecentLog(ctx, "box3", 10)
	if err != nil {
		t.Fatalf("RecentLog: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("expected logs")
	}
	if logs[0].Event != "issued" {
		t.Errorf("event: %q", logs[0].Event)
	}
}

func TestPG_Lancert_GetUnknown(t *testing.T) {
	st := openPGLancertStore(t)
	_, err := st.Get(context.Background(), "no-such-box")
	if err != lancert.ErrUnknownBox {
		t.Errorf("got %v; want ErrUnknownBox", err)
	}
}
