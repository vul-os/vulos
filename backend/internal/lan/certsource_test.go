package lan

import (
	"crypto/tls"
	"net"
	"testing"
)

func TestSelfSignedCertSource_ReturnsUsableCert(t *testing.T) {
	src := NewSelfSignedCertSource(
		[]string{"vulos.local", BoxHostname("01H000000000000000000TEST")},
		[]net.IP{net.IPv4(192, 168, 1, 50)},
	)

	cert, err := src.Certificate(nil)
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	if cert == nil || cert.Leaf == nil {
		t.Fatal("expected a parsed certificate with a leaf")
	}

	// Cached: a second call returns the identical cert object.
	cert2, err := src.Certificate(&tls.ClientHelloInfo{ServerName: "vulos.local"})
	if err != nil {
		t.Fatalf("Certificate (2nd): %v", err)
	}
	if cert2 != cert {
		t.Fatal("expected cached certificate to be reused")
	}

	// SANs must cover the names + IP we asked for.
	if err := cert.Leaf.VerifyHostname("vulos.local"); err != nil {
		t.Errorf("cert does not cover vulos.local: %v", err)
	}
	if err := cert.Leaf.VerifyHostname("192.168.1.50"); err != nil {
		t.Errorf("cert does not cover LAN IP: %v", err)
	}
}

func TestTLSConfig_ResolvesViaSource(t *testing.T) {
	src := NewSelfSignedCertSource([]string{"vulos.local"}, nil)
	cfg := TLSConfig(src)
	if cfg.GetCertificate == nil {
		t.Fatal("expected GetCertificate to be wired")
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want >= TLS1.2", cfg.MinVersion)
	}
	got, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "vulos.local"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got == nil {
		t.Fatal("GetCertificate returned nil cert")
	}
}

// stubCertSource lets us prove an external (e.g. cloud DNS-01) implementation
// can satisfy the interface and is consulted on every handshake.
type stubCertSource struct {
	cert  *tls.Certificate
	calls int
}

func (s *stubCertSource) Certificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.calls++
	return s.cert, nil
}

func TestCertSource_InterfaceIsPluggable(t *testing.T) {
	real := NewSelfSignedCertSource([]string{"vulos.local"}, nil)
	c, err := real.Certificate(nil)
	if err != nil {
		t.Fatalf("seed cert: %v", err)
	}
	var src CertSource = &stubCertSource{cert: c}
	cfg := TLSConfig(src)
	if _, err := cfg.GetCertificate(&tls.ClientHelloInfo{}); err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if src.(*stubCertSource).calls != 1 {
		t.Errorf("expected source consulted once, got %d", src.(*stubCertSource).calls)
	}
}
