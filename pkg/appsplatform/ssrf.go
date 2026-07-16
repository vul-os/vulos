package appsplatform

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// AllowPrivateWebhooksEnv, when truthy ("1"/"true"/"yes"), disables the
// destination guard that blocks outbound webhook deliveries to private,
// loopback, link-local, and metadata addresses. It is OFF by default; only
// self-hosters who legitimately POST to internal targets should set it.
const AllowPrivateWebhooksEnv = "VULOS_APPS_ALLOW_PRIVATE_WEBHOOKS"

// resolveIPs resolves a host to its IP addresses. It is a package var so tests
// can stub DNS resolution without network access.
var resolveIPs = net.LookupIP

// allowPrivateWebhooks reports whether the private-destination guard is disabled
// via the environment.
func allowPrivateWebhooks() bool {
	v := strings.TrimSpace(os.Getenv(AllowPrivateWebhooksEnv))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

// ValidateWebhookURL guards outbound webhook destinations against SSRF. The
// scheme must be http or https, and (unless AllowPrivateWebhooksEnv is set) the
// host must not resolve to a private/loopback/link-local/metadata range
// (RFC1918, 127.0.0.0/8, ::1, 169.254.0.0/16 incl. 169.254.169.254, fc00::/7,
// fe80::/10, 0.0.0.0/::, multicast).
//
// An empty URL is allowed (it means "no webhook configured"). When a host is a
// hostname it is resolved and EVERY resolved address must be permitted, so a
// name that maps to even one blocked address is rejected.
func ValidateWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("appsplatform: invalid webhook_url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("appsplatform: webhook_url scheme %q not allowed (use http or https)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("appsplatform: webhook_url has no host")
	}
	if allowPrivateWebhooks() {
		return nil
	}

	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		resolved, err := resolveIPs(host)
		if err != nil {
			return fmt.Errorf("appsplatform: webhook_url host %q does not resolve: %w", host, err)
		}
		if len(resolved) == 0 {
			return fmt.Errorf("appsplatform: webhook_url host %q resolved to no addresses", host)
		}
		ips = resolved
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("appsplatform: webhook_url host %q resolves to disallowed address %s", host, ip)
		}
	}
	return nil
}

// safeDialContext returns a DialContext that closes the DNS-rebinding TOCTOU
// window between ValidateWebhookURL (check time) and the actual dial (use time).
//
// ValidateWebhookURL resolves and screens a host once, up front. A hostile DNS
// server can answer that lookup with a public address and then, on the dial that
// the HTTP client performs moments later, answer with a private/metadata address
// (DNS rebinding) — the connection would then reach an internal target the guard
// believed it had excluded. This dialer re-resolves and re-screens at dial time
// and PINS the connection to a validated IP literal, so the socket connects to
// exactly the address it just checked rather than to whatever a concurrent
// re-resolution might return.
func safeDialContext(dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("appsplatform: dial: bad address %q: %w", addr, err)
		}

		var ips []net.IP
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IP{ip}
		} else {
			resolved, err := resolveIPs(host)
			if err != nil {
				return nil, fmt.Errorf("appsplatform: dial: host %q does not resolve: %w", host, err)
			}
			if len(resolved) == 0 {
				return nil, fmt.Errorf("appsplatform: dial: host %q resolved to no addresses", host)
			}
			ips = resolved
		}

		allowPrivate := allowPrivateWebhooks()
		for _, ip := range ips {
			if !allowPrivate && isBlockedIP(ip) {
				return nil, fmt.Errorf("appsplatform: dial: refusing blocked address %s for host %q", ip, host)
			}
		}
		// Pin to the first validated address so the connection goes to exactly
		// the IP we screened, never to a value a concurrent re-resolution by the
		// stdlib resolver might have substituted.
		pinned := net.JoinHostPort(ips[0].String(), port)
		return dialer.DialContext(ctx, network, pinned)
	}
}

// NewGuardedHTTPClient builds an *http.Client whose transport dials through the
// SAME safeDialContext the outbound-webhook client uses: every dial re-resolves
// the host, refuses any address in a blocked range (loopback/private/link-local/
// metadata/multicast), and PINS the connection to the validated IP so a DNS
// rebind between check-time and dial-time cannot redirect the request to an
// internal target.
//
// This is the single SSRF-safe client factory the whole suite shares for any
// server-side "fetch a user-supplied URL" feature (webhook delivery, link
// unfurling). Proxy is nil for the same reason as the webhook client: an env
// proxy would bypass the IP guard entirely.
//
// The caller is still responsible for validating the URL up front with
// ValidateWebhookURL (scheme + resolve check) — this only guards the dial.
func NewGuardedHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil, // env proxy would bypass the SSRF IP guard
			DialContext:           safeDialContext(dialer),
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          20,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// isBlockedIP reports whether ip falls in a range that must never be a webhook
// destination (loopback, RFC1918/ULA private, link-local incl. the cloud
// metadata 169.254.169.254, unspecified, and multicast).
func isBlockedIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	return ip.IsUnspecified() || // 0.0.0.0, ::
		ip.IsLoopback() || // 127.0.0.0/8, ::1
		ip.IsPrivate() || // RFC1918, fc00::/7
		ip.IsLinkLocalUnicast() || // 169.254.0.0/16 (incl .169.254), fe80::/10
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast()
}
