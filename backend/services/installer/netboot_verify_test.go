package installer

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/cmd/verify"
	"vulos/backend/services/osdist"
	"vulos/backend/services/signing"
)

// ---------------------------------------------------------------------------
// Test harness: the artifact set a real signed release actually produces.
//
// This fixture builds the SAME chain `cmd/sign` emits, because the defect this
// file now guards against was a verifier that expected a shape no signer here
// could produce (root key over raw image bytes). Fixtures that invent their own
// signature format cannot catch that class of bug — they define it away. So:
//
//	offline ROOT key ──issue-release-cert──▶ cert ──▶ RELEASE key
//	                                                    │ signs canonical(ManifestPayload) → stable.json.sig
//	                                                    └ signs canonical(ImagePayload)    → os-core.squashfs.sig
//
// and the anchor written to disk is the ROOT public key, exactly as SEED-01
// bakes it.
// ---------------------------------------------------------------------------

type verifyFixture struct {
	dir          string
	anchorPath   string
	epochPath    string
	certPath     string
	squashfsPath string
	rootPub      ed25519.PublicKey
	rootPriv     ed25519.PrivateKey
	releasePub   ed25519.PublicKey
	releasePriv  ed25519.PrivateKey
	image        []byte
	// rootHash is the dm-verity root hash the fixture claims for the image.
	// Real installs get this from resolveVerityArtifacts after a real
	// `veritysetup verify`; here it is passed to verifyNetbootSquashfs directly,
	// which is the same value from the verifier's point of view.
	rootHash string
}

// newVerifyFixture creates a temp dir with the root anchor, a certified release
// key, an image, and no signatures yet (each test writes the ones it needs).
func newVerifyFixture(t *testing.T) *verifyFixture {
	t.Helper()
	dir := t.TempDir()

	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate root key: %v", err)
	}
	releasePub, releasePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate release key: %v", err)
	}

	anchorPath := filepath.Join(dir, "trust-anchor.pub")
	// Anchor file format: single line, base64(raw 32-byte pubkey).
	if err := os.WriteFile(anchorPath, []byte(base64.StdEncoding.EncodeToString(rootPub)), 0o644); err != nil {
		t.Fatalf("write anchor: %v", err)
	}

	image := []byte("this-is-a-fake-squashfs-payload-for-testing-only")
	squashfsPath := filepath.Join(dir, "os-core.squashfs")
	if err := os.WriteFile(squashfsPath, image, 0o644); err != nil {
		t.Fatalf("write squashfs: %v", err)
	}

	f := &verifyFixture{
		dir:          dir,
		anchorPath:   anchorPath,
		epochPath:    filepath.Join(dir, "epoch-floor.json"),
		certPath:     filepath.Join(dir, "release-cert.json"),
		squashfsPath: squashfsPath,
		rootPub:      rootPub,
		rootPriv:     rootPriv,
		releasePub:   releasePub,
		releasePriv:  releasePriv,
		image:        image,
		rootHash:     hexHash(image),
	}
	f.writeCert(t, 0, time.Now().Add(24*time.Hour))
	return f
}

// writeCert issues a release certificate from the root key and writes it beside
// the image, where a netboot medium would carry it.
func (f *verifyFixture) writeCert(t *testing.T, minEpoch int64, notAfter time.Time) {
	t.Helper()
	cert, err := signing.IssueReleaseCert(f.rootPriv, f.releasePub, "test-release", notAfter, minEpoch)
	if err != nil {
		t.Fatalf("issue release cert: %v", err)
	}
	data, err := json.Marshal(cert)
	if err != nil {
		t.Fatalf("marshal cert: %v", err)
	}
	if err := os.WriteFile(f.certPath, data, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
}

// writeSigWith signs `over` with the given key and writes a detached .sig file
// in the Vulos detached-signature format.
func (f *verifyFixture) writeSigWith(t *testing.T, priv ed25519.PrivateKey, path string, over []byte) {
	t.Helper()
	data, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "test-release",
		SigBytes:  signing.Sign(priv, over),
	})
	if err != nil {
		t.Fatalf("marshal sig: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write sig %s: %v", path, err)
	}
}

// writeManifest writes stable.json + stable.json.sig beside the squashfs,
// signed by the RELEASE key over the canonical manifest bytes.
func (f *verifyFixture) writeManifest(t *testing.T, minEpoch int64, rootHash string) {
	t.Helper()
	m := &osdist.StableManifest{
		Channel:    "stable",
		Latest:     "v08",
		MinEpoch:   minEpoch,
		RootHash:   rootHash,
		Size:       int64(len(f.image)),
		ReleasedAt: time.Unix(0, 0).UTC(),
		Path:       "os/v08/os-core.squashfs",
	}
	canonical, err := m.Canonical()
	if err != nil {
		t.Fatalf("manifest canonical: %v", err)
	}
	manifestPath := filepath.Join(f.dir, "stable.json")
	// Write the exact canonical bytes so verification re-derives the same input.
	if err := os.WriteFile(manifestPath, canonical, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	f.writeSigWith(t, f.releasePriv, manifestPath+".sig", canonical)
}

// writeImageSig signs the ImagePayload the manifest on disk describes, with the
// release key — i.e. exactly what `cmd/sign sign-image` produces.
func (f *verifyFixture) writeImageSig(t *testing.T) {
	f.writeImageSigWith(t, f.releasePriv)
}

func (f *verifyFixture) writeImageSigWith(t *testing.T, priv ed25519.PrivateKey) {
	t.Helper()
	manifestData, err := os.ReadFile(filepath.Join(f.dir, "stable.json"))
	if err != nil {
		t.Fatalf("read manifest for image payload: %v", err)
	}
	var payload verify.ImagePayload
	if err := json.Unmarshal(manifestData, &payload); err != nil {
		t.Fatalf("unmarshal image payload: %v", err)
	}
	canonical, err := signing.Canonical(payload)
	if err != nil {
		t.Fatalf("canonical image payload: %v", err)
	}
	f.writeSigWith(t, priv, f.squashfsPath+".sig", canonical)
}

// signedMedium is the happy-path medium: the dm-verity siblings, the signed
// manifest, and the image signature. The verity siblings belong here because
// the signature is bound to the image THROUGH them — a "signed medium" without
// a root hash is not a medium this installer will accept, and a fixture that
// omitted them would be testing a state that cannot install.
func (f *verifyFixture) signedMedium(t *testing.T, minEpoch int64) {
	t.Helper()
	dir := filepath.Dir(f.squashfsPath)
	if err := os.WriteFile(filepath.Join(dir, "os-core.hashtree"), []byte("merkle-tree-bytes"), 0o644); err != nil {
		t.Fatalf("write hashtree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "os-core.roothash"), []byte(f.rootHash+"\n"), 0o644); err != nil {
		t.Fatalf("write roothash: %v", err)
	}
	f.writeManifest(t, minEpoch, f.rootHash)
	f.writeImageSig(t)
}

func (f *verifyFixture) cfg() netbootVerifyConfig {
	return netbootVerifyConfig{anchorPath: f.anchorPath, epochPath: f.epochPath, certPath: f.certPath}
}

func (f *verifyFixture) svc() *Service {
	c := f.cfg()
	s := newWithCommander(newMockCmd())
	s.verifyCfg = &c
	return s
}

// verify runs the real entry point with the fixture's claimed verity root hash.
func (f *verifyFixture) verify() error {
	return f.svc().verifyNetbootSquashfs(f.squashfsPath, f.cfg(), f.rootHash)
}

func hexHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// Happy path: the chain cmd/sign produces verifies.
// ---------------------------------------------------------------------------

func TestVerifyNetbootSquashfs_ValidSignature(t *testing.T) {
	f := newVerifyFixture(t)
	f.signedMedium(t, 0)

	if err := f.verify(); err != nil {
		t.Fatalf("a correctly signed medium should verify, got: %v", err)
	}
}

// The regression that motivated this file's rewrite: a detached signature by
// the ROOT ANCHOR over the RAW IMAGE BYTES — the shape this verifier used to
// demand, and which no tool in this repository has ever emitted — must NOT be
// accepted. If it is, the two ends have drifted apart again.
func TestVerifyNetbootSquashfs_RootKeyOverRawBytes_Rejected(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeManifest(t, 0, f.rootHash)
	f.writeSigWith(t, f.rootPriv, f.squashfsPath+".sig", f.image)

	if err := f.verify(); !errors.Is(err, ErrSquashfsBadSignature) {
		t.Fatalf("a root-key signature over raw image bytes must be rejected, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// #3 MITM substitution / #1 tamper.
// ---------------------------------------------------------------------------

// A substituted image is caught by the root-hash binding: the signed payload
// names a root hash, and the medium's validated dm-verity root hash is a
// different one. (Tampering with the bytes alone is caught by dm-verity itself
// — netboot_verity.go refuses to stage a tree that does not describe the image
// — so the binding here is what stops a VALIDLY signed OTHER image being
// swapped in.)
func TestVerifyNetbootSquashfs_SubstitutedImage_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.signedMedium(t, 0)

	// The medium now carries a different image: same manifest, but the verity
	// root hash the installer measured does not match what was signed.
	err := f.svc().verifyNetbootSquashfs(f.squashfsPath, f.cfg(), hexHash([]byte("some-other-image")))
	if !errors.Is(err, ErrSquashfsHashMismatch) {
		t.Fatalf("substituted image must fail with ErrSquashfsHashMismatch, got: %v", err)
	}
}

// A signature produced by a DIFFERENT key (attacker key) must not verify, even
// though its .sig file is well-formed and covers the right payload.
func TestVerifyNetbootSquashfs_WrongSigningKey_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeManifest(t, 0, f.rootHash)
	_, attackerPriv, _ := ed25519.GenerateKey(rand.Reader)
	f.writeImageSigWith(t, attackerPriv)

	if err := f.verify(); !errors.Is(err, ErrSquashfsBadSignature) {
		t.Fatalf("attacker-key signature must fail, got: %v", err)
	}
}

// An attacker who mints their OWN release key and cert cannot get it trusted:
// the cert must chain to the PINNED root anchor, not to any self-consistent
// hierarchy.
func TestVerifyNetbootSquashfs_AttackerIssuedCert_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.signedMedium(t, 0)

	_, evilRootPriv, _ := ed25519.GenerateKey(rand.Reader)
	evilPub, evilPriv, _ := ed25519.GenerateKey(rand.Reader)
	cert, err := signing.IssueReleaseCert(evilRootPriv, evilPub, "evil", time.Now().Add(time.Hour), 0)
	if err != nil {
		t.Fatalf("issue evil cert: %v", err)
	}
	data, _ := json.Marshal(cert)
	if err := os.WriteFile(f.certPath, data, 0o644); err != nil {
		t.Fatalf("write evil cert: %v", err)
	}
	// Re-sign everything with the attacker's release key so the medium is
	// internally consistent — only the anchor disagrees.
	f.releasePriv = evilPriv
	f.signedMedium(t, 0)

	if err := f.verify(); !errors.Is(err, ErrReleaseCertInvalid) {
		t.Fatalf("a cert not chaining to the pinned anchor must be rejected, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Missing inputs ⇒ refuse. Every one of these was reachable in production.
// ---------------------------------------------------------------------------

func TestVerifyNetbootSquashfs_MissingSig_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeManifest(t, 0, f.rootHash)
	// Deliberately do NOT write the image .sig.
	if err := f.verify(); !errors.Is(err, ErrSquashfsSigMissing) {
		t.Fatalf("missing sig must fail with ErrSquashfsSigMissing, got: %v", err)
	}
}

// #2 cert-pinning: absent/malformed pinned anchor ⇒ refuse (never fall back to
// CA-trust).
func TestVerifyNetbootSquashfs_MissingAnchor_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.signedMedium(t, 0)
	c := f.cfg()
	c.anchorPath = filepath.Join(f.dir, "does-not-exist.pub")

	err := f.svc().verifyNetbootSquashfs(f.squashfsPath, c, f.rootHash)
	if !errors.Is(err, ErrAnchorMissing) {
		t.Fatalf("missing anchor must fail with ErrAnchorMissing, got: %v", err)
	}
}

func TestVerifyNetbootSquashfs_MissingReleaseCert_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.signedMedium(t, 0)
	if err := os.Remove(f.certPath); err != nil {
		t.Fatalf("remove cert: %v", err)
	}
	c := f.cfg()
	c.certPath = filepath.Join(f.dir, "no-seed-cert.json")

	err := f.svc().verifyNetbootSquashfs(f.squashfsPath, c, f.rootHash)
	if !errors.Is(err, ErrReleaseCertMissing) {
		t.Fatalf("missing release cert must fail closed, got: %v", err)
	}
}

// An expired certificate is refused even though its signature is genuine.
func TestVerifyNetbootSquashfs_ExpiredReleaseCert_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeCert(t, 0, time.Now().Add(-time.Hour))
	f.signedMedium(t, 0)

	if err := f.verify(); !errors.Is(err, ErrReleaseCertInvalid) {
		t.Fatalf("expired cert must fail closed, got: %v", err)
	}
}

// The manifest is mandatory: without it the ImagePayload the release key signed
// cannot be reconstructed, so there is nothing to verify and nothing to bind.
func TestVerifyNetbootSquashfs_MissingManifest_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeManifest(t, 0, f.rootHash)
	f.writeImageSig(t)
	if err := os.Remove(filepath.Join(f.dir, "stable.json")); err != nil {
		t.Fatalf("remove manifest: %v", err)
	}

	if err := f.verify(); !errors.Is(err, ErrManifestMissing) {
		t.Fatalf("missing manifest must fail closed, got: %v", err)
	}
}

// Without a validated dm-verity root hash the signature covers a description
// and nothing else — refuse rather than accept an unbound signature.
func TestVerifyNetbootSquashfs_NoVerityRootHash_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.signedMedium(t, 0)

	err := f.svc().verifyNetbootSquashfs(f.squashfsPath, f.cfg(), "")
	if !errors.Is(err, ErrVerityRootHashUnknown) {
		t.Fatalf("an unbound signature must be refused, got: %v", err)
	}
}

// A tampered manifest breaks its own signature before anything else is read.
func TestVerifyNetbootSquashfs_TamperedManifest_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.signedMedium(t, 0)
	// Change a FIELD, not the whitespace: ParseAndVerify re-derives the
	// canonical bytes from the parsed struct, so reformatting is not tampering
	// and must not be mistaken for a passing test.
	mp := filepath.Join(f.dir, "stable.json")
	data, _ := os.ReadFile(mp)
	tampered := strings.Replace(string(data), `"latest":"v08"`, `"latest":"v09"`, 1)
	if tampered == string(data) {
		t.Fatalf("tamper did not change the manifest: %s", data)
	}
	if err := os.WriteFile(mp, []byte(tampered), 0o644); err != nil {
		t.Fatalf("tamper manifest: %v", err)
	}

	if err := f.verify(); !errors.Is(err, osdist.ErrBadSignature) {
		t.Fatalf("tampered manifest must fail with ErrBadSignature, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Downgrade / rollback defence.
// ---------------------------------------------------------------------------

// #5 Downgrade/rollback: a signed manifest whose min_epoch is below the device
// floor is refused even though the signature is valid.
func TestVerifyNetbootSquashfs_DowngradeBelowEpochFloor_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	// The cert is issued for a high epoch so the manifest is what fails.
	f.writeCert(t, 5, time.Now().Add(24*time.Hour))
	f.signedMedium(t, 2)

	es, err := signing.NewEpochStore(f.epochPath)
	if err != nil {
		t.Fatalf("epoch store: %v", err)
	}
	if err := es.RaiseTo(5); err != nil {
		t.Fatalf("raise floor: %v", err)
	}

	if err := f.verify(); !errors.Is(err, osdist.ErrEpochTooLow) {
		t.Fatalf("downgrade below floor must fail with ErrEpochTooLow, got: %v", err)
	}
}

// A retired release key cannot be resurrected: its certificate was issued below
// the device's floor, so the key is refused before any artifact is examined.
func TestVerifyNetbootSquashfs_RetiredReleaseCert_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeCert(t, 1, time.Now().Add(24*time.Hour))
	f.signedMedium(t, 9)

	es, _ := signing.NewEpochStore(f.epochPath)
	_ = es.RaiseTo(5)

	if err := f.verify(); !errors.Is(err, ErrReleaseCertInvalid) {
		t.Fatalf("a cert below the epoch floor must be refused, got: %v", err)
	}
}

// persistedFloor re-reads the epoch floor FROM DISK, so these tests measure
// what the next boot would see rather than an in-memory value.
func (f *verifyFixture) persistedFloor(t *testing.T) int64 {
	t.Helper()
	es, err := signing.NewEpochStore(f.epochPath)
	if err != nil {
		t.Fatalf("re-open epoch store: %v", err)
	}
	return es.Current()
}

// The floor must RISE, or revocation revokes nothing.  Before this was wired in,
// verifyNetbootSquashfs read the floor and never moved it: a device sat at 0 for
// life, every min_epoch >= 0 passed, and issuing a release cert with a higher
// -min-epoch had no effect on this path at all.
func TestVerifyNetbootSquashfs_RaisesEpochFloorFromReleaseCert(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeCert(t, 5, time.Now().Add(24*time.Hour))
	f.signedMedium(t, 5)

	if before := f.persistedFloor(t); before != 0 {
		t.Fatalf("fixture should start at floor 0, got %d", before)
	}
	if err := f.verify(); err != nil {
		t.Fatalf("a correctly signed medium at epoch 5 should verify, got: %v", err)
	}
	if got := f.persistedFloor(t); got != 5 {
		t.Fatalf("the root-signed cert's min_epoch 5 must be persisted as the new floor, got %d", got)
	}
}

// THE revocation test: a device that has accepted epoch 5 must REFUSE a
// certificate at 4 — the retired release key cannot be replayed onto it.
//
// The floor here is raised by the FIRST install, not by a test-only RaiseTo, so
// this exercises the mechanism a real device would rely on.
//
// The rollback medium deliberately carries a manifest at epoch 9, ABOVE the
// floor the first install established.  That isolates the gate under test: the
// only thing below the floor is the CERT, so the manifest's own epoch check
// cannot stand in for the cert check and mask a missing raise.  With the raise
// removed, the floor stays 0, cert 4 and manifest 9 both pass, and this medium
// installs.
func TestVerifyNetbootSquashfs_RetiredCertAfterRaise_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeCert(t, 5, time.Now().Add(24*time.Hour))
	f.signedMedium(t, 5)
	if err := f.verify(); err != nil {
		t.Fatalf("first install at epoch 5 should verify, got: %v", err)
	}
	if got := f.persistedFloor(t); got != 5 {
		t.Fatalf("floor should be 5 after the first install, got %d", got)
	}

	// The retired key returns, with a manifest that is NOT stale.
	f.writeCert(t, 4, time.Now().Add(24*time.Hour))
	f.signedMedium(t, 9)

	err := f.verify()
	if !errors.Is(err, ErrReleaseCertInvalid) {
		t.Fatalf("a cert at epoch 4 must be refused by a device that has accepted 5, got: %v", err)
	}
	// And the refusal must be about the epoch, not some incidental breakage.
	if !strings.Contains(err.Error(), "below the device epoch floor") {
		t.Fatalf("refusal should name the epoch floor, got: %v", err)
	}
}

// The manifest is checked against the RAISED floor, not the one that was in
// force when verification started.  cmd/sign's rule is that a manifest's
// min_epoch is at least its cert's; enforcing it here is what stops a medium
// pairing a current cert with a stale manifest.  The device starts at floor 0,
// so ONLY the cert's own raise can refuse this manifest.
func TestVerifyNetbootSquashfs_StaleManifestUnderCurrentCert_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeCert(t, 5, time.Now().Add(24*time.Hour))
	f.signedMedium(t, 2)

	if before := f.persistedFloor(t); before != 0 {
		t.Fatalf("fixture should start at floor 0, got %d", before)
	}
	if err := f.verify(); !errors.Is(err, osdist.ErrEpochTooLow) {
		t.Fatalf("a manifest at epoch 2 under a cert at epoch 5 must be refused, got: %v", err)
	}
}

// Only the ROOT-SIGNED cert may raise the floor.  The manifest is signed by the
// RELEASE key — the very key revocation exists to retire — so if it could move
// the floor, an attacker holding it could publish min_epoch=MaxInt64 and, since
// the floor never falls, permanently brick this install path.
func TestVerifyNetbootSquashfs_ManifestDoesNotRaiseTheFloor(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeCert(t, 0, time.Now().Add(24*time.Hour))
	f.signedMedium(t, 9) // manifest claims epoch 9; the cert says 0

	if err := f.verify(); err != nil {
		t.Fatalf("manifest above the cert's epoch is legitimate, got: %v", err)
	}
	if got := f.persistedFloor(t); got != 0 {
		t.Fatalf("only the root-signed cert may raise the floor; manifest epoch 9 moved it to %d", got)
	}
}

// An epoch store that will not open is FATAL, not floor 0.  It used to fall back
// to zero, which is not "no opinion" — it is "accept every retired release key",
// chosen silently, on the one path where the payload is attacker-reachable in
// transit.  A corrupt record is exactly how an attacker with local write access
// would try to reset the floor.
//
// The medium here is otherwise perfectly valid, so nothing but the store can be
// what refuses it.
func TestVerifyNetbootSquashfs_UnopenableEpochStore_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeCert(t, 0, time.Now().Add(24*time.Hour))
	f.signedMedium(t, 0)
	if err := f.verify(); err != nil {
		t.Fatalf("precondition: this medium must verify with a healthy store, got: %v", err)
	}

	if err := os.WriteFile(f.epochPath, []byte("{ not json"), 0o644); err != nil {
		t.Fatalf("corrupt epoch file: %v", err)
	}
	if err := f.verify(); !errors.Is(err, ErrEpochFloorUnavailable) {
		t.Fatalf("an unreadable epoch floor must refuse the install, got: %v", err)
	}
}

// A validly-signed manifest at or above the floor is accepted.
func TestVerifyNetbootSquashfs_EpochAtFloor_Passes(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeCert(t, 5, time.Now().Add(24*time.Hour))
	f.signedMedium(t, 5)

	es, _ := signing.NewEpochStore(f.epochPath)
	_ = es.RaiseTo(5)

	if err := f.verify(); err != nil {
		t.Fatalf("epoch at floor should pass, got: %v", err)
	}
}

// The seed copy of the certificate is used when the medium ships none — a
// device flashed with a cert can still install a payload that omits it.
func TestVerifyNetbootSquashfs_SeedCertFallback_Passes(t *testing.T) {
	f := newVerifyFixture(t)
	f.signedMedium(t, 0)

	seedCert := filepath.Join(f.dir, "seed", "release-cert.json")
	if err := os.MkdirAll(filepath.Dir(seedCert), 0o755); err != nil {
		t.Fatalf("mkdir seed: %v", err)
	}
	data, _ := os.ReadFile(f.certPath)
	if err := os.WriteFile(seedCert, data, 0o644); err != nil {
		t.Fatalf("write seed cert: %v", err)
	}
	if err := os.Remove(f.certPath); err != nil {
		t.Fatalf("remove sibling cert: %v", err)
	}

	c := f.cfg()
	c.certPath = seedCert
	if err := f.svc().verifyNetbootSquashfs(f.squashfsPath, c, f.rootHash); err != nil {
		t.Fatalf("seed cert fallback should verify, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Pipeline integration: the verify step aborts the install before staging.
// ---------------------------------------------------------------------------

func TestRunNetbootInstall_UnverifiedSquashfs_AbortsBeforeStage(t *testing.T) {
	f := newVerifyFixture(t)
	// No .sig written → verification must fail closed and the pipeline must stop
	// at the verify-squashfs step, never reaching stage-squashfs.
	c := f.cfg()

	mc := newMockCmd()
	mc.set("aaaa-1111", nil, "blkid", "-s", "UUID", "-o", "value", "/dev/sda1")
	mc.set("bbbb-2222", nil, "blkid", "-s", "UUID", "-o", "value", "/dev/sda2")
	svc := newWithCommander(mc)
	svc.verifyCfg = &c

	hub := newProgressHub()
	req := NetbootInstallRequest{
		Disk:         "sda",
		Confirm:      true,
		SquashfsPath: f.squashfsPath,
	}
	svc.runNetbootInstall(req, hub)

	done, err := hub.isDone()
	if !done {
		t.Fatal("pipeline should have completed (with error)")
	}
	if err == nil {
		t.Fatal("unverified squashfs must abort the install")
	}
	if !contains(err.Error(), "verify-squashfs") {
		t.Fatalf("install must fail at verify-squashfs step, got: %v", err)
	}
	// The staged squashfs must NOT exist on the (mock) target.
	staged := filepath.Join(netbootInstallMount, vulosCacheRelPath, "slot-a", "os-core.squashfs")
	if _, statErr := os.Stat(staged); statErr == nil {
		t.Fatalf("squashfs was staged despite failed verification: %s", staged)
	}
}

// TestRunNetbootInstall_UnverifiedSquashfs_DoesNotWipeDisk pins the ordering
// guarantee: because verify-squashfs runs FIRST (before partition/format), an
// image that fails verification must abort with the target disk NEVER touched —
// no parted, no mkfs, no mount. A bad or unsigned image can never cost the
// operator their existing disk.
func TestRunNetbootInstall_UnverifiedSquashfs_DoesNotWipeDisk(t *testing.T) {
	f := newVerifyFixture(t)
	c := f.cfg() // no .sig written → verification fails closed

	mc := newMockCmd()
	svc := newWithCommander(mc)
	svc.verifyCfg = &c

	hub := newProgressHub()
	req := NetbootInstallRequest{Disk: "sda", Confirm: true, SquashfsPath: f.squashfsPath}
	svc.runNetbootInstall(req, hub)

	done, err := hub.isDone()
	if !done || err == nil {
		t.Fatalf("expected failed completion, done=%v err=%v", done, err)
	}
	if !contains(err.Error(), "verify-squashfs") {
		t.Fatalf("must fail at verify-squashfs, got: %v", err)
	}
	// No external command may have run — the disk is pristine.
	for _, dangerous := range []string{"parted", "mkfs", "mkfs.fat", "mkfs.vfat", "mkfs.ext4", "sgdisk", "wipefs", "dd", "mount"} {
		for _, call := range mc.calls {
			if strings.HasPrefix(call, dangerous) {
				t.Fatalf("destructive command %q ran despite failed verification (call=%q) — verify must gate before any disk write", dangerous, call)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Admin gate: the destructive netboot-install endpoint must reject non-admins.
// ---------------------------------------------------------------------------

func TestHandleNetbootInstall_AdminGate_DeniesNonAdmin(t *testing.T) {
	svc := newWithCommander(newMockCmd())
	svc.isAdmin = func(r *http.Request) bool { return false } // deny everyone

	mux := http.NewServeMux()
	RegisterNetbootHandlers(mux, svc)

	body := `{"disk":"sda","confirm":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/installer/netboot-install",
		strings.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin must get 403, got %d", rr.Code)
	}
}

func TestHandleNetbootInstall_AdminGate_AllowsAdmin(t *testing.T) {
	mc := newMockCmd()
	svc := newWithCommander(mc)
	svc.isAdmin = func(r *http.Request) bool { return true } // allow

	mux := http.NewServeMux()
	RegisterNetbootHandlers(mux, svc)

	// Point at a nonexistent squashfs so the async pipeline fails harmlessly.
	body := `{"disk":"sda","confirm":true,"squashfs_path":"/nonexistent/os-core.squashfs"}`
	req := httptest.NewRequest(http.MethodPost, "/api/installer/netboot-install",
		strings.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("admin must be accepted (202), got %d", rr.Code)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
