package safedial

// policy_test.go — the peer dial grant: VULOS_PEER_ALLOW_CIDR and how it
// composes with the older VULOS_PEER_ALLOW_LAN.
//
// This file is INTERNAL to the package (the rest of the suite is
// safedial_test) because the three properties worth proving live below the
// exported surface: that a malformed entry produces an error rather than a
// silently-narrower grant, that an entry naming an always-denied range is
// refused at parse time, and that the explicit never-exempt block list agrees
// with the predicates it mirrors.
//
// Every test here has been shown able to FAIL — see the mutation log in the
// task report: the overlap check, the malformed-entry error, and the
// fail-closed invalid policy were each broken in a throwaway worktree and the
// corresponding test went red.

import (
	"net"
	"strings"
	"sync"
	"testing"
)

// resetPeerPolicy makes the once-per-process env read repeatable, so several
// grants can be exercised in one test binary.
func resetPeerPolicy() {
	peerPolicyOnce = sync.Once{}
	peerPolicyValue = Policy{}
	peerPolicyLines = nil
	peerPolicyErr = nil
}

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("bad test CIDR %q: %v", s, err)
	}
	return n
}

// ─── What the allowlist ACCEPTS ──────────────────────────────────────────────

func TestParsePeerAllowCIDR_Accepts(t *testing.T) {
	spec := "100.64.0.0/10, 192.168.100.0/24,fd12:3456::/32,10.99.0.0/16"
	got, err := ParsePeerAllowCIDR(spec)
	if err != nil {
		t.Fatalf("ParsePeerAllowCIDR(%q) = error %v, want it accepted", spec, err)
	}
	want := []string{"100.64.0.0/10", "192.168.100.0/24", "fd12:3456::/32", "10.99.0.0/16"}
	if len(got) != len(want) {
		t.Fatalf("parsed %d blocks, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].String() != w {
			t.Errorf("block %d = %s, want %s", i, got[i], w)
		}
	}
}

func TestParsePeerAllowCIDR_EmptySpecIsNoGrant(t *testing.T) {
	for _, spec := range []string{"", "   ", ",", " , ,"} {
		got, err := ParsePeerAllowCIDR(spec)
		if err != nil {
			t.Errorf("ParsePeerAllowCIDR(%q) = error %v, want no grant and no error", spec, err)
		}
		if len(got) != 0 {
			t.Errorf("ParsePeerAllowCIDR(%q) granted %v, want nothing", spec, got)
		}
	}
}

// ─── What the allowlist REFUSES ──────────────────────────────────────────────

// The load-bearing one. An operator (or an attacker who can set the box's
// environment, or a copy-pasted config) must not be able to name a range the
// deny-list holds unconditionally and have it honoured. Refusal must also SAY
// SO — a silently-dropped entry is a grant the operator believes they have.
func TestParsePeerAllowCIDR_RefusesAlwaysDeniedRanges(t *testing.T) {
	cases := []struct {
		spec    string
		collide string // the range the error must name
	}{
		{"169.254.0.0/16", "169.254.0.0/16"},     // link-local
		{"169.254.169.254/32", "169.254.0.0/16"}, // the cloud metadata address itself
		{"169.254.0.0/17", "169.254.0.0/16"},     // a slice of link-local
		{"127.0.0.0/8", "127.0.0.0/8"},           // loopback
		{"127.0.0.1/32", "127.0.0.0/8"},
		{"::1/128", "::1/128"},                       // loopback v6
		{"fe80::/10", "fe80::/10"},                   // link-local v6
		{"224.0.0.0/4", "224.0.0.0/4"},               // multicast
		{"239.1.2.0/24", "224.0.0.0/4"},              // inside multicast
		{"ff00::/8", "ff00::/8"},                     // multicast v6
		{"0.0.0.0/32", "0.0.0.0/32"},                 // unspecified
		{"192.0.2.0/24", "192.0.2.0/24"},             // TEST-NET-1
		{"198.18.0.0/15", "198.18.0.0/15"},           // benchmarking
		{"198.51.100.0/24", "198.51.100.0/24"},       // TEST-NET-2
		{"203.0.113.0/24", "203.0.113.0/24"},         // TEST-NET-3
		{"240.0.0.0/4", "240.0.0.0/4"},               // reserved
		{"255.255.255.255/32", "255.255.255.255/32"}, // broadcast
		{"192.0.0.0/24", "192.0.0.0/24"},             // IETF protocol assignments
		{"0.0.0.0/0", ""},                            // "everything" swallows several
		{"::/0", ""},                                 // ditto, v6
		{"128.0.0.0/1", ""},                          // half the internet, incl. link-local
	}
	for _, tc := range cases {
		got, err := ParsePeerAllowCIDR(tc.spec)
		if err == nil {
			t.Errorf("SECURITY: ParsePeerAllowCIDR(%q) was ACCEPTED (granted %v); "+
				"an always-denied range must never be grantable", tc.spec, got)
			continue
		}
		if got != nil {
			t.Errorf("ParsePeerAllowCIDR(%q) returned both an error and a grant %v", tc.spec, got)
		}
		if !strings.Contains(err.Error(), tc.spec) {
			t.Errorf("ParsePeerAllowCIDR(%q) error does not name the offending entry: %v", tc.spec, err)
		}
		if tc.collide != "" && !strings.Contains(err.Error(), tc.collide) {
			t.Errorf("ParsePeerAllowCIDR(%q) error does not name the range it collided with (%s): %v",
				tc.spec, tc.collide, err)
		}
	}
}

// A refusal must reject the WHOLE spec, not keep the entries it liked. A
// partially-applied grant is one nobody can reason about.
func TestParsePeerAllowCIDR_OneBadEntryRefusesTheWholeSpec(t *testing.T) {
	got, err := ParsePeerAllowCIDR("100.64.0.0/10,169.254.0.0/16,192.168.5.0/24")
	if err == nil {
		t.Fatalf("a spec containing an always-denied range was accepted, granting %v", got)
	}
	if got != nil {
		t.Errorf("a refused spec still granted %v — the grant must be all-or-nothing", got)
	}
}

func TestParsePeerAllowCIDR_RefusesMalformed(t *testing.T) {
	cases := []struct {
		spec string
		want string // a phrase the error must contain, so the operator can act
	}{
		{"not-a-cidr/24", "not a valid CIDR"},
		{"100.64.0.0/99", "not a valid CIDR"},
		{"100.64.0.0/-1", "not a valid CIDR"},
		{"100.64.0.1", "not a CIDR block"},   // bare IP: say to write /32
		{"tailscale", "not a CIDR block"},    // a name, not a range
		{"100.64.0.5/10", "host bits set"},   // reads as one host, means four million
		{"192.168.1.1/24", "host bits set"},  // the classic copy-paste
		{"fd00::1/8", "host bits set"},       // v6 form of the same mistake
		{"100.64.0.0/10 192.168.0.0/16", ""}, // space-separated, not comma
	}
	for _, tc := range cases {
		got, err := ParsePeerAllowCIDR(tc.spec)
		if err == nil {
			t.Errorf("ParsePeerAllowCIDR(%q) was ACCEPTED (granted %v); malformed input must fail closed",
				tc.spec, got)
			continue
		}
		if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParsePeerAllowCIDR(%q) error %q does not contain %q — the operator cannot act on it",
				tc.spec, err, tc.want)
		}
		if !strings.Contains(err.Error(), EnvPeerAllowCIDR) {
			t.Errorf("ParsePeerAllowCIDR(%q) error does not name %s: %v", tc.spec, EnvPeerAllowCIDR, err)
		}
	}
}

// A host-bits error must show the operator BOTH readings, because the whole
// point is that the string is ambiguous to a human.
func TestParsePeerAllowCIDR_HostBitsErrorShowsBothReadings(t *testing.T) {
	_, err := ParsePeerAllowCIDR("100.64.0.5/10")
	if err == nil {
		t.Fatal("100.64.0.5/10 accepted")
	}
	for _, want := range []string{"100.64.0.0/10", "100.64.0.5/32"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("host-bits error %q does not offer %q", err, want)
		}
	}
}

// ─── The grant, applied ──────────────────────────────────────────────────────

// The whole reason this exists: opening the tailnet must NOT open the house.
func TestPolicy_CIDRGrantIsNarrow(t *testing.T) {
	p := Policy{AllowCIDR: []*net.IPNet{mustCIDR(t, "100.64.0.0/10")}}

	allowed := []string{"100.64.0.1", "100.100.100.100", "100.127.255.254"}
	for _, s := range allowed {
		if IsDeniedIPPolicy(net.ParseIP(s), p) {
			t.Errorf("%s denied under a 100.64.0.0/10 grant, want allowed", s)
		}
	}

	// Everything the coarse VULOS_PEER_ALLOW_LAN=1 would ALSO have opened, and
	// which this grant must leave shut.
	stillDenied := []string{
		"192.168.1.1",  // the home router
		"192.168.0.10", // a NAS
		"10.0.0.1",     // an internal service
		"10.200.0.1",   // appnet's own range
		"172.16.5.4",   // RFC1918 middle block
		"fd00::1",      // ULA
		"2002:c0a8:101::1",
		"64:ff9b::c0a8:101",
		// And the ranges no grant can ever open.
		"127.0.0.1",
		"169.254.169.254",
		"224.0.0.1",
		"0.0.0.0",
		"203.0.113.9",
	}
	for _, s := range stillDenied {
		if !IsDeniedIPPolicy(net.ParseIP(s), p) {
			t.Errorf("SECURITY: %s ALLOWED under a 100.64.0.0/10-only grant — the grant widened "+
				"beyond what was asked for", s)
		}
	}

	// A public address is unaffected either way.
	if IsDeniedIPPolicy(net.ParseIP("1.1.1.1"), p) {
		t.Error("1.1.1.1 denied under a CIDR grant; public addresses were always allowed")
	}
}

// A grant on an RFC1918 sub-range (Nebula, a wg mesh on 192.168.100/24) works
// too — the strict tier includes IsPrivate, so the allowlist has to override
// that as well as strictDeniedCIDRs.
func TestPolicy_CIDRGrantOverridesIsPrivate(t *testing.T) {
	p := Policy{AllowCIDR: []*net.IPNet{mustCIDR(t, "192.168.100.0/24")}}
	if IsDeniedIPPolicy(net.ParseIP("192.168.100.7"), p) {
		t.Error("192.168.100.7 denied under a 192.168.100.0/24 grant")
	}
	if !IsDeniedIPPolicy(net.ParseIP("192.168.101.7"), p) {
		t.Error("SECURITY: 192.168.101.7 allowed under a 192.168.100.0/24 grant")
	}
	if !IsDeniedIPPolicy(net.ParseIP("100.64.0.1"), p) {
		t.Error("SECURITY: CGNAT allowed under an RFC1918-only grant")
	}
}

// VULOS_PEER_ALLOW_LAN=1 must keep meaning exactly what it meant. This change
// is strictly narrowing; nobody's working setup may break.
func TestPolicy_AllowLANUnchanged(t *testing.T) {
	p := Policy{AllowLAN: true}
	for _, s := range []string{
		"192.168.1.1", "10.0.0.1", "172.16.5.4", "100.64.0.1", "fd00::1",
		"2002:c0a8:101::1", "64:ff9b::c0a8:101",
	} {
		if IsDeniedIPPolicy(net.ParseIP(s), p) {
			t.Errorf("REGRESSION: %s denied under %s=1, which used to allow it", s, EnvPeerAllowLAN)
		}
	}
	for _, s := range []string{"127.0.0.1", "169.254.169.254", "224.0.0.1", "203.0.113.9", "240.0.0.1"} {
		if !IsDeniedIPPolicy(net.ParseIP(s), p) {
			t.Errorf("SECURITY: %s allowed under %s=1", s, EnvPeerAllowLAN)
		}
	}
}

// Both set is a UNION, and because AllowLAN already covers the whole strict
// tier the union equals AllowLAN alone.
func TestPolicy_BothSetIsUnion(t *testing.T) {
	both := Policy{AllowLAN: true, AllowCIDR: []*net.IPNet{mustCIDR(t, "100.64.0.0/10")}}
	lan := Policy{AllowLAN: true}
	for _, s := range []string{
		"192.168.1.1", "10.0.0.1", "100.64.0.1", "fd00::1",
		"127.0.0.1", "169.254.169.254", "1.1.1.1", "240.0.0.1",
	} {
		ip := net.ParseIP(s)
		if got, want := IsDeniedIPPolicy(ip, both), IsDeniedIPPolicy(ip, lan); got != want {
			t.Errorf("%s: allowLAN+CIDR = %v, allowLAN alone = %v — the two must agree "+
				"(union, and allowLAN is already the broader one)", s, got, want)
		}
	}
}

// ─── Fail closed, loudly ─────────────────────────────────────────────────────

func TestBuildPeerPolicy_MalformedFailsClosedAndLoudly(t *testing.T) {
	p, lines, err := buildPeerPolicy("", "100.64.0.0/10,169.254.0.0/16")
	if err == nil {
		t.Fatal("SECURITY: a spec naming link-local built a policy with no error")
	}
	if !p.invalid {
		t.Fatal("a malformed grant did not mark the policy invalid")
	}
	// Fail CLOSED: not "back to the default grant" — nothing dials at all,
	// including addresses that would have been fine by default.
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "100.64.0.1", "192.168.1.1"} {
		if !IsDeniedIPPolicy(net.ParseIP(s), p) {
			t.Errorf("SECURITY: %s dialable under an INVALID policy; a grant that did not parse "+
				"must refuse everything", s)
		}
	}
	// LOUDLY: the printed grant has to say something is wrong, and why.
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "169.254.0.0/16") {
		t.Errorf("the reported grant does not name the offending entry:\n%s", joined)
	}
	if !strings.Contains(strings.ToUpper(joined), "REFUSING") {
		t.Errorf("the reported grant does not say dials are being refused:\n%s", joined)
	}
}

func TestEnsurePeerPolicy_ReturnsTheErrorAndPrintsTheGrant(t *testing.T) {
	resetPeerPolicy()
	t.Cleanup(resetPeerPolicy)
	t.Setenv(EnvPeerAllowLAN, "")
	t.Setenv(EnvPeerAllowCIDR, "127.0.0.0/8")

	var printed []string
	err := EnsurePeerPolicy(func(f string, a ...any) { printed = append(printed, f) })
	if err == nil {
		t.Fatal("EnsurePeerPolicy accepted a loopback grant; startup would have continued")
	}
	if len(printed) == 0 {
		t.Fatal("EnsurePeerPolicy printed nothing about a refused grant")
	}
	if !IsDeniedIPPeer(net.ParseIP("1.1.1.1")) {
		t.Error("SECURITY: a public peer dial succeeded under a grant that did not parse")
	}
}

// A nil logf must not suppress the error — a caller who does not want the
// banner still has to be told the grant is broken.
func TestEnsurePeerPolicy_NilLogfStillReturnsTheError(t *testing.T) {
	resetPeerPolicy()
	t.Cleanup(resetPeerPolicy)
	t.Setenv(EnvPeerAllowCIDR, "nonsense")
	if err := EnsurePeerPolicy(nil); err == nil {
		t.Fatal("EnsurePeerPolicy(nil) hid a malformed grant")
	}
}

// ─── The env seam ────────────────────────────────────────────────────────────

func TestPeerPolicy_FromEnv_NarrowGrant(t *testing.T) {
	resetPeerPolicy()
	t.Cleanup(resetPeerPolicy)
	t.Setenv(EnvPeerAllowLAN, "")
	t.Setenv(EnvPeerAllowCIDR, "100.64.0.0/10")

	if err := EnsurePeerPolicy(nil); err != nil {
		t.Fatalf("EnsurePeerPolicy: %v", err)
	}
	if IsDeniedIPPeer(net.ParseIP("100.101.102.103")) {
		t.Error("a tailnet peer was refused under VULOS_PEER_ALLOW_CIDR=100.64.0.0/10")
	}
	if !IsDeniedIPPeer(net.ParseIP("192.168.1.1")) {
		t.Error("SECURITY: the home LAN was opened by a CGNAT-only grant")
	}

	// The narrow grant is PEER-scoped: it must not widen the call sites that
	// ask for "public only" (webproxy, webhooks, appnet, push).
	if !IsDeniedIP(net.ParseIP("100.101.102.103"), false) {
		t.Error("SECURITY: VULOS_PEER_ALLOW_CIDR widened IsDeniedIP(ip, false), which webproxy " +
			"and the webhook guard rely on to mean 'public only'")
	}
}

func TestPeerPolicy_FromEnv_LegacyLANSpelling(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "Yes"} {
		resetPeerPolicy()
		t.Setenv(EnvPeerAllowLAN, v)
		t.Setenv(EnvPeerAllowCIDR, "")
		if err := EnsurePeerPolicy(nil); err != nil {
			t.Fatalf("%s=%q: %v", EnvPeerAllowLAN, v, err)
		}
		if IsDeniedIPPeer(net.ParseIP("192.168.1.1")) {
			t.Errorf("REGRESSION: %s=%q no longer opens the LAN", EnvPeerAllowLAN, v)
		}
	}
	resetPeerPolicy()
}

func TestPeerPolicy_FromEnv_DefaultDeniesPrivate(t *testing.T) {
	resetPeerPolicy()
	t.Cleanup(resetPeerPolicy)
	t.Setenv(EnvPeerAllowLAN, "")
	t.Setenv(EnvPeerAllowCIDR, "")
	if err := EnsurePeerPolicy(nil); err != nil {
		t.Fatalf("EnsurePeerPolicy: %v", err)
	}
	for _, s := range []string{"192.168.1.1", "10.0.0.1", "100.64.0.1", "fd00::1"} {
		if !IsDeniedIPPeer(net.ParseIP(s)) {
			t.Errorf("SECURITY: %s dialable with neither opt-in set", s)
		}
	}
}

// ─── The banner ──────────────────────────────────────────────────────────────

func TestPeerGrantLines_StateTheGrant(t *testing.T) {
	always := "169.254.169.254"

	def := peerGrantLines(Policy{})
	joinedDef := strings.Join(def, "\n")
	if !strings.Contains(joinedDef, "DEFAULT") || !strings.Contains(joinedDef, "100.64.0.0/10") {
		t.Errorf("the default banner does not say what is shut:\n%s", joinedDef)
	}
	if !strings.Contains(joinedDef, always) {
		t.Errorf("the banner never mentions the metadata address:\n%s", joinedDef)
	}

	cidr := strings.Join(peerGrantLines(Policy{AllowCIDR: []*net.IPNet{
		mustCIDR(t, "100.64.0.0/10"), mustCIDR(t, "192.168.9.0/24"),
	}}), "\n")
	for _, want := range []string{"100.64.0.0/10", "192.168.9.0/24", EnvPeerAllowCIDR} {
		if !strings.Contains(cidr, want) {
			t.Errorf("the CIDR banner does not name %s:\n%s", want, cidr)
		}
	}
	if !strings.Contains(cidr, "stays refused") {
		t.Errorf("the CIDR banner does not say the rest is still shut:\n%s", cidr)
	}

	lan := strings.Join(peerGrantLines(Policy{AllowLAN: true}), "\n")
	for _, want := range []string{EnvPeerAllowLAN, "WHOLE", "192.168.0.0/16", "10.0.0.0/8"} {
		if !strings.Contains(lan, want) {
			t.Errorf("the allowLAN banner does not name %s:\n%s", want, lan)
		}
	}

	// Both set: the operator must be told the narrow list is NOT what is in
	// force, or they will read the CIDR line and believe it is.
	both := strings.Join(peerGrantLines(Policy{
		AllowLAN:  true,
		AllowCIDR: []*net.IPNet{mustCIDR(t, "100.64.0.0/10")},
	}), "\n")
	if !strings.Contains(both, "NOTHING FURTHER") {
		t.Errorf("with both set, the banner does not say the CIDR list adds nothing:\n%s", both)
	}
}

// ─── The two declarations of "never exempt" ──────────────────────────────────

// neverExemptCIDRs exists so an allowlist entry can be refused at parse time;
// the PREDICATES in IsDeniedIPPolicy are what actually block at dial time. Two
// declarations of one fact is the shape this suite keeps finding defects in, so
// pin them: every never-exempt block must in fact be denied under the most
// permissive grant there is.
func TestNeverExemptMatchesAlwaysDeniedPredicates(t *testing.T) {
	if len(neverExemptCIDRs) == 0 {
		t.Fatal("neverExemptCIDRs is empty; the parse-time check would allow anything")
	}
	widest := Policy{AllowLAN: true, AllowCIDR: []*net.IPNet{mustCIDR(t, "0.0.0.0/1"), mustCIDR(t, "::/1")}}
	for _, block := range neverExemptCIDRs {
		for _, ip := range []net.IP{block.IP, lastIPOf(block)} {
			if !IsDeniedIPPolicy(ip, widest) {
				t.Errorf("SECURITY: %s (in never-exempt %s) is dialable under the widest grant — "+
					"the parse-time refusal and the dial-time predicates have drifted",
					ip, block)
			}
		}
	}
}

// The inverse: every strict range must be reachable by SOME grant, or the
// allowlist would be refusing entries it has no reason to refuse.
func TestStrictRangesAreGrantable(t *testing.T) {
	for _, block := range append([]*net.IPNet{}, strictDeniedCIDRs...) {
		spec := block.String()
		if _, err := ParsePeerAllowCIDR(spec); err != nil {
			t.Errorf("the strict range %s cannot be granted: %v — a range that is blocked by "+
				"default but ungrantable is a dead end for the operator", spec, err)
		}
	}
	for _, spec := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		if _, err := ParsePeerAllowCIDR(spec); err != nil {
			t.Errorf("RFC1918 range %s cannot be granted: %v", spec, err)
		}
	}
}

// lastIPOf returns the highest address in a block, so the tests probe both ends
// rather than only the base address.
func lastIPOf(n *net.IPNet) net.IP {
	ip := make(net.IP, len(n.IP))
	copy(ip, n.IP)
	for i := range ip {
		ip[i] |= ^n.Mask[i]
	}
	return ip
}

// ─── The dial path, end to end (simulated tailnet peer) ──────────────────────

// A MagicDNS name buys no exemption: safedial resolves and checks the IP, so
// "box1.tailnet.ts.net" is treated exactly like the 100.x it lands on. That is
// the property the tailnet configuration depends on being TRUE, not assumed.
func TestValidateHostPolicy_SimulatedTailnetPeer(t *testing.T) {
	const magicDNS = "box1.example-tailnet.ts.net"
	resolver := func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("100.101.102.103")}, nil
	}

	if _, err := ValidateHostWithResolverPolicy(magicDNS, Policy{}, resolver); err == nil {
		t.Error("a 100.64/10 MagicDNS name resolved under the DEFAULT grant; a name must not " +
			"exempt an address")
	}

	granted := Policy{AllowCIDR: []*net.IPNet{mustCIDR(t, "100.64.0.0/10")}}
	ip, err := ValidateHostWithResolverPolicy(magicDNS, granted, resolver)
	if err != nil {
		t.Fatalf("a tailnet peer was refused under a 100.64.0.0/10 grant: %v", err)
	}
	if !ip.Equal(net.ParseIP("100.101.102.103")) {
		t.Errorf("pinned IP = %s, want 100.101.102.103", ip)
	}

	// Multi-A: one tailnet address and one LAN address must still fail, because
	// the LAN address is not covered by the grant.
	mixed := func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("100.101.102.103"), net.ParseIP("192.168.1.1")}, nil
	}
	if _, err := ValidateHostWithResolverPolicy(magicDNS, granted, mixed); err == nil {
		t.Error("SECURITY: a name resolving to both a granted and an ungranted address was accepted")
	}
}

func TestControlFuncPolicy_TailnetGrant(t *testing.T) {
	ctl := ControlFuncPolicy(Policy{AllowCIDR: []*net.IPNet{mustCIDR(t, "100.64.0.0/10")}})
	if err := ctl("tcp4", "100.101.102.103:443", nil); err != nil {
		t.Errorf("dial to a granted tailnet address blocked: %v", err)
	}
	for _, addr := range []string{"192.168.1.1:443", "10.0.0.1:80", "127.0.0.1:8080", "169.254.169.254:80"} {
		if err := ctl("tcp4", addr, nil); err == nil {
			t.Errorf("SECURITY: dial to %s allowed under a 100.64.0.0/10-only grant", addr)
		}
	}
}
