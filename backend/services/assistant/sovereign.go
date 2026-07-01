package assistant

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"vulos/backend/services/ai"
)

// ErrEgressBlocked is returned when a completion would send mail content to a
// non-local endpoint that the user has not explicitly authorized. This is the
// hard enforcement of the sovereign "nothing leaves your server" guarantee.
var ErrEgressBlocked = errors.New("assistant: external AI egress is blocked — mail content stays on this instance (set VULOS_ASSISTANT_ALLOW_EXTERNAL=1 and configure an endpoint to override)")

// Egress classifies where a completion's data goes.
type Egress string

const (
	// EgressOnInstance — the model runs on this box (loopback / private LAN /
	// unix socket). Mail content never leaves the instance. This is the default
	// and the product promise.
	EgressOnInstance Egress = "on-instance"
	// EgressExternalConfigured — the model is a third-party/off-box endpoint the
	// user explicitly opted into. Mail content leaves the instance BY CHOICE.
	EgressExternalConfigured Egress = "external-configured"
	// EgressBlocked — the configured model is off-box and external egress is NOT
	// permitted; the assistant refuses to run rather than leak mail.
	EgressBlocked Egress = "blocked"
)

// Sovereignty is the machine-readable state of the no-egress guarantee, surfaced
// to the UI so the promise is visible and auditable to the user.
type Sovereignty struct {
	Provider        string `json:"provider"`
	Endpoint        string `json:"endpoint"`
	Model           string `json:"model"`
	Local           bool   `json:"local"`
	ExternalAllowed bool   `json:"external_allowed"`
	Egress          Egress `json:"egress"`
	Reason          string `json:"reason"`
}

// Evaluate computes the sovereignty state for a model config. allowExternal is
// the operator opt-in (VULOS_ASSISTANT_ALLOW_EXTERNAL). It performs no I/O.
func Evaluate(cfg ai.Config, allowExternal bool) Sovereignty {
	s := Sovereignty{
		Provider:        string(cfg.Provider),
		Endpoint:        cfg.Endpoint,
		Model:           cfg.Model,
		ExternalAllowed: allowExternal,
	}
	s.Local = isLocalConfig(cfg)
	switch {
	case s.Local:
		s.Egress = EgressOnInstance
		s.Reason = "model runs on this instance; mail content never leaves the box"
	case allowExternal:
		s.Egress = EgressExternalConfigured
		s.Reason = "external endpoint explicitly authorized by the operator"
	default:
		s.Egress = EgressBlocked
		s.Reason = "configured model is off-box and external egress is not permitted"
	}
	return s
}

// Guard enforces the guarantee: it returns ErrEgressBlocked unless the
// completion target is on-instance, or the operator has explicitly authorized
// external egress. Every skill calls this before any mail content is sent to a
// model — it is the single choke point.
func Guard(cfg ai.Config, allowExternal bool) error {
	if Evaluate(cfg, allowExternal).Egress == EgressBlocked {
		return ErrEgressBlocked
	}
	return nil
}

// isLocalConfig reports whether the model config points at an on-instance
// endpoint. The fixed-cloud providers (Claude, OpenAI) are never local. Ollama
// and custom OpenAI-compatible endpoints are local iff their host is a
// loopback, private (RFC1918/ULA), link-local, *.local/*.internal name, or a
// unix socket.
func isLocalConfig(cfg ai.Config) bool {
	switch cfg.Provider {
	case ai.ProviderClaude, ai.ProviderOpenAI:
		return false
	case ai.ProviderOllama, ai.ProviderCustom:
		return isLocalEndpoint(cfg.Endpoint)
	default:
		// Unknown provider: fail closed (treat as non-local).
		return false
	}
}

func isLocalEndpoint(endpoint string) bool {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		// Empty endpoint means the default localhost target for Ollama.
		return true
	}
	if strings.HasPrefix(endpoint, "unix:") || strings.HasPrefix(endpoint, "/") {
		return true
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return isLocalHost(u.Hostname())
}

// isLocalHost reports whether host is truly THIS instance (loopback). A LAN /
// private-network host (192.168.x, 10.x, 172.16.x, ULA, link-local) or a
// *.internal / *.local name is a DIFFERENT machine: sending mail there IS
// off-box egress and must be an explicit operator opt-in
// (VULOS_ASSISTANT_ALLOW_EXTERNAL), never silently reported as "on-instance".
// Only loopback and the reserved localhost names qualify.
func isLocalHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	// RFC 6761: "localhost" and *.localhost resolve to loopback by convention.
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
