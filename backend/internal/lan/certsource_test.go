package lan

import (
	"crypto/tls"
	"crypto/x509"
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

// TestSelfSignedCert_IsNotACA is the audit P0-1 regression guard: the
// self-signed LAN leaf must NOT be a CA and must NOT carry KeyUsageCertSign. A
// CA-capable cert added to a trust store could sign arbitrary certs.
func TestSelfSignedCert_IsNotACA(t *testing.T) {
	src := NewSelfSignedCertSource([]string{"vulos.local"}, []net.IP{net.IPv4(192, 168, 1, 50)})
	cert, err := src.Certificate(nil)
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	leaf := cert.Leaf
	if leaf == nil {
		t.Fatal("expected parsed leaf")
	}

	if leaf.IsCA {
		t.Error("self-signed LAN leaf must NOT be a CA (IsCA=true)")
	}
	if !leaf.BasicConstraintsValid {
		t.Error("BasicConstraintsValid should be true so CA:FALSE is explicit on the wire")
	}
	if leaf.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Error("self-signed LAN leaf must NOT carry KeyUsageCertSign")
	}
	// Must keep the server-leaf usages.
	if leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Error("expected KeyUsageDigitalSignature on the leaf")
	}
	if leaf.KeyUsage&x509.KeyUsageKeyEncipherment == 0 {
		t.Error("expected KeyUsageKeyEncipherment on the leaf")
	}
	foundServerAuth := false
	for _, eku := range leaf.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			foundServerAuth = true
		}
	}
	if !foundServerAuth {
		t.Error("expected ExtKeyUsageServerAuth on the leaf")
	}

	// A non-CA cert must fail CA-style verification when used as a root to sign
	// for an arbitrary name. We prove the negative by checking the basic
	// constraint directly: a verifier honouring BasicConstraints would reject it
	// as an intermediate/CA.
	if leaf.MaxPathLen != 0 || leaf.MaxPathLenZero {
		// IsCA=false leaves MaxPathLen unmeaningful; just assert IsCA again for clarity.
		t.Logf("MaxPathLen=%d MaxPathLenZero=%v (irrelevant for non-CA)", leaf.MaxPathLen, leaf.MaxPathLenZero)
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
