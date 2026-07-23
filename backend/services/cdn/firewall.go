// firewall.go — origin-firewall ruleset GENERATION for the box's CDN CIDR
// allowlist, plus a small Applier seam.
//
// ┌───────────────────────────────────────────────────────────────────────┐
// │ SAFETY MODEL — read this before touching anything in this file.       │
// │                                                                       │
// │ This box has no pre-existing iptables/nftables integration (grep-     │
// │ confirmed at port time). A firewall bug here doesn't just misbehave — │
// │ it can permanently sever the owner's only way to reach their own box. │
// │ Every rule below exists to make that impossible, in order of how much │
// │ it is relied on:                                                     │
// │                                                                       │
// │  1. FAIL OPEN, OPT-IN. FirewallEnabled defaults false (see            │
// │     migrations/0001_cdn_init.sql). Nothing in this package is ever    │
// │     applied to a real box unless the owner explicitly turns it on.    │
// │                                                                       │
// │  2. NEVER LOCK THE OWNER OUT. BuildNFTRuleset hard-codes — the caller │
// │     cannot omit or override — an always-allow for: loopback,          │
// │     established/related connections, the SSH port, and any owner-     │
// │     declared extra admin ports. The ruleset only ever RESTRICTS       │
// │     inbound 80/443; every other port is untouched (base chain policy  │
// │     is accept, so anything not explicitly matched passes through).    │
// │                                                                       │
// │  3. EMPTY/STALE CIDRs = REFUSE, NOT BLOCK-ALL. BuildNFTRuleset returns │
// │     ErrEmptyCIDRs rather than a ruleset if there are zero allowed      │
// │     CIDRs — the natural bug here is "empty allowlist -> drop           │
// │     everything on 80/443", which would be a self-inflicted outage.    │
// │     Better exposed (fail open) than locked out or dark.               │
// │                                                                       │
// │  4. SCOPED, REVERSIBLE. The ruleset lives entirely in its own nftables │
// │     table (VulosCDNTable/VulosCDNChain) so disabling it can flush      │
// │     JUST that table and nothing else the owner (or a future feature)   │
// │     might also have configured.                                       │
// │                                                                       │
// │  5. LIVE ENFORCEMENT IS DEFERRED IN THIS BUILD. Applier is an          │
// │     interface; the only implementation wired into                     │
// │     cmd/server/routes_cdn.go is DryRunApplier, which never shells out  │
// │     to `nft` or touches the box's real packet filter — it only         │
// │     records the ruleset BuildNFTRuleset generated. "Enabling" the      │
// │     firewall in this build computes and stores that preview; it does   │
// │     NOT install it. A real nft-invoking Applier can be added later     │
// │     behind this exact interface once it has been exercised against    │
// │     real hardware by a human, not shipped sight-unseen in this fold.   │
// └───────────────────────────────────────────────────────────────────────┘
package cdn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultSSHPort is applied when Config.SSHPort is unset (0).
const DefaultSSHPort = 22

// VulosCDNTable / VulosCDNChain name the dedicated nftables table+chain the
// generated ruleset lives in. A real Applier's Disable must flush/delete
// ONLY this table — never `nft flush ruleset` or anything table-scoped to
// something else the box (or the owner) may also be running.
const (
	VulosCDNTable = "inet vulos_cdn"
	VulosCDNChain = "input"
)

// ErrEmptyCIDRs is returned by BuildNFTRuleset when there are zero allowed
// CIDRs to build an allowlist from. This is a REFUSAL, not a "block
// everything" ruleset — see the fail-open safety note in the package doc.
var ErrEmptyCIDRs = errors.New("cdn: refusing to generate a blocking ruleset — the CDN CIDR allowlist is empty (fail open: fetch ranges first)")

// RulesetSpec is the fully-resolved input to BuildNFTRuleset: a CDN
// provider's CIDRs plus the owner's always-allow safety inputs. Build one
// via ResolveRulesetSpec(cfg, ranges) rather than constructing it by hand,
// so the SSHPort default and dedup/sort logic stay in one place.
type RulesetSpec struct {
	Provider   Provider
	CIDRs      []string // validated, deduped CDN egress CIDRs to allow on 80/443
	SSHPort    int      // always allowed; never 0 here (ResolveRulesetSpec applies DefaultSSHPort)
	ExtraPorts []int    // additional always-allow ports, owner-declared
}

// ResolveRulesetSpec builds a RulesetSpec from a Config and its provider's
// currently cached IPRanges. It does not consult the network — callers
// fetch/refresh ranges separately (RangeFetcher) and pass what's cached.
func ResolveRulesetSpec(cfg Config, ranges []IPRange) RulesetSpec {
	sshPort := cfg.SSHPort
	if sshPort <= 0 {
		sshPort = DefaultSSHPort
	}
	cidrs := make([]string, 0, len(ranges))
	for _, r := range ranges {
		cidrs = append(cidrs, r.CIDR)
	}
	return RulesetSpec{
		Provider:   cfg.Provider,
		CIDRs:      dedupSortedValidCIDRs(cidrs),
		SSHPort:    sshPort,
		ExtraPorts: cfg.ExtraAllowPorts,
	}
}

func dedupSortedValidCIDRs(cidrs []string) []string {
	seen := make(map[string]bool, len(cidrs))
	out := make([]string, 0, len(cidrs))
	for _, c := range sortCIDRs(filterValidCIDRs(cidrs)) {
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// BuildNFTRuleset generates the nftables script text for spec. It NEVER
// produces a ruleset that could drop the owner's own access:
//
//   - Returns ErrEmptyCIDRs (refuses) if spec.CIDRs is empty — see the
//     fail-open note in the package doc.
//   - Always allows loopback, established/related, spec.SSHPort, and every
//     port in spec.ExtraPorts — unconditionally, before any CDN-CIDR
//     matching is even considered.
//   - Only ever restricts inbound tcp/80 and tcp/443, and ONLY to
//     spec.CIDRs; the base chain policy is `accept`, so every other port on
//     the box is completely untouched by this ruleset.
//   - Confined to its own table (VulosCDNTable) so Disable can flush just
//     this table (see DryRunApplier.Disable / a future real Applier).
func BuildNFTRuleset(spec RulesetSpec) (string, error) {
	if len(spec.CIDRs) == 0 {
		return "", ErrEmptyCIDRs
	}
	if bad := ValidateCIDRs(spec.CIDRs); len(bad) > 0 {
		return "", fmt.Errorf("cdn: %d invalid CIDR(s) in ruleset spec, refusing to build: %v", len(bad), bad)
	}
	sshPort := spec.SSHPort
	if sshPort <= 0 {
		sshPort = DefaultSSHPort
	}
	if err := validatePort(sshPort); err != nil {
		return "", fmt.Errorf("cdn: ssh port: %w", err)
	}
	extraPorts := dedupSortedInts(spec.ExtraPorts)
	for _, p := range extraPorts {
		if err := validatePort(p); err != nil {
			return "", fmt.Errorf("cdn: extra allow port: %w", err)
		}
	}

	v4, v6 := splitCIDRsByFamily(spec.CIDRs)

	var b strings.Builder
	fmt.Fprintf(&b, "# vulos CDN origin-firewall ruleset — GENERATED, DO NOT HAND-EDIT\n")
	fmt.Fprintf(&b, "# provider=%s cidrs=%d (v4=%d v6=%d) generated_at=%s\n",
		spec.Provider, len(spec.CIDRs), len(v4), len(v6), time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "# Safety: loopback + established/related + ssh(%d)", sshPort)
	if len(extraPorts) > 0 {
		fmt.Fprintf(&b, " + extra%v", extraPorts)
	}
	fmt.Fprintf(&b, " are ALWAYS allowed. Only tcp dport {80,443} is ever restricted, to the CIDRs below.\n")
	fmt.Fprintf(&b, "# Disabling this feature must delete/flush ONLY table %s — nothing else.\n\n", VulosCDNTable)

	fmt.Fprintf(&b, "table %s {\n", VulosCDNTable)
	fmt.Fprintf(&b, "\tchain %s {\n", VulosCDNChain)
	fmt.Fprintf(&b, "\t\ttype filter hook input priority filter; policy accept;\n\n")

	fmt.Fprintf(&b, "\t\t# Never lock the owner out.\n")
	fmt.Fprintf(&b, "\t\tiif lo accept\n")
	fmt.Fprintf(&b, "\t\tct state established,related accept\n")
	fmt.Fprintf(&b, "\t\ttcp dport %d accept comment \"ssh - always allowed\"\n", sshPort)
	if len(extraPorts) > 0 {
		fmt.Fprintf(&b, "\t\ttcp dport { %s } accept comment \"owner-declared admin ports - always allowed\"\n", joinInts(extraPorts))
	}
	fmt.Fprintf(&b, "\n\t\t# Restrict web ports to the %s CDN's published egress ranges only.\n", spec.Provider)
	if len(v4) > 0 {
		fmt.Fprintf(&b, "\t\tip saddr { %s } tcp dport { 80, 443 } accept\n", strings.Join(v4, ", "))
	}
	if len(v6) > 0 {
		fmt.Fprintf(&b, "\t\tip6 saddr { %s } tcp dport { 80, 443 } accept\n", strings.Join(v6, ", "))
	}
	fmt.Fprintf(&b, "\t\ttcp dport { 80, 443 } drop comment \"not a recognised %s CDN address\"\n", spec.Provider)
	fmt.Fprintf(&b, "\n\t\t# Every other port: falls through to the chain's accept policy above, untouched.\n")
	fmt.Fprintf(&b, "\t}\n")
	fmt.Fprintf(&b, "}\n")

	return b.String(), nil
}

// BuiltStatus is the computed, current firewall status the routes/panel
// surface — always recomputed from live Config + IPRanges rather than
// trusted from a stale stored flag, so a UI never shows "enabled" when the
// CIDR list backing it has since gone empty.
type BuiltStatus struct {
	Configured      bool      `json:"configured"`       // a CDN provider has been chosen
	FirewallEnabled bool      `json:"firewall_enabled"` // owner opt-in flag (stored)
	Provider        Provider  `json:"provider,omitempty"`
	CIDRCount       int       `json:"cidr_count"`
	RangesFetchedAt time.Time `json:"ranges_fetched_at,omitempty"`
	Ruleset         string    `json:"ruleset,omitempty"` // freshly generated preview; empty if it could not be built
	Warning         string    `json:"warning,omitempty"` // set whenever Ruleset is empty despite FirewallEnabled, or other actionable state
	LiveEnforcement bool      `json:"live_enforcement"`  // ALWAYS false in this build — see package doc
}

// BuildStatus computes the current firewall status for cfg from the CDN
// provider's cached ranges. Never errors: an unbuildable ruleset (no
// provider, no CIDRs, bad CIDRs) is reported via Warning, not a Go error,
// since "can't build a ruleset" is a normal, expected, and safe state (fail
// open), not a failure of this call.
func BuildStatus(cfg Config, ranges []IPRange) BuiltStatus {
	st := BuiltStatus{
		Configured:      cfg.IsConfigured(),
		FirewallEnabled: cfg.FirewallEnabled,
		Provider:        cfg.Provider,
		CIDRCount:       len(ranges),
		LiveEnforcement: false, // see package doc: deferred in this build
	}
	for _, r := range ranges {
		if r.FetchedAt.After(st.RangesFetchedAt) {
			st.RangesFetchedAt = r.FetchedAt
		}
	}
	if !cfg.IsConfigured() {
		st.Warning = "no CDN provider configured yet"
		return st
	}
	spec := ResolveRulesetSpec(cfg, ranges)
	ruleset, err := BuildNFTRuleset(spec)
	if err != nil {
		st.Warning = err.Error()
		return st
	}
	st.Ruleset = ruleset
	return st
}

// ─────────────────────────────────────────────────────────────────────────────
// Applier seam
// ─────────────────────────────────────────────────────────────────────────────

// Applier activates (or reverses) a generated ruleset. The ONLY
// implementation wired into cmd/server/routes_cdn.go in this build is
// DryRunApplier — see the package doc for why a real nft-invoking
// implementation is deferred rather than shipped in this fold.
type Applier interface {
	// Apply "activates" ruleset for the given provider. DryRunApplier only
	// records it; a real implementation would additionally run
	// `nft -f -` (or equivalent) against it.
	Apply(ctx context.Context, provider Provider, ruleset string) error
	// Disable reverses Apply. Must touch ONLY VulosCDNTable — never a
	// broader `nft flush ruleset` — so it can never interfere with
	// anything else that may be configured on the box's firewall.
	Disable(ctx context.Context) error
}

// DryRunApplier is the only Applier wired into the HTTP routes in this
// build. It never touches the box's actual packet filter — Apply/Disable
// only update Store's Config (FirewallEnabled/LastRuleset/LastRulesetAt),
// so the owner and the UI can see exactly what WOULD be installed, and
// audit when it was last (re)computed.
type DryRunApplier struct {
	Store Store
}

// compile-time assertion.
var _ Applier = (*DryRunApplier)(nil)

// Apply records ruleset as the box's current "would-apply" state. Does not
// invoke nft, iptables, or any other system firewall tool.
func (d *DryRunApplier) Apply(ctx context.Context, provider Provider, ruleset string) error {
	cfg, err := d.Store.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("cdn: dry-run apply: load config: %w", err)
	}
	cfg.FirewallEnabled = true
	cfg.LastRuleset = ruleset
	cfg.LastRulesetAt = time.Now().UTC()
	if err := d.Store.SetConfig(ctx, cfg); err != nil {
		return fmt.Errorf("cdn: dry-run apply: save config: %w", err)
	}
	return nil
}

// Disable clears the recorded "would-apply" state. Since DryRunApplier
// never touched a real firewall, there is nothing on the box to flush —
// this purely reverses the bookkeeping Apply performed.
func (d *DryRunApplier) Disable(ctx context.Context) error {
	cfg, err := d.Store.GetConfig(ctx)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return fmt.Errorf("cdn: dry-run disable: load config: %w", err)
	}
	cfg.FirewallEnabled = false
	if err := d.Store.SetConfig(ctx, cfg); err != nil {
		return fmt.Errorf("cdn: dry-run disable: save config: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// small local helpers
// ─────────────────────────────────────────────────────────────────────────────

func validatePort(p int) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("port %d out of range 1-65535", p)
	}
	return nil
}

func dedupSortedInts(in []int) []int {
	seen := make(map[int]bool, len(in))
	out := make([]int, 0, len(in))
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

func joinInts(in []int) string {
	parts := make([]string, len(in))
	for i, v := range in {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ", ")
}

// splitCIDRsByFamily separates already-validated CIDRs into IPv4 and IPv6
// buckets (nftables requires separate `ip saddr` / `ip6 saddr` matches).
func splitCIDRsByFamily(cidrs []string) (v4, v6 []string) {
	for _, c := range cidrs {
		ip, _, err := net.ParseCIDR(c)
		if err != nil {
			continue // already validated by caller; defensive only
		}
		if ip.To4() != nil {
			v4 = append(v4, c)
		} else {
			v6 = append(v6, c)
		}
	}
	return v4, v6
}
