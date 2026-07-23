package integrations

// ssrf.go — guards against server-side request forgery on user-supplied mail
// endpoints (audit M4). The CP dials IMAP/SMTP hosts and forwards CalDAV/CardDAV
// URLs that the user controls; without a deny-list an attacker could point them
// at internal services (RFC1918), loopback, link-local, or the cloud metadata
// endpoint (169.254.169.254) to exfiltrate credentials or pivot.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ErrBlockedHost is returned when a user-supplied host resolves to a
// non-routable / internal address.
var ErrBlockedHost = errors.New("integrations: host is not allowed (internal/loopback/link-local addresses are blocked)")

// cgnatIntegrations is the CGNAT shared-address range (RFC 6598, 100.64.0.0/10).
// L1 (audit): add CGNAT to the integrations deny-list to match webhooks/ssrf.go.
var cgnatIntegrations = mustParseCIDRIntegrations("100.64.0.0/10")

func mustParseCIDRIntegrations(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("integrations ssrf: " + err.Error())
	}
	return n
}

// isBlockedIP reports whether ip is in a range we must never dial on behalf of a
// user: loopback, RFC1918 private, link-local (incl. 169.254.169.254 metadata),
// CGNAT (100.64.0.0/10), unique-local IPv6, unspecified, or multicast.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	// L1 (audit): CGNAT 100.64.0.0/10 — Go's IsPrivate may or may not include
	// this depending on the version; check explicitly (mirrors webhooks/ssrf.go).
	if cgnatIntegrations.Contains(ip) {
		return true
	}
	// IPv6 unique-local (fc00::/7) — IsPrivate covers this on modern Go, but be
	// explicit for older semantics.
	if v6 := ip.To16(); v6 != nil && ip.To4() == nil {
		if v6[0]&0xfe == 0xfc {
			return true
		}
	}
	return false
}

// validateExternalHost resolves host and rejects it if it is empty or any
// resolved IP is internal/non-routable. An IP literal is checked directly.
func validateExternalHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("integrations: empty host")
	}
	// Strip an accidental port.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return ErrBlockedHost
		}
		return nil
	}
	// Resolve the hostname and reject if ANY answer is an internal address (this
	// catches both internal hostnames and hostnames that point at private space).
	// If the name does not resolve we deliberately do NOT block: there is no SSRF
	// value in an unresolvable host (the subsequent dial simply fails), and
	// blocking here would make the broker dependent on live DNS at validation
	// time. DNS-rebinding (TOCTOU) is a known residual limitation.
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return ErrBlockedHost
		}
	}
	return nil
}

// ssrfSafeTCPDial resolves host exactly once, validates ALL returned IPs
// against the deny-list, and dials the first approved IP directly (IP-pinning)
// on the given port. This prevents DNS-rebinding attacks: even if a hostname
// initially resolves to a public IP, a rebind returning a private IP at dial
// time is detected and rejected.
//
// SEC-H5 (audit): DialIMAP previously called validateExternalHost then called
// net.Dialer which re-resolved the hostname (TOCTOU / DNS-rebind window). This
// function replaces that two-step with a single resolve+validate+pin call.
func ssrfSafeTCPDial(ctx context.Context, host string, port int, timeout time.Duration) (net.Conn, string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, "", fmt.Errorf("integrations: empty host")
	}
	addr := fmt.Sprintf("%s:%d", host, port)

	// IP literal: validate and dial directly.
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return nil, "", ErrBlockedHost
		}
		conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
		return conn, host, err
	}

	// Hostname: resolve once, validate every answer, pin to first approved.
	resolver := net.DefaultResolver
	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, "", fmt.Errorf("integrations ssrf: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, "", fmt.Errorf("integrations ssrf: no addresses for %q", host)
	}
	for _, ia := range addrs {
		if isBlockedIP(ia.IP) {
			return nil, "", fmt.Errorf("%w: %s resolved to %s at dial time", ErrBlockedHost, host, ia.IP)
		}
	}
	// Pin to the first approved IP so no second DNS lookup occurs at dial time.
	pinned := net.JoinHostPort(addrs[0].IP.String(), fmt.Sprintf("%d", port))
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", pinned)
	// Return original host for TLS ServerName (not the pinned IP).
	return conn, host, err
}

// validateExternalURL parses rawURL, requires https (or http), and validates its
// host with validateExternalHost. Empty input is allowed (caller omits the URL).
func validateExternalURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("integrations: invalid URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("integrations: URL scheme must be http(s)")
	}
	return validateExternalHost(u.Hostname())
}
