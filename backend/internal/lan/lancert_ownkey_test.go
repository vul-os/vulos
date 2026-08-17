package lan

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// FIX-LANCERT-OWNKEY-01 — the issuer must not be able to move the box's SPKI.
//
// D96-D's pinning promise is that a certificate can be RENEWED without every
// paired native client having to pair again. That holds only while the box's
// keypair is stable. The original puller contract had the issuer generate the
// keypair and ship the private half, which breaks the promise silently: the
// browser gets its padlock, and every native client stops connecting with an
// error that points nowhere near the cause.
// ---------------------------------------------------------------------------

func writeTestKey(t *testing.T, path string) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return k
}

func keyPEMOf(t *testing.T, k *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

func newTestPuller(t *testing.T, ownKeyPath string, allowIssuerKey bool) *LANCertPuller {
	t.Helper()
	return &LANCertPuller{cfg: PullerConfig{
		BoxID:                  "test",
		OwnKeyPath:             ownKeyPath,
		AllowIssuerSuppliedKey: allowIssuerKey,
	}}
}

// The CSR flow: no key comes back, so the box keeps the key it already had.
func TestPullerUsesItsOwnKeyWhenTheIssuerSendsNone(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "lan-selfsigned.key")
	own := writeTestKey(t, keyPath)

	p := newTestPuller(t, keyPath, false)
	got, err := p.resolveKeyPEM(certResponse{CertPEM: "x"})
	if err != nil {
		t.Fatalf("CSR flow rejected: %v", err)
	}
	if got != keyPEMOf(t, own) {
		t.Fatal("the puller did not use the box's own persisted key, so the installed certificate would not match the pinned SPKI")
	}
}

// The dangerous case: the issuer generated a DIFFERENT keypair.
func TestPullerRefusesAnIssuerKeyThatWouldBreakEveryPairedClient(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "lan-selfsigned.key")
	writeTestKey(t, keyPath)

	// A key the issuer made up.
	foreign, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	p := newTestPuller(t, keyPath, false)
	_, err = p.resolveKeyPEM(certResponse{CertPEM: "x", KeyPEM: keyPEMOf(t, foreign)})
	if err == nil {
		t.Fatal("the puller installed an issuer-generated key. That changes the box's SPKI, so every native client that already paired stops connecting — while browsers show a padlock and nothing looks wrong")
	}
	if !strings.Contains(err.Error(), "REFUSING") {
		t.Fatalf("refused, but not for the SPKI reason: %v", err)
	}
}

// The escape hatch must work, and must be explicit.
func TestPullerAcceptsAForeignKeyOnlyWhenExplicitlyAllowed(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "lan-selfsigned.key")
	writeTestKey(t, keyPath)

	foreign, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	foreignPEM := keyPEMOf(t, foreign)

	p := newTestPuller(t, keyPath, true)
	got, err := p.resolveKeyPEM(certResponse{CertPEM: "x", KeyPEM: foreignPEM})
	if err != nil {
		t.Fatalf("AllowIssuerSuppliedKey did not take effect: %v", err)
	}
	if got != foreignPEM {
		t.Fatal("opted in, but the issuer's key was not used")
	}
}

// An issuer that echoes back the SAME keypair is harmless and must not be
// treated as an attack — otherwise a conforming issuer looks broken.
func TestPullerAcceptsAnIssuerKeyIdenticalToItsOwn(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "lan-selfsigned.key")
	own := writeTestKey(t, keyPath)

	p := newTestPuller(t, keyPath, false)
	got, err := p.resolveKeyPEM(certResponse{CertPEM: "x", KeyPEM: keyPEMOf(t, own)})
	if err != nil {
		t.Fatalf("an issuer echoing the box's own key was refused: %v", err)
	}
	if got != keyPEMOf(t, own) {
		t.Fatal("unexpected key returned")
	}
}

// samePublicKey must compare the KEY, not the encoding: the same key in PKCS#8
// and SEC1 form is the same key, and treating it as foreign would refuse a
// perfectly conforming issuer.
func TestSamePublicKeyComparesKeysNotEncodings(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	sec1DER, _ := x509.MarshalECPrivateKey(k)
	sec1 := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1DER})

	pkcs8DER, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8DER})

	same, err := samePublicKey(sec1, pkcs8)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatal("the same key in two encodings was judged different — a conforming issuer would be refused")
	}

	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherDER, _ := x509.MarshalECPrivateKey(other)
	otherPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: otherDER})
	same, err = samePublicKey(sec1, otherPEM)
	if err != nil {
		t.Fatal(err)
	}
	if same {
		t.Fatal("two DIFFERENT keys were judged the same — the SPKI guard would never fire")
	}
}
