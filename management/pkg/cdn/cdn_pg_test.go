package cdn_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cdn"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openPGCDNStore(t *testing.T) *cdn.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("cdn_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := cdn.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("cdn.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS cdn_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_CDN_SetGetConfig(t *testing.T) {
	st := openPGCDNStore(t)
	ctx := context.Background()

	cfg := cdn.Config{
		AccountID:   "acc1",
		Provider:    cdn.ProviderCloudflare,
		OriginHost:  "origin.example.com",
		MTLSEnabled: true,
		HostHeader:  "",
		Enabled:     true,
	}
	if err := st.SetConfig(ctx, cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	got, err := st.GetConfig(ctx, "acc1")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.Provider != cdn.ProviderCloudflare {
		t.Errorf("provider: %q", got.Provider)
	}
	if !got.MTLSEnabled {
		t.Error("MTLSEnabled should be true")
	}
	if !got.Enabled {
		t.Error("Enabled should be true")
	}
}

func TestPG_CDN_GetConfig_NotFound(t *testing.T) {
	st := openPGCDNStore(t)
	_, err := st.GetConfig(context.Background(), "no-such-account")
	if err != cdn.ErrNotFound {
		t.Errorf("got %v; want ErrNotFound", err)
	}
}

func TestPG_CDN_DeleteConfig(t *testing.T) {
	st := openPGCDNStore(t)
	ctx := context.Background()

	cfg := cdn.Config{
		AccountID:  "acc-del",
		Provider:   cdn.ProviderFastly,
		OriginHost: "origin-del.example.com",
		Enabled:    true,
	}
	if err := st.SetConfig(ctx, cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := st.DeleteConfig(ctx, "acc-del"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if err := st.DeleteConfig(ctx, "acc-del"); err != cdn.ErrNotFound {
		t.Errorf("second delete: got %v; want ErrNotFound", err)
	}
}

func TestPG_CDN_IPRanges(t *testing.T) {
	st := openPGCDNStore(t)
	ctx := context.Background()

	cidrs := []string{"103.21.244.0/22", "103.22.200.0/22", "198.41.128.0/17"}
	if err := st.SetIPRanges(ctx, cdn.ProviderCloudflare, cidrs); err != nil {
		t.Fatalf("SetIPRanges: %v", err)
	}
	got, err := st.GetIPRanges(ctx, cdn.ProviderCloudflare)
	if err != nil {
		t.Fatalf("GetIPRanges: %v", err)
	}
	if len(got) != len(cidrs) {
		t.Errorf("got %d ranges; want %d", len(got), len(cidrs))
	}
	for _, r := range got {
		if r.Provider != cdn.ProviderCloudflare {
			t.Errorf("provider = %q; want cloudflare", r.Provider)
		}
		if r.FetchedAt.IsZero() {
			t.Error("FetchedAt should not be zero")
		}
	}

	// Replace ranges atomically.
	if err := st.SetIPRanges(ctx, cdn.ProviderCloudflare, []string{"198.41.128.0/17"}); err != nil {
		t.Fatalf("SetIPRanges replace: %v", err)
	}
	replaced, err := st.GetIPRanges(ctx, cdn.ProviderCloudflare)
	if err != nil {
		t.Fatalf("GetIPRanges after replace: %v", err)
	}
	if len(replaced) != 1 {
		t.Errorf("after replace: got %d; want 1", len(replaced))
	}
}
