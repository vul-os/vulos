// config.go — meethost Config.

package meethost

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// FabricIdentity is the box's fabric-level identity (Ed25519), the SAME identity
// used by peering / LAN reachability / gpuhost. meethost does NOT generate it; the
// caller supplies it so a box has exactly one fabric identity across slices.
type FabricIdentity struct {
	// HostID is the canonical SFU-host identifier on the fabric (the box's VulaID).
	// It MUST be stable across restarts.
	HostID string
	// PublicKeyB64 is the base64-standard Ed25519 public key bytes. Informational.
	PublicKeyB64 string
	// Domain is the authority domain this box claims. Informational.
	Domain string
}

// Config configures a meethost Service. Time-valued fields default when zero;
// required fields are validated by validate().
type Config struct {
	// Enabled is a dashboard-persisted flag, independent of the VULOS_SFU_HOST env
	// var. Either being true should cause the caller to construct a Service. The
	// Service itself does not check Enabled.
	Enabled bool

	// Identity is the box's fabric identity. Required.
	Identity FabricIdentity

	// RelayBaseURL is the HTTPS base URL of a vulos-relay node that exposes the
	// SFU-host registry (/api/meet/host/*). Required (empty ⇒ nowhere to register).
	RelayBaseURL string

	// Token is the bearer token the relay authorizes the register with — the SAME
	// token + name grant the box uses for its tunnel. Empty ⇒ unbilled/self-host
	// token (the relay allows an empty-account token but still checks the name
	// grant, so a Name is required regardless).
	Token string

	// Name is the token-authorized tunnel name this box serves (the relay refuses a
	// register for a name the token does not grant). Required.
	Name string

	// Endpoint is the advertised SFU serverUrl clients connect to: a public https://
	// base URL served on the SAME public TLS listener that answers the relay's
	// direct-probe path. Required — an SFU host with no reachable endpoint is
	// useless, so verification (and therefore registration) fails without it.
	Endpoint string

	// WorkerBinary, if set, is an EXTERNAL SFU worker binary meethost supervises
	// (e.g. a co-located vulos-meet LiveKit server). When EMPTY, meethost assumes
	// the SFU is already running IN-PROCESS in the OS binary (the Pion SFU at
	// backend/services/peering/sfu) and only performs registration + heartbeats —
	// there is no external process to supervise. This is the common self-host case.
	WorkerBinary string
	// WorkerArgs are passed before the auto-added "--config <path>" pair (only used
	// when WorkerBinary is set).
	WorkerArgs []string

	// Capabilities the box advertises (participant cap, e2ee, region, codec).
	Capabilities HostCapabilities

	// HeartbeatInterval is how often the Service re-POSTs so a stale host is pruned
	// on crash. Defaults to 30s (must stay well under the relay's 90s TTL).
	HeartbeatInterval time.Duration
	// RegisterTimeout caps each registration/heartbeat HTTP call. Defaults to 10s.
	RegisterTimeout time.Duration
	// RestartBackoff is the initial supervisor back-off (only when WorkerBinary set).
	// Defaults to 1s.
	RestartBackoff time.Duration

	// HTTPClient overrides the default http.Client used by the fabric calls (tests).
	HTTPClient *http.Client

	// fabricOverride is a test seam — when non-nil, New uses it instead of the
	// production HTTPS client. Unexported so production cannot bypass the real relay.
	fabricOverride fabricClient
}

// validate enforces the Config invariants. Called from New.
func (c *Config) validate() error {
	if c.Identity.HostID == "" {
		return errors.New("meethost: Config.Identity.HostID is required")
	}
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("meethost: Config.Name is required (the token-authorized tunnel name)")
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.New("meethost: Config.Endpoint is required (the advertised SFU serverUrl)")
	}
	if strings.TrimSpace(c.RelayBaseURL) == "" {
		return errors.New("meethost: Config.RelayBaseURL is required")
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
	if c.Capabilities.MaxParticipants <= 0 {
		// Mirror the in-process Pion SFU cap (useSFUCall.js MAX_SFU_PARTICIPANTS = 50).
		c.Capabilities.MaxParticipants = 50
	}
}
