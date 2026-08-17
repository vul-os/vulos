package lan

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// FIX-LANCERT-SNI-01 — which certificate does a real handshake actually get?
//
// Every test in this file completes a REAL TLS handshake against a REAL
// listener built from LoadCertSource, and then inspects the certificate the
// client received. None of it reasons about the selection logic from the
// outside; the question "would an owner be locked out" is only answerable by
// connecting.
//
// The property under test is not "the right cert is chosen" in the abstract. It
// is: A DEVICE MUST ALWAYS BE ABLE TO CONNECT WITH ONE CLICK. Never a hard
// failure, never a lockout — not when the CA-issued leaf has expired, not when
// it does not cover the name asked for, and not on the very first connection
// where the owner is downloading the root over a link they have not yet
// trusted.
// ---------------------------------------------------------------------------

// caFixture is a throwaway CA plus the material to write leaves signed by it.
type caFixture struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newCAFixture(t *testing.T) *caFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Test LAN Root CA"},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &caFixture{cert: cert, key: key}
}

// writeLeaf signs a leaf over leafKey for the given names/validity and writes
// the cert+key PEMs to certPath/keyPath, mirroring what the LAN cert puller
// drops on disk.
func (ca *caFixture) writeLeaf(t *testing.T, certPath, keyPath string, leafKey *ecdsa.PrivateKey, dnsNames []string, ips []net.IP, notBefore, notAfter time.Time) {
	t.Helper()
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "ca-issued"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &leafKey.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// serveAndDial runs a real TLS listener backed by src, dials it with the given
// client config, and returns the certificate chain the server actually
// presented plus any verification error the client raised.
//
// The chain is captured with VerifyPeerCertificate, which runs even when
// verification ultimately fails — so a test can see WHAT was served and
// SEPARATELY see whether the client accepted it. Those are two different
// questions and conflating them is how "it connected" gets mistaken for "it
// showed a padlock".
func serveAndDial(t *testing.T, src CertSource, clientCfg *tls.Config) (served []*x509.Certificate, dialErr error) {
	t.Helper()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", TLSConfig(src))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Force the handshake, then close.
		if tc, ok := conn.(*tls.Conn); ok {
			_ = tc.Handshake()
		}
		conn.Close()
	}()

	cfg := clientCfg.Clone()
	cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		for _, raw := range rawCerts {
			if c, err := x509.ParseCertificate(raw); err == nil {
				served = append(served, c)
			}
		}
		return nil
	}

	conn, err := tls.Dial("tcp", ln.Addr().String(), cfg)
	if err != nil {
		<-done
		return served, err
	}
	conn.Close()
	<-done
	return served, nil
}

// setup builds a fileCertSource over a temp dir with a persisted self-signed
// key, and returns the source plus the paths the "puller" writes to.
func setup(t *testing.T, hosts []string, ips []net.IP) (CertSource, string, string, *ecdsa.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "lan.crt")
	keyPath := filepath.Join(dir, "lan.key")
	selfKeyPath := filepath.Join(dir, "lan-selfsigned.key")

	fallback := NewSelfSignedCertSourceWithKeyPath(hosts, ips, selfKeyPath)
	src := &fileCertSource{certFile: certPath, keyFile: keyPath, fallback: fallback}

	// Materialise the self-signed identity so its persisted key exists.
	if _, err := fallback.Certificate(nil); err != nil {
		t.Fatal(err)
	}
	boxKey, err := loadKeyFile(selfKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	return src, certPath, keyPath, boxKey
}

func isSelfSigned(c *x509.Certificate) bool {
	return c.Issuer.CommonName == "Vulos LAN (self-signed)"
}

func isCAIssued(c *x509.Certificate) bool {
	return c.Issuer.CommonName == "Test LAN Root CA"
}

// ---------------------------------------------------------------------------

// TestCAIssuedCertIsServedWhenValidAndMatching is the CONTROL. Without it, every
// "the fallback was served" assertion below could be satisfied by a source that
// simply never serves the on-disk certificate at all.
func TestCAIssuedCertIsServedWhenValidAndMatching(t *testing.T) {
	ca := newCAFixture(t)
	src, certPath, keyPath, boxKey := setup(t, []string{"vulos.local", "vulos"}, nil)

	ca.writeLeaf(t, certPath, keyPath, boxKey,
		[]string{"vulos.local"}, nil,
		time.Now().Add(-time.Hour), time.Now().Add(365*24*time.Hour))

	served, err := serveAndDial(t, src, &tls.Config{
		ServerName:         "vulos.local",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("handshake failed: %v", err)
	}
	if len(served) == 0 {
		t.Fatal("no certificate served")
	}
	if !isCAIssued(served[0]) {
		t.Fatalf("served issuer %q, want the CA-issued cert — the on-disk certificate must win when it is valid and covers the name",
			served[0].Issuer.CommonName)
	}
}

// TestExpiredCAIssuedCertFallsBackInsteadOfLockingTheOwnerOut is requirement 4:
// renewal needs an operator, so expiry must degrade to the self-signed warning
// rather than to a dead box.
func TestExpiredCAIssuedCertFallsBackInsteadOfLockingTheOwnerOut(t *testing.T) {
	ca := newCAFixture(t)
	src, certPath, keyPath, boxKey := setup(t, []string{"vulos.local", "vulos"}, nil)

	// A leaf that expired yesterday — a box that sat offline past renewal.
	ca.writeLeaf(t, certPath, keyPath, boxKey,
		[]string{"vulos.local"}, nil,
		time.Now().Add(-400*24*time.Hour), time.Now().Add(-24*time.Hour))

	served, err := serveAndDial(t, src, &tls.Config{
		ServerName:         "vulos.local",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("handshake failed outright — the box is unreachable, which is the exact lockout this must prevent: %v", err)
	}
	if len(served) == 0 {
		t.Fatal("no certificate served")
	}
	if isCAIssued(served[0]) {
		t.Fatal("the EXPIRED CA-issued certificate was served. A browser treats an expired certificate as a hard failure, so the owner is locked out of their own box until someone runs the issuing tool — which needs the box to be reachable")
	}
	if !isSelfSigned(served[0]) {
		t.Fatalf("served issuer %q, want the self-signed fallback", served[0].Issuer.CommonName)
	}
	if time.Now().After(served[0].NotAfter) {
		t.Fatal("the fallback certificate is ALSO expired")
	}

	// And the fallback must still cover the name that was asked for, else the
	// owner trades a date error for a name error.
	if err := served[0].VerifyHostname("vulos.local"); err != nil {
		t.Fatalf("fallback does not cover the requested name: %v", err)
	}
}

// TestNotYetValidCertAlsoFallsBack covers the same failure from the other side:
// a box with no RTC can boot with a clock far in the past.
func TestNotYetValidCertAlsoFallsBack(t *testing.T) {
	ca := newCAFixture(t)
	src, certPath, keyPath, boxKey := setup(t, []string{"vulos.local"}, nil)

	ca.writeLeaf(t, certPath, keyPath, boxKey,
		[]string{"vulos.local"}, nil,
		time.Now().Add(48*time.Hour), time.Now().Add(365*24*time.Hour))

	served, err := serveAndDial(t, src, &tls.Config{
		ServerName:         "vulos.local",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("handshake failed: %v", err)
	}
	if !isSelfSigned(served[0]) {
		t.Fatalf("served issuer %q, want the self-signed fallback for a not-yet-valid leaf", served[0].Issuer.CommonName)
	}
}

// TestNameTheCACertCannotCoverFallsBackRatherThanMismatching is the bare-label
// case: internal/lanca can never put "vulos" on a leaf, so asking for it must
// get the self-signed cert that does carry it.
func TestNameTheCACertCannotCoverFallsBackRatherThanMismatching(t *testing.T) {
	ca := newCAFixture(t)
	src, certPath, keyPath, boxKey := setup(t, []string{"vulos.local", "vulos"}, nil)

	// The CA leaf covers only the dotted name, exactly as FilterIssuable would
	// have produced it.
	ca.writeLeaf(t, certPath, keyPath, boxKey,
		[]string{"vulos.local"}, nil,
		time.Now().Add(-time.Hour), time.Now().Add(365*24*time.Hour))

	served, err := serveAndDial(t, src, &tls.Config{
		ServerName:         "vulos", // the bare label
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("handshake failed: %v", err)
	}
	if isCAIssued(served[0]) {
		t.Fatal("served the CA-issued cert for a name it does not carry — that turns a one-click unknown-issuer warning into a name-mismatch error")
	}
	if err := served[0].VerifyHostname("vulos"); err != nil {
		t.Fatalf("fallback does not cover the bare label either: %v", err)
	}
}

// TestFirstConnectionFromADeviceWithoutTheRootIsAWarningNotAFailure is the
// chicken-and-egg case from the brief: the owner is about to download the root
// over a connection they have not yet trusted.
//
// The distinction being measured is between the two ways a TLS client can
// refuse. An UnknownAuthorityError is a PROCEEDABLE warning — the browser shows
// "your connection is not private" with an Advanced/Proceed affordance. A
// handshake-level failure, or a date error, is not proceedable on every
// platform. The first is acceptable; the second is a lockout.
func TestFirstConnectionFromADeviceWithoutTheRootIsAWarningNotAFailure(t *testing.T) {
	ca := newCAFixture(t)

	for _, tc := range []struct {
		name       string
		notBefore  time.Time
		notAfter   time.Time
		serverName string
	}{
		{"valid CA leaf", time.Now().Add(-time.Hour), time.Now().Add(365 * 24 * time.Hour), "vulos.local"},
		{"expired CA leaf", time.Now().Add(-400 * 24 * time.Hour), time.Now().Add(-24 * time.Hour), "vulos.local"},
		{"uncovered name", time.Now().Add(-time.Hour), time.Now().Add(365 * 24 * time.Hour), "vulos"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, certPath, keyPath, boxKey := setup(t, []string{"vulos.local", "vulos"}, nil)
			ca.writeLeaf(t, certPath, keyPath, boxKey,
				[]string{"vulos.local"}, nil, tc.notBefore, tc.notAfter)

			// Capture what the server presents. InsecureSkipVerify is used ONLY
			// to observe the chain: crypto/tls does not invoke
			// VerifyPeerCertificate when its own verification fails, so a
			// verifying dial would tell us the handshake failed without ever
			// telling us what was served. The verification a real device would
			// perform is then done explicitly below, against an EMPTY trust
			// store — a device that has neither the owner's root nor any public
			// root that could vouch for this box.
			served, dialErr := serveAndDial(t, src, &tls.Config{
				ServerName:         tc.serverName,
				InsecureSkipVerify: true,
			})
			if dialErr != nil {
				t.Fatalf("the TLS handshake itself failed (%v) — the owner cannot even reach the certificate to accept it", dialErr)
			}
			if len(served) == 0 {
				t.Fatal("no certificate was presented")
			}

			pool := x509.NewCertPool()
			_, err := served[0].Verify(x509.VerifyOptions{
				Roots:     pool,
				DNSName:   tc.serverName,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			if err == nil {
				t.Fatal("test bug: a device with an empty trust store should not have verified this chain")
			}

			// The failure must be "I don't know this issuer", never "this
			// certificate is expired" or a name mismatch.
			var unknownAuth x509.UnknownAuthorityError
			var hostErr x509.HostnameError
			var invalidErr x509.CertificateInvalidError
			switch {
			case errors.As(err, &unknownAuth):
				// Proceedable. This is the intended outcome.
			case errors.As(err, &hostErr):
				t.Fatalf("device saw a NAME MISMATCH (%v) rather than a plain unknown-issuer warning", err)
			case errors.As(err, &invalidErr):
				t.Fatalf("device saw a certificate-invalid error (reason %v): %v — an expired certificate is not click-throughable on every platform", invalidErr.Reason, err)
			default:
				t.Fatalf("device saw an unexpected failure shape %T: %v", err, err)
			}
		})
	}
}

// TestUsableForHelloIsDecidedByTheClockNotByLuck exercises the boundary
// directly, with an injected clock, so the expiry rule is pinned at the
// instant rather than approximately.
func TestUsableForHelloIsDecidedByTheClockNotByLuck(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	notBefore := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "vulos.local"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"vulos.local"},
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	leaf, _ := x509.ParseCertificate(der)
	cert := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}

	hello := &tls.ClientHelloInfo{ServerName: "vulos.local"}

	for _, tc := range []struct {
		when time.Time
		want bool
		why  string
	}{
		{notBefore.Add(-time.Second), false, "one second before NotBefore"},
		{notBefore, true, "exactly at NotBefore"},
		{notBefore.Add(time.Hour), true, "comfortably inside"},
		{notAfter, true, "exactly at NotAfter"},
		{notAfter.Add(time.Second), false, "one second after NotAfter"},
	} {
		if got := usableForHello(cert, hello, tc.when); got != tc.want {
			t.Errorf("usableForHello at %s (%s) = %v, want %v", tc.when, tc.why, got, tc.want)
		}
	}

	// Name mismatch is refused at any time.
	if usableForHello(cert, &tls.ClientHelloInfo{ServerName: "other.local"}, notBefore.Add(time.Hour)) {
		t.Error("a certificate was judged usable for a name it does not carry")
	}
	// No SNI (a bare-IP URL) is judged on validity alone.
	if !usableForHello(cert, &tls.ClientHelloInfo{ServerName: ""}, notBefore.Add(time.Hour)) {
		t.Error("a valid certificate was refused for a client that sent no SNI; browsing by IP would never get the CA-issued cert")
	}
}
