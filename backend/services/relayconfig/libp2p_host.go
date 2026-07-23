package relayconfig

// libp2p_host.go — construction of the ONE real go-libp2p host this package
// may ever run: a Circuit Relay v2 CLIENT reaching the operator's configured
// relay peers. This file is the ONLY place in the OS backend that calls
// libp2p.New; nothing else in this package or elsewhere embeds a second one.
//
// What it deliberately does NOT do:
//   - It never binds a local listen socket (libp2p.NoListenAddrs): this box
//     is a pure outbound-dialing CLIENT. Reachability from other peers comes
//     solely from the reservation autorelay negotiates with the configured
//     relay peers, never from this box accepting raw inbound connections.
//   - It never runs the relay SERVICE (libp2p.EnableRelayService is never
//     called): this box is never itself an open relay for third parties,
//     regardless of what it's configured with.
//   - It never falls back to go-libp2p's own (much larger) default resource
//     limits: ResourceManager/ConnectionManager are ALWAYS the fixed,
//     conservative ones from libp2p_limits.go — see newLibp2pReachabilityHost,
//     which fails closed (returns an error, builds nothing) rather than ever
//     constructing a host without them.
//
// Callers (libp2p_manager.go) MUST have already gated this behind BOTH the
// provider selection AND the explicit env opt-in — this file does not
// re-check either; it only enforces the structural/resource safety of
// whatever host it is asked to build.

import (
	"context"
	"errors"
	"fmt"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	ma "github.com/multiformats/go-multiaddr"
)

// libp2pReachabilityHost wraps the running host. ResourceManager and
// ConnectionManager are closed automatically by host.Close() (they are
// owned by the host once passed in as options), so Close() here only needs
// to close the host itself.
type libp2pReachabilityHost struct {
	host host.Host
}

// newLibp2pReachabilityHost builds and starts a resource-bounded libp2p
// Circuit Relay v2 client host for the given relay peer multiaddrs (each
// MUST already carry a /p2p/<peer-id> component — relayconfig.Validate
// enforces this at Set() time, and parseRelayPeers re-checks it here as
// defence in depth since this function does not otherwise trust its caller).
// It fails closed: any construction error returns (nil, err) and builds
// NOTHING — there is no code path that falls back to an unbounded host.
func newLibp2pReachabilityHost(ctx context.Context, relayPeerAddrs []string) (*libp2pReachabilityHost, error) {
	if len(relayPeerAddrs) == 0 {
		return nil, errors.New("libp2p: no relay peers configured")
	}
	infos, err := parseRelayPeers(relayPeerAddrs)
	if err != nil {
		return nil, fmt.Errorf("libp2p: %w", err)
	}

	rm, err := buildResourceManager()
	if err != nil {
		return nil, fmt.Errorf("libp2p: resource manager: %w", err)
	}
	cm, err := buildConnManager()
	if err != nil {
		_ = rm.Close()
		return nil, fmt.Errorf("libp2p: connection manager: %w", err)
	}

	h, err := libp2p.New(
		// Pure outbound client: never binds a local listen socket.
		libp2p.NoListenAddrs,
		// ALWAYS the fixed, conservative limits from libp2p_limits.go —
		// never go-libp2p's own unbounded/auto-scaled default.
		libp2p.ResourceManager(rm),
		libp2p.ConnectionManager(cm),
		// Circuit Relay v2 CLIENT transport (dial/be-dialed-to through
		// /p2p-circuit addresses). Default-on in go-libp2p; kept explicit
		// for clarity. libp2p.EnableRelayService is intentionally NEVER
		// called — this box never becomes an open relay for third parties.
		libp2p.EnableRelay(),
		// Ask the operator's configured relay peers for a reservation so
		// this box is reachable THROUGH them — the actual "reach peers via
		// relay" client capability.
		libp2p.EnableAutoRelayWithStaticRelays(infos, autorelay.WithNumRelays(len(infos))),
		libp2p.UserAgent("vulos-box/libp2p-reachability"),
	)
	if err != nil {
		_ = rm.Close()
		return nil, fmt.Errorf("libp2p: construct host: %w", err)
	}
	return &libp2pReachabilityHost{host: h}, nil
}

// Close shuts the host down. Safe to call on a nil receiver/host (mirrors
// the other services' fail-safe Close() patterns in this codebase).
func (h *libp2pReachabilityHost) Close() error {
	if h == nil || h.host == nil {
		return nil
	}
	return h.host.Close()
}

// PeerID returns the host's own peer ID (safe, non-secret — the public
// identity a relay reservation is registered under).
func (h *libp2pReachabilityHost) PeerID() string {
	if h == nil || h.host == nil {
		return ""
	}
	return h.host.ID().String()
}

// parseRelayPeers parses each relay peer string into a multiaddr and
// requires it resolve to a full peer.AddrInfo (host/transport + peer ID).
// This is defence in depth: relayconfig.Validate (validate.go's
// validateMultiaddr) already enforces a /p2p/<peer-id> component structurally
// before a config is ever persisted, but this function does not trust that
// path alone — a malformed or peer-ID-less address is rejected here too,
// fitting the "fail closed on misconfig" requirement.
func parseRelayPeers(addrs []string) ([]peer.AddrInfo, error) {
	mas := make([]ma.Multiaddr, 0, len(addrs))
	for _, a := range addrs {
		m, err := ma.NewMultiaddr(a)
		if err != nil {
			return nil, fmt.Errorf("invalid relay multiaddr %q: %w", a, err)
		}
		mas = append(mas, m)
	}
	infos, err := peer.AddrInfosFromP2pAddrs(mas...)
	if err != nil {
		return nil, fmt.Errorf("relay multiaddrs must each include a /p2p/<peer-id>: %w", err)
	}
	if len(infos) == 0 {
		return nil, errors.New("no usable relay peer addresses")
	}
	return infos, nil
}
