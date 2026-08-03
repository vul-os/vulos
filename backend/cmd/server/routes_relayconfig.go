// no-broker-dep:allow-file: comments describe the relayconfig HTTP seam (owner-selectable
// vulos/ephor/turn/libp2p/wireguard/none) and call the legacy-named
// ResetToEphor() wrapper, which itself calls relayconfig.ResetToDefault()
// (Provider: vulos, see relayconfig.go DefaultConfig()) -- no Pier
// import, no default-to-ephor behaviour.

package main

// routes_relayconfig.go — RELAY-01: owner-configurable relay/TURN provider
// seam. Pier (Vulos's own default relay/TURN/rendezvous path) is the
// default; the owner may instead bring their own STUN/TURN, libp2p Circuit
// Relay v2 peers, a WireGuard/Tailscale/Headscale/Nebula endpoint, or turn
// the relay tunnel off entirely ("none" — static IP / port-forward). Every
// app-facing surface (Meet/Talk/diwan's WebRTC, peering/ice.go, the SFU, and
// generic app streaming) reads relayconfig.ICEServers instead of hardcoding
// STUN/TURN, so a change here is instant and suite-wide.
//
// Reachability is modelled as three independent facets (app-media ICE, box
// HTTP ingress, box<->box rendezvous) — see backend/services/relayconfig's
// package doc. Selecting libp2p/wireguard/none never silently disables ICE;
// it only changes which facets THAT provider is claiming.
//
// Endpoints:
//
//	GET  /api/relayconfig       — current provider + safe (credential-redacted)
//	                              config, plus the resolved effective snapshot
//	                              (ICE servers + ingress descriptor). Any
//	                              authenticated user may read (matches
//	                              GET /api/turn/config's posture — nothing
//	                              here is secret to the box's own users).
//	POST /api/relayconfig       — ADMIN + STEP-UP gated. Body is the full
//	                              relayconfig.Config (plus an optional "force"
//	                              to skip the pre-commit health probe).
//	                              Validated (and, for turn/wireguard, health-
//	                              probed) BEFORE persisting; fails closed —
//	                              an invalid or unreachable candidate leaves
//	                              the current, previously-working provider
//	                              completely untouched. Changing the relay/
//	                              reachability provider affects reachability
//	                              + security, hence the step-up bar (same as
//	                              CDN/KMS/webhooks config changes).
//	POST /api/relayconfig/reset — ADMIN + STEP-UP gated. Reverts to ephor.
//	POST /api/relayconfig/test  — ADMIN gated. Best-effort TCP reachability
//	                              probe of the CURRENTLY ACTIVE provider's
//	                              endpoint (mirrors /api/turn/test).
import (
	"encoding/json"
	"net/http"

	"vulos/backend/services/auth"
	"vulos/backend/services/relayconfig"
	"vulos/backend/services/stepup"
)

// relayConfigMaxBody bounds the JSON body for the relayconfig endpoints —
// generous for the largest legitimate payload (a handful of ICE servers or
// relay peers) while keeping the decoder's work bounded regardless of caller.
const relayConfigMaxBody = 32 << 10

// relayConfigSetRequest is the POST /api/relayconfig body: the full desired
// Config plus an optional escape hatch (Force) for the pre-commit health
// probe Set() runs on turn/wireguard candidates.
type relayConfigSetRequest struct {
	relayconfig.Config
	Force bool `json:"force,omitempty"`
}

// registerRelayConfigRoutes wires the relay/TURN provider seam into mux.
func registerRelayConfigRoutes(mux *http.ServeMux, authStore *auth.Store) {
	mux.HandleFunc("GET /api/relayconfig", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		writeJSON(w, map[string]any{
			"config":    relayconfig.Get(),
			"effective": relayconfig.Effective(r.Context(), userID),
		})
	})

	mux.HandleFunc("POST /api/relayconfig", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		// Changing the relay/reachability provider affects reachability +
		// security (e.g. disabling the relay tunnel, or pointing WebRTC/box
		// ingress at a third party) — require a freshly-elevated session,
		// the same bar as CDN/KMS/webhooks config changes.
		if err := stepup.Require(r); err != nil {
			writeErr(w, http.StatusForbidden, "step-up verification required: "+err.Error())
			return
		}
		var req relayConfigSetRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, relayConfigMaxBody)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		view, err := relayconfig.Set(req.Config, req.Force)
		if err != nil {
			// Validation, probe, or persistence failures are the client's to
			// fix — the previously-active provider is guaranteed untouched.
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, view)
	})

	mux.HandleFunc("POST /api/relayconfig/reset", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		if err := stepup.Require(r); err != nil {
			writeErr(w, http.StatusForbidden, "step-up verification required: "+err.Error())
			return
		}
		view, err := relayconfig.ResetToEphor()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, view)
	})

	// POST /api/relayconfig/test — TCP reachability probe of the currently
	// active provider — admin only (SSRF-adjacent capability, same posture
	// as /api/turn/test).
	mux.HandleFunc("POST /api/relayconfig/test", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		writeJSON(w, relayconfig.TestReachability())
	})
}
