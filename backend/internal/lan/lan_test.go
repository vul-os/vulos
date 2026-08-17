package lan

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestService_ServesOSOverHTTPSWithDevCertSource(t *testing.T) {
	const instanceID = "01H000000000000000000TEST"
	lanIP := net.IPv4(127, 0, 0, 1)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "vulos-os")
	})

	host := BoxHostname(instanceID)
	src := NewSelfSignedCertSource([]string{"vulos.local", host}, []net.IP{lanIP})

	svc, err := New(Config{
		InstanceID:  instanceID,
		CertSource:  src,
		Handler:     handler,
		LANIP:       lanIP,
		HTTPSAddr:   "127.0.0.1:0", // ephemeral, unprivileged
		DNSAddr:     "127.0.0.1:0",
		DisableMDNS: true, // multicast not needed for this test
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer svc.Stop(context.Background())

	// Build a client that trusts the dev cert (proves the cert chains correctly,
	// which is what "no warning" means once LANCERT-01 swaps in a public cert).
	devCert, err := src.Certificate(nil)
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(devCert.Leaf)

	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "vulos.local"},
		},
	}

	url := "https://" + svc.HTTPSAddr() + "/"
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "vulos-os" {
		t.Errorf("body = %q, want %q", body, "vulos-os")
	}
}

// TestService_DNSAndHTTPSIntegrated proves the DNS responder, started as part of
// the Service, answers the box hostname while HTTPS is also live — the offline
// reachability scenario end to end (minus mDNS multicast).
func TestService_DNSAndHTTPSIntegrated(t *testing.T) {
	const instanceID = "01H000000000000000000TEST"
	lanIP := net.IPv4(10, 1, 2, 3)

	svc, err := New(Config{
		InstanceID:  instanceID,
		CertSource:  NewSelfSignedCertSource([]string{"vulos.local"}, []net.IP{lanIP}),
		Handler:     http.NewServeMux(),
		LANIP:       lanIP,
		HTTPSAddr:   "127.0.0.1:0",
		DNSAddr:     "127.0.0.1:0",
		DisableMDNS: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer svc.Stop(context.Background())

	if svc.Hostname() != BoxHostname(instanceID) {
		t.Errorf("Hostname() = %q", svc.Hostname())
	}

	resp := queryA(t, svc.DNSAddr(), svc.Hostname())
	if len(resp.Answers) != 1 {
		t.Fatalf("dns: got %d answers, want 1", len(resp.Answers))
	}
	a := resp.Answers[0].Body.(*dnsmessage.AResource)
	if got := net.IP(a.A[:]).String(); got != lanIP.String() {
		t.Errorf("dns A = %s, want %s", got, lanIP)
	}
}

func TestService_RequiresCertSourceAndHandler(t *testing.T) {
	if _, err := New(Config{Handler: http.NewServeMux()}); err == nil {
		t.Error("expected error when CertSource is nil")
	}
	if _, err := New(Config{CertSource: NewSelfSignedCertSource(nil, nil)}); err == nil {
		t.Error("expected error when Handler is nil")
	}
}

func TestMDNSAdvertiser_AdvertisesVulosLocal(t *testing.T) {
	m, err := newMDNSAdvertiser(net.IPv4(192, 168, 1, 42), []string{mdnsHostname})
	if err != nil {
		// Multicast binding is unavailable in some sandboxes/CI; that path is
		// best-effort by design, so skip rather than fail.
		t.Skipf("mDNS multicast unavailable in this environment: %v", err)
	}
	defer m.Close()
	if m.conn == nil {
		t.Fatal("expected an mDNS connection")
	}
}

func TestMDNSAdvertiser_RejectsNonLocalName(t *testing.T) {
	if _, err := newMDNSAdvertiser(net.IPv4(192, 168, 1, 42), []string{"box.x.lan.vulos.org"}); err == nil {
		t.Error("expected error for non-.local mDNS name")
	}
}

// TestLanBindAddr_PinsWildcardToLANIP is the audit P1-5 guard: a wildcard host
// (":443", "0.0.0.0:53", "[::]:53") must be rewritten to bind to the detected
// LAN IP only — never all interfaces. An explicit host (loopback in tests) is
// preserved so the test seam keeps working.
func TestLanBindAddr_PinsWildcardToLANIP(t *testing.T) {
	lanIP := net.IPv4(192, 168, 7, 20)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty host :443", ":443", "192.168.7.20:443"},
		{"empty host :53", ":53", "192.168.7.20:53"},
		{"0.0.0.0", "0.0.0.0:53", "192.168.7.20:53"},
		{"ipv6 wildcard", "[::]:53", "192.168.7.20:53"},
		{"explicit loopback preserved", "127.0.0.1:0", "127.0.0.1:0"},
		{"explicit other host preserved", "10.0.0.5:8443", "10.0.0.5:8443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lanBindAddr(tc.in, lanIP); got != tc.want {
				t.Errorf("lanBindAddr(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// A nil LAN IP must not crash and must leave the addr untouched.
	if got := lanBindAddr(":53", nil); got != ":53" {
		t.Errorf("nil lanIP: got %q, want untouched :53", got)
	}
	// An unparseable address is returned untouched (no silent rewrite).
	if got := lanBindAddr("garbage-no-port", lanIP); got != "garbage-no-port" {
		t.Errorf("unparseable addr: got %q, want untouched", got)
	}
}

// TestService_DisableDNSSkipsResponderButKeepsHTTPS proves DisableDNS is a
// genuine opt-out: no UDP socket is opened at all (not just "unreachable"),
// while HTTPS keeps serving over the self-signed fallback cert exactly as
// when DNS is enabled. This is the pair to TestService_DNSAndHTTPSIntegrated
// above — same setup, DNS flipped off — so the same assertions could have
// caught DNS being left on by accident.
func TestService_DisableDNSSkipsResponderButKeepsHTTPS(t *testing.T) {
	const instanceID = "01H000000000000000000TEST"
	lanIP := net.IPv4(127, 0, 0, 1)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "vulos-os")
	})
	host := BoxHostname(instanceID)
	src := NewSelfSignedCertSource([]string{"vulos.local", host}, []net.IP{lanIP})

	svc, err := New(Config{
		InstanceID:  instanceID,
		CertSource:  src,
		Handler:     handler,
		LANIP:       lanIP,
		HTTPSAddr:   "127.0.0.1:0",
		DNSAddr:     "127.0.0.1:0",
		DisableMDNS: true,
		DisableDNS:  true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer svc.Stop(context.Background())

	// No DNS responder was constructed at all.
	svc.mu.Lock()
	dnsIsNil := svc.dns == nil
	svc.mu.Unlock()
	if !dnsIsNil {
		t.Error("expected svc.dns to be nil when DisableDNS is set")
	}

	// HTTPS still works, including the self-signed fallback cert.
	devCert, err := src.Certificate(nil)
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(devCert.Leaf)
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "vulos.local"},
		},
	}
	url := "https://" + svc.HTTPSAddr() + "/"
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "vulos-os" {
		t.Errorf("body = %q, want %q", body, "vulos-os")
	}
}

// TestService_BindsListenersToLANIPNotWildcard proves the wired path: when the
// configured HTTPS/DNS addresses use a wildcard host, the actually-bound
// listeners use the detected LAN IP, not 0.0.0.0. We use a loopback LAN IP so
// the bind succeeds unprivileged in CI while still exercising the rewrite.
func TestService_BindsListenersToLANIPNotWildcard(t *testing.T) {
	lanIP := net.IPv4(127, 0, 0, 1)
	svc, err := New(Config{
		InstanceID:  "01H000000000000000000TEST",
		CertSource:  NewSelfSignedCertSource([]string{"vulos.local"}, []net.IP{lanIP}),
		Handler:     http.NewServeMux(),
		LANIP:       lanIP,
		HTTPSAddr:   ":0", // wildcard host, ephemeral port
		DNSAddr:     "0.0.0.0:0",
		DisableMDNS: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer svc.Stop(context.Background())

	httpsHost, _, err := net.SplitHostPort(svc.HTTPSAddr())
	if err != nil {
		t.Fatalf("split https addr %q: %v", svc.HTTPSAddr(), err)
	}
	if httpsHost == "0.0.0.0" || httpsHost == "::" || httpsHost == "" {
		t.Errorf("HTTPS listener bound to wildcard %q; want LAN IP %s", httpsHost, lanIP)
	}
	if httpsHost != lanIP.String() {
		t.Errorf("HTTPS listener host = %q, want %s", httpsHost, lanIP)
	}

	dnsHost, _, err := net.SplitHostPort(svc.DNSAddr())
	if err != nil {
		t.Fatalf("split dns addr %q: %v", svc.DNSAddr(), err)
	}
	if dnsHost == "0.0.0.0" || dnsHost == "::" || dnsHost == "" {
		t.Errorf("DNS responder bound to wildcard %q; want LAN IP %s", dnsHost, lanIP)
	}
	if dnsHost != lanIP.String() {
		t.Errorf("DNS responder host = %q, want %s", dnsHost, lanIP)
	}
}
