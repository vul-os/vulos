// no-broker-dep:allow-file: the file implementing the whole provider seam: providerFor's default
// case returns vulosProvider{} (not ephor), and ephorProvider is a peer
// alternative selected only when Provider=="ephor" is explicitly
// persisted. Names Pier extensively to describe this optional peer,
// never as this package's own dependency.

package relayconfig

import (
	"context"
	"fmt"
	"net"
	"sync"
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
// that facet instead falls back to the built-in provider's own gate. Implementations for
// unclaimed facets simply return zero values and are never reached.
type ReachabilityProvider interface {
	Name() Provider
	Capabilities() Facet
	ICEServers(ctx context.Context, userID string) []ICEServer
	Ingress() IngressDescriptor
	ResolvePeer(ctx context.Context, peerID string) (baseURL string, ok bool)
}

// providerFor constructs the ReachabilityProvider for name, closing over
// cfg's per-provider sections. The built-in provider needs no config here (it
// reads env / the injected TURNStore / the injected ingress reporter); an
// unrecognised name fails safe to the built-in default.
func providerFor(name Provider, cfg Config) ReachabilityProvider {
	switch name {
	case ProviderEphor:
		return ephorProvider{}
	case ProviderNone:
		return noneProvider{}
	case ProviderTURN:
		return turnProvider{cfg: cfg.TURN}
	case ProviderLibp2p:
		return libp2pProvider{cfg: cfg.Libp2p}
	case ProviderWireGuard:
		return wireguardProvider{cfg: cfg.WireGuard}
	default:
		return vulosProvider{}
	}
}

func activeProvider() ReachabilityProvider {
	cfg := currentConfig()
	return providerFor(cfg.Provider, cfg)
}

// ICEServers is the ONLY producer of app-media ICE config — Meet/Talk/diwan
// (and anything else that opens an RTCPeerConnection) should call this
// instead of hardcoding STUN/TURN. It NEVER goes dark just because the
// active provider covers ingress/rendezvous but not ICE (none/libp2p/
// wireguard): those providers don't claim FacetICE, so this automatically
// falls back to the built-in ICE default. Callers must not branch on provider
// name; the returned list is already the right one.
func ICEServers(ctx context.Context, userID string) []ICEServer {
	p := activeProvider()
	if p.Capabilities().Has(FacetICE) {
		return p.ICEServers(ctx, userID)
	}
	return vulosProvider{}.ICEServers(ctx, userID)
}

// IngressInfo reports how the box is currently reachable from outside NAT —
// the active provider if it claims FacetIngress, else the built-in default.
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
	return vulosProvider{}.Ingress()
}

// ResolvePeer resolves peerID to a base URL via the active rendezvous
// provider, if it claims FacetRendezvous, else via the built-in default (the
// existing peering.resolve.go cache/HTTP path, which this package does not
// duplicate — see ephorProvider.ResolvePeer).
func ResolvePeer(ctx context.Context, peerID string) (string, bool) {
	p := activeProvider()
	if p.Capabilities().Has(FacetRendezvous) {
		return p.ResolvePeer(ctx, peerID)
	}
	return vulosProvider{}.ResolvePeer(ctx, peerID)
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

// --- vulos: the built-in default. No special privilege — registered and
// dispatched exactly like every other provider. ---

// ingressReporter is injected at startup by the composition root so this
// package can report the LIVE state of the built-in reverse tunnel without
// importing it (services/reach/tunnel would be an import cycle through the
// wiring layer, and this package must stay dependency-light — every other
// service imports it for ICE).
//
// Nil means "nothing has reported", which is the correct state before startup
// finishes and on a box that runs no tunnel at all.
var (
	ingressMu       sync.RWMutex
	ingressReporter func() IngressDescriptor
)

// SetIngressReporter injects the live reachability reporter. Call once at
// startup, from the same place that starts the tunnel agent. Passing nil
// clears it, which is what a box with no relays configured should do.
func SetIngressReporter(fn func() IngressDescriptor) {
	ingressMu.Lock()
	defer ingressMu.Unlock()
	ingressReporter = fn
}

func reportedIngress() (IngressDescriptor, bool) {
	ingressMu.RLock()
	fn := ingressReporter
	ingressMu.RUnlock()
	if fn == nil {
		return IngressDescriptor{}, false
	}
	d := fn()
	return d, d.Mode != ""
}

type vulosProvider struct{}

func (vulosProvider) Name() Provider      { return ProviderVulos }
func (vulosProvider) Capabilities() Facet { return FacetICE | FacetIngress | FacetRendezvous }

func (vulosProvider) ICEServers(_ context.Context, userID string) []ICEServer {
	return builtinICEServers(userID)
}

// Ingress reports the LIVE state of the built-in reverse tunnel, not a guess.
//
// The previous implementation returned a hardcoded relay hostname whether or
// not anything was actually running there, which meant Settings could show a
// confident "relay-tunnel https://…" for a box that was in fact unreachable
// from the internet. Reporting "no relay configured" when that is the truth
// is more useful than reporting a URL nobody is serving.
func (vulosProvider) Ingress() IngressDescriptor {
	return relayTunnelIngress("no relay endpoint configured — this box is reachable on its LAN, and publicly only if the DIRECT-IP listener is enabled")
}

// relayTunnelIngress reports the LIVE state of the built-in reverse-tunnel
// agent, and is shared by BOTH relay-tunnel providers (vulos AND ephor).
//
// # Why one reporter serves both providers — the dual-provider keystone
//
// The embedded agent (services/reach/tunnel) holds one link per configured
// relay REGARDLESS of who operates it. A built-in Vulos relay and a Pier
// relay listed together in VULOS_RELAY_ENDPOINTS are dialled, served, and
// reported through the exact same agent and the exact same header-trust
// boundary — the tunnel layer has no notion of "provider" at all. So an owner
// can run BOTH at once for redundancy, and the ingress status must reflect
// EVERY live link, not just the first relay's URL.
//
// That is why both providers resolve ingress through this one live reporter
// rather than re-reading a single VULOS_RELAY_BASE_URL from the environment:
// re-reading that legacy variable would ignore every relay past the first and
// would show a URL nobody is serving when that first relay happens to be down.
// The provider label an owner selects here changes only which STUN / rendezvous
// facet answers — never which tunnels exist, and never how ingress is reported.
//
// noRelayDetail is the provider-specific message shown when the agent reports
// nothing (no relay configured, or none currently up).
func relayTunnelIngress(noRelayDetail string) IngressDescriptor {
	if d, ok := reportedIngress(); ok {
		return d
	}
	return IngressDescriptor{Mode: "none", Detail: noRelayDetail}
}

// ResolvePeer intentionally returns not-ok: the built-in rendezvous facet is
// already served live by backend/services/peering/resolve.go's existing
// cache/HTTP path. This package does not duplicate that logic (and must not
// import the peering package — peering imports THIS package for ICE, and a
// reverse import would cycle); ResolvePeer exists on this interface purely
// so an alternative rendezvous provider (libp2p) can be swapped in without
// peering.go ever branching on provider name.
func (vulosProvider) ResolvePeer(context.Context, string) (string, bool) { return "", false }

// --- ephor: the supported ALTERNATIVE relay, and a PEER of the built-in one. ---
//
// Pier (github.com/vul-os/pier) speaks the same rendezvous contract and the
// same reverse-tunnel wire protocol, and serves the same purpose. Selecting it
// is a genuine swap, not a downgrade — which is the whole point of the
// coordinator being hired rather than depended on. ICE resolves identically
// either way: which relay carries HTTP ingress has nothing to do with which
// STUN/TURN servers call media uses.
//
// # Pier and Vulos coexist — this label is not exclusive
//
// Because the tunnel agent (services/reach/tunnel) holds one link per relay in
// VULOS_RELAY_ENDPOINTS with no notion of "provider", an owner can run a
// built-in Vulos relay AND a Pier relay AT THE SAME TIME, in one endpoint
// set, for redundant/versatile access. This provider label selects only which
// facet (ICE/rendezvous) reporting style is authoritative; it does NOT make
// the choice mutually exclusive with the built-in Vulos relay at the tunnel
// layer. Ingress therefore reports the SAME live multi-relay state that
// vulosProvider does — a Vulos relay and a Pier relay in the same set both
// show up — via the shared relayTunnelIngress reporter, rather than re-reading
// a single relay URL from the environment (which would hide the coexistence).
//
// SECURITY: coexistence does not widen the trust boundary. Pier being a
// separate project buys its relay no additional trust — the box-side agent
// strips and re-derives every X-Vulos-Reach-* header for a Pier relay
// exactly as it does for a Vulos relay (see services/reach/tunnel/agent.go's
// tunnelHandler and the unconditional, prefix-based stripPrefixHeaders). A
// compromised relay of EITHER kind is untrusted transport, no more.

type ephorProvider struct{}

func (ephorProvider) Name() Provider      { return ProviderEphor }
func (ephorProvider) Capabilities() Facet { return FacetICE | FacetIngress | FacetRendezvous }

func (ephorProvider) ICEServers(_ context.Context, userID string) []ICEServer {
	return builtinICEServers(userID)
}

func (ephorProvider) Ingress() IngressDescriptor {
	return relayTunnelIngress("Pier selected but no relay tunnel is up — add the Pier relay to VULOS_RELAY_ENDPOINTS (it may sit alongside the built-in Vulos relay in the same set) and see GET /api/network/reach")
}

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

// Ingress reports the configured coordinator AND says plainly that selecting
// this provider actuates nothing. A control that appears to do something is
// the defect this suite keeps finding, and this one had two layers of it: the
// selection does not re-route real HTTP ingress, and it does not resolve peers
// either (see ResolvePeer). The endpoint is recorded, not used.
func (p wireguardProvider) Ingress() IngressDescriptor {
	detail := p.cfg.Endpoint
	if detail == "" {
		detail = "no coordinator endpoint recorded"
	}
	return IngressDescriptor{
		Mode: "wireguard-mesh",
		Detail: detail + " (report-only — this selection records where your mesh coordinator is; " +
			"it does not re-route box ingress and does not resolve peers. Boxes that meet over a " +
			"mesh do it through the rendezvous role and VULOS_PEER_ALLOW_CIDR — see docs/REACH.md)",
	}
}

// ResolvePeer returns not-ok, and DELIBERATELY stays that way.
//
// Two things would have to be true before implementing it meant anything, and
// neither is: this provider does not claim FacetRendezvous (Capabilities above
// is FacetIngress alone), so the package-level ResolvePeer dispatcher never
// reaches this method; and nothing in the tree calls relayconfig.ResolvePeer at
// all — peering's resolvePeerBaseURL consults its own reachability cache and
// then the contact's stored server, never this seam.
//
// The deeper reason is the MAPPING, not the wiring. ResolvePeer is handed a
// VulosID; a mesh knows machine names. MagicDNS names are chosen at `tailscale
// up` time and have no relationship to a VulosID, so a DNS lookup of
// "<vulos-id>.<mesh-suffix>" only works if the operator has already named every
// machine after its VulosID — which is the hand-entered mapping this seam would
// exist to remove, moved to a new place. Getting a real dynamic mapping needs a
// coordinator API and therefore credentials, and WireGuardProviderConfig holds
// no key material on purpose.
//
// The box already has a dynamic, mesh-agnostic, key-addressed answer that does
// work: the rendezvous role (internal/fabric + `vulos relay serve -rendezvous`),
// where each box announces under its own Ed25519 key and resolves siblings by
// key, over whatever network can carry it. With VULOS_PEER_ALLOW_CIDR granting
// the mesh range, that path now dials. Building a second, weaker, mesh-specific
// discovery path beside it would duplicate the one seam this package exists to
// consolidate.
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
