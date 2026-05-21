package verify

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
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
		ExpectedRootHash:   payload.RootHash,
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
		ExpectedRootHash:   payload.RootHash,
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
		ExpectedRootHash:   "bbbb0000000000000000000000000000bbbb0000000000000000000000000000ab",
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
		ExpectedRootHash:   payload.RootHash,
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
		ExpectedRootHash:   payload.RootHash,
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
		ExpectedRootHash:   payload.RootHash,
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
