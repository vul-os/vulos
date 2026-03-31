package webproxy

import (
	"compress/gzip"
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
	client *http.Client
}

func New() *Service {
	return &Service{
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
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

		// Block internal/private IPs to prevent SSRF
		if isPrivate(targetURL.Hostname()) {
			http.Error(w, `{"error":"cannot proxy to private addresses"}`, 403)
			return
		}

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

		// Execute
		resp, err := s.client.Do(proxyReq)
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

// isPrivate resolves hostname to IP and checks if it's private/internal.
// Prevents SSRF by blocking access to internal services.
func isPrivate(host string) bool {
	// Quick string check first
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	quickBlock := []string{"localhost", "127.0.0.1", "0.0.0.0", "[::1]"}
	for _, b := range quickBlock {
		if h == b {
			return true
		}
	}

	// Resolve hostname to IPs and check each one
	ips, err := net.LookupHost(h)
	if err != nil {
		// Can't resolve — allow (will fail at fetch anyway)
		return false
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return true
		}
	}
	return false
}
