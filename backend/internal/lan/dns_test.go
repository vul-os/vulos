package lan

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// queryA sends an A query for name to a UDP DNS server at addr and returns the
// parsed response.
func queryA(t *testing.T, addr, name string) dnsmessage.Message {
	t.Helper()
	dnsName, err := dnsmessage.NewName(canonicalName(name))
	if err != nil {
		t.Fatalf("NewName(%q): %v", name, err)
	}
	q := dnsmessage.Message{
		Header: dnsmessage.Header{ID: 0x1234, RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  dnsName,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
	}
	packed, err := q.Pack()
	if err != nil {
		t.Fatalf("pack query: %v", err)
	}

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial dns %s: %v", addr, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := conn.Write(packed); err != nil {
		t.Fatalf("write query: %v", err)
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp dnsmessage.Message
	if err := resp.Unpack(buf[:n]); err != nil {
		t.Fatalf("unpack response: %v", err)
	}
	return resp
}

func TestDNSResponder_AnswersBoxHostnameOffline(t *testing.T) {
	const instanceID = "01H000000000000000000TEST"
	host := BoxHostname(instanceID)
	wantIP := net.IPv4(10, 0, 0, 7)

	// Ephemeral port on loopback — never binds the privileged :53.
	d, err := newDNSResponder("127.0.0.1:0", host, wantIP)
	if err != nil {
		t.Fatalf("newDNSResponder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Close()

	resp := queryA(t, d.Addr(), host)

	if resp.Header.RCode != dnsmessage.RCodeSuccess {
		t.Fatalf("rcode = %v, want success", resp.Header.RCode)
	}
	if !resp.Header.Authoritative {
		t.Error("expected authoritative answer")
	}
	if len(resp.Answers) != 1 {
		t.Fatalf("got %d answers, want 1", len(resp.Answers))
	}
	a, ok := resp.Answers[0].Body.(*dnsmessage.AResource)
	if !ok {
		t.Fatalf("answer body type = %T, want *AResource", resp.Answers[0].Body)
	}
	if got := net.IP(a.A[:]).String(); got != wantIP.String() {
		t.Errorf("answer A = %s, want %s", got, wantIP)
	}
}

func TestDNSResponder_CaseInsensitiveName(t *testing.T) {
	host := BoxHostname("01H000000000000000000TEST")
	d, err := newDNSResponder("127.0.0.1:0", host, net.IPv4(10, 0, 0, 7))
	if err != nil {
		t.Fatalf("newDNSResponder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Close()

	// Upper-cased query for the same name must still resolve.
	upper := "BOX.01H000000000000000000TEST.LAN.VULOS.ORG"
	resp := queryA(t, d.Addr(), upper)
	if len(resp.Answers) != 1 {
		t.Fatalf("case-insensitive query: got %d answers, want 1", len(resp.Answers))
	}
}

func TestDNSResponder_NXDOMAINForUnknown(t *testing.T) {
	host := BoxHostname("01H000000000000000000TEST")
	d, err := newDNSResponder("127.0.0.1:0", host, net.IPv4(10, 0, 0, 7))
	if err != nil {
		t.Fatalf("newDNSResponder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Close()

	resp := queryA(t, d.Addr(), "box.someoneelse.lan.vulos.org")
	if resp.Header.RCode != dnsmessage.RCodeNameError {
		t.Errorf("rcode = %v, want NXDOMAIN for unknown name", resp.Header.RCode)
	}
	if len(resp.Answers) != 0 {
		t.Errorf("expected no answers for unknown name, got %d", len(resp.Answers))
	}
}

func TestBoxHostname(t *testing.T) {
	cases := map[string]string{
		"01ABC": "box.01abc.lan.vulos.org",
		"":      "box.unknown.lan.vulos.org",
		" Up ":  "box.up.lan.vulos.org",
	}
	for in, want := range cases {
		if got := BoxHostname(in); got != want {
			t.Errorf("BoxHostname(%q) = %q, want %q", in, got, want)
		}
	}
}
