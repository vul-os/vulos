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
//	VULOS_RELAY_BASE_URL    the box's configured vulos-relay instance — used
//	                        both for reachability resolve (resolve.go) and, by
//	                        other subsystems (gpuhost/meethost), for capability
//	                        registration. Self-hostable (see vulos-relay).
//	VULOS_VERIFY_URL        the directory/verify service base URL (discovery.go
//	                        + email verification). Defaults to the central
//	                        vulos.org directory when unset.
//	VULOS_RENDEZVOUS_URL    reserved for a future real-time signaling rendezvous
//	                        (Talk/Meet call setup) that is independent of the
//	                        relay's store-and-forward/tunnel roles. No current
//	                        code path consumes it yet; it is surfaced here so
//	                        the profile is complete and forward-compatible —
//	                        set it and it will report as configured.
//	TURN_SECRET / TURN_HOST self-hosted TURN (network/turn.go); when set, the
//	                        ICE config (ice.go) also answers plain STUN on the
//	                        SAME port (selfHostedSTUNURLs) — zero extra infra.
//	VULOS_STUN_DISABLE_PUBLIC opts out of the public Google STUN fallback
//	                        entirely (ice.go's stunURLs) — the last piece
//	                        needed for a box that wants NO third-party network
//	                        dependency for call setup.
package peering

import (
	"net/http"
	"os"
	"strings"

	"vulos/backend/services/network"
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

	// RendezvousConfigured reports whether VULOS_RENDEZVOUS_URL is set.
	RendezvousConfigured bool `json:"rendezvous_configured"`
	RendezvousURL        string `json:"rendezvous_url,omitempty"`

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
	rendezvous := strings.TrimSpace(os.Getenv("VULOS_RENDEZVOUS_URL"))
	tc := network.LoadTURNConfig()
	disablePublic := publicSTUNDisabled()

	return FederationProfile{
		RelayConfigured:      relay != "",
		RelayBaseURL:         relay,
		VerifyConfigured:     verifyConfigured,
		VerifyURL:            verify,
		RendezvousConfigured: rendezvous != "",
		RendezvousURL:        rendezvous,
		TURNEnabled:          tc.Enabled,
		TURNHost:             tc.Host,
		PublicSTUNDisabled:   disablePublic,
		NoThirdPartySTUN:     tc.Enabled && disablePublic,
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
