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
//
// The IP deny-list logic is implemented in internal/safedial so it is shared
// with webproxy and stream services — a single canonical implementation.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"vulos/backend/internal/safedial"
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

// getPeerPolicy is the grant this transport dials under: the UNION of the
// coarse VULOS_PEER_ALLOW_LAN opt-in above and the narrow VULOS_PEER_ALLOW_CIDR
// list (safedial.PeerPolicy), so a box whose peers live on a tailnet can name
// 100.64.0.0/10 without also re-opening 192.168.0.0/16 and 10.0.0.0/8.
//
// The union, not a replacement: a box already setting VULOS_PEER_ALLOW_LAN=1
// keeps exactly the reach it had, and the once-read boolean above stays the
// single reading of the legacy variable so the SSRF regression tests that force
// it keep forcing the real thing.
func getPeerPolicy() safedial.Policy {
	p := safedial.PeerPolicy()
	if getPeerAllowLAN() {
		p.AllowLAN = true
	}
	return p
}

// HTTPPeerTransport is the default PeerTransport over plain HTTP(S).
type HTTPPeerTransport struct {
	http          *http.Client
	addrValidator func(addr string) error // injectable for tests; nil uses validateOwnerAddr
}

// NewHTTPPeerTransport returns an HTTPPeerTransport with a streaming-friendly
// client (no overall client timeout — the per-request context governs the
// deadline so a large download is not cut off mid-stream).
//
// SSRF-FILES-01 (defence in depth): the pre-dial validateOwnerAddr check
// resolves the host ONCE and then discards the result, while a bare
// http.Client would independently re-resolve at dial time and follow
// redirects — so a rebinding DNS name (public on the pre-check, internal at
// dial) or an HTTP 3xx to an internal target would slip past the string-level
// guard. We close both vectors on the client itself:
//
//   - DialContext uses safedial's dial-time Control hook, which re-validates
//     the ACTUAL resolved IP immediately before connect(2) on EVERY dial
//     (initial and redirect), so a rebound/internal IP is refused even if the
//     pre-check saw a public one.
//   - CheckRedirect refuses to follow redirects at all (a peer /serve endpoint
//     never legitimately redirects), so a malicious owner cannot bounce the
//     fetch to an address the guard never inspected.
func NewHTTPPeerTransport() *HTTPPeerTransport {
	dialer := safedial.NewWithPolicy(getPeerPolicy())
	dialer.Timeout = 15 * time.Second
	return &HTTPPeerTransport{http: &http.Client{
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
		},
		// Fail closed: do NOT follow redirects. Fetch treats the returned 3xx as
		// a non-200 owner response and errors out, so the peer fetch can never be
		// bounced to an internal target. The dial-time Control hook re-validates
		// every dial too, so this is belt-and-suspenders against DNS rebinding.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
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
// The deny-list logic is shared with webproxy via internal/safedial.
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
	_, err = safedial.ValidateHostPolicy(host, getPeerPolicy())
	return err
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
