// Package peering implements the Vula OS peering layer.
// This file (ice.go) exposes the ICE/NAT traversal configuration endpoint
// used by browsers to bootstrap WebRTC peer connections for calls.
package peering

import (
	"encoding/json"
	"net/http"
	"os"

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

// stunURLs returns the STUN URLs that are always included in the ICE config.
// Public Google STUN servers are used so calls work even without a local TURN
// deployment. The local STUN port matches whatever Coturn would listen on if
// TURN_SECRET is set.
func stunURLs() []string {
	return []string{
		"stun:stun.l.google.com:19302",
		"stun:stun1.l.google.com:19302",
	}
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

	servers := []iceServer{
		{URLs: stunURLs()},
	}

	tc := network.LoadTURNConfig()
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

// turnSecretIsSet is a helper used by tests to check whether TURN is enabled
// without importing the network package.
func turnSecretIsSet() bool {
	return os.Getenv("TURN_SECRET") != ""
}
