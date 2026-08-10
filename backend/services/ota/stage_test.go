package ota

// stage_test.go — POST /api/os/update/stage, the only wired staging path in the
// product, verified against the artifact set a real signed release produces.
//
// Nothing exercised Stage before this file existed. That is why it shipped
// demanding two shapes no signer here emits: sha256(image) compared against a
// dm-verity root hash, and an image signature over the raw image bytes. Both
// fail closed, so staging was a WALL rather than a hole — but the update path
// had never worked, and no test could tell anyone.
//
// The fixture below therefore builds the REAL two-tier PKI with the same
// `signing` primitives cmd/sign calls, never a shape of its own:
//
//	offline ROOT key ──issue-release-cert──▶ cert ──▶ RELEASE key
//	   │ pinned as the box's baked anchor            │ signs canonical(ManifestPayload)
//	   ▼                                             └ signs canonical(ImagePayload)
//	trust-anchor.pub
//
// and binds the signature to the bytes with a dm-verity stand-in that RE-DERIVES
// the root hash from the file on disk. A double that returns nil would let every
// substitution test below pass while proving nothing.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"vulos/backend/cmd/verify"
	"vulos/backend/services/osdist"
	"vulos/backend/services/signing"
)

// ─── dm-verity stand-in ───────────────────────────────────────────────────────

// fixtureRootHash stands in for a dm-verity root hash.
//
// It is NOT sha256(image) dressed up — the salted prefix makes sure of that,
// because "the root hash is not the image's SHA-256" is precisely the property
// TestStage_RejectsSha256AsRootHash pins. What matters is that only a
// construction OVER THE IMAGE BYTES can produce it.
func fixtureRootHash(imageBytes []byte) string {
	sum := sha256.Sum256(append([]byte("dm-verity-merkle-root\x00"), imageBytes...))
	return hex.EncodeToString(sum[:])
}

// fixtureVerity is the ClientConfig.VerityVerify stand-in used throughout this
// file. macOS has no cryptsetup, so `veritysetup verify` cannot run here (see
// the report accompanying this change) — but the CONTRACT it stands for ("this
// root hash describes these exact bytes, or fail") is preserved exactly: it
// re-reads the image from disk and re-derives the hash. It also insists the
// hash tree was really fetched and written, so a channel publishing none cannot
// quietly pass.
func fixtureVerity(_ context.Context, imagePath, hashtreePath, rootHash string) error {
	ht, err := os.ReadFile(hashtreePath)
	if err != nil || len(ht) == 0 {
		return fmt.Errorf("%w: hash tree %s unusable: %v", ErrVerityUnavailable, hashtreePath, err)
	}
	img, err := os.ReadFile(imagePath)
	if err != nil {
		return fmt.Errorf("%w: read image: %v", ErrVerityUnavailable, err)
	}
	if got := fixtureRootHash(img); got != rootHash {
		return fmt.Errorf("%w: image root hash %s, signed root hash %s", ErrHashMismatch, got, rootHash)
	}
	return nil
}

// ─── release fixture ──────────────────────────────────────────────────────────

// release describes one published release. Tests build a correct one with
// newRelease and then break EXACTLY ONE thing before publishing it.
type release struct {
	version string
	image   []byte

	// rootHash is what the manifest advertises. Defaults to the image's real
	// (fixture) dm-verity root hash.
	rootHash string

	// size is what the manifest advertises. Defaults to len(image).
	size int64

	// releasedAt is the manifest's released_at STRING, exactly as the signer
	// wrote it. Defaults to a whole-second UTC timestamp.
	releasedAt string

	certEpoch     int64
	manifestEpoch int64

	// isSecurity / severity / notes are the release-severity surface, SIGNED
	// along with everything else. Defaults are the zero values, which omitempty
	// drops entirely — so newRelease publishes the byte-identical seven-key
	// document releases signed before this surface existed.
	//
	// They are set here rather than tampered onto the served document because
	// the fixture must be able to produce a GENUINELY signed security release;
	// a test that can only forge one proves nothing about the feature working.
	isSecurity bool
	severity   string
	notes      string

	// certRoot signs the release cert. Defaults to the channel's root key —
	// the one the box has pinned.
	certRoot ed25519.PrivateKey

	// imageSigner signs the image .sig. Defaults to the release key.
	imageSigner ed25519.PrivateKey

	// imageSigOver chooses the BYTES the image .sig covers. Defaults to
	// canonical(ImagePayload), the shape `cmd/sign sign-image` emits.
	imageSigOver func(manifestJSON, image []byte) []byte

	// servedImage is what the channel actually hands over. Defaults to image.
	servedImage []byte

	// omitHashtree drops os/<v>/os-core.hashtree from the channel.
	omitHashtree bool

	// extraManifestFields are written into the served stable.json AFTER it is
	// signed — the shape an attacker modifying a legitimately signed document in
	// transit produces. A key already present is OVERWRITTEN, so this also
	// expresses "flip a signed field on the wire".
	extraManifestFields map[string]any

	// dropManifestFields are deleted from the served stable.json AFTER it is
	// signed. Needed alongside extraManifestFields to express a transit edit
	// that leaves the document STRUCTURALLY VALID — clearing a signed
	// is_security means also removing the severity that must accompany it,
	// otherwise the severity-surface gate refuses the document first and masks
	// the signature gate under test.
	dropManifestFields []string
}

func newRelease(version string, image []byte) *release {
	return &release{
		version:       version,
		image:         image,
		rootHash:      fixtureRootHash(image),
		size:          int64(len(image)),
		releasedAt:    "2026-05-20T09:00:00Z",
		certEpoch:     1,
		manifestEpoch: 1,
	}
}

func (r *release) imagePath() string    { return "os/" + r.version + "/os-core.squashfs" }
func (r *release) hashtreePath() string { return "os/" + r.version + "/os-core.hashtree" }

// canonicalImagePayload is the default image-signature surface: the ImagePayload
// reconstructed from the manifest's own bytes, canonicalised — exactly what
// `cmd/sign sign-image` signs and what cmd/init reconstructs at boot.
func canonicalImagePayload(t *testing.T, manifestJSON []byte) []byte {
	t.Helper()
	var payload verify.ImagePayload
	if err := json.Unmarshal(manifestJSON, &payload); err != nil {
		t.Fatalf("unmarshal image payload: %v", err)
	}
	canonical, err := signing.Canonical(payload)
	if err != nil {
		t.Fatalf("canonical image payload: %v", err)
	}
	return canonical
}

// publishRelease writes the complete artifact set for r onto the channel.
func (ch *fakeChannel) publishRelease(t *testing.T, r *release) {
	t.Helper()

	certRoot := r.certRoot
	if certRoot == nil {
		certRoot = ch.rootPriv
	}
	cert, err := signing.IssueReleaseCert(certRoot, ch.relPub, "release-test",
		time.Now().Add(365*24*time.Hour), r.certEpoch)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}
	certJSON, err := json.Marshal(cert)
	if err != nil {
		t.Fatal(err)
	}
	ch.files["/"+releaseCertName] = certJSON

	// The manifest, in the exact seven-field surface `cmd/sign sign-manifest`
	// signs. The canonical bytes ARE the served document, so the signature
	// covers precisely what the channel hands over.
	payload := manifestSigPayload{
		Channel:    "stable",
		Latest:     r.version,
		MinEpoch:   r.manifestEpoch,
		Path:       r.imagePath(),
		ReleasedAt: r.releasedAt,
		RootHash:   r.rootHash,
		Size:       r.size,
		IsSecurity: r.isSecurity,
		Severity:   r.severity,
		Notes:      r.notes,
	}
	manifestJSON, err := signing.Canonical(payload)
	if err != nil {
		t.Fatalf("canonical manifest payload: %v", err)
	}
	manifestSig := ch.sigFile(t, ch.relPriv, manifestJSON)

	servedManifest := manifestJSON
	if len(r.extraManifestFields) > 0 || len(r.dropManifestFields) > 0 {
		// Rewrite the served document AFTER signing, leaving the .sig untouched
		// — what an attacker able to modify a legitimately signed stable.json in
		// transit can do.
		var doc map[string]any
		if err := json.Unmarshal(manifestJSON, &doc); err != nil {
			t.Fatal(err)
		}
		for k, v := range r.extraManifestFields {
			doc[k] = v
		}
		for _, k := range r.dropManifestFields {
			delete(doc, k)
		}
		servedManifest, err = json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
	}
	ch.files["/"+manifestName] = servedManifest
	ch.files["/"+manifestSigName] = manifestSig

	// The image, its signature, and the hash tree that binds them.
	image := r.servedImage
	if image == nil {
		image = r.image
	}
	ch.files["/"+r.imagePath()] = image

	imageSigner := r.imageSigner
	if imageSigner == nil {
		imageSigner = ch.relPriv
	}
	sigOver := r.imageSigOver
	if sigOver == nil {
		sigOver = func(mj, _ []byte) []byte { return canonicalImagePayload(t, mj) }
	}
	ch.files["/"+r.imagePath()+".sig"] = ch.sigFile(t, imageSigner, sigOver(manifestJSON, r.image))

	if !r.omitHashtree {
		ch.files["/"+r.hashtreePath()] = []byte("merkle-hash-tree-for-" + r.version)
	} else {
		delete(ch.files, "/"+r.hashtreePath())
	}
}

// sigFile wraps a signature in the detached .sig file format.
func (ch *fakeChannel) sigFile(t *testing.T, priv ed25519.PrivateKey, over []byte) []byte {
	t.Helper()
	data, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "release-test",
		SigBytes:  signing.Sign(priv, over),
	})
	if err != nil {
		t.Fatalf("MarshalSig: %v", err)
	}
	return data
}

// ─── box fixture ──────────────────────────────────────────────────────────────

// stageBox is a box with real A/B slots, a pinned anchor and a persistent
// epoch floor — everything Stage touches.
type stageBox struct {
	client *Client
	sm     *osdist.SlotManager
	dir    string
}

func newStageBox(t *testing.T, ch *fakeChannel) *stageBox {
	t.Helper()
	dir := t.TempDir()
	anchor := ch.anchorPath(t, dir)

	cacheDir := filepath.Join(dir, "os-cache")
	sm, err := osdist.NewSlotManager(cacheDir)
	if err != nil {
		t.Fatalf("NewSlotManager: %v", err)
	}
	if err := sm.Save(&osdist.BootState{
		Active:        osdist.SlotA,
		Pending:       osdist.SlotNone,
		BootCounter:   0,
		LastKnownGood: osdist.SlotA,
	}); err != nil {
		t.Fatalf("save boot state: %v", err)
	}

	store, err := signing.NewEpochStore(filepath.Join(dir, "epoch-floor.json"))
	if err != nil {
		t.Fatalf("NewEpochStore: %v", err)
	}
	client, err := NewClient(ClientConfig{
		ChannelURL: ch.srv.URL,
		AnchorPath: anchor,
		// A path that does NOT exist, so a test whose channel serves no release
		// cert fails at the fetch rather than silently reading whatever is
		// installed at /etc/vulos/release-cert.json on the machine running it.
		ReleaseCertPath: filepath.Join(dir, "no-such-release-cert.json"),
		EpochStore:      store,
		SlotManager:     sm,
		RunningVersion:  "v01",
		HTTPClient:      ch.srv.Client(),
		VerityVerify:    fixtureVerity,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return &stageBox{client: client, sm: sm, dir: cacheDir}
}

// stagedImage returns the bytes staged into the inactive slot, or nil.
func (b *stageBox) stagedImage(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b.dir, "slot-b", "os-core.squashfs"))
	if err != nil {
		return nil
	}
	return data
}

// assertNothingStaged is the fail-closed property every rejection test asserts:
// no image under its real name, no pending slot, and no debris left behind
// (a temp download or hash tree surviving a failure would mean the cleanup
// paths are wrong even though the refusal was right).
func (b *stageBox) assertNothingStaged(t *testing.T) {
	t.Helper()
	if img := b.stagedImage(t); img != nil {
		t.Errorf("an image was staged despite the rejection (%d bytes)", len(img))
	}
	entries, err := os.ReadDir(filepath.Join(b.dir, "slot-b"))
	if err != nil {
		t.Fatalf("read slot-b: %v", err)
	}
	for _, e := range entries {
		t.Errorf("leftover file in the inactive slot after a rejection: %s", e.Name())
	}
	bs, err := b.sm.Load()
	if err != nil {
		t.Fatalf("load boot state: %v", err)
	}
	if bs.Pending != osdist.SlotNone {
		t.Errorf("boot state marks slot %q pending after a rejection", bs.Pending)
	}
}

// ─── The happy path ───────────────────────────────────────────────────────────

// TestStage_SignedReleaseStages is the test that could not have passed before
// this change: a release signed exactly as `cmd/sign` signs one, staged.
func TestStage_SignedReleaseStages(t *testing.T) {
	ch := newFakeChannel(t)
	image := []byte("os-core squashfs image bytes for v09 ................")
	ch.publishRelease(t, newRelease("v09", image))
	box := newStageBox(t, ch)

	res, err := box.client.Stage(context.Background())
	if err != nil {
		t.Fatalf("Stage on a correctly signed release failed: %v", err)
	}
	if !res.Staged || res.Slot != string(osdist.SlotB) || res.Version != "v09" {
		t.Errorf("StageResult = %+v, want staged into the inactive slot %q at v09", res, osdist.SlotB)
	}
	if got := box.stagedImage(t); string(got) != string(image) {
		t.Errorf("staged image differs from the published one (%d vs %d bytes)", len(got), len(image))
	}
	bs, err := box.sm.Load()
	if err != nil {
		t.Fatal(err)
	}
	if bs.Pending != osdist.SlotB {
		t.Errorf("pending slot = %q, want slot-b", bs.Pending)
	}
	if bs.Active != osdist.SlotA {
		t.Errorf("Stage moved the ACTIVE slot to %q — it must never touch the running image", bs.Active)
	}
	// The download temp file must not survive a success either.
	entries, err := os.ReadDir(filepath.Join(box.dir, "slot-b"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "os-core.squashfs" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("inactive slot contains %v, want exactly [os-core.squashfs]", names)
	}
}

// ─── The two regressions this file exists for ─────────────────────────────────

// TestStage_RejectsRawImageBytesSignature pins defect #2 directly: the code
// used to call signing.Verify(releasePub, imageData, sig) — a signature over the
// RAW IMAGE BYTES. Nothing in this repository emits that, so a release signed
// that way must be refused now that the correct shape is demanded.
func TestStage_RejectsRawImageBytesSignature(t *testing.T) {
	ch := newFakeChannel(t)
	image := []byte("os-core squashfs image bytes for v09 ................")
	r := newRelease("v09", image)
	r.imageSigOver = func(_, img []byte) []byte { return img } // the old, impossible shape
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	_, err := box.client.Stage(context.Background())
	if !errors.Is(err, ErrImageBadSignature) {
		t.Fatalf("a signature over the RAW IMAGE BYTES was accepted (err = %v); "+
			"cmd/sign sign-image signs canonical(ImagePayload)", err)
	}
	box.assertNothingStaged(t)
}

// TestStage_RejectsSha256AsRootHash pins defect #1: the code used to compare
// sha256(image) to manifest.RootHash. RootHash is a dm-verity root hash over a
// salted Merkle tree, so a manifest advertising the image's SHA-256 describes
// no image at all — and the verity binding, not a hash comparison, is what says
// so. Everything else about this release is correctly signed.
func TestStage_RejectsSha256AsRootHash(t *testing.T) {
	ch := newFakeChannel(t)
	image := []byte("os-core squashfs image bytes for v09 ................")
	r := newRelease("v09", image)
	sum := sha256.Sum256(image)
	r.rootHash = hex.EncodeToString(sum[:])
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	_, err := box.client.Stage(context.Background())
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("sha256(image) was accepted as a dm-verity root hash (err = %v)", err)
	}
	box.assertNothingStaged(t)
}

// ─── Binding the signature to the bytes ───────────────────────────────────────

// TestStage_RejectsSubstitutedImageOfEqualLength is the reason the verity step
// is mandatory. The signature covers a name, a size and a root hash; it does not
// cover the image. Swap the bytes for others of the same length and only the
// root hash can tell.
func TestStage_RejectsSubstitutedImageOfEqualLength(t *testing.T) {
	ch := newFakeChannel(t)
	image := []byte("os-core squashfs image bytes for v09 ................")
	r := newRelease("v09", image)
	r.servedImage = []byte("HOSTILE squashfs image, same length as above ........")
	if len(r.servedImage) != len(image) {
		t.Fatalf("fixture bug: substituted image is %d bytes, original is %d", len(r.servedImage), len(image))
	}
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	_, err := box.client.Stage(context.Background())
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("a same-length substituted image was accepted (err = %v) — "+
			"the size check is not authentication", err)
	}
	box.assertNothingStaged(t)
}

// TestStage_RejectsTruncatedImage covers the cheap check that runs before the
// Merkle walk.
func TestStage_RejectsTruncatedImage(t *testing.T) {
	ch := newFakeChannel(t)
	image := []byte("os-core squashfs image bytes for v09 ................")
	r := newRelease("v09", image)
	r.servedImage = image[:10]
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	_, err := box.client.Stage(context.Background())
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("a truncated image was accepted (err = %v)", err)
	}
	if !strings.Contains(err.Error(), "signed size") {
		t.Errorf("expected the size check to be what refused it, got: %v", err)
	}
	box.assertNothingStaged(t)
}

// TestStage_RefusesWithoutHashtree is the refusal that keeps the whole scheme
// honest: with no hash tree the root hash cannot be computed from the bytes, so
// there is nothing binding the signature to the download. Refuse — do not fall
// back to the size check that already passed.
func TestStage_RefusesWithoutHashtree(t *testing.T) {
	ch := newFakeChannel(t)
	image := []byte("os-core squashfs image bytes for v09 ................")
	r := newRelease("v09", image)
	r.omitHashtree = true
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	_, err := box.client.Stage(context.Background())
	if !errors.Is(err, ErrVerityUnavailable) {
		t.Fatalf("an image with no published hash tree was staged on size alone (err = %v)", err)
	}
	box.assertNothingStaged(t)
}

// ─── The key the signature must come from ─────────────────────────────────────

// TestStage_RejectsRootKeySignedImage — the ROOT key signs exactly one thing,
// the release cert. An image signature made with it is not a shape any signer
// here emits, and must not be accepted just because the key is trusted.
func TestStage_RejectsRootKeySignedImage(t *testing.T) {
	ch := newFakeChannel(t)
	image := []byte("os-core squashfs image bytes for v09 ................")
	r := newRelease("v09", image)
	r.imageSigner = ch.rootPriv
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	_, err := box.client.Stage(context.Background())
	if !errors.Is(err, ErrImageBadSignature) {
		t.Fatalf("an image signed by the ROOT anchor was accepted (err = %v)", err)
	}
	box.assertNothingStaged(t)
}

// TestStage_RejectsCertFromAnotherRoot — the cert must chain to the PINNED
// anchor. Here every signature is internally consistent; only the root differs.
func TestStage_RejectsCertFromAnotherRoot(t *testing.T) {
	ch := newFakeChannel(t)
	_, foreignRoot, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	image := []byte("os-core squashfs image bytes for v09 ................")
	r := newRelease("v09", image)
	r.certRoot = foreignRoot
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	_, err = box.client.Stage(context.Background())
	if !errors.Is(err, ErrCertInvalid) {
		t.Fatalf("a cert chaining to an unpinned root was accepted (err = %v)", err)
	}
	// ...and the refusal must come from VALIDATING THE CERT before its key is
	// used, not incidentally from RaiseFromReleaseCert re-validating it two
	// steps later. Without this assertion, deleting the chain check in
	// ReleaseKeyFromCert looks harmless (mutation-tested: it was) — and it
	// stops being harmless the moment that raise is made non-fatal, which is
	// exactly what osdist's equivalent already does with it.
	if strings.Contains(err.Error(), "epoch floor") {
		t.Errorf("the foreign-root cert was refused by the epoch-floor raise rather than "+
			"by the chain check that guards the release key: %v", err)
	}
	box.assertNothingStaged(t)
}

// TestStage_RejectsRetiredReleaseKey — OTA is where revocation has to bite. A
// box that has accepted epoch 5 must refuse to STAGE anything authorised by a
// cert at epoch 4, however well-formed the rest of the release is.
func TestStage_RejectsRetiredReleaseKey(t *testing.T) {
	ch := newFakeChannel(t)
	image := []byte("os-core squashfs image bytes for v09 ................")
	current := newRelease("v09", image)
	current.certEpoch, current.manifestEpoch = 5, 5
	ch.publishRelease(t, current)
	box := newStageBox(t, ch)

	if _, err := box.client.Check(context.Background()); err != nil {
		t.Fatalf("check at epoch 5: %v", err)
	}

	// The retired key republishes an older image at epoch 4.
	// The CERT is the retired one; its manifest claims the current epoch, so
	// only a check of the cert's OWN min_epoch can refuse this — and the cert
	// is what authorises the release key that revocation is retiring.
	old := newRelease("v08", []byte("older os-core squashfs image bytes .................."))
	old.certEpoch, old.manifestEpoch = 4, 5
	ch.publishRelease(t, old)

	_, err := box.client.Stage(context.Background())
	if !errors.Is(err, ErrEpochRejected) {
		t.Fatalf("a retired release key (epoch 4, floor 5) staged an image (err = %v)", err)
	}
	box.assertNothingStaged(t)
}

// TestStage_RejectsUnverifiedManifestSignature — the manifest gate still holds
// on the staging path, not just on the poll path.
func TestStage_RejectsManifestSignedByRoot(t *testing.T) {
	ch := newFakeChannel(t)
	image := []byte("os-core squashfs image bytes for v09 ................")
	ch.publishRelease(t, newRelease("v09", image))
	// Re-sign the (unchanged) manifest with the ROOT key.
	ch.files["/"+manifestSigName] = ch.sigFile(t, ch.rootPriv, ch.files["/"+manifestName])
	box := newStageBox(t, ch)

	_, err := box.client.Stage(context.Background())
	if !errors.Is(err, ErrManifestBadSignature) {
		t.Fatalf("a manifest signed by the ROOT anchor was accepted (err = %v)", err)
	}
	box.assertNothingStaged(t)
}

// ─── The release cert is mandatory ────────────────────────────────────────────

// TestCheck_NoCertAnywhereVerifiesNothing — without a cert there is no release
// key, so there is nothing to verify anything with. Fail closed rather than
// falling back to the anchor (which signs no manifest here) or to trust.
func TestCheck_NoCertAnywhereVerifiesNothing(t *testing.T) {
	ch := newFakeChannel(t)
	ch.publishRelease(t, newRelease("v09", []byte("image bytes")))
	delete(ch.files, "/"+releaseCertName)
	box := newStageBox(t, ch)

	if _, err := box.client.Check(context.Background()); !errors.Is(err, ErrCertInvalid) {
		t.Fatalf("a channel serving no release cert was accepted (err = %v)", err)
	}
}

// TestCheck_FallsBackToBakedCert — a channel that publishes no cert is still
// verifiable by a box whose image shipped one. The cert is inert until it
// validates against the pinned anchor, so which copy it came from decides
// nothing.
func TestCheck_FallsBackToBakedCert(t *testing.T) {
	ch := newFakeChannel(t)
	ch.publishRelease(t, newRelease("v09", []byte("image bytes")))
	baked := ch.files["/"+releaseCertName]
	delete(ch.files, "/"+releaseCertName)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "release-cert.json")
	if err := os.WriteFile(certPath, baked, 0o444); err != nil {
		t.Fatal(err)
	}
	store, err := signing.NewEpochStore(filepath.Join(dir, "epoch-floor.json"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientConfig{
		ChannelURL:      ch.srv.URL,
		AnchorPath:      ch.anchorPath(t, dir),
		ReleaseCertPath: certPath,
		EpochStore:      store,
		RunningVersion:  "v01",
		HTTPClient:      ch.srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Check(context.Background())
	if err != nil {
		t.Fatalf("check with a baked cert and no channel cert failed: %v", err)
	}
	if !status.Available || status.LatestVersion != "v09" {
		t.Errorf("status = %+v, want v09 available", status)
	}
}

// ─── The signed manifest surface ──────────────────────────────────────────────

// TestCheck_RefusesFieldsNoSignatureCovers pins the rule that made the
// is_security decision necessary: a key the signature does not cover is
// unauthenticated data, appendable in transit to a legitimately signed document,
// and this client refuses the whole document rather than read it.
//
// is_security/severity/notes are INSIDE the signed surface now (they were the
// original offenders), so the extra key here is one that is not — and the
// assertion insists it was the SIGNED-SURFACE gate that refused it, not the
// severity-surface gate that lives beside it. Without that check this test would
// keep passing after the key filter was deleted.
func TestCheck_RefusesFieldsNoSignatureCovers(t *testing.T) {
	ch := newFakeChannel(t)
	r := newRelease("v09", []byte("image bytes"))
	r.extraManifestFields = map[string]any{
		"action_url": "https://vulos-security.example/urgent",
	}
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	status, err := box.client.Check(context.Background())
	if !errors.Is(err, ErrManifestMalformed) {
		t.Fatalf("a manifest carrying an unsigned action_url was accepted (err = %v)", err)
	}
	if !strings.Contains(err.Error(), "outside the signed manifest surface") {
		t.Errorf("the refusal must come from the signed-key filter, got: %v", err)
	}
	if !strings.Contains(err.Error(), "action_url") {
		t.Errorf("the refusal should name the offending key, got: %v", err)
	}
	if status.IsSecurity || status.Notes != "" {
		t.Errorf("unsigned metadata reached the owner-facing status: %+v", status)
	}
}

// TestCheck_VerifiesTheBytesTheSignerSigned pins that the manifest's canonical
// bytes are rebuilt from the SERVED DOCUMENT, not re-encoded from the parsed
// Manifest. released_at is the field that exposes the difference: a signer
// writing "…09:00:00.500Z" round-trips through time.Time as "…09:00:00.5Z", so
// a client that canonicalises the parsed struct computes different bytes from
// the ones the release key signed and rejects a perfectly good release.
func TestCheck_VerifiesTheBytesTheSignerSigned(t *testing.T) {
	ch := newFakeChannel(t)
	r := newRelease("v09", []byte("image bytes"))
	r.releasedAt = "2026-05-20T09:00:00.500Z"
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	status, err := box.client.Check(context.Background())
	if err != nil {
		t.Fatalf("a correctly signed manifest was rejected because its released_at "+
			"does not survive a time.Time round-trip: %v", err)
	}
	if !status.Available || status.LatestVersion != "v09" {
		t.Errorf("status = %+v, want v09 available", status)
	}
}

// TestManifestSignedSurfaceMatchesSigner keeps the struct definitions from
// drifting apart again: Manifest, manifestSigPayload, signedManifestKeys AND
// osdist.StableManifest must describe the SAME ten JSON keys that
// cmd/sign/pki.go:ManifestPayload signs. Adding a field to any one of them alone
// fails here — which is what happened when is_security/severity/notes existed on
// the box side and nowhere in the signer, making every manifest unverifiable the
// moment one was set.
//
// cmd/sign is package main and cannot be imported, so its field set is
// TRANSCRIBED below. That transcription is not the only thing holding it:
// cmd/sign/manifestsurface_test.go asserts the same list from the signer's side
// AND cross-checks ManifestPayload against osdist.StableManifest by
// canonicalising both, so a change made only in cmd/sign fails there.
func TestManifestSignedSurfaceMatchesSigner(t *testing.T) {
	// The field set cmd/sign/pki.go:ManifestPayload signs, transcribed.
	want := map[string]struct{}{
		"channel": {}, "latest": {}, "min_epoch": {}, "path": {},
		"released_at": {}, "roothash": {}, "size": {},
		"is_security": {}, "severity": {}, "notes": {},
	}
	if !reflect.DeepEqual(want, signedManifestKeys) {
		t.Errorf("signedManifestKeys = %v, want %v", signedManifestKeys, want)
	}
	for _, tc := range []struct {
		name string
		typ  reflect.Type
	}{
		{"Manifest", reflect.TypeOf(Manifest{})},
		{"manifestSigPayload", reflect.TypeOf(manifestSigPayload{})},
		{"osdist.StableManifest", reflect.TypeOf(osdist.StableManifest{})},
	} {
		got := map[string]struct{}{}
		for i := 0; i < tc.typ.NumField(); i++ {
			tag := strings.Split(tc.typ.Field(i).Tag.Get("json"), ",")[0]
			got[tag] = struct{}{}
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s covers %v, but the release key signs %v — "+
				"a field outside the signed set is unauthenticated data", tc.name, got, want)
		}
	}

	// The three severity fields must be `omitempty` in every definition. That is
	// not a style preference, it is THE compatibility contract: signing.Canonical
	// drops a zero value, so a release that sets none of them canonicalises to
	// the byte-identical seven-key document earlier releases signed. Drop
	// omitempty from any one of these and every signature ever issued stops
	// verifying — silently, on boxes in the field, not here.
	for _, tc := range []struct {
		name string
		typ  reflect.Type
	}{
		{"Manifest", reflect.TypeOf(Manifest{})},
		{"manifestSigPayload", reflect.TypeOf(manifestSigPayload{})},
		{"osdist.StableManifest", reflect.TypeOf(osdist.StableManifest{})},
	} {
		for i := 0; i < tc.typ.NumField(); i++ {
			f := tc.typ.Field(i)
			parts := strings.Split(f.Tag.Get("json"), ",")
			switch parts[0] {
			case "is_security", "severity", "notes":
			default:
				continue
			}
			if len(parts) < 2 || parts[1] != "omitempty" {
				t.Errorf("%s.%s has json tag %q — the severity fields MUST be omitempty, "+
					"or a manifest that sets none of them no longer canonicalises to the "+
					"seven-key bytes every already-issued signature covers",
					tc.name, f.Name, f.Tag.Get("json"))
			}
		}
	}
}

// TestSevenFieldManifestCanonicalisesUnchanged is the compatibility claim itself,
// measured rather than asserted: a manifest that sets none of the severity fields
// must canonicalise to the EXACT seven-key bytes the pre-existing signers
// produced, so every signature already issued (keys/release-cert.json's
// release-2026-08 among them) keeps verifying with no re-signing.
//
// The expected document is a hard-coded literal, not a re-derivation — a
// re-derivation would move in lockstep with the bug.
func TestSevenFieldManifestCanonicalisesUnchanged(t *testing.T) {
	const want = `{"channel":"stable","latest":"v08","min_epoch":3,` +
		`"path":"os/v08/os-core.squashfs","released_at":"2026-05-20T09:00:00Z",` +
		`"roothash":"deadbeef","size":734003200}`

	got, err := signing.Canonical(manifestSigPayload{
		Channel:    "stable",
		Latest:     "v08",
		MinEpoch:   3,
		Path:       "os/v08/os-core.squashfs",
		ReleasedAt: "2026-05-20T09:00:00Z",
		RootHash:   "deadbeef",
		Size:       734003200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("a manifest with no severity fields now canonicalises to\n  %s\nwant\n  %s\n"+
			"every signature already issued over the seven-key shape has just been invalidated", got, want)
	}

	// ...and the same document parsed through osdist's model must reproduce it
	// too, since a single stable.json serves both verification models.
	var sm osdist.StableManifest
	if err := json.Unmarshal([]byte(want), &sm); err != nil {
		t.Fatal(err)
	}
	osdistBytes, err := sm.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(osdistBytes) != want {
		t.Errorf("osdist.StableManifest re-canonicalises the same seven-key document as\n  %s\nwant\n  %s",
			osdistBytes, want)
	}
}

// ─── Hardware gate ────────────────────────────────────────────────────────────

// TestStage_NotAnImageInstall — no boot-state.json means no A/B slots, and the
// refusal must happen before anything is downloaded or written.
func TestStage_NotAnImageInstall(t *testing.T) {
	ch := newFakeChannel(t)
	ch.publishRelease(t, newRelease("v09", []byte("image bytes")))
	box := newStageBox(t, ch)
	if err := os.Remove(filepath.Join(box.dir, "boot-state.json")); err != nil {
		t.Fatal(err)
	}

	if _, err := box.client.Stage(context.Background()); !errors.Is(err, ErrNotImageInstall) {
		t.Fatalf("Stage on a machine with no A/B slots returned %v, want ErrNotImageInstall", err)
	}
	if img := box.stagedImage(t); img != nil {
		t.Error("an image was written on a machine with no slot layout")
	}
}
