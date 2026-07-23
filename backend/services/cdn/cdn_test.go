package cdn

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidProvider(t *testing.T) {
	for _, p := range []Provider{ProviderCloudflare, ProviderFastly, ProviderBunny} {
		if !ValidProvider(p) {
			t.Errorf("ValidProvider(%q) = false, want true", p)
		}
	}
	if ValidProvider("") || ValidProvider("akamai") {
		t.Error("ValidProvider accepted an invalid provider")
	}
}

func TestValidateCIDRs(t *testing.T) {
	bad := ValidateCIDRs([]string{"1.2.3.0/24", "not-a-cidr", "2001:db8::/32", "10.0.0.1"})
	if len(bad) != 2 || bad[0] != "not-a-cidr" || bad[1] != "10.0.0.1" {
		t.Fatalf("ValidateCIDRs = %v, want [not-a-cidr 10.0.0.1]", bad)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Store round-trip
// ─────────────────────────────────────────────────────────────────────────────

func openTestStore(t *testing.T) *SQLStore {
	t.Helper()
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestStore_ConfigRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	if _, err := st.GetConfig(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetConfig on empty store: err = %v, want ErrNotFound", err)
	}

	cfg := Config{
		Provider:        ProviderCloudflare,
		OriginHost:      "origin.example.org",
		HostHeader:      "origin.example.org",
		MTLSEnabled:     true,
		FirewallEnabled: false,
		SSHPort:         2222,
		ExtraAllowPorts: []int{9090, 9091},
	}
	if err := st.SetConfig(ctx, cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	got, err := st.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.Provider != cfg.Provider || got.OriginHost != cfg.OriginHost || got.HostHeader != cfg.HostHeader {
		t.Fatalf("GetConfig roundtrip mismatch: got %+v", got)
	}
	if !got.MTLSEnabled || got.SSHPort != 2222 || len(got.ExtraAllowPorts) != 2 {
		t.Fatalf("GetConfig roundtrip mismatch (flags/ports): got %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("CreatedAt/UpdatedAt not set: got %+v", got)
	}
	firstCreated := got.CreatedAt

	// Update should preserve CreatedAt.
	cfg.OriginHost = "origin2.example.org"
	time.Sleep(2 * time.Millisecond)
	if err := st.SetConfig(ctx, cfg); err != nil {
		t.Fatalf("SetConfig (update): %v", err)
	}
	got2, err := st.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig (after update): %v", err)
	}
	if got2.OriginHost != "origin2.example.org" {
		t.Fatalf("update did not take: got %+v", got2)
	}
	if !got2.CreatedAt.Equal(firstCreated) {
		t.Fatalf("CreatedAt changed on update: got %v, want %v", got2.CreatedAt, firstCreated)
	}

	if err := st.DeleteConfig(ctx); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if err := st.DeleteConfig(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteConfig (again): err = %v, want ErrNotFound", err)
	}
}

func TestStore_IPRangesRoundTripAndReplace(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	if err := st.SetIPRanges(ctx, ProviderCloudflare, []string{"1.2.3.0/24", "4.5.6.0/24"}); err != nil {
		t.Fatalf("SetIPRanges: %v", err)
	}
	got, err := st.GetIPRanges(ctx, ProviderCloudflare)
	if err != nil {
		t.Fatalf("GetIPRanges: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("GetIPRanges = %d entries, want 2", len(got))
	}

	// A second provider's ranges must not interfere.
	if err := st.SetIPRanges(ctx, ProviderFastly, []string{"7.8.9.0/24"}); err != nil {
		t.Fatalf("SetIPRanges(fastly): %v", err)
	}
	cf, _ := st.GetIPRanges(ctx, ProviderCloudflare)
	if len(cf) != 2 {
		t.Fatalf("cloudflare ranges affected by fastly write: got %d", len(cf))
	}

	// Replacing cloudflare's ranges must fully replace, not append.
	if err := st.SetIPRanges(ctx, ProviderCloudflare, []string{"9.9.9.0/24"}); err != nil {
		t.Fatalf("SetIPRanges (replace): %v", err)
	}
	got2, err := st.GetIPRanges(ctx, ProviderCloudflare)
	if err != nil {
		t.Fatalf("GetIPRanges (after replace): %v", err)
	}
	if len(got2) != 1 || got2[0].CIDR != "9.9.9.0/24" {
		t.Fatalf("SetIPRanges did not replace: got %+v", got2)
	}
}

func TestStore_EmptyDBDirIsInMemoryAndUsable(t *testing.T) {
	st, err := OpenStore("")
	if err != nil {
		t.Fatalf("OpenStore(\"\"): %v", err)
	}
	defer st.Close()
	if err := st.SetIPRanges(context.Background(), ProviderBunny, []string{"1.1.1.0/24"}); err != nil {
		t.Fatalf("SetIPRanges on in-memory store: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Firewall ruleset generation — the safety-critical surface.
// ─────────────────────────────────────────────────────────────────────────────

func TestBuildNFTRuleset_RefusesEmptyCIDRs(t *testing.T) {
	_, err := BuildNFTRuleset(RulesetSpec{Provider: ProviderCloudflare, SSHPort: 22})
	if !errors.Is(err, ErrEmptyCIDRs) {
		t.Fatalf("BuildNFTRuleset with no CIDRs: err = %v, want ErrEmptyCIDRs", err)
	}
}

func TestBuildNFTRuleset_RejectsInvalidCIDR(t *testing.T) {
	_, err := BuildNFTRuleset(RulesetSpec{
		Provider: ProviderCloudflare,
		CIDRs:    []string{"1.2.3.0/24", "not-a-cidr"},
		SSHPort:  22,
	})
	if err == nil {
		t.Fatal("BuildNFTRuleset accepted an invalid CIDR")
	}
}

func TestBuildNFTRuleset_RejectsBadPorts(t *testing.T) {
	_, err := BuildNFTRuleset(RulesetSpec{
		Provider: ProviderCloudflare,
		CIDRs:    []string{"1.2.3.0/24"},
		SSHPort:  70000, // out of range
	})
	if err == nil {
		t.Fatal("BuildNFTRuleset accepted an out-of-range ssh port")
	}
}

// TestBuildNFTRuleset_AlwaysAllowsSafetyPorts is the core lockout-prevention
// test: whatever the CDN CIDR allowlist looks like, the generated ruleset
// MUST unconditionally accept loopback, established/related, ssh, and any
// declared extra ports BEFORE it ever restricts 80/443.
func TestBuildNFTRuleset_AlwaysAllowsSafetyPorts(t *testing.T) {
	ruleset, err := BuildNFTRuleset(RulesetSpec{
		Provider:   ProviderCloudflare,
		CIDRs:      []string{"1.2.3.0/24"},
		SSHPort:    2222,
		ExtraPorts: []int{9090},
	})
	if err != nil {
		t.Fatalf("BuildNFTRuleset: %v", err)
	}
	for _, want := range []string{
		"iif lo accept",
		"ct state established,related accept",
		"tcp dport 2222 accept",
		"tcp dport { 9090 } accept",
	} {
		if !strings.Contains(ruleset, want) {
			t.Errorf("ruleset missing safety rule %q:\n%s", want, ruleset)
		}
	}
	// Must never contain a bare "drop" that isn't scoped to 80/443.
	for _, line := range strings.Split(ruleset, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "drop") && !strings.Contains(trimmed, "80, 443") {
			t.Errorf("found a drop rule not scoped to 80/443: %q", trimmed)
		}
	}
}

func TestBuildNFTRuleset_DefaultSSHPortWhenUnset(t *testing.T) {
	ruleset, err := BuildNFTRuleset(RulesetSpec{
		Provider: ProviderCloudflare,
		CIDRs:    []string{"1.2.3.0/24"},
		// SSHPort left zero.
	})
	if err != nil {
		t.Fatalf("BuildNFTRuleset: %v", err)
	}
	if !strings.Contains(ruleset, "tcp dport 22 accept") {
		t.Errorf("ruleset did not default to port 22:\n%s", ruleset)
	}
}

func TestBuildNFTRuleset_ScopedToOwnTable(t *testing.T) {
	ruleset, err := BuildNFTRuleset(RulesetSpec{
		Provider: ProviderFastly,
		CIDRs:    []string{"1.2.3.0/24"},
		SSHPort:  22,
	})
	if err != nil {
		t.Fatalf("BuildNFTRuleset: %v", err)
	}
	if !strings.Contains(ruleset, "table "+VulosCDNTable) {
		t.Errorf("ruleset not scoped to %s:\n%s", VulosCDNTable, ruleset)
	}
	if !strings.Contains(ruleset, "policy accept") {
		t.Errorf("ruleset base chain policy is not accept (would risk collateral damage to unrelated ports):\n%s", ruleset)
	}
}

func TestBuildNFTRuleset_SplitsIPv4AndIPv6(t *testing.T) {
	ruleset, err := BuildNFTRuleset(RulesetSpec{
		Provider: ProviderCloudflare,
		CIDRs:    []string{"1.2.3.0/24", "2001:db8::/32"},
		SSHPort:  22,
	})
	if err != nil {
		t.Fatalf("BuildNFTRuleset: %v", err)
	}
	if !strings.Contains(ruleset, "ip saddr { 1.2.3.0/24 }") {
		t.Errorf("missing ipv4 saddr match:\n%s", ruleset)
	}
	if !strings.Contains(ruleset, "ip6 saddr { 2001:db8::/32 }") {
		t.Errorf("missing ipv6 saddr match:\n%s", ruleset)
	}
}

func TestBuildStatus_WarnsWhenUnconfigured(t *testing.T) {
	st := BuildStatus(Config{}, nil)
	if st.Configured {
		t.Error("BuildStatus reported Configured=true for a zero-value Config")
	}
	if st.Warning == "" {
		t.Error("BuildStatus did not warn for an unconfigured CDN")
	}
	if st.LiveEnforcement {
		t.Error("LiveEnforcement must always be false in this build")
	}
}

func TestBuildStatus_WarnsOnEmptyCIDRsEvenIfConfigured(t *testing.T) {
	cfg := Config{Provider: ProviderCloudflare, OriginHost: "origin.example.org", FirewallEnabled: true}
	st := BuildStatus(cfg, nil)
	if st.Ruleset != "" {
		t.Error("BuildStatus produced a ruleset with zero cached IP ranges")
	}
	if st.Warning == "" {
		t.Error("BuildStatus did not warn when the ruleset could not be built")
	}
}

func TestBuildStatus_ProducesRulesetWhenReady(t *testing.T) {
	cfg := Config{Provider: ProviderCloudflare, OriginHost: "origin.example.org"}
	ranges := []IPRange{{Provider: ProviderCloudflare, CIDR: "1.2.3.0/24", FetchedAt: time.Now()}}
	st := BuildStatus(cfg, ranges)
	if st.Ruleset == "" {
		t.Fatal("BuildStatus did not produce a ruleset when config+ranges were ready")
	}
	if st.CIDRCount != 1 {
		t.Errorf("CIDRCount = %d, want 1", st.CIDRCount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DryRunApplier — must never touch a real firewall, only Store.
// ─────────────────────────────────────────────────────────────────────────────

func TestDryRunApplier_ApplyAndDisable(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if err := st.SetConfig(ctx, Config{Provider: ProviderCloudflare, OriginHost: "o.example.org"}); err != nil {
		t.Fatalf("seed SetConfig: %v", err)
	}

	applier := &DryRunApplier{Store: st}
	if err := applier.Apply(ctx, ProviderCloudflare, "table inet vulos_cdn { }"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	cfg, err := st.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if !cfg.FirewallEnabled {
		t.Error("Apply did not set FirewallEnabled")
	}
	if cfg.LastRuleset == "" || cfg.LastRulesetAt.IsZero() {
		t.Error("Apply did not record LastRuleset/LastRulesetAt")
	}

	if err := applier.Disable(ctx); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	cfg2, err := st.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig (after disable): %v", err)
	}
	if cfg2.FirewallEnabled {
		t.Error("Disable did not clear FirewallEnabled")
	}
}

func TestDryRunApplier_DisableOnMissingConfigIsNoop(t *testing.T) {
	st := openTestStore(t)
	applier := &DryRunApplier{Store: st}
	if err := applier.Disable(context.Background()); err != nil {
		t.Fatalf("Disable on empty store should be a no-op, got: %v", err)
	}
}
