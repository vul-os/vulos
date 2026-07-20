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

	"vulos/backend/internal/safedial"
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
			http.Error(w, `{"error":"cannot proxy to private or unresolvable addresses"}`, http.StatusForbidden)
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
//
// Delegates to safedial.ValidateHostWithResolver so the canonical deny-list
// lives in one place (internal/safedial).
func (s *Service) resolveAndValidate(host string) (net.IP, error) {
	return safedial.ValidateHostWithResolver(host, false, s.resolveHost)
}

// isPrivate returns true when host resolves to a private/blocked address.
// Used by wsrelay.go as a user-friendly pre-check before dialling.
func isPrivate(host string) bool {
	_, err := safedial.ValidateHost(host, false)
	return err != nil
}

// ─── Thin wrappers for backward-compat with existing in-package tests ─────────
//
// proxy_test.go calls isDeniedIP, normalizeIPLiteral, parseAltIPv4, and
// parseUint64 directly (they are in the same package). These shims delegate
// to the canonical safedial implementations so the tests continue to pass
// without modification while the actual logic lives in one place.

func isDeniedIP(ip net.IP) bool             { return safedial.IsDeniedIP(ip, false) }
func normalizeIPLiteral(host string) net.IP { return safedial.NormalizeIPLiteral(host) }
func parseAltIPv4(h string) net.IP          { return safedial.ParseAltIPv4(h) }
func parseUint64(s string) (uint64, bool)   { return safedial.ParseUint64(s) }
