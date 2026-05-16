package webproxy

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Service is an HTTP reverse proxy for remote mode.
// When vulos is accessed remotely, iframes would fetch content from the
// client's network. This proxy routes all web requests through the server.
//
// Route: GET /api/proxy/{url}
// Example: /api/proxy/https://youtube.com/watch?v=abc
//
// In local mode (WebKit on machine), this is unused — iframes fetch directly.
type Service struct {
	// client intentionally has no Transport set here; we build a per-request
	// Transport in Handler() that pins the resolved IP to prevent DNS-rebinding.
	// The Timeout and CheckRedirect are set below and applied via clientForHost().
	resolveHost func(host string) ([]net.IP, error) // injectable for tests
}

func New() *Service {
	return &Service{
		resolveHost: defaultResolveHost,
	}
}

// defaultResolveHost resolves a hostname to its IP addresses.
func defaultResolveHost(host string) ([]net.IP, error) {
	addrs, err := net.LookupHost(host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip != nil {
			ips = append(ips, ip)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no valid IPs for %s", host)
	}
	return ips, nil
}

// pinnedDialer returns a DialContext function that always connects to pinnedIP,
// regardless of what hostname would normally resolve to (prevents re-resolution).
// For TLS the Transport.TLSClientConfig.ServerName is set to hostname so cert
// validation uses the original hostname, not the IP literal.
func pinnedDialer(pinnedIP net.IP) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		// addr is "host:port" — extract port and replace host with pinned IP
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		target := net.JoinHostPort(pinnedIP.String(), port)
		d := &net.Dialer{Timeout: 10 * time.Second}
		return d.DialContext(ctx, network, target)
	}
}

// clientForHost builds an http.Client whose Transport dials only the supplied
// pinnedIP. TLS certificate validation uses the original hostname.
func clientForHost(hostname string, pinnedIP net.IP) *http.Client {
	transport := &http.Transport{
		DialContext: pinnedDialer(pinnedIP),
		TLSClientConfig: &tls.Config{
			// ServerName ensures TLS handshake validates against the hostname,
			// not the raw IP literal — required when dialing IP directly.
			ServerName: hostname,
		},
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

// Handler returns the HTTP handler for proxy requests.
func (s *Service) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract target URL — prefer ?url= query param, fall back to path
		rawURL := r.URL.Query().Get("url")
		if rawURL == "" {
			rawURL = strings.TrimPrefix(r.URL.Path, "/api/proxy/")
			// Restore double slash that Go's HTTP server strips
			rawURL = strings.Replace(rawURL, "http:/", "http://", 1)
			rawURL = strings.Replace(rawURL, "https:/", "https://", 1)
			if r.URL.RawQuery != "" && !strings.Contains(r.URL.RawQuery, "url=") {
				rawURL += "?" + r.URL.RawQuery
			}
		}

		if rawURL == "" {
			http.Error(w, `{"error":"no url"}`, 400)
			return
		}

		// Ensure it has a scheme
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			rawURL = "https://" + rawURL
		}

		targetURL, err := url.Parse(rawURL)
		if err != nil {
			http.Error(w, `{"error":"invalid url"}`, 400)
			return
		}

		hostname := targetURL.Hostname()

		// --- Single-resolve + pin (fix for DNS-rebinding TOCTOU) ---
		// resolveAndValidate resolves host ONCE, validates ALL returned IPs,
		// and returns the first safe IP to dial. Fails closed on any error.
		pinnedIP, err := s.resolveAndValidate(hostname)
		if err != nil {
			log.Printf("[webproxy] blocked %s: %v", hostname, err)
			http.Error(w, `{"error":"cannot proxy to private or unresolvable addresses"}`, 403)
			return
		}

		// Build a client whose dialer is pinned to the resolved IP.
		// The second DNS lookup that client.Do would normally perform is
		// bypassed because DialContext always connects to pinnedIP directly.
		client := clientForHost(hostname, pinnedIP)

		// Build upstream request
		proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, rawURL, r.Body)
		if err != nil {
			http.Error(w, `{"error":"failed to create request"}`, 500)
			return
		}

		// Forward relevant headers
		for _, h := range []string{"Accept", "Accept-Language", "Content-Type", "Range"} {
			if v := r.Header.Get(h); v != "" {
				proxyReq.Header.Set(h, v)
			}
		}
		proxyReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; VulaOS/1.0)")
		proxyReq.Header.Set("Accept-Encoding", "gzip")

		// Execute — uses pinned IP, no second DNS resolution occurs
		resp, err := client.Do(proxyReq)
		if err != nil {
			log.Printf("[webproxy] fetch %s error: %v", rawURL, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(502)
			fmt.Fprintf(w, `{"error":"fetch failed: %s"}`, err.Error())
			return
		}
		defer resp.Body.Close()

		// Copy response headers
		for _, h := range []string{"Content-Type", "Content-Length", "Content-Disposition", "Cache-Control", "ETag", "Last-Modified"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}

		// CORS — allow same-origin shell requests
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}

		// Rewrite Location headers for redirects
		if loc := resp.Header.Get("Location"); loc != "" {
			w.Header().Set("Location", "/api/proxy/"+loc)
		}

		w.WriteHeader(resp.StatusCode)

		// Decompress gzip if needed and stream
		var reader io.Reader = resp.Body
		if resp.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(resp.Body)
			if err == nil {
				defer gz.Close()
				reader = gz
				w.Header().Del("Content-Encoding")
				w.Header().Del("Content-Length")
			}
		}

		io.Copy(w, reader)
	}
}

// resolveAndValidate resolves host ONCE and checks every IP against the
// deny list. Returns the first IP to use for dialing. Fails closed: any
// error (resolution failure, zero IPs, any denied IP) returns an error.
func (s *Service) resolveAndValidate(host string) (net.IP, error) {
	// 1. Try to parse the host as an IP literal first (handles decimal,
	//    hex, octal, IPv4-in-IPv6 forms via net.ParseIP normalization).
	if ip := normalizeIPLiteral(host); ip != nil {
		if isDeniedIP(ip) {
			return nil, fmt.Errorf("IP %s is in a denied block", ip)
		}
		return ip, nil
	}

	// Quick-block well-known hostnames before DNS
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || lower == "broadcasthost" {
		return nil, fmt.Errorf("hostname %q is blocked", host)
	}

	// 2. Single DNS resolution
	ips, err := s.resolveHost(host)
	if err != nil {
		// Fail closed — don't allow fetches that can't be verified
		return nil, fmt.Errorf("cannot resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no IPs resolved for %s", host)
	}

	// 3. Every IP must pass the deny list
	for _, ip := range ips {
		if isDeniedIP(ip) {
			return nil, fmt.Errorf("resolved IP %s for %s is in a denied block", ip, host)
		}
	}

	// Return the first safe IP to use as the pinned dial target
	return ips[0], nil
}

// normalizeIPLiteral attempts to parse host as an IP literal.
// It handles:
//   - Standard dotted-decimal IPv4 (192.168.1.1)
//   - Standard IPv6 ([::1] bracket form or ::1)
//   - Decimal-encoded IPv4 (2130706433 → 127.0.0.1)
//   - Hex-encoded IPv4 (0x7f000001 → 127.0.0.1)
//   - IPv4-mapped IPv6 (::ffff:127.0.0.1)
//
// Returns nil if host is not recognizable as an IP literal.
func normalizeIPLiteral(host string) net.IP {
	// Strip brackets from IPv6 literals e.g. [::1]
	h := host
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}

	// Standard net.ParseIP handles: dotted-decimal, IPv6, IPv4-in-IPv6
	if ip := net.ParseIP(h); ip != nil {
		return ip
	}

	// Decimal / hex / octal encoded IPv4:
	// These are not handled by net.ParseIP but are accepted by OS APIs.
	// Only attempt if it looks like it could be an integer literal
	// (no dots, or fewer than 3 dots).
	if !strings.Contains(h, ":") { // not IPv6
		if ip := parseAltIPv4(h); ip != nil {
			return ip
		}
	}

	return nil
}

// parseAltIPv4 parses non-standard IPv4 representations:
//   - Pure decimal integer: 2130706433 → 127.0.0.1
//   - Pure hex (0x...): 0x7f000001 → 127.0.0.1
//   - Octal-prefixed octets: 0177.0.0.1 → 127.0.0.1
//   - Mixed representations per RFC/POSIX (up to 4 octets, each in any base)
func parseAltIPv4(h string) net.IP {
	// Split on dots to handle multi-part forms
	parts := strings.Split(h, ".")
	if len(parts) > 4 {
		return nil
	}

	var octets [4]byte

	if len(parts) == 1 {
		// Single numeric form: decimal or hex integer representing full address
		n, ok := parseUint64(parts[0])
		if !ok {
			return nil
		}
		if n > 0xFFFFFFFF {
			return nil
		}
		octets[0] = byte(n >> 24)
		octets[1] = byte(n >> 16)
		octets[2] = byte(n >> 8)
		octets[3] = byte(n)
	} else if len(parts) == 4 {
		// Four-part: each octet may be hex/octal/decimal
		for i, p := range parts {
			n, ok := parseUint64(p)
			if !ok || n > 255 {
				return nil
			}
			octets[i] = byte(n)
		}
	} else if len(parts) == 2 {
		// Two-part: first octet + remaining 24-bit value
		n0, ok0 := parseUint64(parts[0])
		n1, ok1 := parseUint64(parts[1])
		if !ok0 || !ok1 || n0 > 255 || n1 > 0xFFFFFF {
			return nil
		}
		octets[0] = byte(n0)
		octets[1] = byte(n1 >> 16)
		octets[2] = byte(n1 >> 8)
		octets[3] = byte(n1)
	} else if len(parts) == 3 {
		// Three-part: two octets + remaining 16-bit value
		n0, ok0 := parseUint64(parts[0])
		n1, ok1 := parseUint64(parts[1])
		n2, ok2 := parseUint64(parts[2])
		if !ok0 || !ok1 || !ok2 || n0 > 255 || n1 > 255 || n2 > 0xFFFF {
			return nil
		}
		octets[0] = byte(n0)
		octets[1] = byte(n1)
		octets[2] = byte(n2 >> 8)
		octets[3] = byte(n2)
	}

	return net.IPv4(octets[0], octets[1], octets[2], octets[3])
}

// parseUint64 parses a string as decimal, hex (0x prefix), or octal (0 prefix).
func parseUint64(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	// Hex
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		n, err := strconvParseUint(s[2:], 16)
		return n, err == nil
	}
	// Octal (leading zero, more than one char)
	if len(s) > 1 && s[0] == '0' {
		n, err := strconvParseUint(s[1:], 8)
		return n, err == nil
	}
	// Decimal
	n, err := strconvParseUint(s, 10)
	return n, err == nil
}

// strconvParseUint is a thin wrapper so we don't import strconv at package level
// (avoids polluting the import list — inline the logic).
func strconvParseUint(s string, base int) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty string")
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
			return 0, fmt.Errorf("invalid char %c in base %d", c, base)
		}
		if d >= uint64(base) {
			return 0, fmt.Errorf("digit %d out of range for base %d", d, base)
		}
		n = n*uint64(base) + d
	}
	return n, nil
}

// isDeniedIP returns true if the IP falls into any private, loopback,
// link-local, multicast, CGNAT, or otherwise reserved block.
// Covers both IPv4 and IPv6 including IPv4-mapped IPv6 addresses.
func isDeniedIP(ip net.IP) bool {
	// Unmap IPv4-in-IPv6 (::ffff:x.x.x.x) so CIDR checks work uniformly
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}

	// Use Go's built-in predicates first
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsPrivate() {
		return true
	}

	// Additional CIDR blocks not covered by the above predicates
	for _, cidr := range deniedCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// deniedCIDRs is parsed once at init time for efficiency.
var deniedCIDRs []*net.IPNet

func init() {
	blocks := []string{
		// IPv4 blocks
		"100.64.0.0/10",      // CGNAT (RFC 6598)
		"192.0.0.0/24",       // IETF Protocol Assignments
		"192.0.2.0/24",       // TEST-NET-1 (RFC 5737)
		"198.18.0.0/15",      // Benchmarking (RFC 2544)
		"198.51.100.0/24",    // TEST-NET-2 (RFC 5737)
		"203.0.113.0/24",     // TEST-NET-3 (RFC 5737)
		"240.0.0.0/4",        // Reserved (RFC 1112)
		"255.255.255.255/32", // Broadcast
		// IPv6 blocks not covered by IsPrivate/IsLoopback/IsLinkLocal/IsMulticast
		"fc00::/7",     // Unique local (ULA)
		"ff00::/8",     // Multicast (belt-and-suspenders; IsMulticast covers this)
		"2002::/16",    // 6to4 (can encapsulate private IPv4)
		"64:ff9b::/96", // NAT64 well-known prefix
	}
	for _, b := range blocks {
		_, cidr, err := net.ParseCIDR(b)
		if err != nil {
			panic(fmt.Sprintf("webproxy: bad CIDR %s: %v", b, err))
		}
		deniedCIDRs = append(deniedCIDRs, cidr)
	}
}

// isPrivate is kept for the WSRelayHandler (wsrelay.go) which calls it.
// It now uses the same deny logic as resolveAndValidate but is not used
// by the HTTP proxy path (which uses resolveAndValidate directly).
func isPrivate(host string) bool {
	svc := &Service{resolveHost: defaultResolveHost}
	_, err := svc.resolveAndValidate(host)
	return err != nil
}
