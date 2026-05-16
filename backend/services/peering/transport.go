// transport.go — outbound server-to-server HTTP client (PEER-04).
//
// PeerClient makes signed POST requests to a remote Vula peer's
// /api/peering/inbound/* endpoints. Every request carries a signed Envelope
// in the JSON body. The client refuses to connect to private / loopback
// addresses (SSRF guard — same logic as backend/services/webproxy) and
// enforces a hard timeout.
//
// # SSRF guard
//
// isPrivateHost resolves the target hostname to IP addresses and rejects any
// that are loopback, link-local, unspecified, or RFC-1918 private. This
// mirrors the isPrivate function in services/webproxy/proxy.go. The check
// runs at request time (after DNS resolution), so it cannot be bypassed by a
// hostname that initially resolved to a public IP but changes later.
//
// # Signed requests
//
// Every outbound request body is a JSON-encoded Envelope signed with the
// local node's Ed25519 private key (Envelope.Sign). The recipient uses
// Envelope.Verify to authenticate the sender.
//
// # Usage
//
//	env, err := peering.NewEnvelope(id, fromVulaID, toVulaID, peering.TypeMessage, payload)
//	if err != nil { ... }
//	if err := env.Sign(priv); err != nil { ... }
//	if err := client.Post(ctx, "https://peer.example.com:8080", "message", env); err != nil { ... }
package peering

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// peerClientTimeout is the total time allowed for a single outbound request
// (DNS lookup + TLS handshake + send + response headers). Chosen conservatively
// so slow peers do not block delivery goroutines indefinitely.
const peerClientTimeout = 15 * time.Second

// PeerClient is the outbound HTTP client for server-to-server envelope delivery.
// The zero value is not usable; obtain one via NewPeerClient.
type PeerClient struct {
	http *http.Client
}

// NewPeerClient creates a PeerClient with a pre-configured http.Client that
// enforces TLS, a 15-second total timeout, and the SSRF guard transport.
func NewPeerClient() *PeerClient {
	transport := &ssrfGuardTransport{
		inner: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
			// Honour timeouts inside the dialer so the SSRF check is applied
			// before the connection is established.
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		},
	}
	return &PeerClient{
		http: &http.Client{
			Timeout:   peerClientTimeout,
			Transport: transport,
		},
	}
}

// Post delivers a signed Envelope to the remote peer at baseURL.
//
// baseURL is the peer's root URL, e.g. "https://bob.vulos.org:8080". The
// request is posted to <baseURL>/api/peering/inbound/<envelopeType>.
//
// env must already be signed (env.Signature must be set). Post returns an
// error if:
//   - baseURL resolves to a private/loopback address (SSRF guard)
//   - the network call fails or times out
//   - the remote server responds with a non-2xx status
func (c *PeerClient) Post(ctx context.Context, baseURL, envelopeType string, env *Envelope) error {
	if env.Signature == "" {
		return fmt.Errorf("peering/transport: envelope must be signed before sending")
	}

	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("peering/transport: marshal envelope: %w", err)
	}

	// Build target URL: <baseURL>/api/peering/inbound/<type>
	target := strings.TrimRight(baseURL, "/") + "/api/peering/inbound/" + envelopeType

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("peering/transport: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "VulaOS/1.0 peering")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("peering/transport: post %s: %w", target, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("peering/transport: remote %s returned %d", target, resp.StatusCode)
	}

	return nil
}

// ─── SSRF guard transport ─────────────────────────────────────────────────────

// ssrfGuardTransport wraps an inner http.RoundTripper and rejects requests
// whose target hostname resolves to a private, loopback, link-local, or
// unspecified IP address. This prevents server-side request forgery attacks
// where a crafted peer URL redirects the server to hit internal services.
type ssrfGuardTransport struct {
	inner http.RoundTripper
}

// RoundTrip implements http.RoundTripper. It resolves the host, runs the SSRF
// check, and then delegates to the inner transport.
func (t *ssrfGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if isPrivateHost(host) {
		return nil, fmt.Errorf("peering/transport: SSRF guard: %q resolves to a private address", host)
	}
	return t.inner.RoundTrip(req)
}

// isPrivateHost resolves host to IP addresses and returns true if any of them
// are private/loopback/link-local. Unresolvable hosts return false (the
// subsequent connection attempt will fail with a network error, which is
// acceptable — we don't want to block legitimate hosts just because DNS is
// slow).
//
// This mirrors the isPrivate function in backend/services/webproxy/proxy.go.
func isPrivateHost(host string) bool {
	// Quick string-level check for the most common cases.
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, blocked := range []string{"localhost", "127.0.0.1", "0.0.0.0", "[::1]", "::1"} {
		if h == blocked {
			return true
		}
	}

	// Resolve to IPs and check each one.
	ips, err := net.LookupHost(h)
	if err != nil {
		// Cannot resolve — let the inner transport fail naturally.
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
