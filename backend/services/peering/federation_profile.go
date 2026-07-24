// federation_profile.go — the sovereign-federation config profile.
//
// Central-optional reachability (peering resolve.go, directlisten, relay
// deposit/pickup, ICE/STUN/TURN) is each independently config-gated by its own
// env var today. This file surfaces them as ONE coherent, self-reporting
// profile — a single place an operator (or the cockpit UI) can see the box's
// current federation posture, and a single place future code should read the
// same env vars from, so they never drift apart.
//
// Env surface (all optional; every field defaults to "off"/central):
//
//	VULOS_RELAY_BASE_URL    the box's configured Ephor instance — used
//	                        both for reachability resolve (resolve.go) and, by
//	                        other subsystems (gpuhost), for capability
//	                        registration. Self-hostable (see Ephor,
//	                        github.com/vul-os/ephor).
//	VULOS_VERIFY_URL        the directory/verify service base URL (discovery.go
//	                        + email verification). Defaults to the central
//	                        vulos.org directory when unset.
//	VULOS_RENDEZVOUS_URL    the Drop proximity-code rendezvous service
//	                        (drop_proximity.go) — an unauthenticated 6-digit
//	                        code exchange for devices on different networks.
//	                        Defaults to the central https://rendezvous.vulos.org
//	                        when unset.
//	TURN_SECRET / TURN_HOST self-hosted TURN (network/turn.go); when set, the
//	                        ICE config (ice.go) also answers plain STUN on the
//	                        SAME port (selfHostedSTUNURLs) — zero extra infra.
//	VULOS_STUN_DISABLE_PUBLIC opts out of the public Google STUN fallback
//	                        entirely (ice.go's stunURLs) — the last piece
//	                        needed for a box that wants NO third-party network
//	                        dependency for call setup.
//
// RELAY-01 additions: RelayProvider and Ingress report the box's
// owner-selected relay/reachability provider (backend/services/relayconfig —
// ephor by default) and its resolved ingress posture, so this profile stays
// the ONE coherent place to see the box's full federation/reachability
// stance, rather than a second, parallel status struct.
package peering

import (
	"net/http"
	"os"
	"strings"

	"vulos/backend/services/relayconfig"
)

// FederationProfile is the box's current sovereign-federation configuration,
// safe to serialize to a client (no secrets — only whether each piece is
// configured, plus the non-secret URLs).
type FederationProfile struct {
	// RelayConfigured reports whether VULOS_RELAY_BASE_URL is set.
	RelayConfigured bool `json:"relay_configured"`
	// RelayBaseURL is the configured relay base URL (empty when unconfigured).
	RelayBaseURL string `json:"relay_base_url,omitempty"`

	// VerifyConfigured reports whether VULOS_VERIFY_URL overrides the default
	// central vulos.org directory.
	VerifyConfigured bool `json:"verify_configured"`
	// VerifyURL is the effective directory/verify base URL (always non-empty
	// — defaults to the central vulos.org directory).
	VerifyURL string `json:"verify_url"`

	// RendezvousConfigured reports whether VULOS_RENDEZVOUS_URL overrides the
	// default central rendezvous.vulos.org (drop_proximity.go).
	RendezvousConfigured bool `json:"rendezvous_configured"`
	// RendezvousURL is the effective Drop proximity-code rendezvous base URL
	// (always non-empty — defaults to the central rendezvous.vulos.org).
	RendezvousURL string `json:"rendezvous_url"`

	// TURNEnabled reports whether self-hosted TURN is configured (TURN_SECRET set).
	TURNEnabled bool `json:"turn_enabled"`
	// TURNHost is the host clients dial for TURN (only meaningful when TURNEnabled).
	TURNHost string `json:"turn_host,omitempty"`

	// PublicSTUNDisabled reports whether the public Google STUN fallback has
	// been opted out of (VULOS_STUN_DISABLE_PUBLIC).
	PublicSTUNDisabled bool `json:"public_stun_disabled"`

	// NoThirdPartySTUN is true when this box needs NO third-party STUN server
	// at all to set up calls: TURN is self-hosted (which also answers STUN)
	// AND the public STUN fallback has been explicitly disabled. This is the
	// "fully sovereign for call setup" signal the plan calls for.
	NoThirdPartySTUN bool `json:"no_third_party_stun"`

	// RelayProvider is the box's currently-selected relay/reachability
	// provider (RELAY-01, backend/services/relayconfig) — "ephor" unless the
	// owner brought their own (turn/libp2p/wireguard/none).
	RelayProvider relayconfig.Provider `json:"relay_provider"`
	// Ingress is how the box is currently reachable from outside its own NAT
	// under RelayProvider (relay-tunnel/direct-portforward/libp2p-circuit-relay/wireguard-mesh).
	Ingress relayconfig.IngressDescriptor `json:"ingress"`
}

// LoadFederationProfile reads the sovereign-federation env surface into one
// coherent, self-reporting profile.
func LoadFederationProfile() FederationProfile {
	relay := strings.TrimSpace(os.Getenv("VULOS_RELAY_BASE_URL"))
	verify := strings.TrimRight(strings.TrimSpace(os.Getenv("VULOS_VERIFY_URL")), "/")
	verifyConfigured := verify != ""
	if verify == "" {
		verify = discoveryDefaultBaseURL
	}
	rendezvous := strings.TrimSpace(os.Getenv(proxRendezvousEnvKey))
	rendezvousConfigured := rendezvous != ""
	if rendezvous == "" {
		rendezvous = proxDefaultRendezvousBase
	}
	// Effective config (admin-store-authoritative, env-fallback) — the SAME
	// resolver /api/peering/ice and /api/turn/credentials use — so this
	// status profile can't drift from what ICE actually serves.
	tc := relayconfig.EffectiveTURNConfig()
	disablePublic := publicSTUNDisabled()

	return FederationProfile{
		RelayConfigured:      relay != "",
		RelayBaseURL:         relay,
		VerifyConfigured:     verifyConfigured,
		VerifyURL:            verify,
		RendezvousConfigured: rendezvousConfigured,
		RendezvousURL:        rendezvous,
		TURNEnabled:          tc.Enabled,
		TURNHost:             tc.Host,
		PublicSTUNDisabled:   disablePublic,
		NoThirdPartySTUN:     tc.Enabled && disablePublic,
		RelayProvider:        relayconfig.CurrentProvider(),
		Ingress:              relayconfig.IngressInfo(),
	}
}

// RegisterFederationProfileHandler mounts GET /api/peering/federation on mux —
// a session-gated (main.go's default auth middleware applies; this route is
// NOT added to the public-path allowlist) status endpoint the cockpit/UX can
// use to show the box's current sovereign-federation posture.
func RegisterFederationProfileHandler(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/peering/federation", func(w http.ResponseWriter, r *http.Request) {
		discoveryWriteJSON(w, http.StatusOK, LoadFederationProfile())
	})
}
