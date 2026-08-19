// Package safedial provides a canonical SSRF-safe dialer and address validator
// for use across all Vulos OS services.
//
// # Problem
//
// Multiple OS services (webproxy, files transport, stream/VNC) previously
// maintained separate copies of the same private-IP deny-list and obfuscated
// IP-literal parser.  Any divergence created silent security gaps.
//
// # Design
//
// The package provides two complementary layers of SSRF defence:
//
//  1. Pre-dial validation (ValidateHost / ValidateHostWithResolver): resolve the
//     hostname ONCE, check ALL returned IPs, and return the first safe IP for
//     use as a "pinned" dial target.  Using the pinned IP prevents a second DNS
//     lookup at dial time (closes the TOCTOU / DNS-rebinding window).
//
//  2. Dial-time validation (ControlFunc / New): a net.Dialer Control hook that
//     validates the IP the OS resolver hands to the kernel right before the
//     connect(2) syscall.  This defends against DNS-rebinding attacks and
//     HTTP redirects that the pre-dial check cannot see, by checking the actual
//     IP that will be dialled.
//
// Both layers share the same deny-list via IsDeniedIP, so a change to blocked
// ranges is applied in one place.
//
// # LAN opt-in, and the narrower CIDR grant
//
// Some Vulos deployments legitimately need peer-to-peer traffic over private
// networks (RFC1918, Tailscale/CGNAT).  Callers pass allowLAN=true to permit
// those ranges; loopback, link-local, multicast, and truly bogon ranges are
// always blocked regardless of allowLAN.
//
// allowLAN is deliberately coarse: it opens RFC1918, CGNAT, ULA, 6to4 and
// NAT64 in one motion.  Policy and VULOS_PEER_ALLOW_CIDR (see the "Peer dial
// grant" section further down) express the same opt-in as a NAMED LIST of
// ranges, so a box that meets its siblings on a 100.64.0.0/10 tailnet does not
// also have to re-open 192.168.0.0/16 to the peering dialer.  The two compose
// as a union and neither can reach an always-denied range.
package safedial

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
)

// ─── CIDR deny-lists ─────────────────────────────────────────────────────────

// alwaysDeniedCIDRs are blocked regardless of the allowLAN flag.
var alwaysDeniedCIDRs []*net.IPNet

// strictDeniedCIDRs are blocked when allowLAN=false (the default safe mode).
// When allowLAN=true these ranges are skipped so that LAN peer-share,
// Tailscale meshes, and ULA-addressed home networks can be reached.
var strictDeniedCIDRs []*net.IPNet

func init() {
	always := []string{
		// IPv4 bogons / documentation ranges always denied
		"192.0.0.0/24",       // IETF Protocol Assignments (RFC 6890)
		"192.0.2.0/24",       // TEST-NET-1 (RFC 5737)
		"198.18.0.0/15",      // Benchmarking (RFC 2544)
		"198.51.100.0/24",    // TEST-NET-2 (RFC 5737)
		"203.0.113.0/24",     // TEST-NET-3 (RFC 5737)
		"240.0.0.0/4",        // Reserved (RFC 1112)
		"255.255.255.255/32", // Broadcast
		// IPv6 — belt-and-suspenders (IsMulticast covers ff00::/8 already)
		"ff00::/8", // IPv6 multicast
	}
	strict := []string{
		// Blocked without VULOS_PEER_ALLOW_LAN; may be legitimate in self-hosted
		// deployments on private networks.
		"100.64.0.0/10", // CGNAT (RFC 6598) — also used by Tailscale
		"fc00::/7",      // Unique-local (ULA) IPv6 (also covered by IsPrivate)
		"2002::/16",     // 6to4 (can encapsulate private IPv4)
		"64:ff9b::/96",  // NAT64 well-known prefix
	}

	mustParse := func(blocks []string, tag string) []*net.IPNet {
		out := make([]*net.IPNet, 0, len(blocks))
		for _, b := range blocks {
			_, cidr, err := net.ParseCIDR(b)
			if err != nil {
				panic(fmt.Sprintf("safedial: bad CIDR %s (%s): %v", b, tag, err))
			}
			out = append(out, cidr)
		}
		return out
	}
	// neverExempt states, as explicit blocks, the ranges the always-denied
	// PREDICATES in IsDeniedIPPolicy cover.  They exist so an allowlist entry
	// naming one can be refused at PARSE time — with the operator still looking
	// at the error — rather than being accepted and then quietly ignored at
	// dial time.  TestNeverExemptMatchesAlwaysDeniedPredicates pins these to
	// the predicates so the two declarations cannot drift.
	neverExempt := []string{
		"0.0.0.0/32",     // unspecified (IPv4)
		"::/128",         // unspecified (IPv6)
		"127.0.0.0/8",    // loopback (IPv4)
		"::1/128",        // loopback (IPv6)
		"169.254.0.0/16", // link-local — the cloud metadata address lives here
		"fe80::/10",      // link-local (IPv6)
		"224.0.0.0/4",    // multicast (IPv4)
		"ff00::/8",       // multicast (IPv6)
	}

	alwaysDeniedCIDRs = mustParse(always, "always")
	strictDeniedCIDRs = mustParse(strict, "strict")
	neverExemptCIDRs = mustParse(neverExempt, "never-exempt")
}

// ─── Core IP check ───────────────────────────────────────────────────────────

// IsDeniedIP reports whether ip falls in a blocked range.
//
// When allowLAN is false (the default safe mode), private ranges (RFC1918),
// CGNAT (100.64/10), ULA, 6to4, and NAT64 prefixes are also blocked.
//
// When allowLAN is true, only truly dangerous ranges are blocked:
// loopback, link-local, multicast, unspecified, and hard bogons.  Private
// RFC1918 and CGNAT/Tailscale addresses are permitted so that legitimate
// LAN peer-share works (VULOS_PEER_ALLOW_LAN=1).
//
// Either way, loopback, link-local, multicast, and unspecified are always
// blocked because no legitimate outbound target lives there.
//
// This is the boolean form, kept because most call sites want exactly
// "public only" (allowLAN=false) and nothing else.  Call sites that honour the
// operator's peer grant use IsDeniedIPPolicy / IsDeniedIPPeer instead.
func IsDeniedIP(ip net.IP, allowLAN bool) bool {
	return IsDeniedIPPolicy(ip, Policy{AllowLAN: allowLAN})
}

// IsDeniedIPPeer applies the process-wide peer dial grant (VULOS_PEER_ALLOW_LAN
// and VULOS_PEER_ALLOW_CIDR) to ip.
func IsDeniedIPPeer(ip net.IP) bool { return IsDeniedIPPolicy(ip, PeerPolicy()) }

// IsDeniedIPPolicy reports whether ip falls in a range blocked under p.
//
// Order matters and is load-bearing:
//
//  1. An INVALID policy denies everything.  Malformed configuration must not
//     fall through to some other grant.
//  2. The always-denied predicates and CIDRs are checked BEFORE any grant is
//     consulted, so neither AllowLAN nor AllowCIDR can ever reach loopback,
//     link-local (169.254.169.254), multicast, unspecified, or a bogon.
//  3. Only then does the grant apply, skipping the strict tier for addresses it
//     covers.
func IsDeniedIPPolicy(ip net.IP, p Policy) bool {
	// A grant that could not be parsed denies every address.  Failing closed
	// here is what makes buildPeerPolicy's error safe to ignore.
	if p.invalid {
		return true
	}

	// Normalise IPv4-in-IPv6 (::ffff:x.x.x.x) so CIDR checks work uniformly.
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}

	// Always-blocked predicates.  Checked before the grant, so no allowlist
	// entry and no allowLAN=true can reach them.
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}

	// Always-blocked CIDRs (TEST-NETs, reserved, broadcast, IPv6 multicast).
	for _, cidr := range alwaysDeniedCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}

	// The grant. AllowLAN skips the whole strict tier; AllowCIDR skips it only
	// for addresses inside a listed range. Both set is a union, which AllowLAN
	// already subsumes.
	if p.AllowLAN {
		return false
	}
	for _, cidr := range p.AllowCIDR {
		if cidr.Contains(ip) {
			return false
		}
	}

	// Strict tier: private and CGNAT ranges, denied without a grant covering
	// this specific address.
	//
	// IsPrivate covers RFC1918 (10/8, 172.16/12, 192.168/16) + ULA (fc00::/7).
	if ip.IsPrivate() {
		return true
	}
	for _, cidr := range strictDeniedCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ─── IP-literal normalisation ─────────────────────────────────────────────────

// NormalizeIPLiteral attempts to parse host as a bare IP literal.
// It handles:
//   - Standard dotted-decimal IPv4 (192.168.1.1)
//   - Standard IPv6 ([::1] bracket form or ::1)
//   - Decimal-encoded IPv4 (2130706433 → 127.0.0.1)
//   - Hex-encoded IPv4 (0x7f000001 → 127.0.0.1)
//   - Octal-prefixed octets (0177.0.0.1 → 127.0.0.1)
//   - Mixed 2-part and 3-part POSIX forms
//   - IPv4-mapped IPv6 (::ffff:127.0.0.1)
//
// Returns nil if host is not recognisable as an IP literal.
func NormalizeIPLiteral(host string) net.IP {
	h := host
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip
	}
	// Attempt decimal/hex/octal IPv4 — only for non-IPv6 strings.
	if !strings.Contains(h, ":") {
		if ip := ParseAltIPv4(h); ip != nil {
			return ip
		}
	}
	return nil
}

// ParseAltIPv4 parses non-standard IPv4 representations:
//   - Pure decimal integer (2130706433 → 127.0.0.1)
//   - Pure hex (0x7f000001 → 127.0.0.1)
//   - Octal-prefixed octets (0177.0.0.1 → 127.0.0.1)
//   - Mixed 2-part and 3-part POSIX forms (each part in any base)
//
// Returns nil if h cannot be interpreted as an IPv4 address in any of
// the above forms.
func ParseAltIPv4(h string) net.IP {
	parts := strings.Split(h, ".")
	if len(parts) > 4 {
		return nil
	}

	var octets [4]byte
	switch len(parts) {
	case 1:
		// Single numeric form: full 32-bit address.
		n, ok := ParseUint64(parts[0])
		if !ok || n > 0xFFFFFFFF {
			return nil
		}
		octets[0], octets[1], octets[2], octets[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
	case 4:
		// Four octets, each possibly in hex/octal/decimal.
		for i, p := range parts {
			n, ok := ParseUint64(p)
			if !ok || n > 255 {
				return nil
			}
			octets[i] = byte(n)
		}
	case 2:
		// First octet + remaining 24-bit value.
		n0, ok0 := ParseUint64(parts[0])
		n1, ok1 := ParseUint64(parts[1])
		if !ok0 || !ok1 || n0 > 255 || n1 > 0xFFFFFF {
			return nil
		}
		octets[0], octets[1], octets[2], octets[3] = byte(n0), byte(n1>>16), byte(n1>>8), byte(n1)
	case 3:
		// Two octets + remaining 16-bit value.
		n0, ok0 := ParseUint64(parts[0])
		n1, ok1 := ParseUint64(parts[1])
		n2, ok2 := ParseUint64(parts[2])
		if !ok0 || !ok1 || !ok2 || n0 > 255 || n1 > 255 || n2 > 0xFFFF {
			return nil
		}
		octets[0], octets[1], octets[2], octets[3] = byte(n0), byte(n1), byte(n2>>8), byte(n2)
	default:
		return nil
	}
	return net.IPv4(octets[0], octets[1], octets[2], octets[3])
}

// ParseUint64 parses s as a decimal, hex (0x/0X prefix), or octal (leading 0)
// unsigned integer.  Returns (0, false) on any parse error.
func ParseUint64(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return parseDigits(s[2:], 16)
	}
	if len(s) > 1 && s[0] == '0' {
		return parseDigits(s[1:], 8)
	}
	return parseDigits(s, 10)
}

// parseDigits parses s as an unsigned integer in the given base (8, 10, or 16).
func parseDigits(s string, base uint64) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	var n uint64
	for _, c := range s {
		var d uint64
		switch {
		case c >= '0' && c <= '9':
			d = uint64(c - '0')
		case c >= 'a' && c <= 'f' && base == 16:
			d = uint64(c-'a') + 10
		case c >= 'A' && c <= 'F' && base == 16:
			d = uint64(c-'A') + 10
		default:
			return 0, false
		}
		if d >= base {
			return 0, false
		}
		n = n*base + d
	}
	return n, true
}

// ─── Host/URL validation ──────────────────────────────────────────────────────

// defaultResolveHost resolves hostname to IPs using the system resolver.
func defaultResolveHost(host string) ([]net.IP, error) {
	addrs, err := net.LookupHost(host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil {
			ips = append(ips, ip)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no valid IPs for %s", host)
	}
	return ips, nil
}

// ValidateHostWithResolver resolves host using the provided resolver, validates
// ALL returned IPs against the deny-list, and returns the first safe IP to use
// as a pinned dial target.  Fails closed on any error (resolution failure, zero
// IPs, or any denied IP).
//
// This variant is intended for callers that need to inject a mock resolver in
// tests.  For production code, call ValidateHost, which uses net.LookupHost.
//
// Callers that want to avoid a second DNS resolution at dial time should pin
// the returned IP using a DialContext that connects to the IP directly.
func ValidateHostWithResolver(host string, allowLAN bool, resolveHost func(string) ([]net.IP, error)) (net.IP, error) {
	return ValidateHostWithResolverPolicy(host, Policy{AllowLAN: allowLAN}, resolveHost)
}

// ValidateHostWithResolverPolicy is ValidateHostWithResolver under an explicit
// Policy, so a caller can honour the operator's peer grant
// (VULOS_PEER_ALLOW_CIDR) instead of only the coarse allowLAN boolean.
func ValidateHostWithResolverPolicy(host string, p Policy, resolveHost func(string) ([]net.IP, error)) (net.IP, error) {
	// Quick-block well-known hostnames before DNS.
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || lower == "broadcasthost" {
		return nil, fmt.Errorf("safedial: hostname %q is blocked", host)
	}

	// If host is an IP literal, validate directly without DNS — also prevents
	// the resolver from being called for obfuscated IP forms.
	if ip := NormalizeIPLiteral(host); ip != nil {
		if IsDeniedIPPolicy(ip, p) {
			return nil, fmt.Errorf("safedial: IP %s is in a blocked range", ip)
		}
		return ip, nil
	}

	// Single DNS resolution.  Fail closed on any error.
	ips, err := resolveHost(host)
	if err != nil {
		return nil, fmt.Errorf("safedial: resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("safedial: no IPs for %q", host)
	}

	// Every resolved IP must pass the deny-list (prevents multi-A DNS tricks
	// where one of several returned IPs is private).
	for _, ip := range ips {
		if IsDeniedIPPolicy(ip, p) {
			return nil, fmt.Errorf("safedial: resolved IP %s for %q is in a blocked range", ip, host)
		}
	}

	// Return the first safe IP as the pinned dial target.
	return ips[0], nil
}

// ValidateHost resolves host with net.LookupHost, validates all returned IPs,
// and returns the first safe IP.  Fails closed on any error.
func ValidateHost(host string, allowLAN bool) (net.IP, error) {
	return ValidateHostWithResolver(host, allowLAN, defaultResolveHost)
}

// ValidateHostPolicy is ValidateHost under an explicit Policy.
func ValidateHostPolicy(host string, p Policy) (net.IP, error) {
	return ValidateHostWithResolverPolicy(host, p, defaultResolveHost)
}

// ValidateHostPeer is ValidateHost under the process-wide peer dial grant
// (VULOS_PEER_ALLOW_LAN + VULOS_PEER_ALLOW_CIDR).  This is the pre-dial check
// for box-to-box traffic: a peer address on an operator-listed tailnet range is
// accepted, every other private address is not.
func ValidateHostPeer(host string) (net.IP, error) {
	return ValidateHostWithResolverPolicy(host, PeerPolicy(), defaultResolveHost)
}

// ─── Dialer ───────────────────────────────────────────────────────────────────

// ControlFunc returns a net.Dialer.Control hook that validates the resolved IP
// at dial time, defeating DNS-rebinding and HTTP-redirect attacks where the
// resolved IP could differ from what a pre-dial ValidateHost call observed.
//
// The hook receives the actual IP the OS resolved immediately before the kernel
// connect(2) call.  If that IP is in a blocked range, the connection is
// refused.
//
// When allowLAN is true, private and CGNAT ranges are permitted.
func ControlFunc(allowLAN bool) func(network, addr string, c syscall.RawConn) error {
	return ControlFuncPolicy(Policy{AllowLAN: allowLAN})
}

// ControlFuncPolicy is ControlFunc under an explicit Policy.
func ControlFuncPolicy(p Policy) func(network, addr string, c syscall.RawConn) error {
	return func(network, addr string, c syscall.RawConn) error {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("safedial: parse dial addr %q: %w", addr, err)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("safedial: cannot parse resolved IP %q from dial addr", host)
		}
		if IsDeniedIPPolicy(ip, p) {
			return fmt.Errorf("safedial: connection to %s blocked (SSRF guard)", ip)
		}
		return nil
	}
}

// New returns a *net.Dialer configured with a Control hook (via ControlFunc)
// that validates the resolved IP at dial time.  All other Dialer settings use
// their zero values; callers may adjust fields (e.g. Timeout) after calling New.
//
// Usage:
//
//	d := safedial.New(false)          // strict: no private IPs
//	d.Timeout = 10 * time.Second
//	conn, err := d.DialContext(ctx, "tcp", "host:443")
//
// For paths that also need a pre-validated pinned IP (e.g. when building a
// custom http.Transport), call ValidateHost / ValidateHostWithResolver first,
// then dial the returned IP directly.
func New(allowLAN bool) *net.Dialer {
	return NewWithPolicy(Policy{AllowLAN: allowLAN})
}

// NewWithPolicy is New under an explicit Policy.
func NewWithPolicy(p Policy) *net.Dialer {
	return &net.Dialer{Control: ControlFuncPolicy(p)}
}

// NewPeer returns a dialer under the process-wide peer dial grant
// (VULOS_PEER_ALLOW_LAN + VULOS_PEER_ALLOW_CIDR).  Use it for box-to-box
// traffic — rendezvous, reachability resolve, envelope delivery — so an
// operator who opened exactly their tailnet range gets exactly that.
func NewPeer() *net.Dialer { return NewWithPolicy(PeerPolicy()) }

// ─── Peer dial grant: Policy, VULOS_PEER_ALLOW_LAN, VULOS_PEER_ALLOW_CIDR ────
//
// # Why a CIDR list and not another boolean
//
// VULOS_PEER_ALLOW_LAN=1 is ONE switch that opens the WHOLE strict tier:
// RFC1918 (10/8, 172.16/12, 192.168/16), CGNAT (100.64/10), ULA, 6to4 and
// NAT64, all at once.  An operator whose boxes find each other over a
// Tailscale/Headscale tailnet needs exactly 100.64.0.0/10 — but to get it they
// had to re-open their entire home LAN to the peering dialer, so a hostile or
// compromised peer could induce the box to dial the router on 192.168.1.1 or an
// internal service on 10.x.
//
// VULOS_PEER_ALLOW_CIDR names the ranges instead.  It generalises (Headscale
// and Nebula run on operator-chosen ranges, not on 100.64/10), and the grant is
// auditable: it is a list you can read, not a boolean whose meaning you have to
// look up.
//
// # How the two compose: UNION, and ALLOW_LAN is the strictly broader one
//
// Neither variable narrows the other — a box that already sets
// VULOS_PEER_ALLOW_LAN=1 keeps exactly the reach it had, which is the point:
// this change must never break a working deployment.  Because ALLOW_LAN=1
// already skips the entire strict tier, adding VULOS_PEER_ALLOW_CIDR on top of
// it grants nothing new, and the boot banner says so out loud rather than
// letting an operator believe the narrow list is what is in force.
//
// # What the grant can never reach
//
// alwaysDeniedCIDRs (TEST-NETs, benchmarking, reserved, broadcast, IPv6
// multicast) and the always-denied predicates (loopback, link-local — which is
// where 169.254.169.254 lives — multicast, unspecified) are OUTSIDE the grant
// entirely.  An allowlist entry that overlaps any of them is REFUSED at parse
// time and named in the error, rather than being silently dropped: an operator
// who wrote 169.254.0.0/16 must find out that it did nothing, not discover it
// later by not being attacked.
//
// # Malformed input fails closed AND loudly
//
// A malformed VULOS_PEER_ALLOW_CIDR yields an error from EnsurePeerPolicy —
// which the box and the relay call at startup, so the process refuses to run —
// and, on any path that somehow dialled without calling it, an invalid policy
// denies EVERY address rather than falling back to the default grant.  Both
// halves matter: silently widening would be a security hole, and silently
// narrowing would be a support ticket nobody can diagnose.

// Environment variable names, declared here so the code that reads them and the
// code that reports them cannot drift.
const (
	EnvPeerAllowLAN  = "VULOS_PEER_ALLOW_LAN"
	EnvPeerAllowCIDR = "VULOS_PEER_ALLOW_CIDR"
)

// Policy is the address-range grant a dial is made under.  The zero value is
// the default safe mode: no private, CGNAT, ULA, 6to4 or NAT64 address is
// dialable.
type Policy struct {
	// AllowLAN is the legacy coarse opt-in (VULOS_PEER_ALLOW_LAN=1).  It skips
	// the ENTIRE strict tier at once.
	AllowLAN bool

	// AllowCIDR is the surgical opt-in (VULOS_PEER_ALLOW_CIDR).  An address
	// inside one of these ranges skips the strict tier; every other private
	// address stays denied.  Entries can never name an always-denied range —
	// ParsePeerAllowCIDR refuses those outright.
	AllowCIDR []*net.IPNet

	// invalid marks a policy built from malformed configuration.  It is
	// unexported so it can only ever be set by this package's own loader, and
	// it makes IsDeniedIPPolicy deny EVERYTHING: a box whose grant could not be
	// parsed must not fall back to a grant nobody asked for, in either
	// direction.
	invalid bool
}

// neverExemptCIDRs are ranges no allowlist entry may name or overlap.  They
// mirror the always-denied predicates in IsDeniedIPPolicy (loopback,
// link-local, multicast, unspecified) as explicit blocks so an allowlist entry
// can be checked against them at PARSE time — where the operator is still
// looking at an error message — instead of only at dial time.
//
// A test asserts these agree with the predicates, so the two declarations of
// one fact cannot drift.
var neverExemptCIDRs []*net.IPNet

// ParsePeerAllowCIDR parses a comma-separated CIDR list into an allowlist.
//
// It refuses, rather than silently accepting:
//
//   - a bare IP with no prefix length ("100.64.0.1") — write "100.64.0.1/32";
//   - a block whose host bits are set ("100.64.0.5/10") — that string reads as
//     "just this host" to a human and means "four million hosts" to
//     net.ParseCIDR, so it is refused with its canonical form in the error;
//   - anything that overlaps an always-denied range (bogon, link-local,
//     loopback, multicast, unspecified), naming the range it collided with.
//
// Empty segments (a trailing comma, whitespace) are skipped: they are not a
// widening and not a typo worth failing a boot over.  An entirely empty spec
// yields a nil list and no error — that is simply "no grant".
func ParsePeerAllowCIDR(spec string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, raw := range strings.Split(spec, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			return nil, fmt.Errorf(
				"%s: %q is not a CIDR block — write a prefix length (%s/32 for one address)",
				EnvPeerAllowCIDR, entry, entry)
		}
		ip, block, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a valid CIDR block: %w", EnvPeerAllowCIDR, entry, err)
		}
		// Host bits set: net.ParseCIDR would silently hand back the WIDER
		// network the operator did not write.
		if !ip.Equal(block.IP) {
			return nil, fmt.Errorf(
				"%s: %q has host bits set — it means the whole of %s, not one address. "+
					"Write %s if that is what you meant, or %s/32 if it is not",
				EnvPeerAllowCIDR, entry, block.String(), block.String(), ip.String())
		}
		for _, denied := range append(append([]*net.IPNet{}, alwaysDeniedCIDRs...), neverExemptCIDRs...) {
			if cidrsOverlap(block, denied) {
				return nil, fmt.Errorf(
					"%s: %q overlaps %s, which is denied to every dial regardless of any grant "+
						"(bogon, loopback, link-local/metadata, multicast or unspecified). "+
						"Remove it — it can never be honoured",
					EnvPeerAllowCIDR, entry, denied.String())
			}
		}
		out = append(out, block)
	}
	return out, nil
}

// cidrsOverlap reports whether two CIDR blocks share any address.  One block
// overlaps another exactly when either contains the other's base address, since
// CIDR blocks are either disjoint or nested.
func cidrsOverlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

// parseBoolEnv matches the spelling the rest of the OS already accepts for
// VULOS_PEER_ALLOW_LAN (services/files/transport.go, cmd/server/main.go).
func parseBoolEnv(v string) bool {
	v = strings.TrimSpace(v)
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

// buildPeerPolicy turns the two raw environment values into a Policy plus the
// operator-facing lines describing it.  It takes strings rather than reading
// the environment so the grant logic is testable without process state.
//
// On error it returns an INVALID policy — one that denies every address — so a
// caller that ignores the error fails closed rather than silently reverting to
// the default grant.
func buildPeerPolicy(allowLANEnv, allowCIDREnv string) (Policy, []string, error) {
	allowLAN := parseBoolEnv(allowLANEnv)

	blocks, err := ParsePeerAllowCIDR(allowCIDREnv)
	if err != nil {
		return Policy{invalid: true}, []string{
			"peer dial grant: REFUSING EVERY PEER DIAL — " + err.Error(),
		}, err
	}

	p := Policy{AllowLAN: allowLAN, AllowCIDR: blocks}
	return p, peerGrantLines(p), nil
}

// peerGrantLines renders the effective grant.  A security decision states
// itself: a grant nobody can see is a grant nobody audits, so every boot prints
// exactly which ranges are open and which stay shut.
func peerGrantLines(p Policy) []string {
	if p.invalid {
		return []string{"peer dial grant: INVALID — every peer dial is refused"}
	}
	lines := []string{
		"peer dial grant: loopback, link-local (incl. the 169.254.169.254 metadata address), " +
			"multicast, unspecified and bogon ranges are ALWAYS refused, by every grant",
	}
	switch {
	case p.AllowLAN:
		lines = append(lines, fmt.Sprintf(
			"peer dial grant: %s=1 opens the WHOLE private tier — 10.0.0.0/8, 172.16.0.0/12, "+
				"192.168.0.0/16, 100.64.0.0/10 (CGNAT/Tailscale), fc00::/7 (ULA), 2002::/16 (6to4), "+
				"64:ff9b::/96 (NAT64)", EnvPeerAllowLAN))
		if len(p.AllowCIDR) > 0 {
			lines = append(lines, fmt.Sprintf(
				"peer dial grant: %s is set as well but grants NOTHING FURTHER — %s=1 is already "+
					"broader. Unset %s=1 to reduce the grant to the listed ranges alone",
				EnvPeerAllowCIDR, EnvPeerAllowLAN, EnvPeerAllowLAN))
		}
	case len(p.AllowCIDR) > 0:
		for _, b := range p.AllowCIDR {
			lines = append(lines, fmt.Sprintf(
				"peer dial grant: %s opens %s", EnvPeerAllowCIDR, b.String()))
		}
		lines = append(lines, fmt.Sprintf(
			"peer dial grant: every OTHER private address stays refused — 10.0.0.0/8, "+
				"172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, fc00::/7, 2002::/16, 64:ff9b::/96 "+
				"except where a %s entry above covers it", EnvPeerAllowCIDR))
	default:
		lines = append(lines, fmt.Sprintf(
			"peer dial grant: DEFAULT — no private address is dialable (10.0.0.0/8, 172.16.0.0/12, "+
				"192.168.0.0/16, 100.64.0.0/10, fc00::/7, 2002::/16, 64:ff9b::/96 all refused). "+
				"Boxes that meet over a tailnet or private mesh set %s=<range>",
			EnvPeerAllowCIDR))
	}
	return lines
}

// peerPolicy is the process-wide grant, resolved once from the environment.
var (
	peerPolicyOnce  sync.Once
	peerPolicyValue Policy
	peerPolicyLines []string
	peerPolicyErr   error
)

func loadPeerPolicy() {
	peerPolicyOnce.Do(func() {
		peerPolicyValue, peerPolicyLines, peerPolicyErr = buildPeerPolicy(
			os.Getenv(EnvPeerAllowLAN), os.Getenv(EnvPeerAllowCIDR))
	})
}

// PeerPolicy returns the process-wide peer dial grant, read once from
// VULOS_PEER_ALLOW_LAN and VULOS_PEER_ALLOW_CIDR.
//
// If the configuration was malformed the returned policy denies EVERY address.
// Callers that want the operator to see why must use EnsurePeerPolicy.
func PeerPolicy() Policy {
	loadPeerPolicy()
	return peerPolicyValue
}

// PeerGrantLines returns the operator-facing description of the effective
// grant, one line per fact.
func PeerGrantLines() []string {
	loadPeerPolicy()
	return append([]string(nil), peerPolicyLines...)
}

// EnsurePeerPolicy resolves the peer dial grant, prints it through logf, and
// returns an error if the configuration was malformed.
//
// Call it once at startup, BEFORE serving: a box or relay whose grant does not
// parse must refuse to start rather than run under a grant its operator did not
// write.  logf may be nil, in which case nothing is printed — but the error is
// still returned, so a caller can never end up with a silent bad grant.
func EnsurePeerPolicy(logf func(string, ...any)) error {
	loadPeerPolicy()
	if logf != nil {
		for _, line := range PeerGrantLines() {
			logf("[safedial] %s", line)
		}
	}
	return peerPolicyErr
}
