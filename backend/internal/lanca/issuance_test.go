package lanca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// SPKI pinning must survive re-issuance.
//
// D96-D's whole trust model for native clients is "pin the SPKI, not the
// certificate, so the box can renew without forcing every paired device to
// re-pair". Adding a CA to the picture is only acceptable if that stays true.
// It stays true for exactly one reason: the box signs a CSR with the key it
// ALREADY persists, and the CA signs over that same public key. If issuance
// ever generated a fresh key, every paired client would break on renewal.
// ---------------------------------------------------------------------------

// independentSPKIPin recomputes the pin from raw bytes WITHOUT calling
// SPKISHA256, so this test cannot pass merely because SPKISHA256 is
// self-consistent. Transcribed from the convention itself: base64 of the
// SHA-256 over the DER SubjectPublicKeyInfo.
func independentSPKIPin(t *testing.T, cert *x509.Certificate) string {
	t.Helper()
	spki, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(spki)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func TestReissuanceDoesNotChangeTheBoxSPKIPin(t *testing.T) {
	root, err := NewRoot("pin-test")
	if err != nil {
		t.Fatal(err)
	}

	// The box's ONE persistent key — the analogue of what certsource.go's
	// loadOrCreateKey keeps on disk across reboots.
	boxKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// What a native client pinned at first pairing: the SPKI of the box's own
	// self-signed certificate. Built here the same way certsource.go builds it.
	selfSigned := selfSignedLike(t, boxKey, []string{"vulos.local"})
	pinnedAtPairing := independentSPKIPin(t, selfSigned)

	csrPEM, err := NewCSR(boxKey, "vulos.local")
	if err != nil {
		t.Fatal(err)
	}

	// Issue #1 — the box's names as first advertised.
	ns1, err := NewNameSet([]string{"vulos.local"}, []net.IP{net.ParseIP("192.168.1.50")})
	if err != nil {
		t.Fatal(err)
	}
	_, leaf1, err := root.IssueFromCSR(csrPEM, ns1, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Issue #2 — a RENEWAL, months later, after avahi renamed the box and DHCP
	// moved it. Different names, different validity window, different serial.
	// The pin must not move.
	ns2, err := NewNameSet([]string{"vulos-2.local", "box.abc.lan.vulos.org"}, []net.IP{net.ParseIP("10.0.0.9")})
	if err != nil {
		t.Fatal(err)
	}
	_, leaf2, err := root.IssueFromCSR(csrPEM, ns2, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if leaf1.SerialNumber.Cmp(leaf2.SerialNumber) == 0 {
		t.Fatal("test bug: the two issuances produced the same serial, so this is not really a re-issuance")
	}

	got1 := independentSPKIPin(t, leaf1)
	got2 := independentSPKIPin(t, leaf2)

	if got1 != pinnedAtPairing {
		t.Fatalf("FIRST CA-issued cert changed the SPKI pin.\n pinned at pairing: %s\n after issuance:    %s\nEvery already-paired native client would fail to connect.", pinnedAtPairing, got1)
	}
	if got2 != pinnedAtPairing {
		t.Fatalf("RE-ISSUED cert changed the SPKI pin.\n pinned at pairing: %s\n after renewal:     %s\nEvery already-paired native client would have to re-pair on every renewal.", pinnedAtPairing, got2)
	}
	// And the package's own helper must agree with the independent computation,
	// else the CLI would print a pin that does not match what clients store.
	if SPKISHA256(leaf2) != pinnedAtPairing {
		t.Fatalf("SPKISHA256 = %s but the independently computed pin is %s", SPKISHA256(leaf2), pinnedAtPairing)
	}
	t.Logf("pin stable across pairing + 2 issuances: %s", pinnedAtPairing)
}

// selfSignedLike mints a self-signed leaf over key the way
// lan.SelfSignedCertSource does, so the "pinned at pairing" value in the test
// above is the value a real client would really have stored.
func selfSignedLike(t *testing.T, key *ecdsa.PrivateKey, hosts []string) *x509.Certificate {
	t.Helper()
	serial, _ := newSerial()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Vulos LAN (self-signed)"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              hosts,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ---------------------------------------------------------------------------
// CSR handling.
// ---------------------------------------------------------------------------

func TestIssueFromCSRIgnoresNamesTheRequesterAskedFor(t *testing.T) {
	root, err := NewRoot("csr-test")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	// A COMPROMISED box asks for names it should not get. Both a permitted name
	// it was not assigned and an outright forbidden one.
	tmpl := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "google.com"},
		DNSNames: []string{"google.com", "someone-elses-box.local"},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		t.Fatal(err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})

	ns, err := NewNameSet([]string{"vulos.local"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, leaf, err := root.IssueFromCSR(csrPEM, ns, 0)
	if err != nil {
		t.Fatalf("issuance should succeed using the OPERATOR's names: %v", err)
	}

	for _, n := range leaf.DNSNames {
		if n == "google.com" || n == "someone-elses-box.local" {
			t.Fatalf("leaf carries %q, a name the REQUESTER put in the CSR — requester-supplied SANs must be ignored", n)
		}
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "vulos.local" {
		t.Fatalf("leaf DNS names = %v, want exactly [vulos.local] from the operator's NameSet", leaf.DNSNames)
	}
	if leaf.Subject.CommonName != "vulos.local" {
		t.Fatalf("leaf CN = %q; the requester's CN (google.com) must not carry through", leaf.Subject.CommonName)
	}
}

func TestIssueFromCSRRefusesACSRWhoseSignatureDoesNotVerify(t *testing.T) {
	root, err := NewRoot("csr-test")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrPEM, err := NewCSR(key, "vulos.local")
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt the signature by flipping a bit in the last byte of the DER.
	block, _ := pem.Decode(csrPEM)
	corrupted := append([]byte(nil), block.Bytes...)
	corrupted[len(corrupted)-1] ^= 0xff
	badPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: corrupted})

	ns, _ := NewNameSet([]string{"vulos.local"}, nil)
	if _, _, err := root.IssueFromCSR(badPEM, ns, 0); err == nil {
		t.Fatal("SECURITY: signed a CSR whose signature does not verify — proof of possession is not being checked, so a certificate could be minted over a public key the requester does not control")
	}
}

// ---------------------------------------------------------------------------
// The name set is derived, never guessed.
// ---------------------------------------------------------------------------

func TestNameSetRefusesAnEmptySetRatherThanInventingADefault(t *testing.T) {
	// The bug class this guards: a caller whose hostname lookup failed passing
	// nothing, and the CA helpfully substituting "vulos.local". That is exactly
	// the hardcoded guess the two-box collision is made of.
	if _, err := NewNameSet(nil, nil); err == nil {
		t.Fatal("NewNameSet invented a name set from nothing; it must refuse so the caller fixes its derivation")
	}
	if _, err := NewNameSet([]string{"", "  ", "."}, nil); err == nil {
		t.Fatal("NewNameSet accepted a set of blank names")
	}
}

func TestNameSetNormalisesWhatAnMDNSResponderActuallyEmits(t *testing.T) {
	ns, err := NewNameSet(
		[]string{"VULOS.local.", "vulos.local", " vulos-2.local "},
		[]net.IP{net.ParseIP("192.168.1.50"), net.ParseIP("192.168.1.50")},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"vulos.local", "vulos-2.local"}
	if len(ns.DNSNames) != len(want) {
		t.Fatalf("DNS names = %v, want %v (trailing dot, case and duplicates all collapse)", ns.DNSNames, want)
	}
	for i := range want {
		if ns.DNSNames[i] != want[i] {
			t.Fatalf("DNS names = %v, want %v", ns.DNSNames, want)
		}
	}
	if len(ns.IPs) != 1 {
		t.Fatalf("IPs = %v, want the duplicate collapsed to one", ns.IPs)
	}
}

func TestLeafNeverOutlivesTheRoot(t *testing.T) {
	root, err := NewRoot("ttl-test")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// Ask for a leaf lasting a century.
	_, leaf, err := root.Issue(LeafRequest{
		DNSNames:  []string{"vulos.local"},
		PublicKey: &key.PublicKey,
		TTL:       100 * 365 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if leaf.NotAfter.After(root.Cert.NotAfter) {
		t.Fatalf("leaf expires %s, AFTER the root's %s — such a chain fails at the root's expiry with a confusing error",
			leaf.NotAfter, root.Cert.NotAfter)
	}
}

// ---------------------------------------------------------------------------
// The CA key must not end up on a box.
// ---------------------------------------------------------------------------

func TestSaveRootRefusesToWriteTheCAKeyOntoABox(t *testing.T) {
	root, err := NewRoot("box-test")
	if err != nil {
		t.Fatal(err)
	}
	// Hand-transcribed box paths — NOT read from boxOnlyPathPrefixes.
	for _, dir := range []string{
		"/var/lib/vulos",
		"/var/lib/vulos/tls",
		"/var/lib/vulos/ca",
		"/etc/vulos",
		"/etc/vulos/pki",
		"/run/vulos/whatever",
	} {
		if err := SaveRoot(dir, root); err == nil {
			t.Errorf("SaveRoot(%q) wrote CA key material onto a Vulos box path", dir)
		} else if !strings.Contains(err.Error(), "REFUSING") {
			t.Errorf("SaveRoot(%q) failed, but not with the box-path refusal: %v", dir, err)
		}
	}

	// The near-miss must still be allowed: a path that merely SHARES A PREFIX
	// with a box path is not a box path. If this were rejected, the guard would
	// be doing sloppy substring matching.
	tmp := t.TempDir()
	for _, dir := range []string{
		filepath.Join(tmp, "var-lib-vulos-backup"),
		filepath.Join(tmp, "ca"),
	} {
		if err := SaveRoot(dir, root); err != nil {
			t.Errorf("SaveRoot(%q) refused a legitimate operator path: %v", dir, err)
		}
	}
}

func TestSaveRootRefusesToOverwriteALiveCA(t *testing.T) {
	dir := t.TempDir()
	r1, _ := NewRoot("first")
	if err := SaveRoot(dir, r1); err != nil {
		t.Fatal(err)
	}
	r2, _ := NewRoot("second")
	if err := SaveRoot(dir, r2); err == nil {
		t.Fatal("SaveRoot overwrote an existing CA key — every issued certificate and every device that trusts the old root would be silently orphaned")
	}

	// And the original must still be intact and usable.
	got, err := OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cert.SerialNumber.Cmp(r1.Cert.SerialNumber) != 0 {
		t.Fatal("the on-disk CA is no longer the original")
	}
}

func TestOpenRootRefusesAWorldReadableCAKey(t *testing.T) {
	dir := t.TempDir()
	r, _ := NewRoot("perm-test")
	if err := SaveRoot(dir, r); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, RootKeyFile)

	// Control: at 0600 it opens.
	if _, err := OpenRoot(dir); err != nil {
		t.Fatalf("CONTROL FAILED: a correctly-permissioned CA would not open: %v", err)
	}

	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660, 0o606} {
		if err := os.Chmod(keyPath, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenRoot(dir); err == nil {
			t.Errorf("OpenRoot used a CA private key at mode %v — readable by other local users, which makes it their CA too", mode)
		}
	}
}

// TestCheckNotOnBoxDirectly exercises the box-path guard without going through
// SaveRoot's filesystem calls.
//
// This matters because on a developer Mac, SaveRoot("/var/lib/vulos") fails with
// "permission denied" whether or not the guard exists — so a test that only
// asserted "SaveRoot returned an error" would pass vacuously here and provide no
// coverage at all on the one platform (Linux, as root) where the guard is
// load-bearing. The sibling test above defends against that by requiring the
// word REFUSING in the error; this one removes the filesystem from the picture
// entirely.
func TestCheckNotOnBoxDirectly(t *testing.T) {
	onBox := []string{
		"/var/lib/vulos",
		"/var/lib/vulos/",
		"/var/lib/vulos/tls",
		"/var/lib/vulos/../vulos/ca", // must survive path normalisation
		"/etc/vulos",
		"/etc/vulos/pki/ca",
		"/run/vulos",
		"/run/vulos/x",
	}
	for _, p := range onBox {
		if err := CheckNotOnBox(p); err == nil {
			t.Errorf("CheckNotOnBox(%q) allowed a Vulos box path", p)
		}
	}

	offBox := []string{
		"/home/imran/.vulos-lanca",
		"/Users/imran/.vulos-lanca",
		"/opt/ca",
		"/var/lib/vulos-lanca", // prefix-shares "/var/lib/vulos" but is NOT under it
		"/var/lib/vulosbackup", // same trap
		"/etc/vulos-lanca",     // same trap
		"/srv/vulos/ca",        // "vulos" appears, but not at a box prefix
	}
	for _, p := range offBox {
		if err := CheckNotOnBox(p); err != nil {
			t.Errorf("CheckNotOnBox(%q) refused a legitimate operator path — the guard is matching substrings instead of path prefixes: %v", p, err)
		}
	}
}

// ---------------------------------------------------------------------------
// The CA must cope with the REAL name set internal/lan derives.
// ---------------------------------------------------------------------------

// boxNameSetShape is the DNS SAN list internal/lan's NameSet.DNSNames produces,
// transcribed BY HAND from its documented construction (for each base name:
// the bare label, base+".local", base+".lan", base+".home.arpa"; then
// box.<id>.lan.vulos.org).
//
// It is a hand-written fixture rather than a call into package lan on purpose.
// Importing lan here would make this test assert that two pieces of our own
// code agree with each other, which is the weaker claim; a transcribed fixture
// asserts that this CA can serve the name SHAPE the box actually advertises. If
// lan's shape changes, the cross-package guard noted below is what should catch
// it — not this test silently following along.
var boxNameSetShape = []string{
	"vulos-k3n7q2",
	"vulos-k3n7q2.local",
	"vulos-k3n7q2.lan",
	"vulos-k3n7q2.home.arpa",
	"vulos",
	"vulos.local",
	"vulos.lan",
	"vulos.home.arpa",
	"box.01jd8x7k3n7q2.lan.vulos.org",
}

func TestFilterIssuableCoversEveryDottedNameTheBoxAdvertises(t *testing.T) {
	ips := []net.IP{net.ParseIP("192.168.1.50"), net.ParseIP("169.254.7.7")}
	ns, skippedDNS, skippedIPs := FilterIssuable(boxNameSetShape, ips)

	if len(skippedIPs) != 0 {
		t.Fatalf("CA cannot issue for the box's own LAN addresses %v", skippedIPs)
	}

	// Everything the box advertises that has a dot in it must be issuable.
	// These are the names an owner can actually type into a browser and have
	// resolved by mDNS or the router.
	for _, n := range boxNameSetShape {
		if !strings.Contains(n, ".") {
			continue
		}
		if err := CheckDNSName(n); err != nil {
			t.Errorf("the box advertises %q but this CA cannot issue for it: %v", n, err)
		}
	}

	// The bare labels are the KNOWN, ACCEPTED gap: RFC 5280 permittedSubtrees
	// cannot express "any single label", so they can never appear on a
	// CA-issued leaf. They must be reported as skipped, not silently dropped
	// and not smuggled in.
	wantSkipped := map[string]bool{"vulos": true, "vulos-k3n7q2": true}
	if len(skippedDNS) != len(wantSkipped) {
		t.Fatalf("skipped = %v, want exactly the two bare labels %v", skippedDNS, wantSkipped)
	}
	for _, n := range skippedDNS {
		if !wantSkipped[n] {
			t.Errorf("skipped %q, which is NOT a bare label — a dotted name the box advertises is being dropped from the certificate", n)
		}
	}
	for _, n := range ns.DNSNames {
		if !strings.Contains(n, ".") {
			t.Errorf("issuable set contains bare label %q, which no permittedSubtrees entry can cover — the resulting leaf would be rejected by every enforcing verifier", n)
		}
	}
}

func TestAWholeBoxNameSetIssuesAndVerifies(t *testing.T) {
	root, err := NewRoot("shape-test")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrPEM, err := NewCSR(key, "vulos.local")
	if err != nil {
		t.Fatal(err)
	}

	ns, _, _ := FilterIssuable(boxNameSetShape, []net.IP{net.ParseIP("192.168.1.50")})
	_, leaf, err := root.IssueFromCSR(csrPEM, ns, 0)
	if err != nil {
		t.Fatalf("could not issue for the box's real name set: %v", err)
	}

	// Every issuable name must actually verify against the root — proving the
	// issuing-side filter and the verifying-side constraint agree.
	for _, name := range ns.DNSNames {
		if err := verifyAgainstRoot(root, leaf, name); err != nil {
			t.Errorf("issued for %q but verification fails: %v", name, err)
		}
	}
	if err := verifyAgainstRoot(root, leaf, "192.168.1.50"); err != nil {
		t.Errorf("browsing by IP fails: %v", err)
	}
}
