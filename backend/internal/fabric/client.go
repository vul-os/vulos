package fabric

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"vulos/backend/internal/multiinstance"
	"vulos/backend/internal/safedial"
)

// HTTPDoer is the subset of *http.Client the fabric client uses. It lets tests
// inject the httptest server's client (which trusts the test's self-signed
// cert) and lets production inject a LAN-cert-trusting client.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// maxPullBytes caps the size of a changeset we will read from a peer on pull.
const maxPullBytes = 8 << 20

// pull fetches the peer's changeset since the cursor over
// GET /api/fabric/changeset?since=<cursor>, authenticating with X-Fabric-Auth.
func (s *Service) pull(ctx context.Context, p Peer, since time.Time) (*multiinstance.AppChangeset, error) {
	base, err := normalizeBaseURL(p.BaseURL)
	if err != nil {
		return nil, err
	}
	client, err := s.httpClientFor(p, base)
	if err != nil {
		return nil, err
	}
	u := base + "/api/fabric/changeset"
	if !since.IsZero() {
		u += "?since=" + url.QueryEscape(since.UTC().Format(time.RFC3339Nano))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, errFabricWrap("build pull request", err)
	}
	req.Header.Set("X-Fabric-Auth", s.cfg.Secret)

	resp, err := client.Do(req)
	if err != nil {
		return nil, errFabricWrap("pull request", err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("fabric: peer rejected our auth (401) — shared secret mismatch")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fabric: pull unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPullBytes+1))
	if err != nil {
		return nil, errFabricWrap("read pull body", err)
	}
	if len(body) > maxPullBytes {
		return nil, errors.New("fabric: pulled changeset too large")
	}
	cs, err := multiinstance.UnmarshalChangeset(body)
	if err != nil {
		return nil, errFabricWrap("decode pulled changeset", err)
	}
	return cs, nil
}

// push sends our changeset to the peer over POST /api/fabric/changeset,
// authenticating with X-Fabric-Auth.
func (s *Service) push(ctx context.Context, p Peer, cs *multiinstance.AppChangeset) error {
	base, err := normalizeBaseURL(p.BaseURL)
	if err != nil {
		return err
	}
	client, err := s.httpClientFor(p, base)
	if err != nil {
		return err
	}
	body, err := multiinstance.MarshalChangeset(cs)
	if err != nil {
		return errFabricWrap("marshal push changeset", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/api/fabric/changeset", bytes.NewReader(body))
	if err != nil {
		return errFabricWrap("build push request", err)
	}
	req.Header.Set("X-Fabric-Auth", s.cfg.Secret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return errFabricWrap("push request", err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("fabric: peer rejected our auth (401) on push")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fabric: push unexpected status %d", resp.StatusCode)
	}
	return nil
}

// httpClientFor picks the transport for peer p: the operator-supplied LAN
// client (Config.HTTPClient) for a same-network peer, or the safedial-guarded,
// real-TLS WAN client (Config.WANHTTPClient) for a peer whose address came
// back from a rendezvous relay (p.WAN — see rendezvous.go's resolve).
//
// FABRIC-SSRF-01: a WAN peer's BaseURL is exactly as trustworthy as whatever
// relay resolved it — the relay is unauthenticated and, per rendezvous.go's
// resolve doc, its answer carries no verifiable signature today. Reaching it
// with the LAN client (InsecureSkipVerify, no SSRF guard) would let a lying or
// MITM'd relay point this box's shared secret and app-registry changeset at
// an attacker-chosen or internal address. So this function fails CLOSED: a
// WAN peer with no WANHTTPClient configured is an error, never a silent
// fallback to the LAN client, and a WAN peer whose BaseURL is not https is
// also rejected — a plain-http endpoint would carry X-Fabric-Auth with no
// transport security at all regardless of which client dials it.
// base must already be normalizeBaseURL'd.
func (s *Service) httpClientFor(p Peer, base string) (HTTPDoer, error) {
	if !p.WAN {
		return s.cfg.HTTPClient, nil
	}
	if s.cfg.WANHTTPClient == nil {
		return nil, errFabric("peer " + base + " is a WAN (rendezvous-resolved) peer but Config.WANHTTPClient is not configured — refusing to fall back to the LAN client")
	}
	if !strings.HasPrefix(strings.ToLower(base), "https://") {
		return nil, errFabric("WAN peer " + base + " must use https — refusing to send the fabric secret over an unencrypted transport")
	}
	return s.cfg.WANHTTPClient, nil
}

// WANClient is the transport for fabric traffic that leaves the LAN: talking
// to a rendezvous relay's /announce and /resolve endpoints, and reaching a
// peer whose base URL that relay handed back (FABRIC-SSRF-01).
//
// Unlike NewLANClient, it:
//   - dials through safedial's SSRF guard (internal/safedial.New), which
//     validates the ACTUAL resolved IP immediately before connect(2) on every
//     dial — including redirects — closing the DNS-rebinding window a
//     pre-dial string check alone would leave open;
//   - verifies TLS certificates against the system root store like any normal
//     internet client (TLSClientConfig is left nil — no InsecureSkipVerify).
//     A relay or a WAN peer is not a pinned LAN host reached by IP, so there
//     is no basis to skip certificate validation, and skipping it is exactly
//     what let a network MITM substitute its own endpoint silently;
//   - refuses to follow redirects, so a malicious relay or peer cannot bounce
//     a request carrying X-Fabric-Auth to an address neither guard inspected.
//
// It is a distinct Go type, not a *http.Client alias, specifically so it
// cannot be assigned where NewLANClient's deliberately-insecure client is
// expected or vice versa: RendezvousDiscoverer.HTTPClient and
// Config.WANHTTPClient are both typed *WANClient, so passing
// fabric.NewLANClient(...) to either is now a compile error instead of the
// silent, running-in-production mistake main.go made before this fix (both
// were plain *http.Client, so the wrong one type-checked fine).
type WANClient struct{ *http.Client }

// NewWANClient builds a WANClient. allowLAN mirrors the VULOS_PEER_ALLOW_LAN
// opt-in used elsewhere in this codebase (services/files/transport.go): pass
// true only when this box's WAN peers are legitimately reached over a private
// range (e.g. a Tailscale/CGNAT mesh spanning two houses). Cloud-metadata,
// loopback, and link-local targets are always blocked regardless — see
// internal/safedial.IsDeniedIP.
func NewWANClient(timeout time.Duration, allowLAN bool) *WANClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dialer := safedial.New(allowLAN)
	dialer.Timeout = timeout
	return &WANClient{&http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 5 * time.Second,
			MaxIdleConns:        10,
			IdleConnTimeout:     90 * time.Second,
			// TLSClientConfig deliberately omitted: the nil/zero value verifies
			// server certificates against the system root store, which is the
			// whole point of this client relative to NewLANClient.
		},
		// Neither a rendezvous relay's /announce and /resolve nor a peer's
		// /api/fabric/changeset legitimately redirects. Refusing to follow
		// means a hostile response can't bounce the request — which, on a
		// peer call, carries X-Fabric-Auth — to a target neither the pre-dial
		// URL nor the dial-time safedial Control hook ever inspected.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// NewLANClient builds an *http.Client suitable for reaching LAN peers. The LAN
// listener serves the box's LAN cert (cloud-issued DNS-01 in production, or a
// self-signed dev cert). Peers are reached by raw IP, for which a DNS-01 cert
// has no SAN, so verifying the hostname chain is not meaningful on the LAN; the
// authenticity guarantee on the fabric comes from the shared X-Fabric-Auth
// secret, not from the TLS PKI. We therefore skip server-name verification but
// still use TLS for confidentiality on the wire.
//
// SECURITY: this is acceptable specifically because (a) the transport is
// LAN-only (the listener is pinned to the LAN IP, not the public interface),
// and (b) trust is established by the shared fabric secret carried inside the
// TLS tunnel. A man-in-the-middle on the LAN who lacks the secret cannot read
// or inject registry state. Do NOT reuse this client for cloud calls.
//
// This used to be comment-only, and FABRIC-SSRF-01 was main.go doing exactly
// what the comment says not to: wiring this client as both
// RendezvousDiscoverer.HTTPClient and the shared fabric.Config.HTTPClient
// that pull/push then dialled a relay-resolved WAN peer with. It is no longer
// possible to repeat that mistake by accident — both of those fields are now
// typed *WANClient (see NewWANClient below), a distinct type this function
// does not return, so the compiler rejects the reuse instead of a comment
// hoping nobody does it.
func NewLANClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				// LAN peers are dialled by IP; the LAN cert has no IP SAN match
				// for arbitrary peers. Trust comes from the shared secret inside
				// the tunnel (see doc above), so skip name verification here.
				InsecureSkipVerify: true, //nolint:gosec // LAN-only; auth via X-Fabric-Auth
			},
			TLSHandshakeTimeout: 5 * time.Second,
			MaxIdleConns:        10,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

func drainClose(rc io.ReadCloser) {
	if rc == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, 1<<16))
	_ = rc.Close()
}

// ── errors ──────────────────────────────────────────────────────────────────

type fabricError string

func (e fabricError) Error() string { return string(e) }

func errFabric(msg string) error { return fabricError("fabric: " + msg) }

func errFabricWrap(msg string, err error) error {
	return fmt.Errorf("fabric: %s: %w", msg, err)
}
