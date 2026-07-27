package relayconfig

import (
	"fmt"
	"os"
	"strings"

	"vulos/backend/services/network"
)

// ephor.go — the DEFAULT relay/reachability provider: Vulos's own relay/TURN
// path. This is a deliberate, byte-identical extraction of the default logic
// that previously lived only in peering/ice.go, so choosing (or falling back
// to) "ephor" behaves EXACTLY as it always has for anyone who never opens
// Settings — the provider seam is purely additive.

// envDisablePublicSTUN mirrors peering/ice.go's own flag of the same name: a
// fully-sovereign box that self-hosts TURN (which also answers plain STUN on
// the same port) can opt out of the public Google STUN fallback entirely.
const envDisablePublicSTUN = "VULOS_STUN_DISABLE_PUBLIC"

func publicSTUNDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envDisablePublicSTUN))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// publicSTUNURLs returns the public STUN fallback URLs, unless disabled.
func publicSTUNURLs() []string {
	if publicSTUNDisabled() {
		return nil
	}
	return []string{
		"stun:stun.l.google.com:19302",
		"stun:stun1.l.google.com:19302",
	}
}

// selfHostedSTUNURL returns a STUN entry pointing at the box's own coturn
// server, when one is configured — coturn answers plain STUN binding
// requests on the same port it serves TURN on.
func selfHostedSTUNURL(tc network.TURNConfig) []string {
	if !tc.Enabled {
		return nil
	}
	host := tc.Host
	if host == "" {
		host = "localhost"
	}
	return []string{fmt.Sprintf("stun:%s:%d", host, tc.Port)}
}

// builtinICEServers builds the built-in default ICE server list for userID:
// public STUN (unless disabled) + this box's own coturn STUN/TURN. The TURN
// config itself comes from effectiveTURNConfig() — the admin-configured
// network.TURNStore (Settings' "TURN / WebRTC" panel) when set, else the env
// (TURN_HOST/TURN_PORT/TURN_SECRET/TURN_REALM) — with fresh short-lived HMAC
// credentials (<=1h, see network.TURNConfig.GenerateCredentials) whenever
// TURN is enabled.
func builtinICEServers(userID string) []ICEServer {
	var servers []ICEServer
	if urls := publicSTUNURLs(); len(urls) > 0 {
		servers = append(servers, ICEServer{URLs: urls})
	}

	tc := effectiveTURNConfig()
	if urls := selfHostedSTUNURL(tc); len(urls) > 0 {
		servers = append(servers, ICEServer{URLs: urls})
	}
	if tc.Enabled {
		if strings.TrimSpace(userID) == "" {
			userID = "guest"
		}
		creds := tc.GenerateCredentials(userID)
		servers = append(servers, ICEServer{
			URLs:       creds.URLs,
			Username:   creds.Username,
			Credential: creds.Credential,
			TTL:        creds.TTL,
		})
	}
	return servers
}
