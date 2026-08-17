package lanca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Do OTHER X.509 implementations actually enforce permittedSubtrees?
//
// The design's second property — "a stolen CA key cannot mint for google.com" —
// is a claim about code we do not own. Go's crypto/x509 enforcing it (see
// constraints_test.go) is one data point from one implementation, and the
// implementations differ in the details. These tests measure three MORE
// independent implementations when their tools are installed:
//
//   - OpenSSL       (`openssl verify`)       — the OpenSSL/LibreSSL family
//   - NSS           (`vfychain`)             — Mozilla's certificate library
//   - Security.framework (`security verify-cert`, darwin only) — APPLE'S OWN
//     platform verifier, the code path Safari and every macOS/iOS app that uses
//     the system trust evaluation goes through
//
// They SKIP rather than fail when a tool is absent, so CI on a bare image stays
// green — but on a machine with the tools they are real measurements, and the
// skip message says plainly that nothing was proven.
//
// WHAT THESE DO NOT MEASURE, and must never be claimed to:
//   - Firefox does NOT use the NSS verifier tested here. It uses mozilla::pkix,
//     a separate implementation. NSS agreeing is evidence about NSS.
//   - Chrome uses its own Chrome Root Store + BoringSSL verifier on desktop.
//   - Android uses Conscrypt; iOS/macOS use Security.framework.
//   - No browser, and no mobile device, is exercised anywhere in this package.
// ---------------------------------------------------------------------------

// externalFixture writes, into a temp dir:
//
//	root.pem        — the real, name-constrained root
//	legit.pem       — a vulos.local leaf under it            (must VERIFY)
//	forged.pem      — a google.com leaf under it             (must be REJECTED)
//	ncroot.pem      — the SAME root shape MINUS the constraints
//	ncforged.pem    — the SAME google.com leaf under ncroot  (must VERIFY)
//
// The last two are the load-bearing part. Asserting only "the forged leaf is
// rejected" is a hollow measurement: a verifier could reject it for an
// unrelated reason (unknown issuer, bad usage, a quirk of the tool's
// invocation) and the test would report enforcement that is not happening. NSS
// in particular reports a name-constraint violation as "ERROR -8157:
// Certificate extension not found", which reads like something else entirely.
//
// Running the identical leaf against an otherwise-identical UNCONSTRAINED root
// turns the test into a differential: if the only difference between accept and
// reject is the presence of permittedSubtrees, then permittedSubtrees is what
// caused the rejection. That conclusion does not depend on any error string.
type externalFixture struct {
	dir        string
	rootPEM    string
	legitPEM   string
	forgePEM   string
	ncRootPEM  string
	ncForgePEM string
}

// newUnconstrainedRoot builds a root identical to NewRoot's output except that
// it carries NO name constraints. Test-only: this is the negative control, and
// it is deliberately built here rather than by flag-switching production code,
// so no code path can ever ship an unconstrained root by accident.
func newUnconstrainedRoot(t *testing.T) *Root {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Unconstrained Control Root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(RootTTL),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		// No PermittedDNSDomains, no PermittedIPRanges. That is the point.
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &Root{Cert: cert, Key: key, DER: der}
}

func newExternalFixture(t *testing.T) externalFixture {
	t.Helper()
	root, err := NewRoot("external-verifier-test")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	writeCert := func(name string, der []byte) string {
		return write(name, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}

	rootPath := write("root.pem", root.CertPEM())

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	legitPEM, _, err := root.Issue(LeafRequest{
		DNSNames:  []string{"vulos.local"},
		IPs:       []net.IP{net.ParseIP("192.168.1.50")},
		PublicKey: &key.PublicKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	legitPath := write("legit.pem", legitPEM)

	// The stolen-key forgery, signed directly with the root key.
	forged := forgeLeafFor(t, root, []string{"google.com"}, nil)
	forgePath := writeCert("forged.pem", forged.Raw)

	// The negative control: same forgery, unconstrained root.
	ncRoot := newUnconstrainedRoot(t)
	ncForged := forgeLeafFor(t, ncRoot, []string{"google.com"}, nil)
	ncRootPath := writeCert("ncroot.pem", ncRoot.DER)
	ncForgePath := writeCert("ncforged.pem", ncForged.Raw)

	return externalFixture{
		dir: dir, rootPEM: rootPath, legitPEM: legitPath, forgePEM: forgePath,
		ncRootPEM: ncRootPath, ncForgePEM: ncForgePath,
	}
}

func TestOpenSSLEnforcesNameConstraints(t *testing.T) {
	bin, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not installed — NOTHING was proven about OpenSSL's name-constraint enforcement on this machine")
	}
	fx := newExternalFixture(t)

	verify := func(caFile, leafFile string) (string, error) {
		out, err := exec.Command(bin, "verify", "-CAfile", caFile, leafFile).CombinedOutput()
		return string(out), err
	}

	// Control 1 — the legitimate leaf must verify. If it does not, a failure on
	// the forged one proves nothing (it could be failing for an unrelated
	// reason, which is exactly how a hollow gate is born).
	out, err := verify(fx.rootPEM, fx.legitPEM)
	if err != nil {
		t.Fatalf("CONTROL FAILED: openssl rejected the LEGITIMATE vulos.local leaf, so this test cannot distinguish constraint enforcement from a broken fixture.\n%s", out)
	}
	t.Logf("openssl control 1 (vulos.local under constrained root, must PASS): %s", strings.TrimSpace(out))

	// Control 2 — the SAME forgery under an UNCONSTRAINED root must verify.
	// This is what proves the rejection below is caused by the constraint and
	// not by anything else about the certificate or the invocation.
	out, err = verify(fx.ncRootPEM, fx.ncForgePEM)
	if err != nil {
		t.Fatalf("CONTROL FAILED: openssl rejected a google.com leaf under an UNCONSTRAINED root too, so rejecting it under the constrained root would prove nothing about name constraints.\n%s", out)
	}
	t.Logf("openssl control 2 (google.com under UNCONSTRAINED root, must PASS): %s", strings.TrimSpace(out))

	// The measurement.
	out, err = verify(fx.rootPEM, fx.forgePEM)
	if err == nil {
		t.Fatalf("SECURITY: openssl ACCEPTED a google.com leaf signed by the name-constrained root — OpenSSL is not enforcing permittedSubtrees here.\n%s", out)
	}
	t.Logf("openssl REJECTED google.com under the constrained root; the only difference from control 2 is permittedSubtrees: %s", strings.TrimSpace(out))
}

func TestNSSEnforcesNameConstraints(t *testing.T) {
	vfy, err := exec.LookPath("vfychain")
	if err != nil {
		t.Skip("vfychain (nss tools) not installed — NOTHING was proven about NSS's name-constraint enforcement on this machine")
	}
	certutil, err := exec.LookPath("certutil")
	if err != nil {
		t.Skip("certutil (nss tools) not installed — NOTHING was proven about NSS's name-constraint enforcement on this machine")
	}
	fx := newExternalFixture(t)

	run := func(name string, args ...string) (string, error) {
		out, err := exec.Command(name, args...).CombinedOutput()
		return string(out), err
	}

	// newDB builds a fresh NSS database trusting exactly one root ("C,," =
	// trusted CA for TLS). Each root gets its own db so the two arms of the
	// differential cannot contaminate each other.
	newDB := func(tag, rootPath string) string {
		db := filepath.Join(fx.dir, "nssdb-"+tag)
		if err := os.MkdirAll(db, 0o700); err != nil {
			t.Fatal(err)
		}
		pwFile := filepath.Join(fx.dir, "pw")
		if err := os.WriteFile(pwFile, []byte("\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := run(certutil, "-N", "-d", db, "-f", pwFile); err != nil {
			t.Skipf("could not create an NSS db (%v): %s", err, out)
		}
		if out, err := run(certutil, "-A", "-d", db, "-n", "root-"+tag, "-t", "C,,", "-a", "-i", rootPath, "-f", pwFile); err != nil {
			t.Skipf("could not import a root into the NSS db (%v): %s", err, out)
		}
		return db
	}

	// -u 1 selects the TLS *server* usage.
	chainGood := func(db, leaf string) (bool, string) {
		out, err := run(vfy, "-d", db, "-u", "1", "-a", leaf)
		return err == nil && strings.Contains(out, "Chain is good"), strings.TrimSpace(out)
	}

	constrainedDB := newDB("constrained", fx.rootPEM)
	unconstrainedDB := newDB("unconstrained", fx.ncRootPEM)

	// Control 1 — legitimate leaf under the constrained root must verify.
	if ok, out := chainGood(constrainedDB, fx.legitPEM); !ok {
		t.Skipf("CONTROL INCONCLUSIVE: NSS did not validate the legitimate vulos.local leaf, so a rejection of the forged one would prove nothing here. vfychain said:\n%s", out)
	} else {
		t.Logf("NSS control 1 (vulos.local under constrained root, must PASS): %s", out)
	}

	// Control 2 — the SAME forgery under an UNCONSTRAINED root must verify.
	//
	// This control is not optional politeness. NSS reports a name-constraint
	// violation as "ERROR -8157: Certificate extension not found", which is
	// indistinguishable by eye from a dozen unrelated failures. Without this
	// arm, the test would happily report "NSS enforces name constraints" on the
	// strength of a rejection that had nothing to do with them.
	if ok, out := chainGood(unconstrainedDB, fx.ncForgePEM); !ok {
		t.Skipf("CONTROL INCONCLUSIVE: NSS rejected a google.com leaf under an UNCONSTRAINED root too, so rejecting it under the constrained root proves nothing about name constraints. vfychain said:\n%s", out)
	} else {
		t.Logf("NSS control 2 (google.com under UNCONSTRAINED root, must PASS): %s", out)
	}

	// The measurement. Both controls passed, so the ONLY difference between
	// this call and control 2 is the permittedSubtrees extension.
	if ok, out := chainGood(constrainedDB, fx.forgePEM); ok {
		t.Fatalf("SECURITY: NSS ACCEPTED a google.com leaf signed by the name-constrained root — NSS is not enforcing permittedSubtrees here.\n%s", out)
	} else {
		t.Logf("NSS REJECTED google.com under the constrained root; the only difference from control 2 is permittedSubtrees: %s", out)
	}
}

// TestDarwinSecurityFrameworkEnforcesNameConstraints measures APPLE'S OWN
// certificate verifier via `security verify-cert`.
//
// This is the most transferable of the three measurements here: Security.framework
// is the shared trust-evaluation stack across macOS and iOS, so a positive result
// is real evidence about the Apple platform verifier generally. It is still NOT a
// measurement on an iOS device — iOS additionally requires the user to grant full
// trust under Settings → General → About → Certificate Trust Settings before an
// installed root is used at all, and nothing in this package exercises that step.
func TestDarwinSecurityFrameworkEnforcesNameConstraints(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("not darwin — NOTHING was proven about Apple's Security.framework on this machine")
	}
	bin, err := exec.LookPath("security")
	if err != nil {
		t.Skip("`security` tool not found — NOTHING was proven about Apple's Security.framework on this machine")
	}
	fx := newExternalFixture(t)

	// `security verify-cert` exits 0 even when verification FAILS, reporting the
	// outcome only in its text. Keying off the exit status would produce a test
	// that passes unconditionally — a hollow gate of the purest kind. Parse the
	// text, and require one of the two known outcomes so an unrecognised third
	// outcome fails loudly instead of being read as success.
	verify := func(root, leaf, host string) bool {
		out, _ := exec.Command(bin, "verify-cert", "-c", leaf, "-r", root, "-p", "ssl", "-s", host).CombinedOutput()
		txt := string(out)
		switch {
		case strings.Contains(txt, "certificate verification successful"):
			return true
		case strings.Contains(txt, "CSSMERR_"), strings.Contains(txt, "Cert Verify Result"):
			return false
		default:
			t.Fatalf("could not interpret `security verify-cert` output; refusing to guess:\n%s", txt)
			return false
		}
	}

	if !verify(fx.rootPEM, fx.legitPEM, "vulos.local") {
		t.Fatal("CONTROL FAILED: Security.framework rejected the LEGITIMATE vulos.local leaf, so a rejection of the forged one would prove nothing")
	}
	t.Log("Security.framework control 1 (vulos.local under constrained root): verification successful, as required")

	if !verify(fx.ncRootPEM, fx.ncForgePEM, "google.com") {
		t.Fatal("CONTROL FAILED: Security.framework rejected a google.com leaf under an UNCONSTRAINED root too, so rejecting it under the constrained root proves nothing about name constraints")
	}
	t.Log("Security.framework control 2 (google.com under UNCONSTRAINED root): verification successful, as required")

	if verify(fx.rootPEM, fx.forgePEM, "google.com") {
		t.Fatal("SECURITY: Apple's Security.framework ACCEPTED a google.com leaf signed by the name-constrained root — the constraint is not enforced on this platform")
	}
	t.Log("Security.framework REJECTED google.com under the constrained root; the only difference from control 2 is permittedSubtrees")
}
