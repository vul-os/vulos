package verify

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/signing"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// genKeyPair generates a fresh Ed25519 keypair; fatals on error.
func genKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// writeTempAnchor writes a Base64-encoded Ed25519 public key to a temp file
// in the standard trust-anchor format (SEED-01 / signing.LoadAnchor).
func writeTempAnchor(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "trust-anchor.pub")
	encoded := base64.StdEncoding.EncodeToString(pub)
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0444); err != nil {
		t.Fatalf("writeTempAnchor: %v", err)
	}
	return path
}

// issueTestCert creates a ReleaseCert signed by rootPriv for releasePub,
// valid for 24 hours from now.
func issueTestCert(
	t *testing.T,
	rootPriv ed25519.PrivateKey,
	releasePub ed25519.PublicKey,
	minEpoch int64,
	notAfter time.Time,
) ReleaseCert {
	t.Helper()
	body := certBody{
		ReleasePubKey: hex.EncodeToString(releasePub),
		KeyID:         "test-release-key",
		NotAfter:      notAfter.UTC().Format(time.RFC3339),
		MinEpoch:      minEpoch,
	}
	canonical, err := signing.Canonical(body)
	if err != nil {
		t.Fatalf("issueTestCert: canonical: %v", err)
	}
	sigBytes := signing.Sign(rootPriv, canonical)
	return ReleaseCert{
		ReleasePubKey: body.ReleasePubKey,
		KeyID:         body.KeyID,
		NotAfter:      body.NotAfter,
		MinEpoch:      body.MinEpoch,
		RootSig:       base64.StdEncoding.EncodeToString(sigBytes),
	}
}

// writeTempCert marshals cert to JSON and writes it to a temp file.
func writeTempCert(t *testing.T, cert ReleaseCert) string {
	t.Helper()
	data, err := json.Marshal(cert)
	if err != nil {
		t.Fatalf("writeTempCert: marshal: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "release-cert.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("writeTempCert: write: %v", err)
	}
	return path
}

// makeImageSig creates a detached .sig for an ImagePayload signed by releasePriv
// and writes it to a temp file alongside a dummy squashfs file.
// Returns (squashfsPath, sigPath).
func makeImageSig(
	t *testing.T,
	releasePriv ed25519.PrivateKey,
	payload ImagePayload,
) (squashfsPath, sigPath string) {
	t.Helper()
	dir := t.TempDir()

	// Write a stub squashfs file (content irrelevant; sig is over ImagePayload).
	squashfsPath = filepath.Join(dir, "os-core.squashfs")
	if err := os.WriteFile(squashfsPath, []byte("stub-squashfs-data"), 0644); err != nil {
		t.Fatalf("makeImageSig: write squashfs: %v", err)
	}

	canonical, err := signing.Canonical(payload)
	if err != nil {
		t.Fatalf("makeImageSig: canonical: %v", err)
	}
	sigBytes := signing.Sign(releasePriv, canonical)
	sig := signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "test-release-key",
		SigBytes:  sigBytes,
	}
	sigData, err := signing.MarshalSig(sig)
	if err != nil {
		t.Fatalf("makeImageSig: MarshalSig: %v", err)
	}

	sigPath = squashfsPath + ".sig"
	if err := os.WriteFile(sigPath, sigData, 0644); err != nil {
		t.Fatalf("makeImageSig: write sig: %v", err)
	}
	return squashfsPath, sigPath
}

// makeStageFile writes a dummy stage file and its detached .sig (signed over
// raw file bytes by releasePriv).  Returns (stagePath, sigPath).
func makeStageFile(
	t *testing.T,
	releasePriv ed25519.PrivateKey,
	content []byte,
) (stagePath, sigPath string) {
	t.Helper()
	dir := t.TempDir()
	stagePath = filepath.Join(dir, "stage-file")
	if err := os.WriteFile(stagePath, content, 0644); err != nil {
		t.Fatalf("makeStageFile: write: %v", err)
	}

	sigBytes := signing.Sign(releasePriv, content)
	sig := signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "test-release-key",
		SigBytes:  sigBytes,
	}
	sigData, err := signing.MarshalSig(sig)
	if err != nil {
		t.Fatalf("makeStageFile: MarshalSig: %v", err)
	}
	sigPath = stagePath + ".sig"
	if err := os.WriteFile(sigPath, sigData, 0644); err != nil {
		t.Fatalf("makeStageFile: write sig: %v", err)
	}
	return stagePath, sigPath
}

// ─── ValidateReleaseCert tests ────────────────────────────────────────────────

// VERITY02_TestValidateReleaseCert_ValidChain verifies that a correctly issued
// cert (valid root sig, not expired, non-negative epoch) passes validation.
func VERITY02_TestValidateReleaseCert_ValidChain(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, _ := genKeyPair(t)

	cert := issueTestCert(t, rootPriv, releasePub, 1, time.Now().Add(24*time.Hour))

	if err := ValidateReleaseCert(rootPub, cert); err != nil {
		t.Fatalf("ValidateReleaseCert: unexpected error for valid cert: %v", err)
	}
}

// VERITY02_TestValidateReleaseCert_BrokenSig verifies that a cert with a
// tampered root_sig is rejected.
func VERITY02_TestValidateReleaseCert_BrokenSig(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, _ := genKeyPair(t)

	cert := issueTestCert(t, rootPriv, releasePub, 1, time.Now().Add(24*time.Hour))

	// Tamper: replace the root_sig with a signature from a different key.
	_, wrongPriv := genKeyPair(t)
	body := bodyOf(cert)
	canonical, _ := signing.Canonical(body)
	wrongSig := signing.Sign(wrongPriv, canonical)
	cert.RootSig = base64.StdEncoding.EncodeToString(wrongSig)

	if err := ValidateReleaseCert(rootPub, cert); err == nil {
		t.Fatal("ValidateReleaseCert: expected error for tampered root_sig, got nil")
	}
}

// VERITY02_TestValidateReleaseCert_Expired verifies that an expired cert is
// rejected.
func VERITY02_TestValidateReleaseCert_Expired(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, _ := genKeyPair(t)

	// Set notAfter to one hour in the past.
	cert := issueTestCert(t, rootPriv, releasePub, 1, time.Now().Add(-1*time.Hour))

	if err := ValidateReleaseCert(rootPub, cert); err == nil {
		t.Fatal("ValidateReleaseCert: expected error for expired cert, got nil")
	}
}

// VERITY02_TestValidateReleaseCert_WrongRootKey verifies that a cert signed by
// one root key is rejected when validated against a different root key.
func VERITY02_TestValidateReleaseCert_WrongRootKey(t *testing.T) {
	wrongRootPub, _ := genKeyPair(t) // not the key that signed the cert
	_, rootPriv := genKeyPair(t)     // this key signed the cert
	releasePub, _ := genKeyPair(t)

	cert := issueTestCert(t, rootPriv, releasePub, 1, time.Now().Add(24*time.Hour))

	if err := ValidateReleaseCert(wrongRootPub, cert); err == nil {
		t.Fatal("ValidateReleaseCert: expected error for wrong root key, got nil")
	}
}

// ─── VerifyStageFile tests ────────────────────────────────────────────────────

// VERITY02_TestVerifyStageFile_ValidSig verifies that a correctly signed stage
// file passes verification.
func VERITY02_TestVerifyStageFile_ValidSig(t *testing.T) {
	releasePub, releasePriv := genKeyPair(t)
	content := []byte("shim-binary-data")
	stagePath, _ := makeStageFile(t, releasePriv, content)

	if err := VerifyStageFile(releasePub, stagePath); err != nil {
		t.Fatalf("VerifyStageFile: unexpected error: %v", err)
	}
}

// VERITY02_TestVerifyStageFile_BrokenSig verifies that a stage file with a
// corrupted signature is rejected (halts boot).
func VERITY02_TestVerifyStageFile_BrokenSig(t *testing.T) {
	releasePub, releasePriv := genKeyPair(t)
	content := []byte("shim-binary-data")
	stagePath, sigPath := makeStageFile(t, releasePriv, content)

	// Overwrite sig with a sig from a different key.
	_, wrongPriv := genKeyPair(t)
	wrongSigBytes := signing.Sign(wrongPriv, content)
	wrongSig := signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "wrong-key",
		SigBytes:  wrongSigBytes,
	}
	wrongSigData, _ := signing.MarshalSig(wrongSig)
	if err := os.WriteFile(sigPath, wrongSigData, 0644); err != nil {
		t.Fatalf("write tampered sig: %v", err)
	}

	if err := VerifyStageFile(releasePub, stagePath); err == nil {
		t.Fatal("VerifyStageFile: expected error for broken sig, got nil")
	}
}

// VERITY02_TestVerifyStageFile_TamperedContent verifies that a stage file whose
// content has been modified after signing is rejected.
func VERITY02_TestVerifyStageFile_TamperedContent(t *testing.T) {
	releasePub, releasePriv := genKeyPair(t)
	content := []byte("kernel-image-data")
	stagePath, _ := makeStageFile(t, releasePriv, content)

	// Tamper the stage file after signing.
	tampered := append([]byte{}, content...)
	tampered[0] ^= 0xFF
	if err := os.WriteFile(stagePath, tampered, 0644); err != nil {
		t.Fatalf("write tampered stage: %v", err)
	}

	if err := VerifyStageFile(releasePub, stagePath); err == nil {
		t.Fatal("VerifyStageFile: expected error for tampered content, got nil")
	}
}

// VERITY02_TestVerifyStageFile_MissingSig verifies that a missing .sig file
// causes a rejection (fail closed).
func VERITY02_TestVerifyStageFile_MissingSig(t *testing.T) {
	releasePub, releasePriv := genKeyPair(t)
	content := []byte("initramfs-data")
	stagePath, sigPath := makeStageFile(t, releasePriv, content)

	// Remove the sig file.
	if err := os.Remove(sigPath); err != nil {
		t.Fatalf("remove sig: %v", err)
	}

	if err := VerifyStageFile(releasePub, stagePath); err == nil {
		t.Fatal("VerifyStageFile: expected error for missing sig, got nil")
	}
}

// ─── VerifySquashfsBeforePivot tests ─────────────────────────────────────────

// VERITY02_TestVerifySquashfsBeforePivot_ValidChain verifies the full pre-pivot
// verification path: anchor → cert validation → epoch → image sig → hash check.
func VERITY02_TestVerifySquashfsBeforePivot_ValidChain(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, releasePriv := genKeyPair(t)

	anchorPath := writeTempAnchor(t, rootPub)
	cert := issueTestCert(t, rootPriv, releasePub, 1, time.Now().Add(24*time.Hour))
	certPath := writeTempCert(t, cert)

	payload := ImagePayload{
		Path:       "os/v08/os-core.squashfs",
		RootHash:   "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab",
		Size:       12345,
		MinEpoch:   1,
		ReleasedAt: "2026-05-20T09:00:00Z",
	}
	squashfsPath, sigPath := makeImageSig(t, releasePriv, payload)

	cfg := SquashfsVerifyConfig{
		AnchorPath:         anchorPath,
		CertPath:           certPath,
		SquashfsPath:       squashfsPath,
		SigPath:            sigPath,
		BindRootHash:       bindingTo(payload.RootHash),
		ImagePayloadForSig: payload,
		EpochFloor:         0,
	}

	if err := VerifySquashfsBeforePivot(cfg); err != nil {
		t.Fatalf("VerifySquashfsBeforePivot: unexpected error: %v", err)
	}
}

// VERITY02_TestVerifySquashfsBeforePivot_BrokenImageSig verifies that a
// squashfs image with a corrupted .sig halts boot.
func VERITY02_TestVerifySquashfsBeforePivot_BrokenImageSig(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, releasePriv := genKeyPair(t)

	anchorPath := writeTempAnchor(t, rootPub)
	cert := issueTestCert(t, rootPriv, releasePub, 1, time.Now().Add(24*time.Hour))
	certPath := writeTempCert(t, cert)

	payload := ImagePayload{
		Path:       "os/v08/os-core.squashfs",
		RootHash:   "deadbeef000000000000000000000000deadbeef000000000000000000000000ab",
		Size:       99999,
		MinEpoch:   1,
		ReleasedAt: "2026-05-20T09:00:00Z",
	}
	squashfsPath, sigPath := makeImageSig(t, releasePriv, payload)

	// Overwrite the .sig with a signature from a different (wrong) key.
	_, wrongPriv := genKeyPair(t)
	canonical, _ := signing.Canonical(payload)
	wrongSigBytes := signing.Sign(wrongPriv, canonical)
	wrongSig := signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "wrong-key",
		SigBytes:  wrongSigBytes,
	}
	wrongSigData, _ := signing.MarshalSig(wrongSig)
	if err := os.WriteFile(sigPath, wrongSigData, 0644); err != nil {
		t.Fatalf("write broken sig: %v", err)
	}

	cfg := SquashfsVerifyConfig{
		AnchorPath:         anchorPath,
		CertPath:           certPath,
		SquashfsPath:       squashfsPath,
		SigPath:            sigPath,
		BindRootHash:       bindingTo(payload.RootHash),
		ImagePayloadForSig: payload,
		EpochFloor:         0,
	}

	if err := VerifySquashfsBeforePivot(cfg); err == nil {
		t.Fatal("VerifySquashfsBeforePivot: expected error for broken image sig, got nil")
	}
}

// VERITY02_TestVerifySquashfsBeforePivot_HashMismatch verifies that a hash
// mismatch between the manifest and the signed ImagePayload halts boot.
func VERITY02_TestVerifySquashfsBeforePivot_HashMismatch(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, releasePriv := genKeyPair(t)

	anchorPath := writeTempAnchor(t, rootPub)
	cert := issueTestCert(t, rootPriv, releasePub, 1, time.Now().Add(24*time.Hour))
	certPath := writeTempCert(t, cert)

	payload := ImagePayload{
		Path:       "os/v08/os-core.squashfs",
		RootHash:   "aaaa0000000000000000000000000000aaaa0000000000000000000000000000ab",
		Size:       12345,
		MinEpoch:   1,
		ReleasedAt: "2026-05-20T09:00:00Z",
	}
	squashfsPath, sigPath := makeImageSig(t, releasePriv, payload)

	cfg := SquashfsVerifyConfig{
		AnchorPath:   anchorPath,
		CertPath:     certPath,
		SquashfsPath: squashfsPath,
		SigPath:      sigPath,
		// Manifest pins a DIFFERENT hash than what the sig covers.
		BindRootHash:       bindingTo("bbbb0000000000000000000000000000bbbb0000000000000000000000000000ab"),
		ImagePayloadForSig: payload,
		EpochFloor:         0,
	}

	if err := VerifySquashfsBeforePivot(cfg); err == nil {
		t.Fatal("VerifySquashfsBeforePivot: expected error for hash mismatch, got nil")
	}
}

// VERITY02_TestVerifySquashfsBeforePivot_EpochBelowFloor verifies that a cert
// whose MinEpoch is below the device's epoch floor halts boot.
func VERITY02_TestVerifySquashfsBeforePivot_EpochBelowFloor(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, releasePriv := genKeyPair(t)

	anchorPath := writeTempAnchor(t, rootPub)
	// Cert has minEpoch=1 but device floor is 5.
	cert := issueTestCert(t, rootPriv, releasePub, 1, time.Now().Add(24*time.Hour))
	certPath := writeTempCert(t, cert)

	payload := ImagePayload{
		Path:       "os/v08/os-core.squashfs",
		RootHash:   "cccc0000000000000000000000000000cccc0000000000000000000000000000ab",
		Size:       12345,
		MinEpoch:   1,
		ReleasedAt: "2026-05-20T09:00:00Z",
	}
	squashfsPath, sigPath := makeImageSig(t, releasePriv, payload)

	cfg := SquashfsVerifyConfig{
		AnchorPath:         anchorPath,
		CertPath:           certPath,
		SquashfsPath:       squashfsPath,
		SigPath:            sigPath,
		BindRootHash:       bindingTo(payload.RootHash),
		ImagePayloadForSig: payload,
		EpochFloor:         5, // device floor > cert.MinEpoch
	}

	if err := VerifySquashfsBeforePivot(cfg); err == nil {
		t.Fatal("VerifySquashfsBeforePivot: expected error for epoch below floor, got nil")
	}
}

// VERITY02_TestVerifySquashfsBeforePivot_BrokenCertSig verifies that a release
// cert with an invalid root signature halts boot even if the image sig is valid.
func VERITY02_TestVerifySquashfsBeforePivot_BrokenCertSig(t *testing.T) {
	rootPub, _ := genKeyPair(t)       // real root anchor
	_, wrongRootPriv := genKeyPair(t) // cert signed by wrong root
	releasePub, releasePriv := genKeyPair(t)

	anchorPath := writeTempAnchor(t, rootPub)
	// Issue cert with wrongRootPriv — root sig won't verify against rootPub.
	cert := issueTestCert(t, wrongRootPriv, releasePub, 1, time.Now().Add(24*time.Hour))
	certPath := writeTempCert(t, cert)

	payload := ImagePayload{
		Path:       "os/v08/os-core.squashfs",
		RootHash:   "dddd0000000000000000000000000000dddd0000000000000000000000000000ab",
		Size:       12345,
		MinEpoch:   1,
		ReleasedAt: "2026-05-20T09:00:00Z",
	}
	squashfsPath, sigPath := makeImageSig(t, releasePriv, payload)

	cfg := SquashfsVerifyConfig{
		AnchorPath:         anchorPath,
		CertPath:           certPath,
		SquashfsPath:       squashfsPath,
		SigPath:            sigPath,
		BindRootHash:       bindingTo(payload.RootHash),
		ImagePayloadForSig: payload,
		EpochFloor:         0,
	}

	if err := VerifySquashfsBeforePivot(cfg); err == nil {
		t.Fatal("VerifySquashfsBeforePivot: expected error for broken cert sig, got nil")
	}
}

// VERITY02_TestVerifySquashfsBeforePivot_MissingAnchor verifies that a missing
// trust anchor file halts boot.
func VERITY02_TestVerifySquashfsBeforePivot_MissingAnchor(t *testing.T) {
	_, rootPriv := genKeyPair(t)
	releasePub, releasePriv := genKeyPair(t)

	cert := issueTestCert(t, rootPriv, releasePub, 1, time.Now().Add(24*time.Hour))
	certPath := writeTempCert(t, cert)

	payload := ImagePayload{
		Path:       "os/v08/os-core.squashfs",
		RootHash:   "eeee0000000000000000000000000000eeee0000000000000000000000000000ab",
		Size:       12345,
		MinEpoch:   1,
		ReleasedAt: "2026-05-20T09:00:00Z",
	}
	squashfsPath, sigPath := makeImageSig(t, releasePriv, payload)

	cfg := SquashfsVerifyConfig{
		AnchorPath:         "/nonexistent/trust-anchor.pub",
		CertPath:           certPath,
		SquashfsPath:       squashfsPath,
		SigPath:            sigPath,
		BindRootHash:       bindingTo(payload.RootHash),
		ImagePayloadForSig: payload,
		EpochFloor:         0,
	}

	if err := VerifySquashfsBeforePivot(cfg); err == nil {
		t.Fatal("VerifySquashfsBeforePivot: expected error for missing anchor, got nil")
	}
}

// ─── Standard test entry points ──────────────────────────────────────────────

func TestValidateReleaseCert_ValidChain(t *testing.T) {
	VERITY02_TestValidateReleaseCert_ValidChain(t)
}
func TestValidateReleaseCert_BrokenSig(t *testing.T) {
	VERITY02_TestValidateReleaseCert_BrokenSig(t)
}
func TestValidateReleaseCert_Expired(t *testing.T) {
	VERITY02_TestValidateReleaseCert_Expired(t)
}
func TestValidateReleaseCert_WrongRootKey(t *testing.T) {
	VERITY02_TestValidateReleaseCert_WrongRootKey(t)
}
func TestVerifyStageFile_ValidSig(t *testing.T) {
	VERITY02_TestVerifyStageFile_ValidSig(t)
}
func TestVerifyStageFile_BrokenSig(t *testing.T) {
	VERITY02_TestVerifyStageFile_BrokenSig(t)
}
func TestVerifyStageFile_TamperedContent(t *testing.T) {
	VERITY02_TestVerifyStageFile_TamperedContent(t)
}
func TestVerifyStageFile_MissingSig(t *testing.T) {
	VERITY02_TestVerifyStageFile_MissingSig(t)
}
func TestVerifySquashfsBeforePivot_ValidChain(t *testing.T) {
	VERITY02_TestVerifySquashfsBeforePivot_ValidChain(t)
}
func TestVerifySquashfsBeforePivot_BrokenImageSig(t *testing.T) {
	VERITY02_TestVerifySquashfsBeforePivot_BrokenImageSig(t)
}
func TestVerifySquashfsBeforePivot_HashMismatch(t *testing.T) {
	VERITY02_TestVerifySquashfsBeforePivot_HashMismatch(t)
}
func TestVerifySquashfsBeforePivot_EpochBelowFloor(t *testing.T) {
	VERITY02_TestVerifySquashfsBeforePivot_EpochBelowFloor(t)
}
func TestVerifySquashfsBeforePivot_BrokenCertSig(t *testing.T) {
	VERITY02_TestVerifySquashfsBeforePivot_BrokenCertSig(t)
}
func TestVerifySquashfsBeforePivot_MissingAnchor(t *testing.T) {
	VERITY02_TestVerifySquashfsBeforePivot_MissingAnchor(t)
}

// ─── Epoch revocation: the floor must RISE ───────────────────────────────────
//
// Until SquashfsVerifyConfig carried an EpochStore, this gate only ever CHECKED
// the floor.  Nothing raised it, so a device sat at 0 for life, every min_epoch
// >= 0 passed, and issuing a release cert with a higher -min-epoch revoked
// nothing on the pre-pivot path.

// epochFixture builds a complete, valid pre-pivot input set at the given cert
// epoch, sharing one root key so successive certs chain to the same anchor.
type epochFixture struct {
	anchorPath string
	rootPriv   ed25519.PrivateKey
	releasePub ed25519.PublicKey
	priv       ed25519.PrivateKey
	epochPath  string
}

func newEpochFixture(t *testing.T) *epochFixture {
	t.Helper()
	rootPub, rootPriv := genKeyPair(t)
	releasePub, releasePriv := genKeyPair(t)
	return &epochFixture{
		anchorPath: writeTempAnchor(t, rootPub),
		rootPriv:   rootPriv,
		releasePub: releasePub,
		priv:       releasePriv,
		epochPath:  filepath.Join(t.TempDir(), "epoch-floor.json"),
	}
}

// cfgAt returns a config whose cert (and manifest payload) carry certEpoch, with
// a fresh EpochStore opened on the fixture's persistent path.
func (f *epochFixture) cfgAt(t *testing.T, certEpoch int64) SquashfsVerifyConfig {
	t.Helper()
	cert := issueTestCert(t, f.rootPriv, f.releasePub, certEpoch, time.Now().Add(24*time.Hour))
	certPath := writeTempCert(t, cert)

	payload := ImagePayload{
		Path:       "os/v08/os-core.squashfs",
		RootHash:   "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab",
		Size:       12345,
		MinEpoch:   certEpoch,
		ReleasedAt: "2026-05-20T09:00:00Z",
	}
	squashfsPath, sigPath := makeImageSig(t, f.priv, payload)

	es, err := signing.NewEpochStore(f.epochPath)
	if err != nil {
		t.Fatalf("open epoch store: %v", err)
	}
	return SquashfsVerifyConfig{
		AnchorPath:         f.anchorPath,
		CertPath:           certPath,
		SquashfsPath:       squashfsPath,
		SigPath:            sigPath,
		BindRootHash:       bindingTo(payload.RootHash),
		ImagePayloadForSig: payload,
		EpochStore:         es,
	}
}

// persistedFloor re-reads the floor FROM DISK, so these tests measure what the
// NEXT boot would see rather than an in-memory value.
func (f *epochFixture) persistedFloor(t *testing.T) int64 {
	t.Helper()
	es, err := signing.NewEpochStore(f.epochPath)
	if err != nil {
		t.Fatalf("re-open epoch store: %v", err)
	}
	return es.Current()
}

func TestVerifySquashfsBeforePivot_RaisesEpochFloorFromReleaseCert(t *testing.T) {
	f := newEpochFixture(t)
	if before := f.persistedFloor(t); before != 0 {
		t.Fatalf("fixture should start at floor 0, got %d", before)
	}

	if err := VerifySquashfsBeforePivot(f.cfgAt(t, 7)); err != nil {
		t.Fatalf("a valid chain at epoch 7 should verify: %v", err)
	}
	if got := f.persistedFloor(t); got != 7 {
		t.Fatalf("the root-signed cert's min_epoch 7 must be persisted as the new floor, got %d", got)
	}
}

// THE revocation test on this path: a device that has booted epoch 7 must
// REFUSE a cert at 6.
//
// Everything else about the second input set is valid and self-consistent — the
// cert chains to the same anchor, the image signature covers the payload, and
// the root hashes agree — and the static EpochFloor is left at 0.  So the ONLY
// thing that can refuse it is the floor the first boot raised.  With the raise
// removed the floor stays 0, cert min_epoch 6 clears it, and the retired key
// boots the machine.
func TestVerifySquashfsBeforePivot_RetiredCertAfterRaise_FailsClosed(t *testing.T) {
	f := newEpochFixture(t)
	if err := VerifySquashfsBeforePivot(f.cfgAt(t, 7)); err != nil {
		t.Fatalf("first boot at epoch 7 should verify: %v", err)
	}

	err := VerifySquashfsBeforePivot(f.cfgAt(t, 6))
	if err == nil {
		t.Fatal("a cert at epoch 6 must be refused by a device that has accepted 7")
	}
	if !strings.Contains(err.Error(), "below device floor") {
		t.Fatalf("refusal should name the epoch floor, got: %v", err)
	}
}

// The floor never falls: re-presenting the retired cert cannot lower it, and the
// current one still boots afterwards.
func TestVerifySquashfsBeforePivot_FloorSurvivesARetiredCert(t *testing.T) {
	f := newEpochFixture(t)
	if err := VerifySquashfsBeforePivot(f.cfgAt(t, 7)); err != nil {
		t.Fatalf("first boot at epoch 7 should verify: %v", err)
	}
	if err := VerifySquashfsBeforePivot(f.cfgAt(t, 6)); err == nil {
		t.Fatal("expected the retired cert to be refused")
	}
	if got := f.persistedFloor(t); got != 7 {
		t.Fatalf("a refused cert must not move the floor, got %d", got)
	}
	if err := VerifySquashfsBeforePivot(f.cfgAt(t, 7)); err != nil {
		t.Fatalf("the current cert must still boot after a rejected rollback: %v", err)
	}
}

// The static EpochFloor still governs callers that hold no store (backend/
// internal/installer verifies a manifest before writing it to a target disk and
// has no persistent floor of its own to raise).
func TestVerifySquashfsBeforePivot_StaticFloorWithoutStore(t *testing.T) {
	f := newEpochFixture(t)
	cfg := f.cfgAt(t, 1)
	cfg.EpochStore = nil
	cfg.EpochFloor = 5

	if err := VerifySquashfsBeforePivot(cfg); err == nil {
		t.Fatal("a cert at epoch 1 must be refused against a static floor of 5")
	}
}

// ─── Root-hash binding ───────────────────────────────────────────────────────
//
// SquashfsVerifyConfig used to carry an ExpectedRootHash string, and BOTH
// production callers filled it from the same manifest they filled
// ImagePayloadForSig from.  Step 6 therefore compared a value with itself: it
// could not fail, it never touched an image, and it reported a verified root
// hash on every boot.  Only this file ever passed two different values, which is
// exactly why the check looked alive.
//
// The binding is now a function the caller must supply, and these tests use
// doubles that MEASURE something rather than doubles that agree.

// bindingTo simulates a machine whose running root is verified against
// activeHash — the stand-in for `veritysetup status`, which is what cmd/init
// asks the kernel.  It compares; it does not assent.
func bindingTo(activeHash string) RootHashBinder {
	return func(signedRootHash string) error {
		if signedRootHash != activeHash {
			return fmt.Errorf("signed root hash %s, running root is verified against %s",
				signedRootHash, activeHash)
		}
		return nil
	}
}

// bindingToBytesOf is the stronger double: it derives the running system's root
// hash FROM THE BYTES ON DISK, the way dm-verity's Merkle tree does, so a
// substituted image really does produce a different answer.  A double that
// returned nil would reproduce the exact defect under test.
func bindingToBytesOf(imagePath string) RootHashBinder {
	return func(signedRootHash string) error {
		data, err := os.ReadFile(imagePath)
		if err != nil {
			return fmt.Errorf("cannot measure %s: %w", imagePath, err)
		}
		sum := sha256.Sum256(data)
		measured := hex.EncodeToString(sum[:])
		if measured != signedRootHash {
			return fmt.Errorf("signed root hash %s, measured %s", signedRootHash, measured)
		}
		return nil
	}
}

// signedFor builds a valid chain whose payload names the root hash the image
// bytes actually produce, so the binding can be exercised for real.
func signedFor(t *testing.T, content []byte) (cfg SquashfsVerifyConfig, imagePath string) {
	t.Helper()
	rootPub, rootPriv := genKeyPair(t)
	releasePub, releasePriv := genKeyPair(t)
	anchorPath := writeTempAnchor(t, rootPub)
	certPath := writeTempCert(t, issueTestCert(t, rootPriv, releasePub, 1, time.Now().Add(24*time.Hour)))

	sum := sha256.Sum256(content)
	payload := ImagePayload{
		Path:       "os/v08/os-core.squashfs",
		RootHash:   hex.EncodeToString(sum[:]),
		Size:       int64(len(content)),
		MinEpoch:   1,
		ReleasedAt: "2026-05-20T09:00:00Z",
	}
	squashfsPath, sigPath := makeImageSig(t, releasePriv, payload)
	// makeImageSig writes its own stub bytes; overwrite with the content the
	// payload actually describes.
	if err := os.WriteFile(squashfsPath, content, 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	return SquashfsVerifyConfig{
		AnchorPath:         anchorPath,
		CertPath:           certPath,
		SquashfsPath:       squashfsPath,
		SigPath:            sigPath,
		ImagePayloadForSig: payload,
		BindRootHash:       bindingToBytesOf(squashfsPath),
	}, squashfsPath
}

// The image the signed payload describes verifies.
func TestVerifySquashfsBeforePivot_BoundToTheRealBytes(t *testing.T) {
	cfg, _ := signedFor(t, []byte("the-image-that-was-signed"))
	if err := VerifySquashfsBeforePivot(cfg); err != nil {
		t.Fatalf("the signed image should verify: %v", err)
	}
}

// THE test: a different image, with the SAME valid signature over the SAME
// payload, must be refused.  Nothing about the signature chain changes here —
// cert, release key, canonical payload and epoch are all untouched and all
// valid — so the only thing that can catch it is the binding.  Under the old
// ExpectedRootHash comparison this passed, because both sides came from the
// manifest and neither came from the image.
func TestVerifySquashfsBeforePivot_SubstitutedImage_FailsClosed(t *testing.T) {
	cfg, imagePath := signedFor(t, []byte("the-image-that-was-signed"))
	if err := VerifySquashfsBeforePivot(cfg); err != nil {
		t.Fatalf("precondition: the signed image must verify first: %v", err)
	}

	if err := os.WriteFile(imagePath, []byte("a-different-image-entirely"), 0644); err != nil {
		t.Fatalf("substitute image: %v", err)
	}
	err := VerifySquashfsBeforePivot(cfg)
	if err == nil {
		t.Fatal("a substituted image must be refused — the signature covers a description, not the bytes")
	}
	if !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("the refusal should name the binding, got: %v", err)
	}
}

// A caller with no binder is REFUSED, never waved through.  This is what stops
// the tautology coming back as an omission: there is no default that agrees.
func TestVerifySquashfsBeforePivot_NilBinder_FailsClosed(t *testing.T) {
	cfg, _ := signedFor(t, []byte("the-image-that-was-signed"))
	cfg.BindRootHash = nil

	err := VerifySquashfsBeforePivot(cfg)
	if err == nil {
		t.Fatal("a gate with nothing to bind against must refuse, not report a verified image")
	}
	if !strings.Contains(err.Error(), "no root-hash binder") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A payload naming no root hash cannot be bound to anything either.
func TestVerifySquashfsBeforePivot_PayloadWithoutRootHash_FailsClosed(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, releasePriv := genKeyPair(t)
	anchorPath := writeTempAnchor(t, rootPub)
	certPath := writeTempCert(t, issueTestCert(t, rootPriv, releasePub, 1, time.Now().Add(24*time.Hour)))

	payload := ImagePayload{Path: "os/v08/os-core.squashfs", Size: 1, MinEpoch: 1, ReleasedAt: "2026-05-20T09:00:00Z"}
	squashfsPath, sigPath := makeImageSig(t, releasePriv, payload)

	cfg := SquashfsVerifyConfig{
		AnchorPath:         anchorPath,
		CertPath:           certPath,
		SquashfsPath:       squashfsPath,
		SigPath:            sigPath,
		ImagePayloadForSig: payload,
		BindRootHash:       bindingTo(""),
	}
	if err := VerifySquashfsBeforePivot(cfg); err == nil {
		t.Fatal("a payload with no root hash must be refused")
	}
}

// VerifyManifestSignature is the honest half for callers with nothing to bind:
// it verifies the chain and does NOT require (or silently invent) a binding.
func TestVerifyManifestSignature_NoBindingRequired(t *testing.T) {
	cfg, imagePath := signedFor(t, []byte("the-image-that-was-signed"))
	cfg.BindRootHash = nil

	if err := VerifyManifestSignature(cfg); err != nil {
		t.Fatalf("a signed manifest should verify without a binder: %v", err)
	}
	// ...and it must be honest about its limit: substituting the image changes
	// nothing here, which is precisely why the pre-pivot gate needs more.
	if err := os.WriteFile(imagePath, []byte("a-different-image-entirely"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifestSignature(cfg); err != nil {
		t.Fatalf("VerifyManifestSignature examines no image, so this must still pass: %v", err)
	}
	if err := VerifySquashfsBeforePivot(cfg); err == nil {
		t.Fatal("...and the pre-pivot gate must catch what it cannot")
	}
}

// A broken signature is still caught before the binding is even attempted, so a
// permissive binder cannot rescue an unsigned image.
func TestVerifySquashfsBeforePivot_BindingDoesNotReplaceTheSignature(t *testing.T) {
	cfg, _ := signedFor(t, []byte("the-image-that-was-signed"))
	cfg.BindRootHash = func(string) error { return nil } // agrees with anything

	// Re-sign the payload with a key the cert does not authorise.
	_, wrongPriv := genKeyPair(t)
	canonical, _ := signing.Canonical(cfg.ImagePayloadForSig)
	sigData, _ := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "wrong-key",
		SigBytes:  signing.Sign(wrongPriv, canonical),
	})
	if err := os.WriteFile(cfg.SigPath, sigData, 0644); err != nil {
		t.Fatal(err)
	}

	if err := VerifySquashfsBeforePivot(cfg); err == nil {
		t.Fatal("an image signed by an uncertified key must be refused whatever the binder says")
	}
}
