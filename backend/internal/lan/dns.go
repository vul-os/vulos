package lan

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"

	"golang.org/x/net/dns/dnsmessage"
)

// dnsResponder is a tiny authoritative UDP DNS server. It answers A queries for
// exactly one name — the box's box.<id>.lan.vulos.org — with the box's LAN IP,
// and returns NXDOMAIN for anything else. It does no recursion and talks to no
// upstream, so it keeps working with the internet (and public DNS) down.
type dnsResponder struct {
	conn    *net.UDPConn
	name    string // canonical, lower-case, trailing-dot form
	ip      net.IP // IPv4 answer
	wg      sync.WaitGroup
	closeMu sync.Mutex
	closed  bool
}

// newDNSResponder binds a UDP socket at addr and prepares to answer A queries
// for hostname with ip. It does not start reading until Start is called.
func newDNSResponder(addr, hostname string, ip net.IP) (*dnsResponder, error) {
	if ip == nil {
		return nil, errors.New("lan: dns responder needs a LAN IP")
	}
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("lan: resolve dns addr %q: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("lan: dns listen on %s: %w", addr, err)
	}
	return &dnsResponder{
		conn: conn,
		name: canonicalName(hostname),
		ip:   ip,
	}, nil
}

// Addr returns the bound UDP address (useful when a :0 ephemeral port was used).
func (d *dnsResponder) Addr() string {
	if d.conn == nil {
		return ""
	}
	return d.conn.LocalAddr().String()
}

// Start spins up the read loop. The loop exits when the context is cancelled or
// the connection is closed.
func (d *dnsResponder) Start(ctx context.Context) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		<-ctx.Done()
		d.Close()
	}()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		buf := make([]byte, 512)
		for {
			n, src, err := d.conn.ReadFromUDP(buf)
			if err != nil {
				return // connection closed
			}
			resp, ok := d.buildResponse(buf[:n])
			if !ok {
				continue
			}
			if _, err := d.conn.WriteToUDP(resp, src); err != nil {
				log.Printf("[lan] dns write to %v failed: %v", src, err)
			}
		}
	}()
}

// buildResponse parses a DNS query and, if it is an A/ANY question for our name,
// builds an authoritative answer. Otherwise it returns an NXDOMAIN response.
// The bool is false only when the request could not be parsed at all.
func (d *dnsResponder) buildResponse(req []byte) ([]byte, bool) {
	var msg dnsmessage.Message
	if err := msg.Unpack(req); err != nil {
		return nil, false
	}
	if len(msg.Questions) == 0 {
		return nil, false
	}
	q := msg.Questions[0]

	resp := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:                 msg.Header.ID,
			Response:           true,
			Authoritative:      true,
			RecursionDesired:   msg.Header.RecursionDesired,
			RecursionAvailable: false,
		},
		Questions: msg.Questions,
	}

	matchesName := strings.EqualFold(canonicalName(q.Name.String()), d.name)
	wantsA := q.Type == dnsmessage.TypeA || q.Type == dnsmessage.TypeALL
	v4 := d.ip.To4()

	if matchesName && wantsA && v4 != nil {
		var a [4]byte
		copy(a[:], v4)
		resp.Answers = []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  q.Name,
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
				TTL:   60,
			},
			Body: &dnsmessage.AResource{A: a},
		}}
	} else {
		// Known authoritative zone but no matching record → NXDOMAIN.
		resp.Header.RCode = dnsmessage.RCodeNameError
	}

	out, err := resp.Pack()
	if err != nil {
		return nil, false
	}
	return out, true
}

// Close shuts the responder down. It is idempotent.
func (d *dnsResponder) Close() error {
	d.closeMu.Lock()
	defer d.closeMu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	return d.conn.Close()
}

// canonicalName normalises a DNS name to lower-case with a single trailing dot,
// so comparisons are robust to client casing and the optional FQDN dot.
func canonicalName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimSuffix(n, ".")
	return n + "."
}
