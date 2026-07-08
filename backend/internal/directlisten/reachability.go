package directlisten

// reachability.go — a box-side self-reachability check for the advertised direct
// endpoint. Before a box tells the relay "advertise my direct endpoint", it can
// confirm the endpoint is actually reachable FROM OUTSIDE (not firewalled) by
// fetching its OWN probe path over the public endpoint with a fresh nonce and
// checking the echo. This avoids advertising a direct endpoint that a firewall/NAT
// silently drops — the client would otherwise attempt direct, time out, and fall
// back (correct, but slower). The relay performs the authoritative external probe;
// this is a fast local pre-check.
//
// It is NOT SSRF-exploitable: it only ever fetches s.endpoint (this box's own
// configured, https, hostname-based advertised endpoint) — never a caller-supplied
// URL. The endpoint was validated as https at construction.

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"
)

// selfProbeTimeout bounds the self-reachability check.
const selfProbeTimeout = 6 * time.Second

// CheckReachable fetches this box's OWN probe path over its advertised public
// endpoint and returns nil iff the endpoint answered and echoed our nonce. A
// non-nil error means the endpoint is not (yet) externally reachable — the caller
// should NOT advertise it as a usable direct endpoint. insecureTLS skips cert
// verification (TEST-ONLY, for self-signed loopback listeners); production leaves
// it false so a real cert is validated.
func (s *Service) CheckReachable(ctx context.Context, insecureTLS bool) error {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return errReach("nonce")
	}
	nonce := hex.EncodeToString(b[:])

	cctx, cancel := context.WithTimeout(ctx, selfProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, s.endpoint+ProbePath, nil)
	if err != nil {
		return errReach("request")
	}
	req.Header.Set(ProbeHeader, nonce)

	tr := &http.Transport{DisableKeepAlives: true}
	if insecureTLS {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // TEST-ONLY self-signed loopback
	}
	client := &http.Client{Timeout: selfProbeTimeout, Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errReach("redirect") }}

	resp, err := client.Do(req)
	if err != nil {
		return errReach("unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errReach("unreachable")
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProbeNonce))
	if strings.TrimSpace(string(body)) != nonce {
		return errReach("nonce mismatch")
	}
	return nil
}

// setEndpointForTest overrides the advertised endpoint. TEST-ONLY: lets a test
// point the self-reachability check at the actual ephemeral loopback address the
// listener bound, without a racy rebind of a freed port.
func (s *Service) setEndpointForTest(ep string) { s.endpoint = ep }

type reachErr struct{ s string }

func (e reachErr) Error() string { return "directlisten: " + e.s }

func errReach(s string) error { return reachErr{s} }
