package lan

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/pion/mdns/v2"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// mdnsAdvertiser publishes the box's zero-config name (e.g. vulos.local) over
// multicast DNS so any client on the same LAN can discover and reach the box
// with no configuration. It wraps the pure-Go pion/mdns server.
type mdnsAdvertiser struct {
	conn *mdns.Conn
}

// newMDNSAdvertiser starts advertising hostname (which must end in ".local")
// over mDNS, publishing lanIP as the answer when lanIP is a routable IPv4.
//
// Multicast binding can legitimately fail in restricted environments (CI,
// containers without CAP_NET_RAW, hosts with no multicast-capable interface);
// in that case it returns an error and the caller treats mDNS as best-effort.
func newMDNSAdvertiser(lanIP net.IP, hostname string) (*mdnsAdvertiser, error) {
	name := strings.TrimSuffix(hostname, ".")
	if !strings.HasSuffix(name, ".local") {
		return nil, fmt.Errorf("lan: mdns name %q must end in .local", hostname)
	}

	addr4, err := net.ResolveUDPAddr("udp4", mdns.DefaultAddressIPv4)
	if err != nil {
		return nil, fmt.Errorf("lan: resolve mdns v4 addr: %w", err)
	}
	addr6, err := net.ResolveUDPAddr("udp6", mdns.DefaultAddressIPv6)
	if err != nil {
		return nil, fmt.Errorf("lan: resolve mdns v6 addr: %w", err)
	}

	// Bind each family independently; an mDNS server needs at least one. IPv6
	// multicast is frequently unavailable, so a v4-only bind is acceptable.
	var pc4 *ipv4.PacketConn
	var pc6 *ipv6.PacketConn
	if l4, err := net.ListenUDP("udp4", addr4); err == nil {
		pc4 = ipv4.NewPacketConn(l4)
	}
	if l6, err := net.ListenUDP("udp6", addr6); err == nil {
		pc6 = ipv6.NewPacketConn(l6)
	}
	if pc4 == nil && pc6 == nil {
		return nil, errors.New("lan: mdns could not bind any multicast socket")
	}

	cfg := &mdns.Config{
		Name:       "vulos-lan",
		LocalNames: []string{name},
	}
	// Publish the detected LAN IP explicitly when it is a usable IPv4, rather
	// than relying on pion's per-interface auto-detection.
	if v4 := lanIP.To4(); v4 != nil && !v4.IsLoopback() {
		cfg.LocalAddress = v4
	}

	conn, err := mdns.Server(pc4, pc6, cfg)
	if err != nil {
		return nil, fmt.Errorf("lan: start mdns server: %w", err)
	}
	return &mdnsAdvertiser{conn: conn}, nil
}

// Close stops the mDNS advertisement.
func (m *mdnsAdvertiser) Close() error {
	if m == nil || m.conn == nil {
		return nil
	}
	return m.conn.Close()
}
