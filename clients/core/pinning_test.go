package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mintedCert bundles a freshly generated self-signed certificate with the
// pieces tests need: the tls.Certificate to hand an httptest server, the
// parsed leaf, and its Fingerprint per this package's own String() format.
type mintedCert struct {
	tlsCert tls.Certificate
	leaf    *x509.Certificate
	fp      Fingerprint
}

// mintCert generates a brand-new ECDSA keypair and a self-signed certificate
// valid for 127.0.0.1/localhost, so an httptest TLS server presenting it is
// reachable over loopback. Each call produces distinct key material, which is
// what lets the "wrong pin" tests isolate the pin comparison as the sole
// rejection cause.
func mintCert(t *testing.T, commonName string) mintedCert {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(720 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse minted cert: %v", err)
	}
	tlsCert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
		Leaf:        leaf,
	}

	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return mintedCert{
		tlsCert: tlsCert,
		leaf:    leaf,
		fp:      Fingerprint{SPKISHA256: sum},
	}
}

// newPinnedTLSServer starts an httptest TLS server presenting mc, on
// loopback. Using NewUnstartedServer + explicit srv.TLS (rather than
// NewTLSServer's baked-in cert) is what lets each test control exactly which
// certificate is presented.
func newPinnedTLSServer(t *testing.T, mc mintedCert) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{mc.tlsCert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func serverAddr(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	return u.Host
}

// --- Fingerprint / String format -------------------------------------------------

func TestFingerprintString_MatchesSPKIPinConvention(t *testing.T) {
	mc := mintCert(t, "vulos-test-box")

	// The independently-computed reference: standard base64 of the raw
	// SHA-256 of the DER SubjectPublicKeyInfo — the exact convention
	// backend/internal/lan/lancert_puller.go's decodeSPKIPins documents and
	// consumes ("openssl ... | openssl dgst -sha256 -binary | base64").
	sum := sha256.Sum256(mc.leaf.RawSubjectPublicKeyInfo)
	wantPin := base64.StdEncoding.EncodeToString(sum[:])

	got := FingerprintFromCert(mc.leaf).String()
	if got != wantPin {
		t.Fatalf("Fingerprint.String() = %q, want %q (base64 SHA-256 of RawSubjectPublicKeyInfo)", got, wantPin)
	}

	// Round trip through ParseFingerprint.
	fp, err := ParseFingerprint(got)
	if err != nil {
		t.Fatalf("ParseFingerprint(%q): %v", got, err)
	}
	if fp != mc.fp {
		t.Fatalf("ParseFingerprint round-trip mismatch: got %v want %v", fp, mc.fp)
	}
}

func TestParseFingerprint_RejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"not-base64!!!",
		base64.StdEncoding.EncodeToString([]byte("too short")),
		base64.StdEncoding.EncodeToString(make([]byte, 64)), // too long
	}
	for _, c := range cases {
		if _, err := ParseFingerprint(c); err == nil {
			t.Errorf("ParseFingerprint(%q): want error, got nil", c)
		}
	}
}

// --- TLSConfig / Client: the pinning contract -------------------------------------

func TestTLSConfig_UnpairedBoxIsRejected(t *testing.T) {
	b := Box{Name: "unpaired-box", Addr: "127.0.0.1:0"} // zero-value Pin

	cfg, err := TLSConfig(b)
	if err == nil {
		t.Fatal("TLSConfig on an unpaired box: want error, got nil")
	}
	if !errors.Is(err, ErrUnpaired) {
		t.Errorf("TLSConfig error = %v, want wrapping ErrUnpaired", err)
	}
	if cfg != nil {
		t.Fatal("TLSConfig on an unpaired box: want nil config, got non-nil — " +
			"a caller must never be able to obtain a config that skips verification")
	}

	if _, err := Client(b); err == nil {
		t.Fatal("Client on an unpaired box: want error, got nil")
	}
}

func TestTLSConfig_CorrectPinConnects(t *testing.T) {
	mc := mintCert(t, "vulos-correct-pin")
	srv := newPinnedTLSServer(t, mc)

	b := Box{Name: "test-box", Addr: serverAddr(t, srv), Pin: mc.fp}
	client, err := Client(b)
	if err != nil {
		t.Fatalf("Client: %v", err)
	}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET with correct pin: want success, got error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET with correct pin: status = %d, want 200", resp.StatusCode)
	}
}

func TestTLSConfig_WrongPinIsRejected(t *testing.T) {
	mc := mintCert(t, "vulos-real")
	other := mintCert(t, "vulos-impostor") // distinct key/SPKI
	srv := newPinnedTLSServer(t, mc)

	// Pin the box to the OTHER cert's fingerprint — simulates the server
	// presenting a certificate that does not match what the client trusts.
	b := Box{Name: "test-box", Addr: serverAddr(t, srv), Pin: other.fp}
	client, err := Client(b)
	if err != nil {
		t.Fatalf("Client: %v", err)
	}

	resp, err := client.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("GET with wrong pin: want error (rejected handshake), got success — " +
			"this is the whole point of pinning")
	}
}

// --- Pairing payload codec ---------------------------------------------------------

func TestPairPayload_RoundTrips(t *testing.T) {
	mc := mintCert(t, "vulos-pair-roundtrip")
	payload := EncodePairPayload("my box", "192.168.1.50:443", mc.fp)

	name, addr, fp, err := ParsePairPayload(payload)
	if err != nil {
		t.Fatalf("ParsePairPayload(%q): %v", payload, err)
	}
	if name != "my box" {
		t.Errorf("name = %q, want %q", name, "my box")
	}
	if addr != "192.168.1.50:443" {
		t.Errorf("addr = %q, want %q", addr, "192.168.1.50:443")
	}
	if fp != mc.fp {
		t.Errorf("fp = %v, want %v", fp, mc.fp)
	}
}

func TestParsePairPayload_RejectsUnknownScheme(t *testing.T) {
	if _, _, _, err := ParsePairPayload("https://pair?name=x&addr=1.2.3.4:443&spki=" + url.QueryEscape("AAAA")); err == nil {
		t.Fatal("want error for non-vulos scheme")
	} else if !errors.Is(err, ErrUnknownPairScheme) {
		t.Errorf("err = %v, want wrapping ErrUnknownPairScheme", err)
	}
}

func TestParsePairPayload_RejectsMissingFields(t *testing.T) {
	cases := []string{
		"vulos://pair?addr=1.2.3.4:443&spki=AAAA",            // missing name
		"vulos://pair?name=x&spki=AAAA",                      // missing addr
		"vulos://pair?name=x&addr=1.2.3.4:443",               // missing spki
		"vulos://pair?name=x&addr=not-a-host-port&spki=AAAA", // malformed addr
	}
	for _, c := range cases {
		if _, _, _, err := ParsePairPayload(c); err == nil {
			t.Errorf("ParsePairPayload(%q): want error, got nil", c)
		} else if !errors.Is(err, ErrMalformedPairPayload) {
			t.Errorf("ParsePairPayload(%q): err = %v, want wrapping ErrMalformedPairPayload", c, err)
		}
	}
}

func TestParsePairPayload_RejectsMalformedPin(t *testing.T) {
	payload := "vulos://pair?name=x&addr=1.2.3.4:443&spki=" + url.QueryEscape("not-valid-base64!!")
	if _, _, _, err := ParsePairPayload(payload); err == nil {
		t.Fatal("want error for malformed spki")
	} else if !errors.Is(err, ErrMalformedPairPin) {
		t.Errorf("err = %v, want wrapping ErrMalformedPairPin", err)
	}
}

// --- Pair: end-to-end trust-on-first-use --------------------------------------------

func TestPair_SucceedsAndStores(t *testing.T) {
	mc := mintCert(t, "vulos-pair-ok")
	srv := newPinnedTLSServer(t, mc)
	addr := serverAddr(t, srv)

	payload := EncodePairPayload("living-room-box", addr, mc.fp)
	store := NewFileStore(filepath.Join(t.TempDir(), "pins.txt"))

	box, err := Pair(context.Background(), payload, store)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if box.Name != "living-room-box" || box.Addr != addr || box.Pin != mc.fp {
		t.Fatalf("Pair returned unexpected Box: %+v", box)
	}

	stored, err := store.Load(context.Background(), "living-room-box")
	if err != nil {
		t.Fatalf("store.Load after Pair: %v", err)
	}
	if stored != mc.fp {
		t.Fatalf("stored pin = %v, want %v", stored, mc.fp)
	}
}

// TestPair_TamperedSPKIFailsAndDoesNotStore is the key negative case: a
// payload whose spki field does not match what the server at addr actually
// presents (e.g. tampered in transit, or an attacker's QR code pointing at a
// real box's address with a substituted pin) must fail to pair, and must
// leave the store untouched.
func TestPair_TamperedSPKIFailsAndDoesNotStore(t *testing.T) {
	mc := mintCert(t, "vulos-pair-real")
	tampered := mintCert(t, "vulos-pair-tampered") // distinct SPKI
	srv := newPinnedTLSServer(t, mc)
	addr := serverAddr(t, srv)

	// Payload claims the TAMPERED fingerprint, but the server at addr
	// presents mc's certificate — a mismatch.
	payload := EncodePairPayload("box", addr, tampered.fp)
	store := NewFileStore(filepath.Join(t.TempDir(), "pins.txt"))

	if _, err := Pair(context.Background(), payload, store); err == nil {
		t.Fatal("Pair with tampered spki: want error, got success")
	}

	if _, err := store.Load(context.Background(), "box"); !errors.Is(err, ErrPinNotFound) {
		t.Fatalf("store.Load after failed Pair: want ErrPinNotFound, got %v — "+
			"a failed pairing attempt must never write a pin", err)
	}
}

func TestPair_UnreachableAddrFails(t *testing.T) {
	mc := mintCert(t, "vulos-unreachable")
	// A closed loopback port: guaranteed to refuse the connection.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	payload := EncodePairPayload("box", addr, mc.fp)
	store := NewFileStore(filepath.Join(t.TempDir(), "pins.txt"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Pair(ctx, payload, store); err == nil {
		t.Fatal("Pair against an unreachable address: want error, got nil")
	}
}

// --- FileStore -----------------------------------------------------------------------

func TestFileStore_SaveLoadForget(t *testing.T) {
	mc := mintCert(t, "vulos-filestore")
	store := NewFileStore(filepath.Join(t.TempDir(), "sub", "pins.txt"))
	ctx := context.Background()

	if _, err := store.Load(ctx, "nope"); !errors.Is(err, ErrPinNotFound) {
		t.Fatalf("Load before Save: want ErrPinNotFound, got %v", err)
	}

	if err := store.Save(ctx, "box-a", mc.fp); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load(ctx, "box-a")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != mc.fp {
		t.Fatalf("Load = %v, want %v", got, mc.fp)
	}

	// File must be 0600.
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("pin file mode = %v, want 0600", info.Mode().Perm())
	}

	if err := store.Forget(ctx, "box-a"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := store.Load(ctx, "box-a"); !errors.Is(err, ErrPinNotFound) {
		t.Fatalf("Load after Forget: want ErrPinNotFound, got %v", err)
	}
}
