// fabric.go — SFU-host registration with the vulos-relay fabric (HTTPS contract).
//
// For DISCOVERY — telling the relay "I run an SFU, reach me at <endpoint>, here
// is my identity" — meethost speaks a tiny HTTPS/JSON contract, exactly like
// gpuhost does for the GPU streaming host. Defining it here keeps meethost
// zero-dep from vulos-relay's Go code; the wire shape is a small, stable JSON
// contract the relay serves at /api/meet/host/* (SFU Phase 2).
//
// Wire shape (HTTPS, JSON):
//
//	POST {RelayBaseURL}/api/meet/host/register      → 200 (verifies endpoint)
//	POST {RelayBaseURL}/api/meet/host/heartbeat     → 200 (refreshes TTL)
//	POST {RelayBaseURL}/api/meet/host/deregister    → 200
//
// The relay VERIFIES the advertised Endpoint (reachable + ownership-proven via a
// nonce echo, SSRF-guarded) before it will surface it to clients — see
// vulos-relay/tunnel/server/directprobe.go. So the box MUST serve the relay's
// direct-probe path on the SAME public TLS listener it advertises as Endpoint.
package meethost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HostRegistration is the JSON wire shape POSTed to the relay's
// /api/meet/host/{register,heartbeat,deregister} endpoints. It deliberately
// mirrors gpuhost.HostRegistration so the two BYO-worker slices stay parallel.
type HostRegistration struct {
	// HostID is the stable fabric identifier this box claims (its VulaID).
	HostID string `json:"host_id"`
	// PublicKeyB64 is the box's Ed25519 public key (base64-standard). Informational
	// at Phase 2 (the endpoint ownership proof is the relay's nonce echo).
	PublicKeyB64 string `json:"public_key_b64,omitempty"`
	// Domain is the box's fabric authority domain (informational).
	Domain string `json:"domain,omitempty"`
	// Name is the token-authorized tunnel name this box serves. The bearer token
	// must grant it — the relay refuses a register for a name the token does not.
	Name string `json:"name"`
	// Endpoint is the advertised SFU serverUrl a client should connect to: a public
	// https:// base URL. Verified (reachable + owned) by the relay before storage.
	Endpoint string `json:"endpoint"`
	// Capabilities describes the SFU's capacity (advisory matchmaking metadata).
	Capabilities HostCapabilities `json:"capabilities"`
}

// HostCapabilities is the SFU-capacity subset the relay surfaces to the
// allocation step for matchmaking. The SFU's own token gate is the security
// boundary, not these fields.
type HostCapabilities struct {
	// MaxParticipants is the per-room cap (the in-process Pion SFU is 50).
	MaxParticipants int `json:"max_participants"`
	// HasE2EE reports whether the host supports client-side E2EE. For a self-host /
	// BYO host this is typically FALSE — the operator owns the media node, so no
	// E2EE is needed (SFU.md §4, LOCKED stance: self-host = your own infra).
	HasE2EE bool `json:"has_e2ee"`
	// Region is an optional locality hint (e.g. "eu"). Informational.
	Region string `json:"region,omitempty"`
	// Codec is the negotiated video codec (informational).
	Codec string `json:"codec,omitempty"`
}

// fabricClient is the seam between meethost.Service and the relay. Tests supply a
// stub via Config.fabricOverride; production wires httpFabricClient.
type fabricClient interface {
	Register(ctx context.Context, r HostRegistration) error
	Heartbeat(ctx context.Context, r HostRegistration) error
	Deregister(ctx context.Context, r HostRegistration) error
}

// httpFabricClient is the production fabric client. One per Service.
type httpFabricClient struct {
	base   string
	token  string
	client *http.Client
}

func newHTTPFabricClient(base, token string, client *http.Client) fabricClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &httpFabricClient{
		base:   strings.TrimRight(base, "/"),
		token:  token,
		client: client,
	}
}

func (c *httpFabricClient) Register(ctx context.Context, r HostRegistration) error {
	return c.post(ctx, "/api/meet/host/register", r)
}

func (c *httpFabricClient) Heartbeat(ctx context.Context, r HostRegistration) error {
	return c.post(ctx, "/api/meet/host/heartbeat", r)
}

func (c *httpFabricClient) Deregister(ctx context.Context, r HostRegistration) error {
	return c.post(ctx, "/api/meet/host/deregister", r)
}

// post JSON-marshals r and POSTs it to base+path with the box's bearer token,
// returning an error if the response is not 2xx.
func (c *httpFabricClient) post(ctx context.Context, path string, r HostRegistration) error {
	if c.base == "" {
		return errors.New("meethost/fabric: RelayBaseURL is empty")
	}
	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("fabric: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		// The relay authorizes the SFU-host register with the SAME bearer token +
		// name grant the box uses for its tunnel.
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("fabric: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fabric: POST %s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}
