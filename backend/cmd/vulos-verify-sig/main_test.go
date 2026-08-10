package main

// Integration tests for vulos-verify-sig: the initramfs roothash-authenticity
// gate.  The binary is built once and invoked with real files, so the exit codes
// here are the ones a boot sees.
//
// THE FIXTURE BUILDS THE REAL TWO-TIER PKI.  That is the whole point of this
// file's shape.  The version it replaces generated one keypair, called it the
// anchor, and signed the RAW BYTES of the roothash file with it — a shape
// `cmd/sign` cannot emit and no other verifier in this repository accepts.  Every
// test passed, and the command was unreachable in production: exactly the defect
// c1fba1b0 and 7d1101af describe, where "a fixture that defines its own
// signature shape cannot catch this class of bug".
//
// So the chain here is built the way a release is built:
//
//	root key ──signs──> release cert ──authorises──> release key
//	                                                    │ signs
//	                                       canonical(ImagePayload)
//
// and the regressions are pinned directly: an anchor-key signature is rejected,
// a raw-bytes signature is rejected, a cert chaining to another root is
// rejected, and a payload that names a different root hash or size is rejected
// even though its signature is perfectly valid.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	verify "vulos/backend/cmd/verify"
	"vulos/backend/services/signing"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "vulos-verify-sig")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// harness lays out on disk exactly what a slot directory holds at boot: a
// squashfs, the roothash file veritysetup is handed, the signed bundle, plus the
// anchor and release cert the initramfs carries from SEED-01.
type harness struct {
	dir string

	rootPub  ed25519.PublicKey
	rootPriv ed25519.PrivateKey

	releasePub  ed25519.PublicKey
	releasePriv ed25519.PrivateKey

	anchorPath   string
	certPath     string
	imagePath    string
	roothashPath string
	bundlePath   string

	rootHash string
	size     int64
}

const testRootHash = "3f786850e387550fdab836ed7e6dc881de23001b3f786850e387550fdab836ed"

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()

	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("root keygen: %v", err)
	}
	releasePub, releasePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("release keygen: %v", err)
	}

	h := &harness{
		dir:          dir,
		rootPub:      rootPub,
		rootPriv:     rootPriv,
		releasePub:   releasePub,
		releasePriv:  releasePriv,
		anchorPath:   filepath.Join(dir, "trust-anchor.pub"),
		certPath:     filepath.Join(dir, "release-cert.json"),
		imagePath:    filepath.Join(dir, "os-core.squashfs"),
		roothashPath: filepath.Join(dir, "os-core.roothash"),
		bundlePath:   filepath.Join(dir, "os-core.roothash.sig"),
		rootHash:     testRootHash,
	}

	// The pinned anchor, in the exact wire format SEED-01 bakes.
	if err := os.WriteFile(h.anchorPath, []byte(signing.EncodeAnchor(rootPub)), 0o644); err != nil {
		t.Fatalf("write anchor: %v", err)
	}
	// A real root-signed release cert.
	h.writeCert(t, rootPriv, releasePub, time.Now().Add(365*24*time.Hour), 0)

	// The image, and the roothash file that describes it.
	body := []byte(strings.Repeat("squashfs-bytes", 64))
	if err := os.WriteFile(h.imagePath, body, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	h.size = int64(len(body))
	if err := os.WriteFile(h.roothashPath, []byte(h.rootHash+"\n"), 0o644); err != nil {
		t.Fatalf("write roothash: %v", err)
	}
	return h
}

func (h *harness) writeCert(t *testing.T, rootPriv ed25519.PrivateKey, releasePub ed25519.PublicKey, notAfter time.Time, minEpoch int64) {
	t.Helper()
	cert, err := signing.IssueReleaseCert(rootPriv, releasePub, "test-release", notAfter, minEpoch)
	if err != nil {
		t.Fatalf("issue cert: %v", err)
	}
	data, err := json.Marshal(cert)
	if err != nil {
		t.Fatalf("marshal cert: %v", err)
	}
	if err := os.WriteFile(h.certPath, data, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
}

// payload is the ImagePayload that honestly describes this harness's artifacts.
func (h *harness) payload() verify.ImagePayload {
	return verify.ImagePayload{
		Path:       "os-core.squashfs",
		RootHash:   h.rootHash,
		Size:       h.size,
		MinEpoch:   0,
		ReleasedAt: "2026-08-10T00:00:00Z",
	}
}

// writeBundle produces the signed-roothash bundle the way
// scripts/verity/sign-roothash.sh does: canonical(ImagePayload) signed by
// signer, plus the payload document carried as a keyed line.
func (h *harness) writeBundle(t *testing.T, signer ed25519.PrivateKey, payload verify.ImagePayload) {
	t.Helper()
	canonical, err := signing.Canonical(payload)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	h.writeBundleOverBytes(t, signer, payload, canonical)
}

// writeBundleOverBytes signs arbitrary bytes while still shipping payload, so a
// test can pin "signed the wrong thing" as distinct from "signed with the wrong
// key".
func (h *harness) writeBundleOverBytes(t *testing.T, signer ed25519.PrivateKey, payload verify.ImagePayload, signed []byte) {
	t.Helper()
	sigFile, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "test-release",
		SigBytes:  signing.Sign(signer, signed),
	})
	if err != nil {
		t.Fatalf("marshal sig: %v", err)
	}
	doc, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	out := string(sigFile) + "payload: " + base64.StdEncoding.EncodeToString(doc) + "\n"
	if err := os.WriteFile(h.bundlePath, []byte(out), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
}

func (h *harness) args() []string {
	return []string{
		"-anchor", h.anchorPath,
		"-cert", h.certPath,
		"-roothash", h.roothashPath,
		"-bundle", h.bundlePath,
		"-image", h.imagePath,
	}
}

func runBin(t *testing.T, bin string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(out)
	}
	t.Fatalf("run: %v", err)
	return -1, ""
}

// ── The happy path ───────────────────────────────────────────────────────────

func TestVerifySig_ReleaseSignedBundleExitsZero(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	h.writeBundle(t, h.releasePriv, h.payload())
	if code, out := runBin(t, bin, h.args()...); code != 0 {
		t.Fatalf("a properly release-signed bundle must exit 0, got %d\n%s", code, out)
	}
}

// ── The regressions this command was rewritten to close ──────────────────────

// The old command verified a signature made by the ANCHOR key.  cmd/sign has no
// subcommand that signs an image with the root key, and never had one.
func TestVerifySig_AnchorKeySignatureRejected(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	h.writeBundle(t, h.rootPriv, h.payload())
	code, out := runBin(t, bin, h.args()...)
	if code != 1 {
		t.Fatalf("a ROOT-key signature must be rejected (the release key signs images), got %d\n%s", code, out)
	}
}

// ...and it verified it over the RAW BYTES of the roothash file, which is not
// the surface any signer in this repository produces.
func TestVerifySig_RawBytesSignatureRejected(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	raw, err := os.ReadFile(h.roothashPath)
	if err != nil {
		t.Fatalf("read roothash: %v", err)
	}
	h.writeBundleOverBytes(t, h.releasePriv, h.payload(), raw)
	code, out := runBin(t, bin, h.args()...)
	if code != 1 {
		t.Fatalf("a raw-file-bytes signature must be rejected (signed surface is canonical(ImagePayload)), got %d\n%s", code, out)
	}
}

func TestVerifySig_CertFromAnotherRootRejected(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	_, otherRootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	// Same release key, cert signed by a root the device does not pin.
	h.writeCert(t, otherRootPriv, h.releasePub, time.Now().Add(24*time.Hour), 0)
	h.writeBundle(t, h.releasePriv, h.payload())
	code, out := runBin(t, bin, h.args()...)
	if code != 1 {
		t.Fatalf("a cert chaining to any root but the pinned one must be rejected, got %d\n%s", code, out)
	}
}

func TestVerifySig_ExpiredCertRejected(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	h.writeCert(t, h.rootPriv, h.releasePub, time.Now().Add(-1*time.Hour), 0)
	h.writeBundle(t, h.releasePriv, h.payload())
	code, out := runBin(t, bin, h.args()...)
	if code != 1 {
		t.Fatalf("an expired release cert must be rejected, got %d\n%s", code, out)
	}
}

func TestVerifySig_CertBelowEpochFloorRejected(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	h.writeCert(t, h.rootPriv, h.releasePub, time.Now().Add(24*time.Hour), 3)
	h.writeBundle(t, h.releasePriv, h.payload())
	args := append(h.args(), "-epoch-floor", "9")
	code, out := runBin(t, bin, args...)
	if code != 1 {
		t.Fatalf("a retired release cert (min_epoch below the floor) must be rejected, got %d\n%s", code, out)
	}
}

// ── The BINDING: a valid signature over the wrong subject is not a pass ──────

// This is the gap the whole change exists to close.  The signature is genuine
// and the chain is intact; it simply describes a different image.  Accepting it
// would let an attacker pair any release-signed bundle with any roothash.
func TestVerifySig_PayloadNamingAnotherRootHashRejected(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	p := h.payload()
	p.RootHash = strings.Repeat("a", 64)
	h.writeBundle(t, h.releasePriv, p)
	code, out := runBin(t, bin, h.args()...)
	if code != 1 {
		t.Fatalf("a validly signed payload naming a DIFFERENT root hash must be rejected, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "root hash") {
		t.Fatalf("the failure must name the root-hash binding, got:\n%s", out)
	}
}

func TestVerifySig_PayloadNamingAnotherSizeRejected(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	p := h.payload()
	p.Size = h.size + 1
	h.writeBundle(t, h.releasePriv, p)
	code, out := runBin(t, bin, h.args()...)
	if code != 1 {
		t.Fatalf("a validly signed payload naming a DIFFERENT size must be rejected, got %d\n%s", code, out)
	}
}

// Tampering with the roothash file after signing must fail, because that file is
// what veritysetup is handed.
func TestVerifySig_TamperedRootHashFileRejected(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	h.writeBundle(t, h.releasePriv, h.payload())
	if err := os.WriteFile(h.roothashPath, []byte(strings.Repeat("b", 64)+"\n"), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if code, out := runBin(t, bin, h.args()...); code != 1 {
		t.Fatalf("a substituted roothash file must be rejected, got %d\n%s", code, out)
	}
}

// ── Malformed / missing inputs all fail closed ───────────────────────────────

// A bare `cmd/sign sign-image` output is a real signature with no manifest.
// There is no honest way to check it, and reconstructing the payload from the
// local artifacts would make it verify against whatever an attacker left there.
func TestVerifySig_BundleWithoutPayloadLineRejected(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	canonical, err := signing.Canonical(h.payload())
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	sigFile, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID, KeyID: "test-release",
		SigBytes: signing.Sign(h.releasePriv, canonical),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(h.bundlePath, sigFile, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	code, out := runBin(t, bin, h.args()...)
	if code != 1 {
		t.Fatalf("a bundle with no payload line must be rejected, got %d\n%s", code, out)
	}
}

// The mutation the smoke harness performs on a real disk, in miniature: flip a
// byte of the base64 signature and the boot must not proceed.
func TestVerifySig_CorruptedSignatureRejected(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	h.writeBundle(t, h.releasePriv, h.payload())
	data, err := os.ReadFile(h.bundlePath)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "sig: ") {
			body := l[len("sig: "):]
			swap := map[byte]byte{'A': 'B'}
			b := []byte(body)
			if r, ok := swap[b[0]]; ok {
				b[0] = r
			} else {
				b[0] = 'A'
			}
			l = "sig: " + string(b)
		}
		lines = append(lines, l)
	}
	if err := os.WriteFile(h.bundlePath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if code, out := runBin(t, bin, h.args()...); code != 1 {
		t.Fatalf("a corrupted signature must exit 1, got %d\n%s", code, out)
	}
}

func TestVerifySig_MissingInputsExitOne(t *testing.T) {
	bin := buildBinary(t)
	for _, tc := range []struct {
		name    string
		mutate  func(h *harness) []string
		wantOne bool
	}{
		{"missing bundle", func(h *harness) []string {
			os.Remove(h.bundlePath)
			return h.args()
		}, true},
		{"missing anchor", func(h *harness) []string {
			os.Remove(h.anchorPath)
			return h.args()
		}, true},
		{"missing cert", func(h *harness) []string {
			os.Remove(h.certPath)
			return h.args()
		}, true},
		{"missing roothash file", func(h *harness) []string {
			os.Remove(h.roothashPath)
			return h.args()
		}, true},
		{"missing image", func(h *harness) []string {
			os.Remove(h.imagePath)
			return h.args()
		}, true},
		{"non-hex roothash file", func(h *harness) []string {
			os.WriteFile(h.roothashPath, []byte("not-a-root-hash\n"), 0o644)
			return h.args()
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.writeBundle(t, h.releasePriv, h.payload())
			args := tc.mutate(h)
			if code, out := runBin(t, bin, args...); code != 1 {
				t.Fatalf("%s must exit 1 (fail closed), got %d\n%s", tc.name, code, out)
			}
		})
	}
}

func TestVerifySig_BadArgsExitsTwo(t *testing.T) {
	bin := buildBinary(t)
	if code, _ := runBin(t, bin, "-roothash", "only-one"); code != 2 {
		t.Fatalf("bad args must exit 2, got %d", code)
	}
}

// ── The ceremony script emits what this command reads ────────────────────────
//
// Without this, the signer and the verifier are two independent implementations
// of the bundle format that agree only in the test fixture — the exact way three
// verifiers came to disagree about what a signature covers.  This runs the real
// scripts/verity/sign-roothash.sh and hands its output to the real binary.
func TestVerifySig_AcceptsWhatSignRoothashShProduces(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	bin := buildBinary(t)
	h := newHarness(t)

	// The ceremony reads the private key in cmd/sign's MarshalPrivateKey format.
	privPath := filepath.Join(h.dir, "release.priv.json")
	privDoc := fmt.Sprintf(`{"algorithm":"ed25519","private_key":"%x","public_key":"%x"}`,
		h.releasePriv, h.releasePub)
	if err := os.WriteFile(privPath, []byte(privDoc), 0o600); err != nil {
		t.Fatalf("write priv: %v", err)
	}

	script := filepath.Join("..", "..", "..", "scripts", "verity", "sign-roothash.sh")
	cmd := exec.Command("sh", script,
		h.imagePath, h.roothashPath, privPath, h.certPath, h.bundlePath, "ci-test", "os-core.squashfs")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sign-roothash.sh failed: %v\n%s", err, out)
	}

	if code, out := runBin(t, bin, h.args()...); code != 0 {
		t.Fatalf("vulos-verify-sig rejected what sign-roothash.sh produced (exit %d)\n%s", code, out)
	}
}
