// ssrf.go — SSRF guard for outbound webhook URLs.
//
// Two-layer protection against server-side request forgery:
//
//  1. Subscription-create validation: validateWebhookURL rejects URLs that point
//     at loopback, RFC1918 private space, CGNAT (100.64.0.0/10), link-local /
//     169.254.x.x metadata endpoints, IPv6 ULA, multicast, and non-http(s)
//     schemes. IP literals (including obfuscated decimal/hex/octal forms) are
//     checked directly; hostnames are resolved and every returned address is
//     checked so that an internal hostname cannot be used to bypass the filter.
//
//  2. Dial-time re-screen (DNS-rebind protection): the Dispatcher's HTTP
//     transport validates the IP the OS resolver hands to the kernel
//     immediately before connect(2), so a hostname that resolved to a public
//     IP at subscribe time but a private one at delivery time is still
//     blocked.
//
// Both layers delegate their actual deny-list to backend/internal/safedial —
// the ONE canonical SSRF policy shared by every outbound-URL path in this OS
// (webproxy, files transport, stream/VNC, UnifiedPush, ...). This package
// used to carry its own parallel copy of the deny-list (ported verbatim from
// management/pkg/webhooks/ssrf.go during the management -> vulos fold); that
// duplicate had silently drifted from safedial's list — it was missing the
// 255.255.255.255 broadcast block and the 6to4 (2002::/16) / NAT64
// (64:ff9b::/96) IPv6-encapsulation ranges safedial blocks specifically
// because they can tunnel an RFC1918/metadata IPv4 target past an
// IP-range filter that only inspects IPv4 CIDRs. See safedial_delegation_test.go
// for regression coverage of that gap. Do not reintroduce a second deny-list
// here — add new ranges to safedial instead.
package webhooks

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"vulos/backend/internal/safedial"
)

// Sentinel errors returned by the SSRF guard.
var (
	// ErrWebhookURLBlocked is returned when a webhook URL resolves to an
	// internal/reserved address (loopback, RFC1918, link-local, metadata,
	// CGNAT, ULA, multicast, or any other range safedial denies).
	ErrWebhookURLBlocked = errors.New("webhooks: URL resolves to a blocked address (internal/reserved ranges are not allowed)")
	// ErrWebhookURLInvalid is returned for a malformed or non-http(s) URL.
	ErrWebhookURLInvalid = errors.New("webhooks: URL must be an absolute http(s) URL with a resolvable public host")
)

// validateWebhookURL parses rawURL, requires an http or https scheme, a
// non-empty host, and delegates address-space screening to safedial so the
// SSRF deny-list lives in exactly one place across the OS.
//
// Unlike some SSRF guards we DO block on unresolvable hostnames — a webhook
// that can never be contacted is useless and is more likely to be a
// mis-typed internal name than a legitimate external endpoint. safedial
// already fails closed on resolution errors, so that behaviour carries over
// unchanged.
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
	// allowLAN=false: a webhook receiver is expected to be reachable on the
	// public internet; the box's own LAN is explicitly in scope of what this
	// guard must deny (same policy UnifiedPush uses for distributor
	// endpoints — see internal/unifiedpush).
	if _, err := safedial.ValidateHost(host, false); err != nil {
		return fmt.Errorf("%w: %v", ErrWebhookURLBlocked, err)
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
// dialer re-screens the RESOLVED IP at connect time (DNS-rebind guard) via
// safedial. Reused by other backend packages that POST to an
// owner-influenced outbound URL. It does NOT follow redirects by default —
// the caller should set CheckRedirect to refuse them so a vendor 30x cannot
// bounce the request to an internal target after the parse-time screen.
func SSRFSafeClient() *http.Client { return ssrfSafeDialer() }

// ssrfSafeDialer returns an *http.Client whose dial-time Control hook
// validates the actual IP the OS resolver is about to connect(2) to, using
// safedial's shared deny-list. This defeats DNS-rebinding attacks (a
// hostname that resolved to a public IP at subscribe time but returns a
// private/internal IP at delivery time) and, because it inspects the real
// dial address rather than a separately-resolved-and-pinned one, it also
// covers HTTP redirects that hand the transport a new host to resolve.
func ssrfSafeDialer() *http.Client {
	baseDialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   safedial.ControlFunc(false),
	}
	transport := &http.Transport{
		DialContext:           baseDialer.DialContext,
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
