// Package peering implements the Vula OS peering layer.
// This file (ice.go) exposes the ICE/NAT traversal configuration endpoint
// used by browsers to bootstrap WebRTC peer connections for calls.
package peering

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"vulos/backend/services/relayconfig"
)

// iceServer represents a single STUN or TURN server entry as defined by the
// W3C WebRTC RTCIceServer dictionary.
type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
	TTL        int      `json:"ttl,omitempty"`
}

// iceConfigResponse is returned by GET /api/peering/ice.
type iceConfigResponse struct {
	IceServers []iceServer `json:"ice_servers"`
}

// envDisablePublicSTUN, when set truthy, suppresses the public Google STUN
// servers from the ICE config (sovereign-federation config profile). It is
// read by relayconfig's ephor provider now (see relayconfig/ephor.go); kept
// here too only because federation_profile.go's publicSTUNDisabled() (an
// existing, separately-consumed status field) still reads it directly.
const envDisablePublicSTUN = "VULOS_STUN_DISABLE_PUBLIC"

// publicSTUNDisabled reports whether the operator opted out of the public
// STUN fallback. Used by federation_profile.go's status view.
func publicSTUNDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envDisablePublicSTUN))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// handleICEConfig serves GET /api/peering/ice.
//
// SINGLE SOURCE OF TRUTH (RELAY-01): the ICE server list is produced
// EXCLUSIVELY by relayconfig.ICEServers — the box's chosen relay/TURN
// provider (ephor by default; BYO turn/libp2p/wireguard/none otherwise).
// This also fixes the historical split-brain where Settings' "TURN / WebRTC"
// panel persisted network.TURNStore's turn.json but this handler only ever
// read the TURN_SECRET/TURN_HOST env vars — relayconfig's ephor provider now
// treats the admin-configured store as authoritative when set (see
// relayconfig.SetTURNStore, wired in cmd/server/main.go).
//
// This handler must NEVER branch on which provider is active — whatever
// relayconfig.ICEServers returns is already the right list for the box's
// current configuration, always including app-media ICE even when the
// active provider only affects ingress/rendezvous (libp2p/wireguard/none —
// see relayconfig's package doc on the three-facet reachability model).
//
// The caller identity comes from the X-User-ID header injected by the auth
// middleware. An anonymous "guest" string is used as the TURN username when
// no authenticated user is present (TURN credentials are still valid; the
// username is part of the HMAC-SHA256 time-limited credential, not an auth
// check).
func handleICEConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		userID = "guest"
	}

	resolved := relayconfig.ICEServers(r.Context(), userID)
	servers := make([]iceServer, 0, len(resolved))
	for _, s := range resolved {
		servers = append(servers, iceServer{
			URLs:       s.URLs,
			Username:   s.Username,
			Credential: s.Credential,
			TTL:        s.TTL,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(iceConfigResponse{IceServers: servers})
}

// RegisterICEHandlers registers the peering ICE config route on mux.
// Call this from the main server setup to wire in the endpoint.
//
//	GET /api/peering/ice → iceConfigResponse
func RegisterICEHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/peering/ice", handleICEConfig)
}
