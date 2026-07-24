package relayconfig

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
)

// Facet is a bitmask of the three orthogonal reachability concerns a
// provider may cover. See the package doc for the full rationale.
type Facet uint8

const (
	// FacetICE is app-media ICE (STUN/TURN for browser WebRTC).
	FacetICE Facet = 1 << iota
	// FacetIngress is reaching the box's HTTP surface from outside NAT.
	FacetIngress
	// FacetRendezvous is box<->box rendezvous/discovery.
	FacetRendezvous
)

// Has reports whether f includes x.
func (f Facet) Has(x Facet) bool { return f&x != 0 }

// IngressDescriptor is a safe-to-display (no secrets) summary of how the box
// is currently reachable from outside its own NAT.
type IngressDescriptor struct {
	// Mode is a short machine-readable tag: "relay-tunnel" |
	// "direct-portforward" | "libp2p-circuit-relay" | "wireguard-mesh".
	Mode string `json:"mode"`
	// Detail is a human-readable, non-secret elaboration (a base URL, peer
	// count, endpoint host — never a credential).
	Detail string `json:"detail,omitempty"`
}

// ReachabilityProvider is implemented by each concrete backend (ephor/
// turn/libp2p/wireguard/none). A provider only needs to behave sensibly for
// the facets it claims via Capabilities() — callers (ICEServers/IngressInfo/
// ResolvePeer below) never invoke a provider for a facet it doesn't claim;
// that facet instead falls back to ephor's own gate. Implementations for
// unclaimed facets simply return zero values and are never reached.
type ReachabilityProvider interface {
	Name() Provider
	Capabilities() Facet
	ICEServers(ctx context.Context, userID string) []ICEServer
	Ingress() IngressDescriptor
	ResolvePeer(ctx context.Context, peerID string) (baseURL string, ok bool)
}

// providerFor constructs the ReachabilityProvider for name, closing over
// cfg's per-provider sections. ephor needs no config (it reads env/the
// injected TURNStore); an unrecognised name fails safe to ephor.
func providerFor(name Provider, cfg Config) ReachabilityProvider {
	switch name {
	case ProviderNone:
		return noneProvider{}
	case ProviderTURN:
		return turnProvider{cfg: cfg.TURN}
	case ProviderLibp2p:
		return libp2pProvider{cfg: cfg.Libp2p}
	case ProviderWireGuard:
		return wireguardProvider{cfg: cfg.WireGuard}
	default:
		return ephorProvider{}
	}
}

func activeProvider() ReachabilityProvider {
	cfg := currentConfig()
	return providerFor(cfg.Provider, cfg)
}

// ICEServers is the ONLY producer of app-media ICE config — Meet/Talk/ofisi
// (and anything else that opens an RTCPeerConnection) should call this
// instead of hardcoding STUN/TURN. It NEVER goes dark just because the
// active provider covers ingress/rendezvous but not ICE (none/libp2p/
// wireguard): those providers don't claim FacetICE, so this automatically
// falls back to ephor's ICE default. Callers must not branch on provider
// name; the returned list is already the right one.
func ICEServers(ctx context.Context, userID string) []ICEServer {
	p := activeProvider()
	if p.Capabilities().Has(FacetICE) {
		return p.ICEServers(ctx, userID)
	}
	return ephorProvider{}.ICEServers(ctx, userID)
}

// IngressInfo reports how the box is currently reachable from outside NAT —
// the active provider if it claims FacetIngress, else ephor's default
// (relay tunnel).
//
// HONESTY NOTE: for libp2p/wireguard this is REPORT-ONLY today — it
// describes what the operator configured, not a live re-route of real HTTP
// ingress. Only facet A (ICEServers, above) is actually actuated end-to-end;
// selecting libp2p or wireguard here does not yet make the box's HTTP
// surface reachable through that path (see each provider's Ingress()/
// ResolvePeer() for the specific stub behaviour).
func IngressInfo() IngressDescriptor {
	p := activeProvider()
	if p.Capabilities().Has(FacetIngress) {
		return p.Ingress()
	}
	return ephorProvider{}.Ingress()
}

// ResolvePeer resolves peerID to a base URL via the active rendezvous
// provider, if it claims FacetRendezvous, else via ephor's default (the
// existing peering.resolve.go cache/HTTP path, which this package does not
// duplicate — see ephorProvider.ResolvePeer).
func ResolvePeer(ctx context.Context, peerID string) (string, bool) {
	p := activeProvider()
	if p.Capabilities().Has(FacetRendezvous) {
		return p.ResolvePeer(ctx, peerID)
	}
	return ephorProvider{}.ResolvePeer(ctx, peerID)
}

// EffectiveRelay is the resolved, ready-to-display relay/reachability
// snapshot for Settings/status surfaces — includes ALL three facets, unlike
// ICEServers/IngressInfo/ResolvePeer which each resolve exactly one.
type EffectiveRelay struct {
	Provider         Provider                 `json:"provider"`
	ICEServers       []ICEServer              `json:"ice_servers,omitempty"`
	Ingress          IngressDescriptor        `json:"ingress"`
	Libp2pRelayPeers []string                 `json:"libp2p_relay_peers,omitempty"`
	WireGuard        *WireGuardProviderConfig `json:"wireguard,omitempty"`
}

// Effective returns the full resolved snapshot for userID (ICE credentials,
// when the active ICE-capable provider needs per-user scoping, are minted
// for this user).
func Effective(ctx context.Context, userID string) EffectiveRelay {
	cfg := currentConfig()
	eff := EffectiveRelay{
		Provider:   cfg.Provider,
		ICEServers: ICEServers(ctx, userID),
		Ingress:    IngressInfo(),
	}
	if cfg.Provider == ProviderLibp2p {
		eff.Libp2pRelayPeers = cfg.Libp2p.RelayPeers
	}
	if cfg.Provider == ProviderWireGuard {
		wg := cfg.WireGuard
		eff.WireGuard = &wg
	}
	return eff
}

// --- ephor: the default. No special privilege — registered and dispatched
// exactly like every other provider. ---

type ephorProvider struct{}

func (ephorProvider) Name() Provider      { return ProviderEphor }
func (ephorProvider) Capabilities() Facet { return FacetICE | FacetIngress | FacetRendezvous }

func (ephorProvider) ICEServers(_ context.Context, userID string) []ICEServer {
	return ephorICEServers(userID)
}

func (ephorProvider) Ingress() IngressDescriptor {
	relay := strings.TrimSpace(os.Getenv("VULOS_RELAY_BASE_URL"))
	if relay == "" {
		relay = "https://relay.vulos.org"
	}
	return IngressDescriptor{Mode: "relay-tunnel", Detail: relay}
}

// ResolvePeer intentionally returns not-ok: ephor's rendezvous facet is
// already served live by backend/services/peering/resolve.go's existing
// cache/HTTP path. This package does not duplicate that logic (and must not
// import the peering package — peering imports THIS package for ICE, and a
// reverse import would cycle); ResolvePeer exists on this interface purely
// so an alternative rendezvous provider (libp2p) can be swapped in without
// peering.go ever branching on provider name.
func (ephorProvider) ResolvePeer(context.Context, string) (string, bool) { return "", false }

// --- none: opts ingress out of the relay tunnel. Facets A/C untouched. ---

type noneProvider struct{}

func (noneProvider) Name() Provider                                 { return ProviderNone }
func (noneProvider) Capabilities() Facet                            { return FacetIngress }
func (noneProvider) ICEServers(context.Context, string) []ICEServer { return nil }

func (noneProvider) Ingress() IngressDescriptor {
	return IngressDescriptor{
		Mode:   "direct-portforward",
		Detail: "no relay tunnel — reachable only via the operator's own static IP / port-forward",
	}
}
func (noneProvider) ResolvePeer(context.Context, string) (string, bool) { return "", false }

// --- turn: BYO STUN/TURN ICE list. Facet A ONLY. ---

type turnProvider struct{ cfg TURNProviderConfig }

func (turnProvider) Name() Provider                                     { return ProviderTURN }
func (turnProvider) Capabilities() Facet                                { return FacetICE }
func (p turnProvider) ICEServers(context.Context, string) []ICEServer   { return p.cfg.ICEServers }
func (turnProvider) Ingress() IngressDescriptor                         { return IngressDescriptor{} }
func (turnProvider) ResolvePeer(context.Context, string) (string, bool) { return "", false }

// --- libp2p: BYO Circuit Relay v2 peers. Facets B+C. ---

type libp2pProvider struct{ cfg Libp2pProviderConfig }

func (libp2pProvider) Name() Provider                                 { return ProviderLibp2p }
func (libp2pProvider) Capabilities() Facet                            { return FacetIngress | FacetRendezvous }
func (libp2pProvider) ICEServers(context.Context, string) []ICEServer { return nil }

// Ingress calls Reconcile() (libp2p_manager.go) so the optional embedded
// host self-heals to match the current config on every read, then reports
// honestly on whatever that reconciliation found: a live host's peer ID
// when both opt-in gates are satisfied and construction succeeded, why it
// isn't live otherwise (env gate not set, or a fail-closed construction
// error), or the pre-existing report-only detail as a last resort.
func (p libp2pProvider) Ingress() IngressDescriptor {
	st := ensureLibp2pManager(currentConfig())
	base := fmt.Sprintf("%d relay peer(s) configured", len(p.cfg.RelayPeers))
	switch {
	case st.Running:
		return IngressDescriptor{Mode: "libp2p-circuit-relay", Detail: fmt.Sprintf("%s, live host peer_id=%s", base, st.PeerID)}
	case st.LastError != "":
		return IngressDescriptor{Mode: "libp2p-circuit-relay", Detail: fmt.Sprintf("%s (embedded host disabled: %s)", base, st.LastError)}
	case !st.HostEnvEnabled:
		return IngressDescriptor{Mode: "libp2p-circuit-relay", Detail: fmt.Sprintf("%s (report-only — set %s=1 to embed a real libp2p host)", base, envLibp2pHostEnable)}
	default:
		return IngressDescriptor{Mode: "libp2p-circuit-relay", Detail: base}
	}
}

// ResolvePeer keeps the embedded host (if any) reconciled via the same
// Reconcile() path as Ingress, but still honestly reports not-ok: even with
// a live host maintaining relay reservations, this seam does not yet
// implement dial-through-Circuit-Relay-v2 peer resolution (that would need
// to know which specific peer ID to dial and via which reservation) — wiring
// that is future work. Facet C therefore still stays unresolved rather than
// lying about success, exactly as before.
func (libp2pProvider) ResolvePeer(_ context.Context, _ string) (string, bool) {
	ensureLibp2pManager(currentConfig())
	return "", false
}

// --- wireguard: mesh coordinator endpoint. Facet B ONLY. No key material. ---

type wireguardProvider struct{ cfg WireGuardProviderConfig }

func (wireguardProvider) Name() Provider                                 { return ProviderWireGuard }
func (wireguardProvider) Capabilities() Facet                            { return FacetIngress }
func (wireguardProvider) ICEServers(context.Context, string) []ICEServer { return nil }

func (p wireguardProvider) Ingress() IngressDescriptor {
	return IngressDescriptor{Mode: "wireguard-mesh", Detail: p.cfg.Endpoint}
}
func (wireguardProvider) ResolvePeer(context.Context, string) (string, bool) { return "", false }

// probeCandidate performs the pre-commit health probe Set() runs unless
// force=true. Only turn/wireguard are probed — they're the two providers
// with a literal dialable endpoint AND enough lockout risk (a wrong TURN/mesh
// endpoint silently breaks calls/ingress) to justify a default speed bump;
// ephor/none/libp2p are never blocked (ephor is the safe fallback, none
// has nothing to dial, libp2p's multiaddrs aren't TCP-dialable without a
// libp2p stack this package doesn't embed).
func probeCandidate(cfg Config) error {
	switch cfg.Provider {
	case ProviderTURN:
		host, port, ok := firstICEHostPort(cfg.TURN.ICEServers)
		if !ok {
			return nil // structurally validated already; nothing to dial
		}
		res := dialTest(host, port)
		if !res.Success {
			return fmt.Errorf("could not reach TURN/STUN server %s: %s", net.JoinHostPort(host, port), res.Detail)
		}
	case ProviderWireGuard:
		host, port, ok := wireGuardHostPort(cfg.WireGuard.Endpoint)
		if !ok {
			return nil
		}
		res := dialTest(host, port)
		if !res.Success {
			return fmt.Errorf("could not reach WireGuard coordinator %s: %s", net.JoinHostPort(host, port), res.Detail)
		}
	}
	return nil
}
