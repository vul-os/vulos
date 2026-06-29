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
// # LAN opt-in
//
// Some Vulos deployments legitimately need peer-to-peer traffic over private
// networks (RFC1918, Tailscale/CGNAT).  Callers pass allowLAN=true to permit
// those ranges; loopback, link-local, multicast, and truly bogon ranges are
// always blocked regardless of allowLAN.
package safedial

import (
	"fmt"
	"net"
	"strings"
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
	alwaysDeniedCIDRs = mustParse(always, "always")
	strictDeniedCIDRs = mustParse(strict, "strict")
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
func IsDeniedIP(ip net.IP, allowLAN bool) bool {
	// Normalise IPv4-in-IPv6 (::ffff:x.x.x.x) so CIDR checks work uniformly.
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}

	// Always-blocked predicates.
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

	// Without the LAN opt-in, also block private and CGNAT ranges.
	if !allowLAN {
		// IsPrivate covers RFC1918 (10/8, 172.16/12, 192.168/16) + ULA (fc00::/7).
		if ip.IsPrivate() {
			return true
		}
		for _, cidr := range strictDeniedCIDRs {
			if cidr.Contains(ip) {
				return true
			}
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
	// Quick-block well-known hostnames before DNS.
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || lower == "broadcasthost" {
		return nil, fmt.Errorf("safedial: hostname %q is blocked", host)
	}

	// If host is an IP literal, validate directly without DNS — also prevents
	// the resolver from being called for obfuscated IP forms.
	if ip := NormalizeIPLiteral(host); ip != nil {
		if IsDeniedIP(ip, allowLAN) {
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
		if IsDeniedIP(ip, allowLAN) {
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
	return func(network, addr string, c syscall.RawConn) error {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("safedial: parse dial addr %q: %w", addr, err)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("safedial: cannot parse resolved IP %q from dial addr", host)
		}
		if IsDeniedIP(ip, allowLAN) {
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
	return &net.Dialer{Control: ControlFunc(allowLAN)}
}
