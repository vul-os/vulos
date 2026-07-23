// Package ddos provides DDoS defense middleware for the vulos.cloud control plane.
//
// All layers are Cloudflare-portable: they work without Cloudflare and integrate
// seamlessly when behind it via the VULOS_EDGE_TRUST_HEADER env var.
package ddos

import (
	"net"
	"net/http"
	"os"
	"strings"
)

// Env vars that control trusted-IP header behaviour.
const (
	envTrustHeader = "VULOS_EDGE_TRUST_HEADER" // e.g. "CF-Connecting-IP" or "X-Forwarded-For"
	envTrustCIDR   = "VULOS_EDGE_TRUST_CIDR"   // comma-separated CIDRs for upstream trust
)

// trustedCIDRs parses VULOS_EDGE_TRUST_CIDR once at call time.
func parseTrustedCIDRs() []*net.IPNet {
	raw := os.Getenv(envTrustCIDR)
	if raw == "" {
		return nil
	}
	var nets []*net.IPNet
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		_, n, err := net.ParseCIDR(s)
		if err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}

// upstreamIP returns the immediate upstream IP from r.RemoteAddr (without port).
func upstreamIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(strings.TrimSpace(host))
}

// inCIDRList reports whether ip is contained in any of the provided nets.
func inCIDRList(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// RealClientIP returns the canonical client IP for DDoS decisions.
//
// Resolution order:
//  1. If VULOS_EDGE_TRUST_HEADER is set AND (VULOS_EDGE_TRUST_CIDR is empty OR
//     the immediate upstream is within those CIDRs), read the first non-empty
//     value from the named header.
//  2. Fall back to r.RemoteAddr.
//
// This makes the function Cloudflare-portable: when VULOS_EDGE_TRUST_HEADER is
// unset, RemoteAddr is used directly (no Cloudflare, raw TCP). When set to
// "CF-Connecting-IP" with VULOS_EDGE_TRUST_CIDR listing Cloudflare ranges,
// only requests arriving from Cloudflare PoPs will have their header trusted.
//
// Fail-safe: when no trust header is configured (or its value is unusable) and
// the only thing we can see is a private/shared upstream RemoteAddr, we return
// "" (a non-matchable client id) rather than that shared IP. This degrades a
// missing/misconfigured trust header to "don't rate-limit/block anyone" instead
// of "block everyone behind the proxy" — the outage this repo actually hit.
func RealClientIP(r *http.Request) string {
	hdr := os.Getenv(envTrustHeader)
	if hdr == "" {
		return realClientFallback(r.RemoteAddr)
	}

	// Optional: only trust the header if the immediate upstream is in our CIDRs.
	trusted := parseTrustedCIDRs()
	if len(trusted) > 0 {
		upIP := upstreamIP(r)
		if upIP == nil || !inCIDRList(upIP, trusted) {
			// Upstream not in trusted range — ignore the header, use RemoteAddr.
			return realClientFallback(r.RemoteAddr)
		}
	}

	// Read the header. For X-Forwarded-For take only the leftmost value.
	val := r.Header.Get(hdr)
	if val == "" {
		return realClientFallback(r.RemoteAddr)
	}
	if strings.EqualFold(hdr, "x-forwarded-for") {
		parts := strings.SplitN(val, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	return strings.TrimSpace(val)
}

// realClientFallback returns the client id to use when no trusted edge header is
// available. It strips the port from RemoteAddr, but returns "" when the only
// visible address is a private/shared/unroutable upstream (a proxy, the internal
// fabric, or loopback). Rate-limit and blocklist decisions keyed on a shared
// upstream would punish EVERY real client behind it, so we return a
// non-matchable id and let those layers fail open instead.
func realClientFallback(addr string) string {
	ip := remoteAddrIP(addr)
	if ip == "" || IsUnblockableIP(ip) {
		return ""
	}
	return ip
}

// remoteAddrIP strips the port from an addr string, returning just the IP.
func remoteAddrIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
