package cdn

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// openTestSQLStore opens an in-memory SQLite store for unit tests.
func openTestSQLStore(t *testing.T) *SQLStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("cdn.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_SetGetConfig(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()

	cfg := Config{
		AccountID:   "acct-1",
		Provider:    ProviderCloudflare,
		OriginHost:  "origin-acct1.vulos.cloud",
		MTLSEnabled: true,
		HostHeader:  "acct1.example.com",
		Enabled:     true,
	}
	if err := st.SetConfig(ctx, cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	got, err := st.GetConfig(ctx, "acct-1")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.Provider != ProviderCloudflare {
		t.Errorf("provider = %q; want %q", got.Provider, ProviderCloudflare)
	}
	if !got.MTLSEnabled {
		t.Error("MTLSEnabled should be true")
	}
	if got.Enabled != true {
		t.Error("Enabled should be true")
	}
}

func TestMemStore_GetConfig_NotFound(t *testing.T) {
	st := NewMemStore()
	_, err := st.GetConfig(context.Background(), "no-such-account")
	if err != ErrNotFound {
		t.Errorf("got %v; want ErrNotFound", err)
	}
}

func TestMemStore_DeleteConfig(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	cfg := Config{AccountID: "acct-del", Provider: ProviderFastly,
		OriginHost: "origin-del.vulos.cloud", Enabled: true}
	_ = st.SetConfig(ctx, cfg)

	if err := st.DeleteConfig(ctx, "acct-del"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if err := st.DeleteConfig(ctx, "acct-del"); err != ErrNotFound {
		t.Errorf("second delete: got %v; want ErrNotFound", err)
	}
}

func TestMemStore_IPRanges(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()

	cidrs := []string{"103.21.244.0/22", "103.22.200.0/22", "198.41.128.0/17"}
	if err := st.SetIPRanges(ctx, ProviderCloudflare, cidrs); err != nil {
		t.Fatalf("SetIPRanges: %v", err)
	}

	got, err := st.GetIPRanges(ctx, ProviderCloudflare)
	if err != nil {
		t.Fatalf("GetIPRanges: %v", err)
	}
	if len(got) != len(cidrs) {
		t.Errorf("got %d ranges; want %d", len(got), len(cidrs))
	}
	for _, r := range got {
		if r.Provider != ProviderCloudflare {
			t.Errorf("provider = %q; want cloudflare", r.Provider)
		}
		if r.FetchedAt.IsZero() {
			t.Error("FetchedAt should not be zero")
		}
	}
}

func TestMemStore_IPRanges_Empty(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	got, err := st.GetIPRanges(ctx, ProviderBunny)
	if err != nil {
		t.Fatalf("GetIPRanges on empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty; got %d", len(got))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SQLStore tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSQLStore_SetGetConfig(t *testing.T) {
	ctx := context.Background()
	st := openTestSQLStore(t)

	cfg := Config{
		AccountID:   "acct-sql",
		Provider:    ProviderFastly,
		OriginHost:  "origin-sql.vulos.cloud",
		MTLSEnabled: true,
		HostHeader:  "sql.example.com",
		Enabled:     true,
	}
	if err := st.SetConfig(ctx, cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	got, err := st.GetConfig(ctx, "acct-sql")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.Provider != ProviderFastly {
		t.Errorf("provider = %q; want fastly", got.Provider)
	}
	if !got.MTLSEnabled {
		t.Error("MTLSEnabled should be true")
	}
	if got.OriginHost != "origin-sql.vulos.cloud" {
		t.Errorf("origin_host = %q", got.OriginHost)
	}
}

func TestSQLStore_GetConfig_NotFound(t *testing.T) {
	st := openTestSQLStore(t)
	_, err := st.GetConfig(context.Background(), "no-such")
	if err != ErrNotFound {
		t.Errorf("got %v; want ErrNotFound", err)
	}
}

func TestSQLStore_DeleteConfig(t *testing.T) {
	ctx := context.Background()
	st := openTestSQLStore(t)

	cfg := Config{AccountID: "acct-del", Provider: ProviderBunny,
		OriginHost: "origin-del.vulos.cloud", Enabled: true}
	_ = st.SetConfig(ctx, cfg)
	if err := st.DeleteConfig(ctx, "acct-del"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if err := st.DeleteConfig(ctx, "acct-del"); err != ErrNotFound {
		t.Errorf("second delete: got %v; want ErrNotFound", err)
	}
}

func TestSQLStore_IPRanges(t *testing.T) {
	ctx := context.Background()
	st := openTestSQLStore(t)

	cidrs := []string{"23.235.32.0/20", "43.249.72.0/22"}
	if err := st.SetIPRanges(ctx, ProviderFastly, cidrs); err != nil {
		t.Fatalf("SetIPRanges: %v", err)
	}
	got, err := st.GetIPRanges(ctx, ProviderFastly)
	if err != nil {
		t.Fatalf("GetIPRanges: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d ranges; want 2", len(got))
	}
}

func TestSQLStore_IPRanges_Replace(t *testing.T) {
	ctx := context.Background()
	st := openTestSQLStore(t)

	_ = st.SetIPRanges(ctx, ProviderCloudflare, []string{"103.21.244.0/22", "103.22.200.0/22"})
	_ = st.SetIPRanges(ctx, ProviderCloudflare, []string{"198.41.128.0/17"}) // replace
	got, _ := st.GetIPRanges(ctx, ProviderCloudflare)
	if len(got) != 1 {
		t.Errorf("after replace: got %d; want 1", len(got))
	}
	if got[0].CIDR != "198.41.128.0/17" {
		t.Errorf("CIDR = %q", got[0].CIDR)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// BuildFirewallRules
// ─────────────────────────────────────────────────────────────────────────────

func TestBuildFirewallRules(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()

	_ = st.SetConfig(ctx, Config{
		AccountID:   "acct-fw",
		Provider:    ProviderCloudflare,
		OriginHost:  "origin-fw.vulos.cloud",
		MTLSEnabled: true,
		HostHeader:  "fw.example.com",
		Enabled:     true,
	})
	_ = st.SetIPRanges(ctx, ProviderCloudflare, []string{
		"103.21.244.0/22", "103.22.200.0/22",
	})

	rules, err := BuildFirewallRules(ctx, st, "acct-fw")
	if err != nil {
		t.Fatalf("BuildFirewallRules: %v", err)
	}
	if len(rules.AllowCIDRs) != 2 {
		t.Errorf("AllowCIDRs len = %d; want 2", len(rules.AllowCIDRs))
	}
	if rules.HostHeader != "fw.example.com" {
		t.Errorf("HostHeader = %q", rules.HostHeader)
	}
	if !rules.MTLSEnabled {
		t.Error("MTLSEnabled should be true")
	}
	if rules.FetchedAt.IsZero() {
		t.Error("FetchedAt should not be zero")
	}
}

func TestBuildFirewallRules_Disabled(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	_ = st.SetConfig(ctx, Config{
		AccountID: "acct-off", Provider: ProviderBunny,
		OriginHost: "origin-off.vulos.cloud", Enabled: false,
	})
	_, err := BuildFirewallRules(ctx, st, "acct-off")
	if err != ErrNotFound {
		t.Errorf("disabled: got %v; want ErrNotFound", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ValidProvider / ValidateCIDRs
// ─────────────────────────────────────────────────────────────────────────────

func TestValidProvider(t *testing.T) {
	for _, p := range []Provider{ProviderCloudflare, ProviderFastly, ProviderBunny} {
		if !ValidProvider(p) {
			t.Errorf("ValidProvider(%q) = false; want true", p)
		}
	}
	if ValidProvider("akamai") {
		t.Error("ValidProvider(akamai) = true; want false")
	}
}

func TestValidateCIDRs(t *testing.T) {
	bad := ValidateCIDRs([]string{"10.0.0.0/8", "not-a-cidr", "192.168.1.0/24"})
	if len(bad) != 1 || bad[0] != "not-a-cidr" {
		t.Errorf("bad = %v; want [not-a-cidr]", bad)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SetConfig preserves created_at on update
// ─────────────────────────────────────────────────────────────────────────────

func TestSQLStore_SetConfig_PreservesCreatedAt(t *testing.T) {
	ctx := context.Background()
	st := openTestSQLStore(t)

	_ = st.SetConfig(ctx, Config{
		AccountID: "acct-ts", Provider: ProviderCloudflare,
		OriginHost: "origin-ts.vulos.cloud", Enabled: true,
	})
	first, _ := st.GetConfig(ctx, "acct-ts")

	time.Sleep(2 * time.Millisecond)
	_ = st.SetConfig(ctx, Config{
		AccountID: "acct-ts", Provider: ProviderFastly,
		OriginHost: "origin-ts.vulos.cloud", Enabled: true,
	})
	second, _ := st.GetConfig(ctx, "acct-ts")

	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("created_at changed: %v → %v", first.CreatedAt, second.CreatedAt)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Error("updated_at should advance on update")
	}
}
