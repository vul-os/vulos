package lan

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/pion/mdns/v2"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// mdnsAdvertiser publishes the box's zero-config names (e.g. study.local,
// vulos-k3n7q2.local, vulos.local) over multicast DNS so any client on the same
// LAN can discover and reach the box with no configuration. It wraps the
// pure-Go pion/mdns server.
type mdnsAdvertiser struct {
	conn  *mdns.Conn
	names []string // the names actually claimed, after conflict probing
}

// Names returns the names this advertiser actually claimed. It can be shorter
// than the requested set: a name already answered by another box on the link is
// dropped rather than claimed a second time. See probeConflict.
func (m *mdnsAdvertiser) Names() []string {
	if m == nil {
		return nil
	}
	return append([]string(nil), m.names...)
}

// mdnsProbeTimeout bounds the RFC 6762 §8-style conflict probe per name.
//
// pion's QueryAddr re-sends every QueryInterval until it gets an answer or the
// context expires, so this is the cost paid ONCE per name at startup when
// nobody answers (the common single-box case). 750ms is comfortably above a
// LAN round trip and keeps a three-name probe under 2.5s of boot time.
const mdnsProbeTimeout = 750 * time.Millisecond

// newMDNSAdvertiser starts advertising names (each of which must end in
// ".local") over mDNS, publishing lanIP as the answer when lanIP is a routable
// IPv4.
//
// CONFLICT PROBING (added 2026-08-17). pion/mdns performs no RFC 6762 §8
// probing whatsoever: Server() claims every LocalName unconditionally and
// answers every matching question. With every box shipping the identical
// "vulos.local" that meant TWO boxes answered the same question and the client
// took whichever reply raced in first. Measured, two boxes on one link, ten
// lookups from a third host: .6 .5 .5 .5 .5 .5 .5 .5 .6 .5 — a coin flip, and
// because both certificates carried vulos.local, TLS succeeded on the wrong box
// without a single warning.
//
// So: before claiming anything, query the link for each requested name. A name
// somebody else already answers is DROPPED from the claim (logged by the
// caller), which makes the outcome first-come-first-served instead of random.
// Names derived from the box's own instance id never collide in practice, so a
// box always keeps at least one working name — that is the whole point of
// NameSet.Unique.
//
// This is not full RFC 6762 §8.2 tie-breaking: two boxes booting close enough
// together can both probe before either answers, and both will claim. That is a
// narrow window with a benign outcome (the pre-existing behaviour) and it does
// not affect the per-instance names, which differ by construction.
//
// Multicast binding can legitimately fail in restricted environments (CI,
// containers without CAP_NET_RAW, hosts with no multicast-capable interface);
// in that case it returns an error and the caller treats mDNS as best-effort.
func newMDNSAdvertiser(lanIP net.IP, names []string) (*mdnsAdvertiser, error) {
	want := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSuffix(strings.TrimSpace(n), ".")
		if !strings.HasSuffix(n, mdnsSuffix) {
			return nil, fmt.Errorf("lan: mdns name %q must end in %s", n, mdnsSuffix)
		}
		want = append(want, n)
	}
	if len(want) == 0 {
		return nil, errors.New("lan: mdns needs at least one name")
	}

	claim := make([]string, 0, len(want))
	for _, n := range want {
		if taken, by := probeConflict(n, lanIP); taken {
			// Another box on this link already answers for n. Leave it alone.
			log.Printf("[lan] mDNS name %s is already claimed on this link by %s — not advertising it from this box", n, by)
			continue
		}
		claim = append(claim, n)
	}
	if len(claim) == 0 {
		return nil, fmt.Errorf("lan: every mDNS name (%v) is already claimed on this link", want)
	}

	conn, err := serveMDNS(lanIP, claim)
	if err != nil {
		return nil, err
	}
	return &mdnsAdvertiser{conn: conn, names: claim}, nil
}

// bindMulticast binds the v4 and v6 mDNS multicast sockets. An mDNS endpoint
// needs at least one; IPv6 multicast is frequently unavailable, so a v4-only
// bind is acceptable.
func bindMulticast() (*ipv4.PacketConn, *ipv6.PacketConn, error) {
	addr4, err := net.ResolveUDPAddr("udp4", mdns.DefaultAddressIPv4)
	if err != nil {
		return nil, nil, fmt.Errorf("lan: resolve mdns v4 addr: %w", err)
	}
	addr6, err := net.ResolveUDPAddr("udp6", mdns.DefaultAddressIPv6)
	if err != nil {
		return nil, nil, fmt.Errorf("lan: resolve mdns v6 addr: %w", err)
	}

	var pc4 *ipv4.PacketConn
	var pc6 *ipv6.PacketConn
	if l4, err := net.ListenUDP("udp4", addr4); err == nil {
		pc4 = ipv4.NewPacketConn(l4)
	}
	if l6, err := net.ListenUDP("udp6", addr6); err == nil {
		pc6 = ipv6.NewPacketConn(l6)
	}
	if pc4 == nil && pc6 == nil {
		return nil, nil, errors.New("lan: mdns could not bind any multicast socket")
	}
	return pc4, pc6, nil
}

// serveMDNS starts a pion mDNS server answering for names with lanIP.
func serveMDNS(lanIP net.IP, names []string) (*mdns.Conn, error) {
	pc4, pc6, err := bindMulticast()
	if err != nil {
		return nil, err
	}
	cfg := &mdns.Config{
		Name:       "vulos-lan",
		LocalNames: names,
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
	return conn, nil
}

// probeConflict reports whether name is already answered on this link by
// somebody other than us, and by which address.
//
// selfIP is excluded so that a restarting box, a second Service in the same
// process, or a co-resident avahi-daemon advertising the SAME host does not
// look like a foreign claimant. (On the bare-metal image avahi-daemon is
// started by cmd/init and answers for the system hostname; it lives at the same
// address we do, so it must not cause us to abandon our own name.)
//
// A probe failure — no multicast, no interfaces, CI — returns false: mDNS is
// best-effort and an unprobeable link must not stop the box from advertising.
// That is fail-open by design; the cost of a false negative is the
// pre-existing coin flip, the cost of fail-closed is a box with no name at all.
var probeConflict = func(name string, selfIP net.IP) (bool, string) {
	pc4, pc6, err := bindMulticast()
	if err != nil {
		return false, ""
	}
	conn, err := mdns.Server(pc4, pc6, &mdns.Config{Name: "vulos-lan-probe"})
	if err != nil {
		return false, ""
	}
	defer conn.Close() //nolint:errcheck // best-effort probe

	ctx, cancel := context.WithTimeout(context.Background(), mdnsProbeTimeout)
	defer cancel()

	_, addr, err := conn.QueryAddr(ctx, strings.TrimSuffix(name, "."))
	if err != nil {
		return false, "" // nobody answered within the probe window
	}
	if isSelfAddr(addr, selfIP) {
		return false, ""
	}
	return true, addr.String()
}

// isSelfAddr reports whether a probe answer came from this box.
func isSelfAddr(addr netip.Addr, selfIP net.IP) bool {
	if !addr.IsValid() {
		return true // unusable answer; treat as ours rather than yielding the name
	}
	if addr.IsLoopback() || addr.IsUnspecified() {
		return true
	}
	if selfIP != nil {
		if s, ok := netip.AddrFromSlice(selfIP.To4()); ok && s == addr.Unmap() {
			return true
		}
		if s, ok := netip.AddrFromSlice(selfIP); ok && s.Unmap() == addr.Unmap() {
			return true
		}
	}
	// An answer from any address this host itself holds is also us.
	if ifAddrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifAddrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if s, ok := netip.AddrFromSlice(ipNet.IP); ok && s.Unmap() == addr.Unmap() {
				return true
			}
		}
	}
	return false
}

// Close stops the mDNS advertisement.
func (m *mdnsAdvertiser) Close() error {
	if m == nil || m.conn == nil {
		return nil
	}
	return m.conn.Close()
}
