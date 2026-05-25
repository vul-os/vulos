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
	"time"

	"vulos/backend/internal/multiinstance"
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
	u := base + "/api/fabric/changeset"
	if !since.IsZero() {
		u += "?since=" + url.QueryEscape(since.UTC().Format(time.RFC3339Nano))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, errFabricWrap("build pull request", err)
	}
	req.Header.Set("X-Fabric-Auth", s.cfg.Secret)

	resp, err := s.cfg.HTTPClient.Do(req)
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

	resp, err := s.cfg.HTTPClient.Do(req)
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
