// Package cdn implements BYO-CDN configuration and an (opt-in, dry-run)
// origin-firewall preview for a Vulos box — Settings -> Network -> CDN.
//
// Ported down from vulos.cloud's multi-tenant control plane
// (management/pkg/cdn) to single-owner box scope: one box, one CDN vendor
// fronting it, no account_id keying. What survives the port:
//
//   - Provider selection (Cloudflare/Fastly/Bunny) + origin host / Host
//     header / mTLS(authenticated-origin-pulls) flag — see Config.
//   - RangeFetcher (fetcher.go): periodically pulls each provider's
//     published egress IP CIDRs so the box always has a current allowlist
//     to build a rule set from.
//   - The origin-firewall RULE SET, computed from the fetched CIDRs
//     (firewall.go).
//
// What does NOT survive the port, deliberately:
//
//   - Live enforcement. The reference control plane left "the OS agent
//     enforces the actual iptables/nftables rules" as a TODO for a
//     separate agent process; this box has no existing firewall code at
//     all (grep-confirmed at port time). Writing a first nftables
//     integration AND getting its lockout-safety right in the same pass
//     as everything else here was judged too risky to ship live. So:
//     firewall.go generates a real, valid nftables ruleset and an Applier
//     interface exists for it — but the only Applier wired into
//     cmd/server/routes_cdn.go is DryRunApplier, which never touches the
//     box's actual packet filter. Enabling the firewall in this build
//     computes and RECORDS the ruleset an owner would get, and nothing
//     more. A future change can add a real nft-invoking Applier behind
//     the same interface once it has been exercised against real
//     hardware — a half-safe feature beats a lockout.
//
// Safety invariants baked into the generated ruleset (see firewall.go):
// loopback, established/related connections, the SSH port, and any
// owner-declared extra admin ports are ALWAYS allowed, regardless of CDN
// CIDR state; only inbound 80/443 is ever restricted; an empty/stale/
// unfetched CIDR list refuses to generate a blocking ruleset at all
// (fail open, never fail closed).
package cdn

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"time"
)

// Provider identifies a supported CDN vendor.
type Provider string

const (
	ProviderCloudflare Provider = "cloudflare"
	ProviderFastly     Provider = "fastly"
	ProviderBunny      Provider = "bunny"
)

// AllProviders is every provider RangeFetcher knows how to fetch, in a
// stable order (used to drive "refresh everything" and UI listings).
var AllProviders = []Provider{ProviderCloudflare, ProviderFastly, ProviderBunny}

// ValidProvider reports whether p is a supported CDN provider. An empty
// string is NOT valid — Config.Provider is empty only in its unconfigured
// zero-value state.
func ValidProvider(p Provider) bool {
	return p == ProviderCloudflare || p == ProviderFastly || p == ProviderBunny
}

// Config is the box's singleton BYO-CDN configuration. Single-owner scope:
// exactly one row exists (see migrations/0001_cdn_init.sql, id=1).
//
// FirewallEnabled/SSHPort/ExtraAllowPorts are the owner's origin-firewall
// opt-in and its safety inputs — kept on the same record as the CDN vendor
// choice because the firewall only ever makes sense relative to a
// configured provider's CIDRs. LastRuleset/LastRulesetAt are an audit trail
// of the most recently GENERATED ruleset, not proof anything was applied
// (see package doc: live application is deferred in this build).
type Config struct {
	Provider    Provider `json:"provider"`
	OriginHost  string   `json:"origin_host"`  // e.g. "origin.example.org"
	HostHeader  string   `json:"host_header"`  // Host header to enforce on origin requests
	MTLSEnabled bool     `json:"mtls_enabled"` // authenticated-origin-pulls (informational; not enforced here)

	// Origin-firewall opt-in. Defaults false — the firewall is OFF until the
	// owner explicitly turns it on (see routes_cdn.go, stepup-gated).
	FirewallEnabled bool `json:"firewall_enabled"`
	// SSHPort is always allowed by the generated ruleset regardless of CDN
	// CIDR state. 0 means "unset"; DefaultSSHPort is applied at build time.
	SSHPort int `json:"ssh_port"`
	// ExtraAllowPorts are additional ports the generated ruleset must always
	// allow (e.g. a dedicated admin-dashboard port), on top of loopback,
	// established/related, and SSHPort, which are hard-coded and cannot be
	// removed by config. Optional; empty by default.
	ExtraAllowPorts []int `json:"extra_allow_ports"`

	LastRuleset   string    `json:"last_ruleset,omitempty"`
	LastRulesetAt time.Time `json:"last_ruleset_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsConfigured reports whether a CDN provider has been chosen yet.
func (c Config) IsConfigured() bool {
	return ValidProvider(c.Provider) && c.OriginHost != ""
}

// IPRange is a cached CDN egress IP CIDR entry.
type IPRange struct {
	Provider  Provider  `json:"provider"`
	CIDR      string    `json:"cidr"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Sentinel errors.
var (
	ErrNotFound        = errors.New("cdn: config not found")
	ErrInvalidProvider = errors.New("cdn: invalid provider (must be cloudflare|fastly|bunny)")
	ErrOriginRequired  = errors.New("cdn: origin_host is required")
)

// Store is the cdn package's persistence contract. SQLStore (store.go) is
// the production backend.
type Store interface {
	// GetConfig returns the box's singleton BYO-CDN config.
	// Returns ErrNotFound if no config has been saved yet.
	GetConfig(ctx context.Context) (Config, error)

	// SetConfig upserts the singleton BYO-CDN config.
	SetConfig(ctx context.Context, cfg Config) error

	// DeleteConfig removes the BYO-CDN config entirely (also implicitly
	// disables the firewall — callers should verify FirewallEnabled is
	// already false, or accept that deleting turns it off too).
	// Returns ErrNotFound if no config exists.
	DeleteConfig(ctx context.Context) error

	// GetIPRanges returns cached CDN IP ranges for the given provider,
	// sorted by CIDR for stable output.
	GetIPRanges(ctx context.Context, provider Provider) ([]IPRange, error)

	// SetIPRanges replaces all cached IP ranges for the given provider in a
	// single transaction. FetchedAt is set to now on every entry.
	SetIPRanges(ctx context.Context, provider Provider, cidrs []string) error

	Close() error
}

// ValidateCIDRs returns the subset of cidrs that failed net.ParseCIDR
// validation (empty slice = all valid).
func ValidateCIDRs(cidrs []string) []string {
	var bad []string
	for _, c := range cidrs {
		if _, _, err := net.ParseCIDR(c); err != nil {
			bad = append(bad, c)
		}
	}
	return bad
}

// filterValidCIDRs returns only the CIDRs that pass net.ParseCIDR, in the
// same relative order.
func filterValidCIDRs(cidrs []string) []string {
	out := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		if _, _, err := net.ParseCIDR(c); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// sortCIDRs sorts a CIDR slice lexically for stable, diffable output.
func sortCIDRs(cidrs []string) []string {
	out := append([]string(nil), cidrs...)
	sort.Strings(out)
	return out
}

// marshalPorts/unmarshalPorts round-trip ExtraAllowPorts through the
// cdn_config.extra_allow_ports TEXT column (JSON array of ints).
func marshalPorts(ports []int) string {
	if len(ports) == 0 {
		return "[]"
	}
	b, err := json.Marshal(ports)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func unmarshalPorts(raw string) []int {
	if raw == "" {
		return nil
	}
	var ports []int
	if err := json.Unmarshal([]byte(raw), &ports); err != nil {
		return nil
	}
	return ports
}
