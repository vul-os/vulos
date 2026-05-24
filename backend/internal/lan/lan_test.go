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
	m, err := newMDNSAdvertiser(net.IPv4(192, 168, 1, 42), mdnsHostname)
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
	if _, err := newMDNSAdvertiser(net.IPv4(192, 168, 1, 42), "box.x.lan.vulos.org"); err == nil {
		t.Error("expected error for non-.local mDNS name")
	}
}
