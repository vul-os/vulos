// Package peering implements the Vula OS peering layer.
// This file (ice.go) exposes the ICE/NAT traversal configuration endpoint
// used by browsers to bootstrap WebRTC peer connections for calls.
package peering

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"vulos/backend/services/network"
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
// servers from the ICE config (sovereign-federation config profile: a fully
// self-hosted TURN deployment already answers STUN binding requests on the
// same port — see selfHostedSTUNURL — so a fully-sovereign box can opt out of
// any third-party STUN dependency entirely). Off by default: most boxes
// benefit from the public STUN servers as a free, always-available fallback,
// and turning them off is a deliberate sovereignty choice, not the default.
const envDisablePublicSTUN = "VULOS_STUN_DISABLE_PUBLIC"

// publicSTUNDisabled reports whether the operator opted out of the public
// STUN fallback.
func publicSTUNDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envDisablePublicSTUN))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// stunURLs returns the public STUN URLs included in the ICE config, unless
// the operator has disabled them (envDisablePublicSTUN) for a fully-sovereign
// deployment. Public Google STUN servers are used by default so calls work
// even without a local TURN deployment.
func stunURLs() []string {
	if publicSTUNDisabled() {
		return nil
	}
	return []string{
		"stun:stun.l.google.com:19302",
		"stun:stun1.l.google.com:19302",
	}
}

// selfHostedSTUNURLs returns a STUN entry pointing at the operator's OWN TURN
// server, when one is configured. Coturn answers plain STUN binding requests
// on the SAME port it serves TURN on, so a box that self-hosts TURN already
// has a fully self-hosted STUN option with zero extra infrastructure — this
// is what lets a fully-sovereign box (envDisablePublicSTUN=1) need no
// third-party STUN server at all.
func selfHostedSTUNURLs(tc network.TURNConfig) []string {
	if !tc.Enabled {
		return nil
	}
	host := tc.Host
	if host == "" {
		host = "localhost"
	}
	return []string{fmt.Sprintf("stun:%s:%d", host, tc.Port)}
}

// handleICEConfig serves GET /api/peering/ice.
//
// Response always includes STUN servers. When the TURN_SECRET environment
// variable is set, short-lived TURN credentials are appended using the
// existing network.TURNConfig.GenerateCredentials API — no new TURN code.
//
// The caller identity comes from the X-User-ID header injected by the auth
// middleware. An anonymous "guest" string is used as the TURN username when
// no authenticated user is present (TURN credentials are still valid; the
// username is part of the HMAC-SHA1 time-based credential, not an auth check).
func handleICEConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	servers := []iceServer{}
	if urls := stunURLs(); len(urls) > 0 {
		servers = append(servers, iceServer{URLs: urls})
	}

	tc := network.LoadTURNConfig()
	if urls := selfHostedSTUNURLs(tc); len(urls) > 0 {
		servers = append(servers, iceServer{URLs: urls})
	}
	if tc.Enabled {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			userID = "guest"
		}
		creds := tc.GenerateCredentials(userID)
		servers = append(servers, iceServer{
			URLs:       creds.URLs,
			Username:   creds.Username,
			Credential: creds.Credential,
			TTL:        creds.TTL,
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
