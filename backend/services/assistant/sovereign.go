package assistant

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"vulos/backend/services/ai"
)

// ErrEgressBlocked is returned when a completion would send mail content to an
// endpoint whose sovereignty tier is not permitted. It is the hard enforcement
// of the "you choose where your AI runs, and it is never sent to a company that
// mines you" guarantee.
var ErrEgressBlocked = errors.New("assistant: this endpoint's sovereignty tier is not permitted — mail content stays inside the sovereignty boundary (use a local/sovereign endpoint, or set VULOS_ASSISTANT_ALLOW_EXTERNAL=1 to authorize a brokered/external one)")

// Tier is the sovereignty dial: WHERE your AI runs, ordered most→least private.
// The four values are shared byte-for-byte with the llmux gateway so the posture
// is consistent across the whole suite.
type Tier string

const (
	// TierLocal — inference on THIS box (loopback / unix socket). Mail content
	// never leaves the instance. Always allowed.
	TierLocal Tier = "local"
	// TierSovereign — an OPERATOR-DECLARED off-box endpoint the operator asserts
	// is in-region / no-train and inside their sovereignty boundary. Vulos does
	// NOT operate or verify it; the claim is the operator's, not ours. Allowed by
	// default (the operator vouched for it), but it is OFF-box, so it is only
	// reached via an EXPLICIT operator declaration — never inferred from a
	// private IP.
	TierSovereign Tier = "sovereign"
	// TierBrokered — a named third-party model under a no-train agreement,
	// operator-configured. Allowed ONLY when the operator opts in.
	TierBrokered Tier = "brokered"
	// TierExternal — any other off-box endpoint (may mine / train). Blocked
	// unless the explicit allow_egress escape hatch is set. This is also the
	// fail-closed bucket for anything unclassifiable.
	TierExternal Tier = "external"
)

// TierLabel returns the honest human UI label for a tier. These strings are the
// shared contract with the llmux side and the Assistant badge/picker.
func TierLabel(t Tier) string {
	switch t {
	case TierLocal:
		return "On your device"
	case TierSovereign:
		return "Operator-declared endpoint (unverified)"
	case TierBrokered:
		return "Brokered · no-train"
	default:
		return "External · not private"
	}
}

// NormalizeTier parses an operator-declared tier string, returning "" for
// anything that is not one of the four canonical values (so callers fail closed).
func NormalizeTier(s string) Tier {
	switch Tier(strings.ToLower(strings.TrimSpace(s))) {
	case TierLocal:
		return TierLocal
	case TierSovereign:
		return TierSovereign
	case TierBrokered:
		return TierBrokered
	case TierExternal:
		return TierExternal
	default:
		return ""
	}
}

// Sovereignty is the machine-readable state of the "where your AI runs"
// guarantee, surfaced to the UI so the posture is visible and auditable.
type Sovereignty struct {
	Provider        string `json:"provider"`
	Endpoint        string `json:"endpoint"`
	Model           string `json:"model"`
	Tier            Tier   `json:"tier"`
	Label           string `json:"label"`
	Local           bool   `json:"local"`
	Allowed         bool   `json:"allowed"`
	ExternalAllowed bool   `json:"external_allowed"`
	Reason          string `json:"reason"`
}

// Evaluate computes the sovereignty state for a model config. allowExternal is
// the operator opt-in (VULOS_ASSISTANT_ALLOW_EXTERNAL) that authorizes the
// brokered/external tiers. It performs no I/O.
func Evaluate(cfg ai.Config, allowExternal bool) Sovereignty {
	tier := classifyTier(cfg)
	allowed := tierAllowed(tier, allowExternal)
	s := Sovereignty{
		Provider:        string(cfg.Provider),
		Endpoint:        cfg.Endpoint,
		Model:           cfg.Model,
		Tier:            tier,
		Label:           TierLabel(tier),
		Local:           tier == TierLocal,
		Allowed:         allowed,
		ExternalAllowed: allowExternal,
	}
	switch {
	case tier == TierLocal:
		s.Reason = "model runs on this device; mail content never leaves the box"
	case tier == TierSovereign:
		s.Reason = "operator-declared off-box endpoint (asserted in-region / no-train by the operator); not operated or verified by Vulos"
	case tier == TierBrokered && allowed:
		s.Reason = "named third-party under a no-train agreement; authorized by the operator"
	case tier == TierBrokered:
		s.Reason = "brokered endpoint is not authorized; opt in (VULOS_ASSISTANT_ALLOW_EXTERNAL=1) to allow"
	case allowed:
		s.Reason = "external endpoint explicitly authorized by the operator"
	default:
		s.Reason = "configured model is off-box and external egress is not permitted"
	}
	return s
}

// Guard enforces the guarantee: it returns ErrEgressBlocked unless the target
// endpoint's tier is permitted (local + sovereign always; brokered + external
// only with the operator opt-in). Every skill calls this before any mail content
// is sent to a model — it is the single choke point.
func Guard(cfg ai.Config, allowExternal bool) error {
	if !tierAllowed(classifyTier(cfg), allowExternal) {
		return ErrEgressBlocked
	}
	return nil
}

// tierAllowed applies the default-deny policy: local + sovereign are always
// allowed; brokered + external require the operator opt-in; anything else
// (unclassifiable) is denied.
func tierAllowed(t Tier, allowExternal bool) bool {
	switch t {
	case TierLocal, TierSovereign:
		return true
	case TierBrokered, TierExternal:
		return allowExternal
	default:
		return false
	}
}

// classifyTier maps a model config onto the sovereignty dial.
//
//   - A verifiably on-instance endpoint (loopback / unix socket) is TierLocal,
//     regardless of any declaration — it is the most private tier.
//   - Otherwise the endpoint is OFF-box: we honour an EXPLICIT operator
//     declaration of "sovereign" or "brokered". This is never inferred from a
//     private IP — the F4/F5 hardening (loopback-only for local) is intact, so
//     a LAN / .local / .internal host is NOT silently trusted.
//   - Everything else off-box (declared "external", an unverifiable "local"
//     declaration, or unmarked/unknown) fails closed to TierExternal.
func classifyTier(cfg ai.Config) Tier {
	if isLocalConfig(cfg) {
		return TierLocal
	}
	switch NormalizeTier(cfg.Tier) {
	case TierSovereign:
		return TierSovereign
	case TierBrokered:
		return TierBrokered
	default:
		return TierExternal
	}
}

// isLocalConfig reports whether the model config points at an on-instance
// endpoint. The fixed-cloud providers (Claude, OpenAI) are never local. Ollama
// and custom OpenAI-compatible endpoints are local iff their host is a loopback
// address / reserved localhost name, or a unix socket.
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
// *.internal / *.local name is a DIFFERENT machine: reaching it IS off-box
// egress and must be an explicit operator declaration (sovereign/brokered) or
// opt-in, never silently reported as "local". Only loopback and the reserved
// localhost names qualify.
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
