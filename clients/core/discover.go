package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/pion/mdns/v2"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// mdnsBoxHostname is the name the box advertises over mDNS. It MUST match
// backend/internal/lan/lan.go's mdnsHostname constant exactly — this is
// another cross-repo format the two sides share.
//
// The box's mDNS server (backend/internal/lan/mdns.go, built on
// github.com/pion/mdns/v2) only ever answers hostname A/AAAA queries for this
// one fixed name. It does not publish DNS-SD PTR/SRV/TXT records for a
// service type (e.g. "_vulos._tcp.local."), so there is no service to browse
// and no way to discover a box's name or port over mDNS — only to resolve
// this one hardcoded hostname to an IP. Discover reflects that: it performs
// the same hostname query the box actually answers, rather than inventing a
// service-type string the box would never respond to.
const mdnsBoxHostname = "vulos.local"

// boxHTTPSPort is the box's default LAN HTTPS listen port (see
// backend/internal/lan/lan.go's Config.HTTPSAddr, which defaults to ":443").
// mDNS resolves only an address, never a port, so Discover assumes this
// documented default.
const boxHTTPSPort = "443"

// discoverTimeout bounds how long Discover waits for an mDNS answer when the
// caller's context carries no deadline of its own.
const discoverTimeout = 5 * time.Second

// Discover finds Vulos boxes on the local network over mDNS.
//
// Discovery is NOT trust: a returned Box may be an impostor advertising the
// same name. Nothing discovered here may be connected to without a pin that
// was confirmed out of band via Pair.
//
// LIMITATION: because the box advertises a single fixed hostname rather than
// a discoverable, per-instance service record (see mdnsBoxHostname), Discover
// can find at most one Box per call: whichever machine on the LAN currently
// answers for "vulos.local". On a LAN with more than one Vulos box this
// cannot distinguish between them — that needs a box-side change (per-
// instance mDNS names, or real DNS-SD records) that is out of scope for this
// client package.
func Discover(ctx context.Context) ([]Box, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, discoverTimeout)
		defer cancel()
	}

	addr4, err := net.ResolveUDPAddr("udp4", mdns.DefaultAddressIPv4)
	if err != nil {
		return nil, fmt.Errorf("clients/core: discover: resolve mdns v4 addr: %w", err)
	}
	addr6, err := net.ResolveUDPAddr("udp6", mdns.DefaultAddressIPv6)
	if err != nil {
		return nil, fmt.Errorf("clients/core: discover: resolve mdns v6 addr: %w", err)
	}

	var pc4 *ipv4.PacketConn
	var pc6 *ipv6.PacketConn
	if l4, lerr := net.ListenUDP("udp4", addr4); lerr == nil {
		pc4 = ipv4.NewPacketConn(l4)
	}
	if l6, lerr := net.ListenUDP("udp6", addr6); lerr == nil {
		pc6 = ipv6.NewPacketConn(l6)
	}
	if pc4 == nil && pc6 == nil {
		return nil, errors.New("clients/core: discover: could not bind an mDNS multicast socket " +
			"(no usable network interface, or multicast blocked — common in CI/sandboxed environments)")
	}

	// mdns.Server is pion's name for "an mDNS Conn" — it plays both server and
	// client roles depending on config. With no LocalNames set it advertises
	// nothing of its own; it is used purely to send queries and read answers.
	conn, err := mdns.Server(pc4, pc6, &mdns.Config{Name: "vulos-client"})
	if err != nil {
		// mdns.Server takes ownership of pc4/pc6 only on success; on failure
		// close whichever we opened so this path leaks no sockets.
		if pc4 != nil {
			_ = pc4.Close()
		}
		if pc6 != nil {
			_ = pc6.Close()
		}
		return nil, fmt.Errorf("clients/core: discover: start mdns client: %w", err)
	}
	defer conn.Close()

	_, ipAddr, err := conn.QueryAddr(ctx, mdnsBoxHostname)
	if err != nil {
		return nil, fmt.Errorf("clients/core: discover: mdns query for %q: %w", mdnsBoxHostname, err)
	}

	return []Box{{
		Name: mdnsBoxHostname,
		Addr: net.JoinHostPort(ipAddr.String(), boxHTTPSPort),
	}}, nil
}
