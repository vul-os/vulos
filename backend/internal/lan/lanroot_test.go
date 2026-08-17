package lan

// lanroot_test.go — ROOTDIST-01.
//
// Every test here runs a CONTROL before it measures a refusal: the legitimate
// constrained root must be ACCEPTED by the same call in the same test, so a
// green "refused" result cannot be produced by a function that refuses
// everything. That is the failure mode this repository keeps finding — a guard
// that reports PASS while checking nothing — and a refusal test with no control
// is exactly that guard.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/lanca"
)

// testRoot mints a real name-constrained root via the production CA package,
// plus a leaf it issued over a box key, so the tests measure the actual
// artefacts an owner would handle rather than hand-built lookalikes.
func testRoot(t *testing.T, label string) (rootPEM []byte, leaf *x509.Certificate, boxKey *ecdsa.PrivateKey) {
	t.Helper()
	root, err := lanca.NewRoot(label)
	if err != nil {
		t.Fatalf("lanca.NewRoot: %v", err)
	}
	boxKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("box key: %v", err)
	}
	ns, err := lanca.NewNameSet([]string{"vulos.local"}, []net.IP{net.ParseIP("192.168.1.50")})
	if err != nil {
		t.Fatalf("NewNameSet: %v", err)
	}
	_, leaf, err = root.Issue(lanca.LeafRequest{
		DNSNames:  ns.DNSNames,
		IPs:       ns.IPs,
		PublicKey: &boxKey.PublicKey,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return root.CertPEM(), leaf, boxKey
}

// unconstrainedRootPEM builds a CA that is identical in every respect that
// matters EXCEPT that it carries no permittedSubtrees — the one difference
// under test, and the one no install dialog on any OS shows the owner.
func unconstrainedRootPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(42),
		Subject:               pkix.Name{CommonName: "Totally Normal Root CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestParseRootPEM_AcceptsConstrainedRoot is the CONTROL for every refusal test
// below, and it also proves the reported facts are the certificate's own.
func TestParseRootPEM_AcceptsConstrainedRoot(t *testing.T) {
	rootPEM, _, _ := testRoot(t, "kitchen shelf")

	info, err := ParseRootPEM(rootPEM)
	if err != nil {
		t.Fatalf("CONTROL FAILED: a legitimate name-constrained root was refused: %v — "+
			"every refusal assertion in this file is meaningless if the accept path does not work", err)
	}
	if !info.Constrained() {
		t.Fatalf("a lanca root reported as unconstrained; permitted DNS=%v IP=%v", info.PermittedDNS, info.PermittedIP)
	}
	if !strings.Contains(info.Subject, "kitchen shelf") {
		t.Fatalf("subject %q lost the operator's label — an owner with several roots installed cannot tell them apart without it", info.Subject)
	}
	if !info.PathLenZero {
		t.Fatalf("root does not carry pathLenConstraint=0; it could sign a subordinate CA")
	}

	// The fingerprint must be the SHA-256 of the DER — the value `openssl x509
	// -fingerprint -sha256` prints — because that is what the owner compares.
	// Computed here independently rather than by calling the same helper.
	block, _ := pem.Decode(rootPEM)
	sum := sha256.Sum256(block.Bytes)
	want := strings.ToUpper(hex.EncodeToString(sum[:]))
	got := strings.ReplaceAll(info.SHA256Hex, ":", "")
	if got != want {
		t.Fatalf("SHA256Hex is not the SHA-256 of the certificate DER.\n got %s\nwant %s\n"+
			"An owner comparing this against `vulos-lanca inspect` or `openssl x509 -fingerprint` would be comparing the wrong number.", got, want)
	}
	if strings.Count(info.SHA256Hex, ":") != 31 {
		t.Fatalf("SHA256Hex %q is not colon-grouped hex pairs; it will not line up against openssl output", info.SHA256Hex)
	}
}

// TestParseRootPEM_RefusesUnconstrainedRoot is the security property D101-B
// rests on: a root with no permittedSubtrees can vouch for any name on earth,
// and no OS install dialog tells the owner that.
func TestParseRootPEM_RefusesUnconstrainedRoot(t *testing.T) {
	t.Setenv("VULOS_LANCERT_ALLOW_UNCONSTRAINED_ROOT", "")

	// CONTROL: the constrained root goes through the same call, first.
	constrained, _, _ := testRoot(t, "control")
	if _, err := ParseRootPEM(constrained); err != nil {
		t.Fatalf("CONTROL FAILED: constrained root refused (%v) — a refusal below would prove nothing", err)
	}

	_, err := ParseRootPEM(unconstrainedRootPEM(t))
	if !errors.Is(err, ErrRootUnconstrained) {
		t.Fatalf("ROOTDIST-01 REGRESSION: an UNCONSTRAINED CA was accepted for distribution to owners' devices (err=%v). "+
			"Installing it grants every device a standing authority for ANY name, including public sites — the exact "+
			"property D101 claims this design does not have.", err)
	}
}

// TestParseRootPEM_UnconstrainedOverrideIsOptIn proves the escape hatch exists
// and is off by default — so the refusal above is a decision, not an accident
// of the environment the test happened to run in.
func TestParseRootPEM_UnconstrainedOverrideIsOptIn(t *testing.T) {
	pemBytes := unconstrainedRootPEM(t)

	t.Setenv("VULOS_LANCERT_ALLOW_UNCONSTRAINED_ROOT", "")
	if _, err := ParseRootPEM(pemBytes); !errors.Is(err, ErrRootUnconstrained) {
		t.Fatalf("without the override an unconstrained root must be refused, got %v", err)
	}

	t.Setenv("VULOS_LANCERT_ALLOW_UNCONSTRAINED_ROOT", "1")
	if _, err := ParseRootPEM(pemBytes); err != nil {
		t.Fatalf("with the override set an unconstrained root must be accepted, got %v", err)
	}
}

// TestParseRootPEM_RefusesNonCA covers the likeliest operator slip: copying the
// LEAF to the root path. It installs cleanly into a trust store on several
// platforms and validates nothing, so the owner concludes the flow is broken.
func TestParseRootPEM_RefusesNonCA(t *testing.T) {
	rootPEM, leaf, _ := testRoot(t, "control")

	if _, err := ParseRootPEM(rootPEM); err != nil {
		t.Fatalf("CONTROL FAILED: the root was refused (%v)", err)
	}

	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
	if _, err := ParseRootPEM(leafPEM); !errors.Is(err, ErrRootNotCA) {
		t.Fatalf("a server leaf was accepted as a CA root (err=%v)", err)
	}
}

// TestInstallRootPEM_RefusesARootThatDidNotIssueTheLeaf is the check that
// separates "distributing a useful root" from "adding a stranger's CA to every
// device in the house". The wrong root produces no padlock AND a standing
// authority, so there is no benign version of the mismatch.
func TestInstallRootPEM_RefusesARootThatDidNotIssueTheLeaf(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lan-root.crt")

	rootA, leafA, _ := testRoot(t, "ours")
	rootB, _, _ := testRoot(t, "somebody else's")

	// CONTROL: the root that DID issue this leaf installs.
	if _, err := InstallRootPEM(path, rootA, leafA); err != nil {
		t.Fatalf("CONTROL FAILED: the issuing root was refused for its own leaf: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("CONTROL FAILED: nothing was written to %s: %v", path, err)
	}

	// MEASURE: an unrelated (but perfectly valid, perfectly constrained) root.
	before, _ := os.ReadFile(path)
	if _, err := InstallRootPEM(path, rootB, leafA); err == nil {
		t.Fatal("ROOTDIST-01 REGRESSION: a root that did NOT issue this box's certificate was accepted. " +
			"An owner installing it gets no padlock for this box and a device-wide CA they did not need.")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("the refused root overwrote the good one on disk — a refusal must not mutate state")
	}
}

// TestInstallRootPEM_WritesWorldReadableCertOnly guards the perms split: the
// root is public and must be readable (it is about to be downloaded by every
// device in the house), while nothing here may ever write key material.
func TestInstallRootPEM_WritesWorldReadableCertOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lan-root.crt")
	rootPEM, leaf, _ := testRoot(t, "perms")

	if _, err := InstallRootPEM(path, rootPEM, leaf); err != nil {
		t.Fatalf("install: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != rootFileMode {
		t.Fatalf("root cert mode %s, want %s", fi.Mode().Perm(), rootFileMode)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(b), "PRIVATE KEY") {
		t.Fatal("the root certificate file contains PRIVATE KEY material — the CA key must never be on the box (lanca.CheckNotOnBox)")
	}
}

// TestLoadRootInfo_MissingIsNotAnError: an owner who has never run the CA is in
// a normal state, and a surface that renders it as a fault teaches them
// something is broken when nothing is.
func TestLoadRootInfo_MissingIsNotAnError(t *testing.T) {
	_, err := LoadRootInfo(filepath.Join(t.TempDir(), "nope.crt"))
	if !errors.Is(err, ErrRootNotPresent) {
		t.Fatalf("missing root returned %v, want ErrRootNotPresent", err)
	}
}

// TestLoadRootInfo_VetsOnREAD, not only on write. The manual install path
// (`scp root.crt box:/var/lib/vulos/tls/lan-root.crt`) never passes through the
// puller, so a check that only ran on the puller's write would be bypassed by
// the exact flow the docs tell owners to use.
func TestLoadRootInfo_VetsOnRead(t *testing.T) {
	t.Setenv("VULOS_LANCERT_ALLOW_UNCONSTRAINED_ROOT", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "lan-root.crt")

	// CONTROL: a hand-placed CONSTRAINED root reads back fine.
	good, _, _ := testRoot(t, "hand-placed")
	if err := os.WriteFile(path, good, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRootInfo(path); err != nil {
		t.Fatalf("CONTROL FAILED: a hand-placed constrained root was refused on read: %v", err)
	}

	// MEASURE: a hand-placed UNCONSTRAINED root must be refused on read.
	if err := os.WriteFile(path, unconstrainedRootPEM(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRootInfo(path); !errors.Is(err, ErrRootUnconstrained) {
		t.Fatalf("ROOTDIST-01 REGRESSION: a hand-placed unconstrained root passed the read path (err=%v). "+
			"scp'ing a file in is the documented manual flow, so a write-only check is no check at all.", err)
	}
}

// --- the carrier: the puller is how the root reaches the box ----------------

// rootIssuer stands in for the operator's control plane. It answers the two
// lancert endpoints, and `sendRoot` decides whether it carries the root at all
// — which is the field ROOTDIST-01 added and the thing under test.
func rootIssuer(t *testing.T, leafPEM, keyPEM, rootPEM string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/lancert/report-ip", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/lancert/cert", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"box_id":    r.URL.Query().Get("box_id"),
			"fqdn":      "vulos.local",
			"cert_pem":  leafPEM,
			"key_pem":   keyPEM,
			"root_pem":  rootPEM,
			"not_after": time.Now().Add(397 * 24 * time.Hour).Format(time.RFC3339),
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// pullerFor wires a puller at temp paths against srv.
func pullerFor(t *testing.T, srv *httptest.Server, dir, ownKeyPath string) *LANCertPuller {
	t.Helper()
	p, err := NewLANCertPuller(PullerConfig{
		CloudBaseURL:  srv.URL,
		SharedSecret:  "test",
		BoxID:         "test-box",
		CertPath:      filepath.Join(dir, "lan.crt"),
		KeyPath:       filepath.Join(dir, "lan.key"),
		RootPath:      filepath.Join(dir, "lan-root.crt"),
		OwnKeyPath:    ownKeyPath,
		LANIPProvider: func() net.IP { return net.IPv4(192, 168, 1, 50) },
		HTTPClient:    srv.Client(),
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("NewLANCertPuller: %v", err)
	}
	return p
}

// seedBoxKey writes a box private key to a temp path and returns both, so a
// leaf can be issued over the SAME key the puller will pair the cert with —
// which is the CSR-based shape D101-C requires.
func seedBoxKey(t *testing.T, key *ecdsa.PrivateKey) (path, keyPEM string) {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	path = filepath.Join(t.TempDir(), "lan-selfsigned.key")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, string(pemBytes)
}

// TestPullerCarriesTheRootToTheBox is the whole point of ROOTDIST-01: before
// it, the box held its leaf and NOTHING ELSE, so the one machine every device
// on the LAN can reach had no copy of the one file a human has to move.
func TestPullerCarriesTheRootToTheBox(t *testing.T) {
	rootPEM, leaf, boxKey := testRoot(t, "carrier")
	ownKeyPath, keyPEM := seedBoxKey(t, boxKey)
	leafPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))

	dir := t.TempDir()
	srv := rootIssuer(t, leafPEM, keyPEM, string(rootPEM))
	p := pullerFor(t, srv, dir, ownKeyPath)

	if err := p.fetchOnce(context.Background()); err != nil {
		t.Fatalf("fetchOnce: %v", err)
	}

	got, err := LoadRootInfo(filepath.Join(dir, "lan-root.crt"))
	if err != nil {
		t.Fatalf("ROOTDIST-01: the puller did not leave a usable CA root on the box (%v). "+
			"Without it there is no way for an owner to get a padlock onto a phone from the box.", err)
	}
	want, err := ParseRootPEM(rootPEM)
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256Hex != want.SHA256Hex {
		t.Fatalf("the box stored a different root than the issuer sent\n got %s\nwant %s", got.SHA256Hex, want.SHA256Hex)
	}
}

// TestPullerRefusesAForeignRootButKeepsTheLeaf pins the best-effort contract:
// a bad root must never cost the box a good certificate.
func TestPullerRefusesAForeignRootButKeepsTheLeaf(t *testing.T) {
	_, leaf, boxKey := testRoot(t, "ours")
	foreignRoot, _, _ := testRoot(t, "somebody else's")
	ownKeyPath, keyPEM := seedBoxKey(t, boxKey)
	leafPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))

	dir := t.TempDir()
	srv := rootIssuer(t, leafPEM, keyPEM, string(foreignRoot))
	p := pullerFor(t, srv, dir, ownKeyPath)

	if err := p.fetchOnce(context.Background()); err != nil {
		t.Fatalf("a rejected root must not fail the pull, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lan.crt")); err != nil {
		t.Fatalf("the LEAF was not installed (%v) — a bad root must not cost the box a good certificate", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lan-root.crt")); err == nil {
		t.Fatal("ROOTDIST-01 REGRESSION: a root that did not issue this box's certificate was stored for owners to install")
	}
}

// TestPullerWithNoRootInResponseStillInstallsTheLeaf: an issuer that predates
// ROOTDIST-01 sends no root_pem, and that has to keep working unchanged.
func TestPullerWithNoRootInResponseStillInstallsTheLeaf(t *testing.T) {
	_, leaf, boxKey := testRoot(t, "legacy issuer")
	ownKeyPath, keyPEM := seedBoxKey(t, boxKey)
	leafPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))

	dir := t.TempDir()
	srv := rootIssuer(t, leafPEM, keyPEM, "")
	p := pullerFor(t, srv, dir, ownKeyPath)

	if err := p.fetchOnce(context.Background()); err != nil {
		t.Fatalf("fetchOnce: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lan.crt")); err != nil {
		t.Fatalf("the leaf was not installed: %v", err)
	}
	if _, err := LoadRootInfo(filepath.Join(dir, "lan-root.crt")); !errors.Is(err, ErrRootNotPresent) {
		t.Fatalf("with no root_pem the box must report no root, got %v", err)
	}
}

// TestRootInfoExpired covers the fact the panel has to be able to state: a root
// past NotAfter installs on most platforms and validates nothing.
func TestRootInfoExpired(t *testing.T) {
	rootPEM, _, _ := testRoot(t, "expiry")
	info, err := ParseRootPEM(rootPEM)
	if err != nil {
		t.Fatal(err)
	}
	if info.Expired(time.Now()) {
		t.Fatal("a freshly minted root reported as expired")
	}
	if !info.Expired(info.NotAfter.Add(time.Second)) {
		t.Fatal("a root one second past NotAfter did not report as expired")
	}
	if !info.Expired(info.NotBefore.Add(-time.Second)) {
		t.Fatal("a root one second before NotBefore did not report as expired — a box with no RTC can boot with a badly wrong clock")
	}
}
