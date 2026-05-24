// fabric.go — host registration with the vulos-relay fabric (HTTPS contract).
//
// The relay's VULOS-STREAM/1 sub-protocol (vulos-relay/internal/relay/
// streamsignal.go) is a peering-envelope protocol used between two boxes
// once a session is in flight. For *discovery* — telling the relay "I am a
// streaming host, reach me at <addr>, here is my pubkey" — we use a tiny
// HTTPS contract. Defining it here keeps gpuhost zero-dep from vulos-relay's
// Go code (per task brief: "you don't need to import vulos-relay's Go code —
// just speak its wire format / use HTTPS to a configured relay endpoint").
//
// Wire shape (HTTPS, JSON):
//
//	POST {RelayBaseURL}/api/stream/host/register      → 200 (idempotent)
//	POST {RelayBaseURL}/api/stream/host/heartbeat     → 200
//	POST {RelayBaseURL}/api/stream/host/deregister    → 200
//	GET  {RelayBaseURL}/api/stream/host/{id}/probe    → 200 if reachable
//
// All POST bodies are HostRegistration JSON. Responses are not parsed beyond
// the HTTP status code (the relay echoes the host record back, but we don't
// need to consume it for the supervisor to make decisions).

package gpuhost

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
// /api/stream/host/{register,heartbeat,deregister} endpoints.
type HostRegistration struct {
	// HostID is the stable fabric identifier this box claims.
	HostID string `json:"host_id"`
	// PublicKeyB64 is the box's Ed25519 public key (base64-standard, 32 raw bytes).
	PublicKeyB64 string `json:"public_key_b64"`
	// Domain is the authority domain the box claims in VULOS-STREAM/1 envelopes.
	Domain string `json:"domain"`
	// Hostname is the advertised host (FQDN or IP) the client should dial.
	Hostname string `json:"hostname"`
	// Port is the advertised UDP/TCP port the streamer listens on.
	Port int `json:"port"`
	// Capabilities describes what this host can encode.
	Capabilities HostCapabilities `json:"capabilities"`
}

// HostCapabilities is the subset of the streamer config the relay surfaces
// to the cloud orchestrator for matchmaking.
type HostCapabilities struct {
	Codec       string `json:"codec"`
	MaxBitrate  int    `json:"max_bitrate_kbps"`
	GPUVendor   string `json:"gpu_vendor"`
	HasHardware bool   `json:"has_hardware_encoder"`
}

// fabricClient is the seam between gpuhost.Service and the relay. Tests
// supply a stub via Config.fabricOverride; production wires httpFabricClient.
type fabricClient interface {
	Register(ctx context.Context, r HostRegistration) error
	Heartbeat(ctx context.Context, r HostRegistration) error
	Deregister(ctx context.Context, hostID string) error
}

// httpFabricClient is the production fabric client. One per Service.
type httpFabricClient struct {
	base   string
	id     FabricIdentity
	client *http.Client
}

func newHTTPFabricClient(base string, id FabricIdentity, client *http.Client) fabricClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &httpFabricClient{
		base:   strings.TrimRight(base, "/"),
		id:     id,
		client: client,
	}
}

func (c *httpFabricClient) Register(ctx context.Context, r HostRegistration) error {
	return c.post(ctx, "/api/stream/host/register", r)
}

func (c *httpFabricClient) Heartbeat(ctx context.Context, r HostRegistration) error {
	return c.post(ctx, "/api/stream/host/heartbeat", r)
}

func (c *httpFabricClient) Deregister(ctx context.Context, hostID string) error {
	// Deregister carries only the HostID; we reuse HostRegistration with just
	// the ID populated so the relay handler sees a uniform body shape.
	return c.post(ctx, "/api/stream/host/deregister", HostRegistration{HostID: hostID})
}

// post JSON-marshals r and POSTs it to base+path, returning an error if the
// response is not 2xx. The response body is consumed (and limited) so
// connections may be reused.
func (c *httpFabricClient) post(ctx context.Context, path string, r HostRegistration) error {
	if c.base == "" {
		return errors.New("gpuhost/fabric: RelayBaseURL is empty")
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
	req.Header.Set("X-Vulos-Host-ID", c.id.HostID)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("fabric: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	// Drain a bounded amount so the underlying connection is reusable.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fabric: POST %s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}
