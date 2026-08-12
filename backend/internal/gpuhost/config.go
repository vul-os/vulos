// no-broker-dep:allow-file: doc comment on RelayBaseURL names 'the Pier node' an operator points
// this box at, and states plainly: 'Required: there is no default,
// because nobody runs a relay on the owner's behalf.' Names the broker to
// state its ABSENCE as a default.

// config.go — gpuhost Config + external-streamer config-file generator.

package gpuhost

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FabricIdentity is the box's fabric-level identity (Ed25519). It is the same
// identity used by LAN reachability (backend/internal/lan/) and by peering
// (backend/services/peering/). gpuhost does NOT generate it; it is supplied by
// the caller so a single box has exactly one fabric identity across slices.
//
// Concretely the caller derives HostID from peering.Service.VulosID() (or, on
// older boxes, the lan instance ULID) and PublicKeyB64 from the same Ed25519
// public key. Domain follows the existing fabric convention
// (e.g. <ulid>.vulos.org or the configured authority domain).
type FabricIdentity struct {
	// HostID is the canonical stream-host identifier on the fabric. It MUST
	// be stable across restarts. The cloud session orchestrator references
	// boxes by this ID.
	HostID string
	// PublicKeyB64 is the base64-standard Ed25519 public key bytes (32 raw
	// bytes encoded). It is what the relay/cloud uses to verify the host's
	// signature on later signaling messages.
	PublicKeyB64 string
	// Domain is the authority domain this box claims (e.g. the box's
	// fabric domain). Used as the "from" in VULOS-STREAM/1 envelopes.
	Domain string
}

// Config configures a gpuhost Service.
//
// All time-valued fields default to a sensible value if zero. Required fields
// are validated by validate(); missing values produce a clear error from New.
type Config struct {
	// Enabled is a dashboard-persisted flag. It is independent of the
	// VULOS_GPU_HOST env var; either being true should cause the caller to
	// construct a Service. The Service itself does not check Enabled.
	Enabled bool

	// Identity is the box's fabric identity. Required.
	Identity FabricIdentity

	// RelayBaseURL is the HTTPS base URL of the Pier node that exposes the
	// host-registration endpoint. Required: there is no default, because nobody
	// runs a relay on the owner's behalf.
	RelayBaseURL string

	// StreamerBinary is the absolute path to the external WebRTC+NVENC
	// streaming server binary (Sunshine / Moonlight host / equivalent).
	// Required.
	StreamerBinary string

	// StreamerArgs are command-line arguments passed before the auto-added
	// "--config <path>" pair.
	StreamerArgs []string

	// ConfigDir is the directory the generated streamer config is written
	// to. Defaults to a tempdir under os.TempDir.
	ConfigDir string

	// AdvertiseHostname / AdvertisePort are the (host:port) the client will
	// be told to dial for direct-P2P media. Typically a public/UPnP-mapped
	// address or the LAN address for same-LAN clients.
	AdvertiseHostname string
	AdvertisePort     int

	// Codec is the negotiated video codec (e.g. "h264", "h265", "av1").
	// Recorded in HostCapabilities; the streamer enforces it.
	Codec string
	// MaxBitrateKbps caps the streamer's outbound bitrate (Kbps).
	MaxBitrateKbps int
	// GPUVendor is one of "nvidia"|"amd"|"intel"|"cpu". Informational.
	GPUVendor string
	// HasHardwareEncoder is true when NVENC / VCE / QSV is available.
	HasHardwareEncoder bool

	// HeartbeatInterval is how often the Service re-POSTs to the relay's
	// registration endpoint so a stale host is pruned on crash. Defaults
	// to 30s.
	HeartbeatInterval time.Duration

	// RegisterTimeout caps each registration / heartbeat HTTP call.
	// Defaults to 10s.
	RegisterTimeout time.Duration

	// RestartBackoff is the initial back-off used by the supervisor after
	// the streamer exits. Defaults to 1s; the supervisor doubles up to a
	// cap of 30s.
	RestartBackoff time.Duration

	// P2PProbeTimeout is how long the path chooser waits for a P2P probe
	// to succeed before falling back to TURN. Defaults to 5s.
	P2PProbeTimeout time.Duration

	// HTTPClient overrides the default http.Client used by the fabric
	// registration calls. Useful for tests.
	HTTPClient *http.Client

	// fabricOverride is a test seam — when non-nil, gpuhost.New uses this
	// instead of constructing the production HTTPS client. Unexported so
	// production callers cannot bypass the real fabric.
	fabricOverride fabricClient
}

// validate enforces the Config invariants. Called from New.
func (c *Config) validate() error {
	if c.Identity.HostID == "" {
		return errors.New("gpuhost: Config.Identity.HostID is required")
	}
	if c.Identity.PublicKeyB64 == "" {
		return errors.New("gpuhost: Config.Identity.PublicKeyB64 is required")
	}
	if c.Identity.Domain == "" {
		return errors.New("gpuhost: Config.Identity.Domain is required")
	}
	if strings.TrimSpace(c.StreamerBinary) == "" {
		return errors.New("gpuhost: Config.StreamerBinary is required")
	}
	return nil
}

// applyDefaults fills in zero-valued fields. Idempotent.
func (c *Config) applyDefaults() {
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = 30 * time.Second
	}
	if c.RegisterTimeout <= 0 {
		c.RegisterTimeout = 10 * time.Second
	}
	if c.RestartBackoff <= 0 {
		c.RestartBackoff = 1 * time.Second
	}
	if c.P2PProbeTimeout <= 0 {
		c.P2PProbeTimeout = 5 * time.Second
	}
	if c.Codec == "" {
		c.Codec = "h264"
	}
	if c.AdvertisePort == 0 {
		// 47989 is the Sunshine HTTPS default; not load-bearing for us, just
		// a non-zero placeholder so the registration JSON is well-formed.
		c.AdvertisePort = 47989
	}
}

// writeStreamerConfig renders the streamer's config file into ConfigDir.
//
// The generated format is intentionally simple key=value lines, which is the
// shape Sunshine and most NVENC daemons accept. We do NOT pretend to cover
// every streamer's full config grammar — the operator can layer overrides via
// StreamerArgs. The point of this file is so the streamer starts with the
// codec / bitrate / port the rest of the slice has advertised to the fabric.
func (c *Config) writeStreamerConfig() (string, error) {
	dir := c.ConfigDir
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "vulos-gpuhost-")
		if err != nil {
			return "", err
		}
		c.ConfigDir = dir
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(dir, "streamer.conf")
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by vulos/backend/internal/gpuhost — do not edit by hand.\n")
	fmt.Fprintf(&b, "host_id=%s\n", c.Identity.HostID)
	fmt.Fprintf(&b, "domain=%s\n", c.Identity.Domain)
	fmt.Fprintf(&b, "advertise_hostname=%s\n", c.AdvertiseHostname)
	fmt.Fprintf(&b, "advertise_port=%d\n", c.AdvertisePort)
	fmt.Fprintf(&b, "codec=%s\n", c.Codec)
	fmt.Fprintf(&b, "max_bitrate_kbps=%d\n", c.MaxBitrateKbps)
	fmt.Fprintf(&b, "gpu_vendor=%s\n", c.GPUVendor)
	fmt.Fprintf(&b, "hardware_encoder=%v\n", c.HasHardwareEncoder)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
