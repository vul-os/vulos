package fabric

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pion/mdns/v2"
	"golang.org/x/net/ipv4"
)

// Peer is a discovered sibling Vulos instance on the LAN.
type Peer struct {
	// InstanceID is the peer's stable ULID, when known. Empty when discovery
	// only learned an address (the pull/push still works; self-skip then relies
	// on SelfBaseURLs).
	InstanceID string
	// BaseURL is the peer's LAN HTTPS base, e.g. https://192.168.1.42:443.
	// The fabric endpoints hang off this base (/api/fabric/changeset).
	BaseURL string
	// WAN marks a peer whose BaseURL came from a rendezvous relay's /resolve
	// response rather than same-LAN discovery (mDNS/StaticDiscoverer). The
	// relay is an unauthenticated third party (see rendezvous.go's resolve
	// doc), so a WAN peer is dialled through the safedial-guarded, real-TLS
	// WAN client (Config.WANHTTPClient) instead of the LAN client — see
	// Service.httpClientFor in client.go. Defaults to false, which preserves
	// existing LAN-client behaviour for every discoverer except
	// RendezvousDiscoverer.
	WAN bool
}

// Discoverer reports the current set of fabric peers on the LAN. Implementations
// must be safe for concurrent use and must not block indefinitely — honour ctx.
type Discoverer interface {
	Peers(ctx context.Context) ([]Peer, error)
}

// StaticDiscoverer is a fixed peer set. It is the test seam (point it at
// httptest listeners) and is also useful for a manually-configured peer list
// in environments where multicast is unavailable.
type StaticDiscoverer struct {
	mu    sync.RWMutex
	peers []Peer
}

// NewStaticDiscoverer returns a StaticDiscoverer seeded with peers.
func NewStaticDiscoverer(peers ...Peer) *StaticDiscoverer {
	return &StaticDiscoverer{peers: append([]Peer(nil), peers...)}
}

// Peers implements Discoverer.
func (d *StaticDiscoverer) Peers(_ context.Context) ([]Peer, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return append([]Peer(nil), d.peers...), nil
}

// Set replaces the peer set (e.g. when configuration changes).
func (d *StaticDiscoverer) Set(peers ...Peer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.peers = append([]Peer(nil), peers...)
}

// ── mDNS-backed discovery ──────────────────────────────────────────────────

// FabricMDNSName is the SHARED multicast-DNS name every fabric-enabled box
// advertises. It is the bootstrap name: pion/mdns's QueryAddr resolves a name to
// a SINGLE responder, so the shared name alone reliably finds only ONE peer (the
// first to answer). It remains useful for first-contact on a fresh LAN, but
// multi-peer (>2 box) discovery additionally uses per-instance qualified names —
// see InstanceFabricName.
const FabricMDNSName = "vulos-fabric.local"

// FabricMDNSSuffix is the suffix appended after a per-instance label to build a
// box's unique mDNS name (InstanceFabricName). Each box answers BOTH the shared
// FabricMDNSName and its own <id>.vulos-fabric.local, so a querier that knows a
// roster of peer instance ids can resolve EACH peer's distinct A record — which
// is what makes a 3+ box LAN converge despite QueryAddr's one-answer-per-name
// limitation.
const FabricMDNSSuffix = ".vulos-fabric.local"

// InstanceFabricName returns the per-instance mDNS name a box advertises and
// peers resolve to find that specific box. The instance id is lower-cased and
// stripped of characters that are not valid in a DNS label so the name is a
// well-formed hostname (ULIDs are Crockford base32 — already label-safe — but we
// sanitise defensively for any id shape). Returns "" for an empty id.
func InstanceFabricName(instanceID string) string {
	label := sanitizeDNSLabel(instanceID)
	if label == "" {
		return ""
	}
	return label + FabricMDNSSuffix
}

// sanitizeDNSLabel lower-cases and keeps only [a-z0-9-] from s, truncating to the
// 63-octet DNS label limit. A leading/trailing '-' is trimmed. Returns "" when
// nothing usable remains.
func sanitizeDNSLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		}
		if b.Len() >= 63 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// MDNSDiscoverer discovers fabric peers over multicast DNS. It advertises this
// box under FabricMDNSName (so peers find us) and queries the same name to find
// peers. Because pion/mdns resolves a name to a single A record per responder,
// discovery is run against a configurable list of candidate suffixes plus a
// best-effort multicast query; the resolved IPs are turned into peer base URLs.
//
// SECURITY: discovery only yields candidate addresses. It confers NO trust — a
// discovered address is just "someone on the LAN claims to run a fabric". The
// authenticated exchange (X-Fabric-Auth) is what actually gates whether two
// boxes share state, so a rogue mDNS responder cannot inject registry data.
type MDNSDiscoverer struct {
	conn       *mdns.Conn
	port       int
	queryNames []string // static names always queried (incl. the shared name)

	// peerNamesFunc, when set, supplies additional per-instance qualified names
	// to resolve each round (sourced from the registry roster). This is the
	// multi-peer (>2 box) path: querying <id>.vulos-fabric.local for each known
	// peer id resolves EACH peer's distinct A record, working around QueryAddr's
	// one-answer-per-name limitation.
	peerNamesFunc func() []string

	mu    sync.RWMutex
	cache []Peer
}

// MDNSConfig configures an MDNSDiscoverer.
type MDNSConfig struct {
	// SelfIP is this box's LAN IP, published as the answer to its mDNS names so
	// peers can find us. When nil, advertisement is best-effort/disabled.
	SelfIP net.IP
	// Port is the LAN HTTPS port peers should be dialled on. Defaults to 443.
	Port int
	// InstanceID, when set, makes this box ALSO advertise the per-instance name
	// InstanceFabricName(InstanceID) in addition to the shared FabricMDNSName, so
	// peers that know this box's id can resolve it specifically. This is what
	// enables >2-box convergence.
	InstanceID string
	// QueryNames are extra static mDNS names to resolve when looking for peers.
	// The shared FabricMDNSName is ALWAYS queried; these are appended.
	QueryNames []string
	// PeerNamesFunc, when set, is called each discovery round to obtain the
	// per-instance qualified names to resolve (typically built from the registry
	// roster of known peer ULIDs via InstanceFabricName). It lets a box discover
	// every rostered peer individually rather than only the single responder the
	// shared name yields. Must be safe for concurrent use.
	PeerNamesFunc func() []string
}

// NewMDNSDiscoverer starts advertising this box (shared name + its per-instance
// name when InstanceID is set) and prepares to resolve peers. Multicast binding
// can fail in restricted environments (CI/containers); callers should treat a
// non-nil error as "mDNS discovery unavailable" and fall back to a
// StaticDiscoverer.
func NewMDNSDiscoverer(cfg MDNSConfig) (*MDNSDiscoverer, error) {
	if cfg.Port == 0 {
		cfg.Port = 443
	}
	// Always query the shared name (bootstrap/first-contact), then any extra
	// static names. Per-instance roster names are added per-round via
	// PeerNamesFunc, not baked in here.
	names := []string{FabricMDNSName}
	names = append(names, cfg.QueryNames...)

	addr4, err := net.ResolveUDPAddr("udp4", mdns.DefaultAddressIPv4)
	if err != nil {
		return nil, fmt.Errorf("fabric: resolve mdns addr: %w", err)
	}
	l, err := net.ListenUDP("udp4", addr4)
	if err != nil {
		return nil, fmt.Errorf("fabric: mdns listen: %w", err)
	}

	// Advertise the shared name AND (when known) this box's per-instance name so
	// a peer that learns our id from the roster can resolve us specifically.
	localNames := []string{strings.TrimSuffix(FabricMDNSName, ".")}
	if self := InstanceFabricName(cfg.InstanceID); self != "" {
		localNames = append(localNames, strings.TrimSuffix(self, "."))
	}
	mcfg := &mdns.Config{LocalNames: localNames}
	if v4 := selfV4(cfg.SelfIP); v4 != nil {
		mcfg.LocalAddress = v4
	}
	conn, err := mdns.Server(ipv4.NewPacketConn(l), nil, mcfg)
	if err != nil {
		l.Close()
		return nil, fmt.Errorf("fabric: mdns server: %w", err)
	}

	return &MDNSDiscoverer{
		conn:          conn,
		port:          cfg.Port,
		queryNames:    names,
		peerNamesFunc: cfg.PeerNamesFunc,
	}, nil
}

// SetPeerNamesFunc registers (or replaces) the roster provider used to build the
// per-instance names queried each round. Safe to call after construction (e.g.
// once the registry handle is available in main.go).
func (d *MDNSDiscoverer) SetPeerNamesFunc(fn func() []string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.peerNamesFunc = fn
	d.mu.Unlock()
}

func selfV4(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil && !v4.IsLoopback() {
		return v4
	}
	return nil
}

// Peers resolves the configured query names over mDNS and returns the resulting
// peer base URLs. Each query is given a short deadline so the call bounds its
// own latency; unresolved names are skipped (they simply mean "no peer answered
// under that name right now"). The last successful resolution set is cached so a
// transient multicast miss does not immediately drop a known peer.
func (d *MDNSDiscoverer) Peers(ctx context.Context) ([]Peer, error) {
	if d.conn == nil {
		return nil, errors.New("fabric: mdns discoverer closed")
	}

	// Build the full query set for this round: the static names (shared + any
	// configured extras) plus the per-instance roster names. Each roster entry
	// is "<id>.vulos-fabric.local" and carries the originating instance id, so a
	// resolved address can be stamped with the peer's id (sharpening self-skip).
	type query struct {
		name       string
		instanceID string // "" for the shared/static names
	}
	queries := make([]query, 0, len(d.queryNames)+4)
	for _, n := range d.queryNames {
		queries = append(queries, query{name: n})
	}
	d.mu.RLock()
	pn := d.peerNamesFunc
	d.mu.RUnlock()
	if pn != nil {
		for _, id := range pn() {
			name := InstanceFabricName(id)
			if name == "" {
				continue
			}
			queries = append(queries, query{name: name, instanceID: id})
		}
	}

	seen := make(map[string]Peer)
	for _, q := range queries {
		qctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		_, addr, err := d.conn.QueryAddr(qctx, strings.TrimSuffix(q.name, "."))
		cancel()
		if err != nil {
			continue // no answer for this name this round
		}
		if !addr.IsValid() || addr.IsLoopback() {
			continue
		}
		base := fmt.Sprintf("https://%s", net.JoinHostPort(addr.String(), itoa(d.port)))
		// Prefer an entry carrying the instance id (qualified-name resolution)
		// over a bare shared-name hit for the same base URL.
		if existing, ok := seen[base]; !ok || (existing.InstanceID == "" && q.instanceID != "") {
			seen[base] = Peer{BaseURL: base, InstanceID: q.instanceID}
		}
	}

	if len(seen) == 0 {
		// Nothing answered this round — return the cached set rather than
		// flapping the peer list to empty.
		d.mu.RLock()
		cached := append([]Peer(nil), d.cache...)
		d.mu.RUnlock()
		return cached, nil
	}

	out := make([]Peer, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	d.mu.Lock()
	d.cache = out
	d.mu.Unlock()
	return out, nil
}

// Close stops the mDNS advertisement/resolver.
func (d *MDNSDiscoverer) Close() error {
	if d == nil || d.conn == nil {
		return nil
	}
	err := d.conn.Close()
	d.conn = nil
	return err
}

// itoa is a tiny strconv.Itoa without importing strconv just for this.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// PortFromAddr extracts the TCP port from a listen address (e.g. ":443" or
// "127.0.0.1:8443"), returning the supplied fallback when the address has no
// parseable port. It lets the caller advertise fabric peers on the same port
// the LAN HTTPS listener actually serves.
func PortFromAddr(addr string, fallback int) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return fallback
	}
	p, err := net.LookupPort("tcp", portStr)
	if err != nil || p <= 0 {
		return fallback
	}
	return p
}

// normalizeBaseURL trims a trailing slash from a base URL so endpoint joins are
// consistent. It validates the URL is parseable and absolute.
func normalizeBaseURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("fabric: bad peer base URL %q: %w", base, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("fabric: peer base URL %q must be absolute", base)
	}
	return strings.TrimRight(base, "/"), nil
}
