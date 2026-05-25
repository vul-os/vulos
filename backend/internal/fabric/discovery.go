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

// FabricMDNSName is the multicast-DNS name every fabric-enabled box advertises.
// pion/mdns answers A queries for ".local" names; we use one shared service
// name and let every box answer it with its own LAN IP. A querier therefore
// learns the set of LAN IPs running the fabric. The port is the well-known LAN
// HTTPS port unless the box advertises an override via FabricPort.
const FabricMDNSName = "vulos-fabric.local"

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
	queryNames []string

	mu    sync.RWMutex
	cache []Peer
}

// MDNSConfig configures an MDNSDiscoverer.
type MDNSConfig struct {
	// SelfIP is this box's LAN IP, published as the answer to FabricMDNSName so
	// peers can find us. When nil, advertisement is best-effort/disabled.
	SelfIP net.IP
	// Port is the LAN HTTPS port peers should be dialled on. Defaults to 443.
	Port int
	// QueryNames are the mDNS names to resolve when looking for peers. Defaults
	// to [FabricMDNSName]. A deployment that gives each box a distinct
	// box.<id>.local name can list those here too.
	QueryNames []string
}

// NewMDNSDiscoverer starts advertising this box under FabricMDNSName and
// prepares to resolve peers. Multicast binding can fail in restricted
// environments (CI/containers); callers should treat a non-nil error as
// "mDNS discovery unavailable" and fall back to a StaticDiscoverer.
func NewMDNSDiscoverer(cfg MDNSConfig) (*MDNSDiscoverer, error) {
	if cfg.Port == 0 {
		cfg.Port = 443
	}
	names := cfg.QueryNames
	if len(names) == 0 {
		names = []string{FabricMDNSName}
	}

	addr4, err := net.ResolveUDPAddr("udp4", mdns.DefaultAddressIPv4)
	if err != nil {
		return nil, fmt.Errorf("fabric: resolve mdns addr: %w", err)
	}
	l, err := net.ListenUDP("udp4", addr4)
	if err != nil {
		return nil, fmt.Errorf("fabric: mdns listen: %w", err)
	}

	mcfg := &mdns.Config{
		// Answer FabricMDNSName so peers querying it discover this box.
		LocalNames: []string{strings.TrimSuffix(FabricMDNSName, ".")},
	}
	if v4 := selfV4(cfg.SelfIP); v4 != nil {
		mcfg.LocalAddress = v4
	}
	conn, err := mdns.Server(ipv4.NewPacketConn(l), nil, mcfg)
	if err != nil {
		l.Close()
		return nil, fmt.Errorf("fabric: mdns server: %w", err)
	}

	return &MDNSDiscoverer{
		conn:       conn,
		port:       cfg.Port,
		queryNames: names,
	}, nil
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

	seen := make(map[string]Peer)
	for _, name := range d.queryNames {
		qctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		_, addr, err := d.conn.QueryAddr(qctx, strings.TrimSuffix(name, "."))
		cancel()
		if err != nil {
			continue // no answer for this name this round
		}
		if !addr.IsValid() || addr.IsLoopback() {
			continue
		}
		base := fmt.Sprintf("https://%s", net.JoinHostPort(addr.String(), itoa(d.port)))
		seen[base] = Peer{BaseURL: base}
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
