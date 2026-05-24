package lan

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCertSource_FallsBackWhenFilesMissing(t *testing.T) {
	src := LoadCertSource(
		filepath.Join(t.TempDir(), "nope.pem"),
		filepath.Join(t.TempDir(), "nope-key.pem"),
		[]string{"vulos.local"}, []net.IP{net.IPv4(127, 0, 0, 1)},
	)
	cert, err := src.Certificate(nil)
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	if cert == nil || cert.Leaf == nil {
		t.Fatal("expected fallback self-signed cert")
	}
}

func TestLoadCertSource_LoadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	// Reuse the self-signed generator to produce a real keypair on disk.
	gen := NewSelfSignedCertSource([]string{"box.test.lan.vulos.org"}, nil)
	c, err := gen.Certificate(nil)
	if err != nil {
		t.Fatalf("seed cert: %v", err)
	}
	writePEM(t, certPath, "CERTIFICATE", c.Certificate[0])
	keyDER, err := x509.MarshalPKCS8PrivateKey(c.PrivateKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	writePEM(t, keyPath, "PRIVATE KEY", keyDER)

	src := LoadCertSource(certPath, keyPath, []string{"vulos.local"}, nil)
	got, err := src.Certificate(nil)
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	if got.Leaf == nil {
		t.Fatal("expected parsed leaf from disk cert")
	}
	if err := got.Leaf.VerifyHostname("box.test.lan.vulos.org"); err != nil {
		t.Errorf("loaded cert missing expected SAN: %v", err)
	}
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	b := pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
