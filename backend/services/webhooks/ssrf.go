// ssrf.go — SSRF guard for outbound webhook URLs.
//
// Two-layer protection against server-side request forgery:
//
//  1. Subscription-create validation: validateWebhookURL rejects URLs that point
//     at loopback, RFC1918 private space, CGNAT (100.64.0.0/10), link-local /
//     169.254.x.x metadata endpoints, IPv6 ULA, multicast, and non-http(s)
//     schemes. IP literals are checked directly; hostnames are resolved and every
//     returned address is checked so that an internal hostname cannot be used to
//     bypass the filter.
//
//  2. Dial-time IP pinning (DNS-rebind protection): ssrfSafeDialer returns an
//     *http.Client whose custom DialContext resolves the hostname exactly once,
//     validates ALL resolved addresses against the deny-list, and then dials the
//     FIRST approved address directly (IP-pinning). A rebind attack that swaps the
//     DNS answer between validation and dial is therefore blocked.
//
// Ported verbatim (logic unchanged) from management/pkg/webhooks/ssrf.go during
// the management -> vulos fold; this is the security-critical part of the
// feature and must not be weakened.
package webhooks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sentinel errors returned by the SSRF guard.
var (
	// ErrWebhookURLBlocked is returned when a webhook URL resolves to an
	// internal/reserved address (loopback, RFC1918, link-local, metadata,
	// CGNAT, ULA, multicast).
	ErrWebhookURLBlocked = errors.New("webhooks: URL resolves to a blocked address (internal/reserved ranges are not allowed)")
	// ErrWebhookURLInvalid is returned for a malformed or non-http(s) URL.
	ErrWebhookURLInvalid = errors.New("webhooks: URL must be an absolute http(s) URL with a resolvable public host")
)

// cgnat is the CGNAT shared-address range (RFC 6598, 100.64.0.0/10).
var cgnat = mustParseCIDR("100.64.0.0/10")

func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("webhooks ssrf: " + err.Error())
	}
	return n
}

// isBlockedIP reports whether ip is in a range that must never be dialled on
// behalf of the box owner. Covered ranges:
//
//   - Loopback (127.0.0.0/8, ::1)
//   - Link-local unicast/multicast (169.254.0.0/16, fe80::/10) — covers the
//     AWS/GCP/Azure metadata endpoint (169.254.169.254).
//   - RFC1918 private (10/8, 172.16/12, 192.168/16)
//   - IPv6 ULA (fc00::/7)
//   - CGNAT (100.64.0.0/10, RFC 6598)
//   - Multicast and unspecified addresses
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() ||
		ip.IsPrivate() {
		return true
	}
	// CGNAT 100.64.0.0/10 — Go's IsPrivate may or may not include this
	// depending on the version; check explicitly.
	if cgnat.Contains(ip) {
		return true
	}
	// IPv6 ULA fc00::/7 — IsPrivate() covers this on modern Go, but be
	// explicit for older Go semantics.
	if v6 := ip.To16(); v6 != nil && ip.To4() == nil {
		if v6[0]&0xfe == 0xfc {
			return true
		}
	}
	return false
}

// validateWebhookURL parses rawURL, requires an http or https scheme, a
// non-empty host, and verifies that every resolved IP of the host is outside
// all blocked ranges.
//
// An IP literal is checked directly without DNS. A hostname is resolved: if any
// returned address is blocked the URL is rejected. Unlike some SSRF guards we
// DO block on unresolvable hostnames — a webhook that can never be contacted
// is useless and is more likely to be a mis-typed internal name than a
// legitimate external endpoint.
func validateWebhookURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("%w: empty URL", ErrWebhookURLInvalid)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWebhookURLInvalid, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme must be http or https, got %q", ErrWebhookURLInvalid, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrWebhookURLInvalid)
	}

	// IP literal: check directly, no DNS.
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("%w: %s", ErrWebhookURLBlocked, host)
		}
		return nil
	}

	// Hostname: resolve and check every address.
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%w: host %q does not resolve: %v", ErrWebhookURLInvalid, host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: host %q returned no addresses", ErrWebhookURLInvalid, host)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("%w: %s resolves to %s", ErrWebhookURLBlocked, host, ip)
		}
	}
	return nil
}

// ValidatePublicURL is the exported form of validateWebhookURL: it accepts an
// absolute http(s) URL only when its host resolves entirely to public addresses
// (no loopback/RFC1918/CGNAT/link-local/metadata/ULA/multicast). Other backend
// packages that make outbound POSTs on behalf of the box owner may reuse this
// same guard so the SSRF policy lives in exactly one place. Returns
// ErrWebhookURLBlocked / ErrWebhookURLInvalid.
func ValidatePublicURL(rawURL string) error { return validateWebhookURL(rawURL) }

// SSRFSafeClient is the exported form of ssrfSafeDialer: an *http.Client whose
// dialer re-screens the RESOLVED IP at connect time (DNS-rebind guard) and pins
// to the first approved address. Reused by other backend packages that POST to
// an owner-influenced outbound URL. It does NOT follow redirects by default —
// the caller should set CheckRedirect to refuse them so a vendor 30x cannot
// bounce the request to an internal target after the parse-time screen.
func SSRFSafeClient() *http.Client { return ssrfSafeDialer() }

// ssrfSafeDialer returns an *http.Client whose DialContext resolves the target
// hostname exactly once, validates ALL returned IPs against the deny-list, and
// dials the first approved IP directly (IP-pinning).
//
// This prevents DNS-rebinding attacks: even if the box owner registers a
// hostname that initially resolves to a public IP, a re-keyed DNS answer
// returning a private IP at delivery time is detected and blocked by this
// dialer.
func ssrfSafeDialer() *http.Client {
	baseDialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("webhooks ssrf: bad address %q: %w", addr, err)
			}

			// If the dialer is given an IP literal (e.g. from a redirect),
			// validate it and dial directly.
			if ip := net.ParseIP(host); ip != nil {
				if isBlockedIP(ip) {
					return nil, fmt.Errorf("%w: %s", ErrWebhookURLBlocked, ip)
				}
				return baseDialer.DialContext(ctx, network, addr)
			}

			// Resolve hostname once, validate every answer, pin to first approved.
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("webhooks ssrf: resolve %q: %w", host, err)
			}
			if len(addrs) == 0 {
				return nil, fmt.Errorf("webhooks ssrf: no addresses for %q", host)
			}
			for _, ia := range addrs {
				if isBlockedIP(ia.IP) {
					// DNS answered with an internal/reserved IP — block even if the
					// original URL passed subscription-time validation (rebind guard).
					return nil, fmt.Errorf("%w: %s resolved to %s at dial time", ErrWebhookURLBlocked, host, ia.IP)
				}
			}
			// Pin to the first resolved IP to prevent race between resolve and dial.
			pinned := net.JoinHostPort(addrs[0].IP.String(), port)
			return baseDialer.DialContext(ctx, network, pinned)
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          64,
		IdleConnTimeout:       90 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}
}
