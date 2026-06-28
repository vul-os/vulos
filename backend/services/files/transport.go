package files

// transport.go — HTTPPeerTransport: the production PeerTransport. It streams a
// capability's bytes from a remote owner box by POSTing the signed fetch request
// to <ownerAddr>/api/files/peer/serve and returning the streaming response body.
//
// # SSRF hardening (SSRF-FILES-01)
//
// Prior to this fix, Fetch dialled ownerAddr taken directly from inside the
// signed capability with NO address validation.  The comment acknowledged this
// was intentional to allow LAN peer-share between self-hosted boxes on the same
// network, but it created a Server-Side Request Forgery vector: a malicious
// signer could embed any OwnerAddr in a capability (e.g. http://169.254.169.254
// or http://10.0.0.1/admin) and the recipient's box would faithfully POST to it.
//
// The fix applies two layers of validation before dialling:
//
//  (a) Scheme + host sanity: ownerAddr must have an http:// or https:// scheme
//      and a non-empty host.  This blocks opaque blobs and file:// URIs.
//
//  (b) IP deny-list: the resolved IP(s) are checked against the same deny-list
//      used by the webproxy service — loopback, private (RFC1918), link-local,
//      CGNAT (100.64/10), cloud-metadata (169.254.169.254), and reserved blocks
//      are all rejected.
//
// Self-host / LAN opt-in: operators running two Vulos boxes on a private LAN
// (box-to-box peer-share within a home network, corporate intranet, or Tailscale
// mesh) may set VULOS_PEER_ALLOW_LAN=1 to bypass the private-range deny-list.
// Metadata addresses (169.254.0.0/16 — cloud IMDSv1/v2) are ALWAYS blocked even
// when the env flag is set, because no legitimate peer-share target lives there.
//
// Note: end-to-end capability authentication (Ed25519 signature + recipient
// proof) remains in force regardless of the SSRF guard.  The guard is an
// additional layer that limits the blast radius if a signer is compromised.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PeerServePath is the route an owner box exposes to serve capability bytes.
const PeerServePath = "/api/files/peer/serve"

// peerTransferTimeout bounds the whole streamed transfer. Generous to tolerate
// large files over slow links; the context can cancel sooner.
const peerTransferTimeout = 30 * time.Minute

// peerAllowLAN is read once from VULOS_PEER_ALLOW_LAN at startup.
// When true the private-IP deny-list is skipped (LAN peer-share opt-in).
// Cloud metadata addresses (169.254.0.0/16) are still blocked regardless.
var (
	peerAllowLANOnce sync.Once
	peerAllowLAN     bool
)

func getPeerAllowLAN() bool {
	peerAllowLANOnce.Do(func() {
		v := os.Getenv("VULOS_PEER_ALLOW_LAN")
		peerAllowLAN = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	})
	return peerAllowLAN
}

// HTTPPeerTransport is the default PeerTransport over plain HTTP(S).
type HTTPPeerTransport struct {
	http          *http.Client
	addrValidator func(addr string) error // injectable for tests; nil uses validateOwnerAddr
}

// NewHTTPPeerTransport returns an HTTPPeerTransport with a streaming-friendly
// client (no overall client timeout — the per-request context governs the
// deadline so a large download is not cut off mid-stream).
func NewHTTPPeerTransport() *HTTPPeerTransport {
	return &HTTPPeerTransport{http: &http.Client{}}
}

// Fetch POSTs the fetch request to the owner box and returns the streaming body.
// ownerAddr is validated against the SSRF deny-list before dialling; pass
// VULOS_PEER_ALLOW_LAN=1 to allow private-range addresses for LAN peer-share.
func (t *HTTPPeerTransport) Fetch(ctx context.Context, ownerAddr string, req PeerFetchRequest) (io.ReadCloser, int64, error) {
	// SSRF-FILES-01: validate the owner address before dialling.
	validator := t.addrValidator
	if validator == nil {
		validator = validateOwnerAddr
	}
	if err := validator(ownerAddr); err != nil {
		return nil, 0, fmt.Errorf("files/transport: owner address rejected: %w", err)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("files/transport: marshal fetch: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, peerTransferTimeout)
	target := strings.TrimRight(ownerAddr, "/") + PeerServePath
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, 0, fmt.Errorf("files/transport: build request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(hreq)
	if err != nil {
		cancel()
		return nil, 0, fmt.Errorf("files/transport: fetch %s: %w", target, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, 0, fmt.Errorf("files/transport: owner returned %d", resp.StatusCode)
	}
	size := int64(-1)
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if v, perr := strconv.ParseInt(cl, 10, 64); perr == nil {
			size = v
		}
	}
	// Wrap so closing the body also cancels the request context.
	return &ctxReadCloser{rc: resp.Body, cancel: cancel}, size, nil
}

// validateOwnerAddr checks that addr is a safe peer URL:
//  1. Scheme must be http or https.
//  2. Host must be non-empty.
//  3. Resolved IP(s) must not be in the deny-list (loopback, link-local,
//     private, CGNAT, metadata, reserved).  Private-range blocking is relaxed
//     when VULOS_PEER_ALLOW_LAN=1, but metadata addresses are always blocked.
//
// Returns a descriptive error; callers should map to ErrCapability to avoid
// leaking internal topology to the requester.
func validateOwnerAddr(addr string) error {
	u, err := url.Parse(strings.TrimRight(addr, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("ownerAddr must have an http/https scheme")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("ownerAddr has no host")
	}

	allowLAN := getPeerAllowLAN()

	// Quick-block well-known names before DNS.
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || lower == "broadcasthost" {
		return fmt.Errorf("ownerAddr host %q is blocked", host)
	}

	// If the host is an IP literal, validate it directly (handles hex/decimal/octal).
	if ip := peerNormalizeIPLiteral(host); ip != nil {
		return checkPeerIP(ip, allowLAN)
	}

	// DNS resolution — validate ALL returned IPs.
	addrs, lerr := net.LookupHost(host)
	if lerr != nil {
		return fmt.Errorf("ownerAddr: cannot resolve %q: %w", host, lerr)
	}
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			continue
		}
		if err := checkPeerIP(ip, allowLAN); err != nil {
			return fmt.Errorf("ownerAddr: %w", err)
		}
	}
	return nil
}

// checkPeerIP returns an error if ip is in a blocked range.
// Cloud metadata (169.254.0.0/16) is always blocked; other private ranges
// are blocked unless allowLAN is true.
func checkPeerIP(ip net.IP, allowLAN bool) error {
	// Normalise IPv4-in-IPv6.
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}

	// Loopback is always blocked — a peer-share target on 127.x.x.x is
	// the recipient box itself, which is never a legitimate owner address.
	if ip.IsLoopback() || ip.IsUnspecified() {
		return fmt.Errorf("IP %s is blocked (loopback/unspecified)", ip)
	}

	// Metadata IP (169.254.169.254 and the full link-local range 169.254/16)
	// are ALWAYS blocked regardless of VULOS_PEER_ALLOW_LAN, because no
	// legitimate peer box lives on the cloud instance metadata service.
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("IP %s is blocked (link-local / cloud metadata)", ip)
	}

	// Multicast is always blocked.
	if ip.IsMulticast() {
		return fmt.Errorf("IP %s is blocked (multicast)", ip)
	}

	// Private ranges and additional reserved blocks: blocked unless VULOS_PEER_ALLOW_LAN=1.
	if !allowLAN {
		if ip.IsPrivate() {
			return fmt.Errorf("IP %s is a private address; set VULOS_PEER_ALLOW_LAN=1 for LAN peer-share", ip)
		}
		for _, cidr := range peerDeniedCIDRs {
			if cidr.Contains(ip) {
				return fmt.Errorf("IP %s is in a reserved block (%s); set VULOS_PEER_ALLOW_LAN=1 for LAN peer-share", ip, cidr)
			}
		}
	} else {
		// Even with LAN opt-in, block truly reserved / bogon ranges that are
		// never legitimate peer-share targets.
		for _, cidr := range peerAlwaysDeniedCIDRs {
			if cidr.Contains(ip) {
				return fmt.Errorf("IP %s is in a permanently blocked reserved block (%s)", ip, cidr)
			}
		}
	}

	return nil
}

// peerDeniedCIDRs are private/reserved blocks blocked without VULOS_PEER_ALLOW_LAN.
var peerDeniedCIDRs []*net.IPNet

// peerAlwaysDeniedCIDRs are ALWAYS blocked even with VULOS_PEER_ALLOW_LAN=1.
var peerAlwaysDeniedCIDRs []*net.IPNet

func init() {
	// Blocks denied unless VULOS_PEER_ALLOW_LAN=1 (RFC1918 + CGNAT — may be
	// legitimate LAN peer-share targets in self-hosted deployments).
	denied := []string{
		"100.64.0.0/10",   // CGNAT (RFC 6598) — also used by Tailscale
		"192.0.0.0/24",    // IETF Protocol Assignments
		"192.0.2.0/24",    // TEST-NET-1 (RFC 5737)
		"198.18.0.0/15",   // Benchmarking (RFC 2544)
		"198.51.100.0/24", // TEST-NET-2 (RFC 5737)
		"203.0.113.0/24",  // TEST-NET-3 (RFC 5737)
		"240.0.0.0/4",     // Reserved (RFC 1112)
		"fc00::/7",        // IPv6 ULA
		"2002::/16",       // 6to4 (can encapsulate private IPv4)
		"64:ff9b::/96",    // NAT64 well-known prefix
	}
	// Blocks ALWAYS denied regardless of env flag.
	alwaysDenied := []string{
		"255.255.255.255/32", // Broadcast
		"ff00::/8",           // IPv6 multicast (belt-and-suspenders; IsMulticast covers this)
	}
	mustParseCIDRs := func(blocks []string) []*net.IPNet {
		out := make([]*net.IPNet, 0, len(blocks))
		for _, b := range blocks {
			_, cidr, err := net.ParseCIDR(b)
			if err != nil {
				panic(fmt.Sprintf("files/transport: bad CIDR %s: %v", b, err))
			}
			out = append(out, cidr)
		}
		return out
	}
	peerDeniedCIDRs = mustParseCIDRs(denied)
	peerAlwaysDeniedCIDRs = mustParseCIDRs(alwaysDenied)
}

// peerNormalizeIPLiteral attempts to parse host as a bare IP literal.
// Handles standard dotted-decimal, IPv6, hex (0x7f000001), decimal (2130706433),
// and IPv4-mapped IPv6.  Returns nil if host is not an IP literal.
func peerNormalizeIPLiteral(host string) net.IP {
	h := host
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip
	}
	// Attempt decimal/hex/octal IPv4 — same logic as webproxy.parseAltIPv4.
	if !strings.Contains(h, ":") {
		if ip := peerParseAltIPv4(h); ip != nil {
			return ip
		}
	}
	return nil
}

// peerParseAltIPv4 parses non-standard IPv4 encodings (decimal int, hex 0x…,
// octal 0…, multi-part forms).  Mirrors webproxy.parseAltIPv4 — duplicated
// here to avoid cross-service imports; update both if logic changes.
func peerParseAltIPv4(h string) net.IP {
	parts := strings.Split(h, ".")
	if len(parts) > 4 {
		return nil
	}
	var octets [4]byte
	parseOctet := func(s string) (uint64, bool) {
		if s == "" {
			return 0, false
		}
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			return peerParseUint(s[2:], 16)
		}
		if len(s) > 1 && s[0] == '0' {
			return peerParseUint(s[1:], 8)
		}
		return peerParseUint(s, 10)
	}
	switch len(parts) {
	case 1:
		n, ok := parseOctet(parts[0])
		if !ok || n > 0xFFFFFFFF {
			return nil
		}
		octets[0], octets[1], octets[2], octets[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
	case 4:
		for i, p := range parts {
			n, ok := parseOctet(p)
			if !ok || n > 255 {
				return nil
			}
			octets[i] = byte(n)
		}
	case 2:
		n0, ok0 := parseOctet(parts[0])
		n1, ok1 := parseOctet(parts[1])
		if !ok0 || !ok1 || n0 > 255 || n1 > 0xFFFFFF {
			return nil
		}
		octets[0], octets[1], octets[2], octets[3] = byte(n0), byte(n1>>16), byte(n1>>8), byte(n1)
	case 3:
		n0, ok0 := parseOctet(parts[0])
		n1, ok1 := parseOctet(parts[1])
		n2, ok2 := parseOctet(parts[2])
		if !ok0 || !ok1 || !ok2 || n0 > 255 || n1 > 255 || n2 > 0xFFFF {
			return nil
		}
		octets[0], octets[1], octets[2], octets[3] = byte(n0), byte(n1), byte(n2>>8), byte(n2)
	default:
		return nil
	}
	return net.IPv4(octets[0], octets[1], octets[2], octets[3])
}

func peerParseUint(s string, base int) (uint64, bool) {
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
		if d >= uint64(base) {
			return 0, false
		}
		n = n*uint64(base) + d
	}
	return n, true
}

// ctxReadCloser cancels the request context when the body is closed.
type ctxReadCloser struct {
	rc     io.ReadCloser
	cancel context.CancelFunc
}

func (c *ctxReadCloser) Read(p []byte) (int, error) { return c.rc.Read(p) }
func (c *ctxReadCloser) Close() error {
	err := c.rc.Close()
	c.cancel()
	return err
}
