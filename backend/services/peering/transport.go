// transport.go — outbound server-to-server HTTP client (PEER-04).
//
// PeerClient makes signed POST requests to a remote Vulos peer's
// /api/peering/inbound/* endpoints. Every request carries a signed Envelope
// in the JSON body. The client refuses to connect to private / loopback
// addresses (SSRF guard via safedial) and enforces a hard timeout.
//
// # SSRF guard
//
// M1 fix: the hand-rolled ssrfGuardTransport / isPrivateHost implementation
// has been removed and replaced with safedial, the canonical SSRF-safe dialer
// used throughout the OS. safedial validates IP addresses at the kernel
// connect(2) call (after DNS resolution), closing the DNS-rebinding window that
// the old RoundTripper-level check left open.
//
// The grant is safedial.NewPeer — the operator's PEER dial grant — not a
// hardcoded "public only". A box whose correspondents live on a Tailscale /
// Headscale / Nebula mesh reaches them at a private address by definition, and
// this client is the one seam every envelope goes through, so hardcoding
// public-only here made VULOS_PEER_ALLOW_LAN=1 look like it enabled box-to-box
// delivery over a mesh when it never did. With nothing configured the behaviour
// is byte-identical to the old safedial.New(false); the operator has to name
// the range (VULOS_PEER_ALLOW_CIDR) before anything private is dialable, and
// loopback, link-local and bogons stay refused under every grant.
//
// # Signed requests
//
// Every outbound request body is a JSON-encoded Envelope signed with the
// local node's Ed25519 private key (Envelope.Sign). The recipient uses
// Envelope.Verify to authenticate the sender.
//
// # Usage
//
//	env, err := peering.NewEnvelope(id, fromVulosID, toVulosID, peering.TypeMessage, payload)
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

	"vulos/backend/internal/safedial"
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
// enforces TLS, a 15-second total timeout, and safedial SSRF protection.
// M1 fix: uses safedial instead of the removed ssrfGuardTransport.
func NewPeerClient() *PeerClient {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		// safedial validates the resolved IP at kernel connect(2) time,
		// preventing DNS-rebinding attacks and SSRF via private IPs, under the
		// operator's peer grant (see the package note above).
		// The peeringSSRFBypass closure allows httptest.Server in package tests.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if peeringSSRFBypass {
				return (&net.Dialer{
					Timeout:   5 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext(ctx, network, addr)
			}
			return safedial.NewPeer().DialContext(ctx, network, addr)
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
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
	req.Header.Set("User-Agent", "VulosOS/1.0 peering")

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
