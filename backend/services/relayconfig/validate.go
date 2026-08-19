package relayconfig

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Size caps bound how much an owner can stuff into a single config — generous
// for any real deployment, tight enough to keep the persisted file small and
// keep validation itself cheap (no unbounded loops over attacker-sized input).
const (
	maxICEServers    = 8
	maxURLsPerServer = 4
	maxURLLen        = 512
	maxUsernameLen   = 256
	maxCredentialLen = 4096 // long-term TURN credentials can be sizable tokens
	maxRelayPeers    = 16
	maxMultiaddrLen  = 512
	maxEndpointLen   = 256
	maxNetworkLen    = 128
)

// Validate enforces the security invariants for cfg: a known provider, and —
// for whichever section(s) are populated — well-formed values. It is always
// safe to call Validate on a Config before persisting it: nothing here has
// side effects.
//
// Every populated section is validated (not just the one matching Provider),
// so switching providers back and forth never lets a malformed leftover
// section slip into the persisted file.
func Validate(cfg Config) error {
	if !cfg.Provider.Valid() {
		return fmt.Errorf("unknown relay provider %q", cfg.Provider)
	}

	if cfg.Provider == ProviderTURN && len(cfg.TURN.ICEServers) == 0 {
		return errors.New("turn provider requires at least one ICE server")
	}
	if len(cfg.TURN.ICEServers) > 0 {
		if err := validateICEServers(cfg.TURN.ICEServers); err != nil {
			return err
		}
	}

	if cfg.Provider == ProviderLibp2p && len(cfg.Libp2p.RelayPeers) == 0 {
		return errors.New("libp2p provider requires at least one relay peer multiaddr")
	}
	if len(cfg.Libp2p.RelayPeers) > 0 {
		if err := validateRelayPeers(cfg.Libp2p.RelayPeers); err != nil {
			return err
		}
	}

	if cfg.Provider == ProviderWireGuard && strings.TrimSpace(cfg.WireGuard.Endpoint) == "" {
		return errors.New("wireguard provider requires an endpoint")
	}
	if strings.TrimSpace(cfg.WireGuard.Endpoint) != "" {
		if err := validateWireGuardConfig(cfg.WireGuard); err != nil {
			return err
		}
	}

	return nil
}

func validateICEServers(servers []ICEServer) error {
	if len(servers) > maxICEServers {
		return fmt.Errorf("too many ICE servers (max %d)", maxICEServers)
	}
	for i, s := range servers {
		if len(s.URLs) == 0 {
			return fmt.Errorf("ICE server #%d has no urls", i+1)
		}
		if len(s.URLs) > maxURLsPerServer {
			return fmt.Errorf("ICE server #%d has too many urls (max %d)", i+1, maxURLsPerServer)
		}
		for _, u := range s.URLs {
			if err := validateICEURL(u); err != nil {
				return fmt.Errorf("ICE server #%d: %w", i+1, err)
			}
		}
		if len(s.Username) > maxUsernameLen {
			return fmt.Errorf("ICE server #%d username too long (max %d)", i+1, maxUsernameLen)
		}
		if len(s.Credential) > maxCredentialLen {
			return fmt.Errorf("ICE server #%d credential too long (max %d)", i+1, maxCredentialLen)
		}
		if containsControlChars(s.Username) {
			return fmt.Errorf("ICE server #%d username contains control characters", i+1)
		}
		// "Set this credential" and "delete the stored credential" in one
		// request is not a preference to resolve silently — either reading
		// loses data the caller did not mean to lose. Reject it and make the
		// caller say which they meant. (See ICEServer.ClearCredential and
		// mergeStoredCredentials for the three-state write contract.)
		if s.ClearCredential && s.Credential != "" {
			return fmt.Errorf("ICE server #%d sets both a credential and clear_credential — send one or the other", i+1)
		}
	}
	return nil
}

// iceURLSchemes are the schemes the WebRTC RTCIceServer dictionary accepts.
var iceURLSchemes = map[string]bool{"stun": true, "stuns": true, "turn": true, "turns": true}

// validateICEURL enforces a well-formed stun:/stuns:/turn:/turns: URI with a
// real host (and, if present, a numeric port). These URIs are opaque (not
// hierarchical like https://), e.g. "turn:relay.example.org:3478?transport=tcp",
// so they are parsed by hand rather than via url.Parse (which treats the part
// after the scheme as an opaque string for schemes it doesn't special-case,
// but we want to validate the host:port shape ourselves regardless).
func validateICEURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return errors.New("ICE server URL is empty")
	}
	if len(s) > maxURLLen {
		return fmt.Errorf("ICE server URL is too long (max %d)", maxURLLen)
	}
	if containsControlChars(s) {
		return errors.New("ICE server URL contains control characters")
	}
	idx := strings.Index(s, ":")
	if idx < 0 {
		return fmt.Errorf("ICE server URL %q has no scheme", s)
	}
	scheme := strings.ToLower(s[:idx])
	if !iceURLSchemes[scheme] {
		return fmt.Errorf("ICE server URL %q has unsupported scheme %q (want stun/stuns/turn/turns)", s, scheme)
	}
	rest := s[idx+1:]
	if q := strings.Index(rest, "?"); q >= 0 {
		rest = rest[:q]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return fmt.Errorf("ICE server URL %q is missing a host", s)
	}
	if strings.ContainsAny(rest, " \t\r\n") {
		return fmt.Errorf("ICE server URL %q host contains whitespace", s)
	}
	// TURN/STUN URIs (RFC 7064/7065) carry no userinfo component — reject an
	// embedded "user:pass@host" so a credential can never be smuggled inside
	// the URL string itself (where it would end up logged/displayed
	// unredacted alongside the non-secret URL list).
	if strings.Contains(rest, "@") {
		return fmt.Errorf("ICE server URL %q must not embed credentials in the URL (use the separate username/credential fields)", s)
	}
	host := rest
	if h, port, err := net.SplitHostPort(rest); err == nil {
		if strings.TrimSpace(h) == "" {
			return fmt.Errorf("ICE server URL %q has an empty host", s)
		}
		if _, perr := strconv.Atoi(port); perr != nil {
			return fmt.Errorf("ICE server URL %q has an invalid port %q", s, port)
		}
		host = h
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("ICE server URL %q has an empty host", s)
	}
	return nil
}

func validateRelayPeers(peers []string) error {
	if len(peers) > maxRelayPeers {
		return fmt.Errorf("too many relay peers (max %d)", maxRelayPeers)
	}
	for i, p := range peers {
		if err := validateMultiaddr(p); err != nil {
			return fmt.Errorf("relay peer #%d: %w", i+1, err)
		}
	}
	return nil
}

// validateMultiaddr is a deliberately conservative structural check — this
// package has no multiformats dependency, so it does not fully parse
// multicodec varints the way go-multiaddr would. It rejects anything that
// isn't shaped like a multiaddr and, specifically, anything missing a
// /p2p/<peer-id> (or legacy /ipfs/<peer-id>) component, since a Circuit
// Relay v2 relay address is meaningless without one.
func validateMultiaddr(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return errors.New("multiaddr is empty")
	}
	if len(s) > maxMultiaddrLen {
		return fmt.Errorf("multiaddr is too long (max %d)", maxMultiaddrLen)
	}
	if containsControlChars(s) || strings.ContainsAny(s, " \t\r\n") {
		return fmt.Errorf("multiaddr %q contains whitespace or control characters", s)
	}
	if !strings.HasPrefix(s, "/") {
		return fmt.Errorf("multiaddr %q must start with /", s)
	}
	parts := strings.Split(s, "/")
	if len(parts) < 3 {
		return fmt.Errorf("multiaddr %q is too short to be a valid address", s)
	}
	hasPeer := false
	for i := 0; i+1 < len(parts); i++ {
		if (parts[i] == "p2p" || parts[i] == "ipfs") && strings.TrimSpace(parts[i+1]) != "" {
			hasPeer = true
			break
		}
	}
	if !hasPeer {
		return fmt.Errorf("multiaddr %q must include a /p2p/<peer-id> component (required for a Circuit Relay v2 relay address)", s)
	}
	return nil
}

func validateWireGuardConfig(cfg WireGuardProviderConfig) error {
	if err := validateWireGuardEndpoint(cfg.Endpoint); err != nil {
		return err
	}
	if len(cfg.Network) > maxNetworkLen {
		return fmt.Errorf("wireguard network label too long (max %d)", maxNetworkLen)
	}
	if containsControlChars(cfg.Network) {
		return errors.New("wireguard network label contains control characters")
	}
	return nil
}

// validateWireGuardEndpoint accepts either a bare "host:port" or a full
// https:// (or http://, for LAN-only coordinators) URL.
func validateWireGuardEndpoint(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return errors.New("endpoint is empty")
	}
	if len(s) > maxEndpointLen {
		return fmt.Errorf("endpoint is too long (max %d)", maxEndpointLen)
	}
	if containsControlChars(s) || strings.ContainsAny(s, " \t\r\n") {
		return fmt.Errorf("endpoint %q contains whitespace or control characters", s)
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return fmt.Errorf("endpoint %q is not a valid URL: %w", s, err)
		}
		if u.Scheme != "https" && u.Scheme != "http" {
			return fmt.Errorf("endpoint %q has unsupported scheme %q (want http/https)", s, u.Scheme)
		}
		if u.Hostname() == "" {
			return fmt.Errorf("endpoint %q is missing a host", s)
		}
		return nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return fmt.Errorf("endpoint %q must be host:port or an http(s) URL: %w", s, err)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("endpoint %q has an empty host", s)
	}
	if _, perr := strconv.Atoi(port); perr != nil {
		return fmt.Errorf("endpoint %q has an invalid port %q", s, port)
	}
	return nil
}

func containsControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
