package osdist

// update_test.go — Unit tests for the OS update fetch loop (OSDIST-04).
//
// Test coverage:
//   - Update detected and downloaded to the inactive slot.
//   - The release cert must chain to the PINNED anchor; a cert from any other
//     root is refused even when it authorises the key that signed everything.
//   - A retired release key (cert min_epoch below the device floor) is refused,
//     and a root-signed cert RAISES that floor — the OTA path is where
//     revocation has to bite.
//   - The manifest must carry a RELEASE-key signature; a ROOT-key one is refused.
//   - The image .sig must be a release-key signature over canonical(ImagePayload);
//     the raw-image-bytes shape this file used to demand is refused.
//   - The signed root hash must describe the bytes that arrived: a same-length
//     substituted image is refused, and an image with no published hash tree is
//     refused rather than falling back to a size check.
//   - Nothing is staged and no slot is marked pending on any rejection.
//   - Mirror failover: first mirror failure causes failover to second mirror.
//   - Already-up-to-date skips the download.
//   - RebootPrompt callback is fired after successful staging.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/cmd/verify"
	"vulos/backend/services/signing"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────
//
// The fixture builds the artifact set a real signed release actually produces,
// because the defect this file now guards against was a verifier that demanded
// a shape no signer here can emit.  It used to sign the manifest, and the RAW
// IMAGE BYTES, with the trust anchor itself — a chain `cmd/sign` cannot
// produce, so every "verified" assertion below proved nothing about production.
// A fixture that invents its own signature format cannot catch that class of
// bug; it defines it away.  So:
//
//	offline ROOT key ──issue-release-cert──▶ cert ──▶ RELEASE key
//	   │ handed to the Updater as AnchorPub          │ signs canonical(ManifestPayload)
//	   ▼                                             └ signs canonical(ImagePayload)
//	pinned anchor
//
// Every signature below is made with the same `signing` primitives cmd/sign
// calls, not a stand-in for them.

// updateTestEnv bundles everything needed by update tests.
type updateTestEnv struct {
	rootPub     ed25519.PublicKey
	rootPriv    ed25519.PrivateKey
	releasePub  ed25519.PublicKey
	releasePriv ed25519.PrivateKey
	certJSON    []byte
	sm          *SlotManager
	cacheDir    string
	// certPath is a path that does NOT exist, so a test whose channel serves no
	// release cert fails at the fetch rather than silently reading whatever is
	// installed at /etc/vulos/release-cert.json on the machine running the test.
	certPath string
}

// newUpdateTestEnv creates a throwaway two-tier PKI and a slot manager in a
// temp dir with slot-a as the active slot.
func newUpdateTestEnv(t *testing.T) *updateTestEnv {
	t.Helper()
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (root): %v", err)
	}
	releasePub, releasePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (release): %v", err)
	}
	dir := t.TempDir()
	sm, err := NewSlotManager(dir)
	if err != nil {
		t.Fatalf("NewSlotManager: %v", err)
	}
	// Write initial boot state: slot-a active.
	bs := &BootState{
		Active:        SlotA,
		Pending:       SlotNone,
		BootCounter:   0,
		LastKnownGood: SlotA,
	}
	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save initial boot state: %v", err)
	}
	e := &updateTestEnv{
		rootPub:     rootPub,
		rootPriv:    rootPriv,
		releasePub:  releasePub,
		releasePriv: releasePriv,
		sm:          sm,
		cacheDir:    dir,
		certPath:    filepath.Join(dir, "no-such-release-cert.json"),
	}
	e.certJSON = e.issueCert(t, rootPriv, releasePub, 1)
	return e
}

// issueCert produces a root-signed release certificate as it would ship on the
// channel.
func (e *updateTestEnv) issueCert(t *testing.T, rootPriv ed25519.PrivateKey, releasePub ed25519.PublicKey, minEpoch int64) []byte {
	t.Helper()
	cert, err := signing.IssueReleaseCert(rootPriv, releasePub, "test-release", time.Now().Add(24*time.Hour), minEpoch)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}
	data, err := json.Marshal(cert)
	if err != nil {
		t.Fatalf("marshal release cert: %v", err)
	}
	return data
}

// fixtureRootHash is the stand-in for a dm-verity root hash.
//
// It is NOT sha256(image) dressed up: the point is that the manifest's roothash
// is a value only a Merkle-tree construction over the image bytes can produce,
// and that the fixture's verifier RE-DERIVES it from the bytes on disk rather
// than trusting the manifest.  `veritysetup` is not available on every host
// that runs this suite, so UpdaterConfig.VerityVerify is the seam; the contract
// it stands for — "this root hash describes these exact bytes, or fail" — is
// preserved exactly, which is what the substitution tests below rely on.
func fixtureRootHash(imageBytes []byte) string {
	sum := sha256.Sum256(append([]byte("dm-verity-merkle-root\x00"), imageBytes...))
	return hex.EncodeToString(sum[:])
}

// fixtureVerity is the VerityVerify stand-in.  It re-reads the image from disk,
// re-derives its root hash, and rejects any mismatch — and it insists the hash
// tree really was fetched and written, so a channel that publishes no hashtree
// cannot quietly pass.
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

// makeManifest constructs a StableManifest whose RootHash is the dm-verity root
// hash of imageBytes and whose Path matches latest.
func (e *updateTestEnv) makeManifest(t *testing.T, latest string, imageBytes []byte) StableManifest {
	t.Helper()
	return StableManifest{
		Channel:    "stable",
		Latest:     latest,
		MinEpoch:   1,
		RootHash:   fixtureRootHash(imageBytes),
		Size:       int64(len(imageBytes)),
		ReleasedAt: time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC),
		Path:       VersionPath(latest),
	}
}

// signWithRelease wraps raw bytes in the detached .sig file format, signed by
// the release key — the only key `cmd/sign sign-manifest`/`sign-image` accept.
func (e *updateTestEnv) signWithRelease(t *testing.T, over []byte) []byte {
	t.Helper()
	return signWith(t, e.releasePriv, "test-release", over)
}

func signWith(t *testing.T, priv ed25519.PrivateKey, keyID string, over []byte) []byte {
	t.Helper()
	data, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     keyID,
		SigBytes:  signing.Sign(priv, over),
	})
	if err != nil {
		t.Fatalf("MarshalSig: %v", err)
	}
	return data
}

// signedManifestJSON returns the canonical bytes of m (what the channel serves,
// and what the release key signed) plus its detached .sig file.
func (e *updateTestEnv) signedManifestJSON(t *testing.T, m StableManifest) (jsonBytes []byte, sigFileBytes []byte) {
	t.Helper()
	canonical, err := m.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	return canonical, e.signWithRelease(t, canonical)
}

// imagePayloadOf reconstructs the ImagePayload from the manifest bytes exactly
// as the updater (and cmd/init) will: unmarshalled from the served JSON, never
// re-encoded from a parsed manifest.
func imagePayloadOf(t *testing.T, manifestBytes []byte) []byte {
	t.Helper()
	var payload verify.ImagePayload
	if err := json.Unmarshal(manifestBytes, &payload); err != nil {
		t.Fatalf("unmarshal image payload: %v", err)
	}
	canonical, err := signing.Canonical(payload)
	if err != nil {
		t.Fatalf("canonical image payload: %v", err)
	}
	return canonical
}

// signedImageSig returns the .sig file bytes for the image described by the
// manifest — a RELEASE-key signature over canonical(ImagePayload), the shape
// `cmd/sign sign-image` emits.
func (e *updateTestEnv) signedImageSig(t *testing.T, manifestBytes []byte) []byte {
	t.Helper()
	return e.signWithRelease(t, imagePayloadOf(t, manifestBytes))
}

// channelFiles assembles the complete, correctly signed artifact set a release
// publishes for one version.  Individual tests overwrite single entries to make
// exactly one thing wrong.
func (e *updateTestEnv) channelFiles(t *testing.T, latest string, imageBytes []byte) map[string][]byte {
	t.Helper()
	m := e.makeManifest(t, latest, imageBytes)
	manifestJSON, manifestSig := e.signedManifestJSON(t, m)
	return map[string][]byte{
		ReleaseCertBucketPath:       e.certJSON,
		ManifestBucketPath:          manifestJSON,
		ManifestSigBucketPath:       manifestSig,
		VersionPath(latest):         imageBytes,
		VersionSigPath(latest):      e.signedImageSig(t, manifestJSON),
		VersionHashtreePath(latest): []byte("merkle-hash-tree-for-" + latest),
	}
}

// cfg returns an UpdaterConfig wired to srv with the pinned ROOT anchor.
func (e *updateTestEnv) cfg(t *testing.T, srv *httptest.Server, runningVersion string) UpdaterConfig {
	t.Helper()
	return UpdaterConfig{
		Source:          buildSource(t, srv.Client(), srv.URL),
		SlotManager:     e.sm,
		AnchorPub:       e.rootPub,
		ReleaseCertPath: e.certPath,
		EpochFloor:      0,
		RunningVersion:  runningVersion,
		VerityVerify:    fixtureVerity,
	}
}

// bucketHandler returns an http.Handler that serves an in-memory map of
// relPath → body bytes (simulating the OS bucket).
func bucketHandler(files map[string][]byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip leading slash — relPath in the map has no leading slash.
		key := strings.TrimLeft(r.URL.Path, "/")
		data, ok := files[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(data) //nolint:errcheck
	})
}

// buildSource builds a Source pointing at the given httptest server URLs in
// order; the first URL is the override, remaining are mirrors.
func buildSource(t *testing.T, client *http.Client, urls ...string) *Source {
	t.Helper()
	if len(urls) == 0 {
		t.Fatal("buildSource: no URLs provided")
	}
	opts := []SourceOption{
		WithEmbeddedURLPath("/nonexistent/os-bucket-url"),
		WithOverride(urls[0]),
		withHTTPClient(client),
	}
	if len(urls) > 1 {
		opts = append(opts, WithMirrors(urls[1:]...))
	}
	return NewSource(opts...)
}

// ─── OSDIST04: happy path ─────────────────────────────────────────────────────

// OSDIST04_TestUpdate_HappyPath verifies the full update flow: manifest
// fetched, version newer, image downloaded, hash+sig verified, inactive slot
// staged, SetPending called, reboot prompt fired.
func OSDIST04_TestUpdate_HappyPath(t *testing.T) {
	env := newUpdateTestEnv(t)

	imageBytes := []byte("squashfs-image-content-v09")
	files := env.channelFiles(t, "v09", imageBytes)

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	rebootCalled := make(chan struct{}, 1)

	cfg := env.cfg(t, srv, "v08")
	cfg.RebootPrompt = func() { rebootCalled <- struct{}{} }
	upd, err := NewUpdater(cfg)
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}

	if err := upd.CheckAndUpdate(context.Background()); err != nil {
		t.Fatalf("CheckAndUpdate: %v", err)
	}

	// Inactive slot (b) should contain the staged image.
	slotBDir := filepath.Join(env.cacheDir, "slot-b")
	stagedPath := filepath.Join(slotBDir, "os-core.squashfs")
	data, err := os.ReadFile(stagedPath)
	if err != nil {
		t.Fatalf("read staged image: %v", err)
	}
	if !bytes.Equal(data, imageBytes) {
		t.Errorf("staged image content mismatch: got %q, want %q", data, imageBytes)
	}

	// Boot state should have slot-b as pending.
	bs, err := env.sm.Load()
	if err != nil {
		t.Fatalf("load boot state: %v", err)
	}
	if bs.Pending != SlotB {
		t.Errorf("Pending = %q, want %q", bs.Pending, SlotB)
	}
	if bs.Active != SlotA {
		t.Errorf("Active = %q, want %q", bs.Active, SlotA)
	}

	// RebootPrompt must have been called.
	select {
	case <-rebootCalled:
		// OK
	case <-time.After(time.Second):
		t.Error("RebootPrompt was not called within 1s")
	}
}

// ─── OSDIST04: already up to date ────────────────────────────────────────────

// OSDIST04_TestUpdate_AlreadyUpToDate verifies that CheckAndUpdate returns
// ErrAlreadyUpToDate when the running version matches the manifest's latest.
func OSDIST04_TestUpdate_AlreadyUpToDate(t *testing.T) {
	env := newUpdateTestEnv(t)

	imageBytes := []byte("squashfs-image-content-v08")
	files := env.channelFiles(t, "v08", imageBytes)
	// The image and its siblings must not be requested at all.
	delete(files, VersionPath("v08"))
	delete(files, VersionSigPath("v08"))
	delete(files, VersionHashtreePath("v08"))

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(env.cfg(t, srv, "v08")) // running == manifest.Latest
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}

	err = upd.CheckAndUpdate(context.Background())
	if !errors.Is(err, ErrAlreadyUpToDate) {
		t.Fatalf("expected ErrAlreadyUpToDate, got: %v", err)
	}

	// No pending slot should have been set.
	bs, err := env.sm.Load()
	if err != nil {
		t.Fatalf("load boot state: %v", err)
	}
	if bs.Pending != SlotNone {
		t.Errorf("Pending = %q, want SlotNone", bs.Pending)
	}
}

// ─── OSDIST04: hash mismatch (poisoned image) ─────────────────────────────────

// OSDIST04_TestUpdate_HashMismatch verifies that an image substituted for the
// one the release key described is rejected with ErrHashMismatch and
// SetPending is NOT called.
//
// The substitute is the SAME LENGTH as the real image on purpose. The signature
// covers a name, a size and a root hash — never the image — so a same-size
// substitution defeats every check except the root-hash binding. If that
// binding is ever weakened to "size matches", this test is what notices.
func OSDIST04_TestUpdate_HashMismatch(t *testing.T) {
	env := newUpdateTestEnv(t)

	// Real image that the manifest was signed for.
	realImage := []byte("squashfs-image-content-v09-real")
	files := env.channelFiles(t, "v09", realImage)

	// Serve a DIFFERENT image of identical length at the image URL.
	poisonedImage := []byte("squashfs-image-content-v09-XXXX")
	if len(poisonedImage) != len(realImage) {
		t.Fatalf("fixture bug: substitute must be the same length (%d vs %d)", len(poisonedImage), len(realImage))
	}
	files[VersionPath("v09")] = poisonedImage

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(env.cfg(t, srv, "v08"))
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}

	err = upd.CheckAndUpdate(context.Background())
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("expected ErrHashMismatch, got: %v", err)
	}

	// No pending slot should have been set.
	bs, err := env.sm.Load()
	if err != nil {
		t.Fatalf("load boot state: %v", err)
	}
	if bs.Pending != SlotNone {
		t.Errorf("Pending = %q after poisoned image, want SlotNone", bs.Pending)
	}

	// The staged file must NOT exist in the inactive slot.
	slotBDir := filepath.Join(env.cacheDir, "slot-b")
	stagedPath := filepath.Join(slotBDir, "os-core.squashfs")
	if _, err := os.Stat(stagedPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staged image exists in slot-b after rejected poisoned image (err=%v)", err)
	}
}

// ─── OSDIST04: image sig mismatch ────────────────────────────────────────────

// OSDIST04_TestUpdate_ImageSigMismatch verifies that an image whose detached
// .sig was made by a key the release cert does not authorise is rejected with
// ErrImageBadSignature and SetPending is NOT called.
func OSDIST04_TestUpdate_ImageSigMismatch(t *testing.T) {
	env := newUpdateTestEnv(t)

	imageBytes := []byte("squashfs-image-content-v09")
	files := env.channelFiles(t, "v09", imageBytes)

	// Sign the RIGHT bytes with a DIFFERENT (attacker) key.
	_, attackerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	files[VersionSigPath("v09")] = signWith(t, attackerPriv, "attacker-key",
		imagePayloadOf(t, files[ManifestBucketPath]))

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(env.cfg(t, srv, "v08"))
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}

	err = upd.CheckAndUpdate(context.Background())
	if !errors.Is(err, ErrImageBadSignature) {
		t.Fatalf("expected ErrImageBadSignature, got: %v", err)
	}

	// No pending slot should have been set.
	bs, err := env.sm.Load()
	if err != nil {
		t.Fatalf("load boot state: %v", err)
	}
	if bs.Pending != SlotNone {
		t.Errorf("Pending = %q after sig-mismatch image, want SlotNone", bs.Pending)
	}
}

// ─── OSDIST04: poisoned manifest (wrong manifest signature) ──────────────────

// OSDIST04_TestUpdate_PoisonedManifest verifies that a manifest whose
// signature was produced by an unknown key is rejected (ErrBadSignature) and
// the download loop does not proceed.
func OSDIST04_TestUpdate_PoisonedManifest(t *testing.T) {
	env := newUpdateTestEnv(t)

	imageBytes := []byte("squashfs-image-content-v09")
	files := env.channelFiles(t, "v09", imageBytes)

	// Sign the manifest with a DIFFERENT (attacker) key.
	_, attackerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	files[ManifestSigBucketPath] = signWith(t, attackerPriv, "attacker-key", files[ManifestBucketPath])

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(env.cfg(t, srv, "v08"))
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}

	err = upd.CheckAndUpdate(context.Background())
	if err == nil {
		t.Fatal("expected error for poisoned manifest, got nil")
	}
	if !strings.Contains(err.Error(), ErrBadSignature.Error()) {
		t.Fatalf("expected error to wrap ErrBadSignature, got: %v", err)
	}

	// No pending slot should have been set.
	bs, err := env.sm.Load()
	if err != nil {
		t.Fatalf("load boot state: %v", err)
	}
	if bs.Pending != SlotNone {
		t.Errorf("Pending = %q after poisoned manifest, want SlotNone", bs.Pending)
	}
}

// ─── OSDIST04: mirror failover ────────────────────────────────────────────────

// OSDIST04_TestUpdate_MirrorFailover verifies that when the first mirror
// returns an error for os/stable.json, Fetch automatically tries the second
// mirror and the update succeeds.
func OSDIST04_TestUpdate_MirrorFailover(t *testing.T) {
	env := newUpdateTestEnv(t)

	imageBytes := []byte("squashfs-image-content-v09")
	files := env.channelFiles(t, "v09", imageBytes)

	// Good server serves all files.
	goodSrv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(goodSrv.Close)

	// Dead server — closed immediately so it causes a connection error.
	deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadSrv.Close()

	// Use goodSrv's client for both servers (standard HTTP client for local test servers).
	src := NewSource(
		WithEmbeddedURLPath("/nonexistent/os-bucket-url"),
		WithOverride(deadSrv.URL), // first: connection error → failover
		WithMirrors(goodSrv.URL),  // second: success
		withHTTPClient(goodSrv.Client()),
	)

	cfg := env.cfg(t, goodSrv, "v08")
	cfg.Source = src
	upd, err := NewUpdater(cfg)
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}

	if err := upd.CheckAndUpdate(context.Background()); err != nil {
		t.Fatalf("CheckAndUpdate with mirror failover: %v", err)
	}

	// Verify staging succeeded.
	slotBDir := filepath.Join(env.cacheDir, "slot-b")
	stagedPath := filepath.Join(slotBDir, "os-core.squashfs")
	if _, err := os.Stat(stagedPath); err != nil {
		t.Errorf("staged image not found after mirror failover: %v", err)
	}

	bs, err := env.sm.Load()
	if err != nil {
		t.Fatalf("load boot state: %v", err)
	}
	if bs.Pending != SlotB {
		t.Errorf("Pending = %q, want SlotB", bs.Pending)
	}
}

// ─── OSDIST04: NewUpdater validation ─────────────────────────────────────────

// OSDIST04_TestNewUpdater_Validation verifies that NewUpdater returns errors
// for missing required fields.
func OSDIST04_TestNewUpdater_Validation(t *testing.T) {
	env := newUpdateTestEnv(t)
	src := NewSource(WithEmbeddedURLPath("/nonexistent"))

	cases := []struct {
		name string
		cfg  UpdaterConfig
	}{
		{
			name: "nil Source",
			cfg: UpdaterConfig{
				Source:      nil,
				SlotManager: env.sm,
				AnchorPub:   env.rootPub,
			},
		},
		{
			name: "nil SlotManager",
			cfg: UpdaterConfig{
				Source:      src,
				SlotManager: nil,
				AnchorPub:   env.rootPub,
			},
		},
		{
			name: "empty AnchorPub",
			cfg: UpdaterConfig{
				Source:      src,
				SlotManager: env.sm,
				AnchorPub:   nil,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewUpdater(tc.cfg)
			if err == nil {
				t.Errorf("NewUpdater(%s): expected error, got nil", tc.name)
			}
		})
	}
}

// ─── OSDIST04: both hash AND sig must pass ────────────────────────────────────

// OSDIST04_TestUpdate_BothVerificationsRequired verifies that the image is
// rejected when the root-hash binding would pass but the signature does not
// (the substituted-image case is covered by TestUpdate_HashMismatch).
// This test specifically exercises the "right image, corrupted signature"
// branch: one flipped bit in the signature must be fatal.
func OSDIST04_TestUpdate_BothVerificationsRequired(t *testing.T) {
	env := newUpdateTestEnv(t)

	// Correct image bytes, so the binding would pass — but the sig is corrupt.
	imageBytes := []byte("squashfs-image-content-v09")
	files := env.channelFiles(t, "v09", imageBytes)

	// Flip one bit of the raw signature, keeping the .sig envelope well-formed.
	good, err := signing.ParseSig(files[VersionSigPath("v09")])
	if err != nil {
		t.Fatalf("ParseSig: %v", err)
	}
	good.SigBytes[0] ^= 0xFF // corrupt
	corrupted, err := signing.MarshalSig(good)
	if err != nil {
		t.Fatalf("MarshalSig: %v", err)
	}
	files[VersionSigPath("v09")] = corrupted

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(env.cfg(t, srv, "v08"))
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}

	err = upd.CheckAndUpdate(context.Background())
	if !errors.Is(err, ErrImageBadSignature) {
		t.Fatalf("expected ErrImageBadSignature (hash OK, sig bad), got: %v", err)
	}

	bs, err := env.sm.Load()
	if err != nil {
		t.Fatalf("load boot state: %v", err)
	}
	if bs.Pending != SlotNone {
		t.Errorf("Pending = %q after bad sig, want SlotNone", bs.Pending)
	}
}

// ─── OSDIST04: Run loop smoke test ───────────────────────────────────────────

// OSDIST04_TestRun_ContextCancellation verifies that Run returns promptly when
// the context is cancelled.
func OSDIST04_TestRun_ContextCancellation(t *testing.T) {
	env := newUpdateTestEnv(t)

	imageBytes := []byte("squashfs-image-content-v08")
	files := env.channelFiles(t, "v08", imageBytes)

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	cfg := env.cfg(t, srv, "v08") // same → ErrAlreadyUpToDate, no download
	cfg.Interval = 24 * time.Hour // very long — won't tick during test
	upd, err := NewUpdater(cfg)
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		upd.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// OK — Run exited after context cancel
	case <-time.After(2 * time.Second):
		t.Error("Run did not stop within 2s after context cancellation")
	}
}

// ─── OSDIST04: anchor key format (base64 round-trip sanity) ──────────────────

// OSDIST04_TestAnchorKeyRoundTrip verifies that a key written via
// signing.LoadAnchor-compatible base64 encoding round-trips correctly through
// the update pipeline (integration of anchor.go's LoadAnchor format with
// update.go's direct pub key usage).
func OSDIST04_TestAnchorKeyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Write the public key in the format LoadAnchor expects.
	dir := t.TempDir()
	anchorPath := filepath.Join(dir, "trust-anchor.pub")
	encoded := base64.StdEncoding.EncodeToString(pub)
	if err := os.WriteFile(anchorPath, []byte(encoded+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loadedPub, err := signing.LoadAnchor(anchorPath)
	if err != nil {
		t.Fatalf("LoadAnchor: %v", err)
	}

	// Verify that the loaded key is the same as the original.
	if !bytes.Equal(pub, loadedPub) {
		t.Errorf("loaded key mismatch: got %x, want %x", loadedPub, pub)
	}

	// Sign something with priv and verify with loadedPub.
	msg := []byte("test-canonical-bytes")
	rawSig := signing.Sign(priv, msg)
	if !signing.Verify(loadedPub, msg, rawSig) {
		t.Error("Verify with loaded anchor key failed")
	}
}

// ─── Regressions: the shapes no signer in this repository emits ──────────────
//
// These four pin the defect directly. Until this file was corrected, the first
// two shapes were what the updater DEMANDED and what the fixture above
// produced — so the suite was green while no real release could ever pass.

// TestUpdate_RejectsAnchorSignedManifest pins the manifest half of the defect:
// a manifest signed by the ROOT anchor must be refused. `cmd/sign sign-manifest`
// requires -release-priv; nothing anywhere signs a manifest with the root key,
// so accepting one would mean accepting a shape only a confused verifier (or an
// attacker who had compromised the offline root) could produce.
func TestUpdate_RejectsAnchorSignedManifest(t *testing.T) {
	env := newUpdateTestEnv(t)

	imageBytes := []byte("squashfs-image-content-v09")
	files := env.channelFiles(t, "v09", imageBytes)
	files[ManifestSigBucketPath] = signWith(t, env.rootPriv, "root-key", files[ManifestBucketPath])

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(env.cfg(t, srv, "v08"))
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}
	err = upd.CheckAndUpdate(context.Background())
	if err == nil || !strings.Contains(err.Error(), ErrBadSignature.Error()) {
		t.Fatalf("a ROOT-key manifest signature must be rejected; got: %v", err)
	}
	assertNothingStaged(t, env)
}

// TestUpdate_RejectsRawBytesImageSignature pins the image half of the defect:
// a release-key signature over the RAW IMAGE BYTES must be refused. That is the
// exact shape update.go used to demand, and it is one `cmd/sign sign-image`
// cannot produce — it signs canonical(ImagePayload).
func TestUpdate_RejectsRawBytesImageSignature(t *testing.T) {
	env := newUpdateTestEnv(t)

	imageBytes := []byte("squashfs-image-content-v09")
	files := env.channelFiles(t, "v09", imageBytes)
	files[VersionSigPath("v09")] = env.signWithRelease(t, imageBytes) // raw bytes, right key

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(env.cfg(t, srv, "v08"))
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}
	if err := upd.CheckAndUpdate(context.Background()); !errors.Is(err, ErrImageBadSignature) {
		t.Fatalf("a raw-image-bytes signature must be rejected; got: %v", err)
	}
	assertNothingStaged(t, env)
}

// TestUpdate_RejectsCertFromWrongRoot proves the anchor is load-bearing: a
// perfectly well-formed cert, signed by SOME root, authorising the key that
// signed everything else on the channel, must still be refused because it does
// not chain to the anchor this box pins.
func TestUpdate_RejectsCertFromWrongRoot(t *testing.T) {
	env := newUpdateTestEnv(t)

	_, otherRootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	imageBytes := []byte("squashfs-image-content-v09")
	files := env.channelFiles(t, "v09", imageBytes)
	files[ReleaseCertBucketPath] = env.issueCert(t, otherRootPriv, env.releasePub, 1)

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(env.cfg(t, srv, "v08"))
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}
	if err := upd.CheckAndUpdate(context.Background()); !errors.Is(err, ErrReleaseCertInvalid) {
		t.Fatalf("a cert chaining to the wrong root must be rejected; got: %v", err)
	}
	assertNothingStaged(t, env)
}

// TestUpdate_RefusesWithoutHashtree proves the binding is mandatory rather than
// best-effort. Everything is correctly signed; only the Merkle hash tree is
// absent, so the signed root hash cannot be checked against the bytes that
// arrived. The signature alone describes an artifact; it does not authenticate
// one, so this must refuse rather than fall back to the size check.
func TestUpdate_RefusesWithoutHashtree(t *testing.T) {
	env := newUpdateTestEnv(t)

	imageBytes := []byte("squashfs-image-content-v09")
	files := env.channelFiles(t, "v09", imageBytes)
	delete(files, VersionHashtreePath("v09"))

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(env.cfg(t, srv, "v08"))
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}
	if err := upd.CheckAndUpdate(context.Background()); !errors.Is(err, ErrVerityUnavailable) {
		t.Fatalf("an image with no publishable root-hash proof must be refused; got: %v", err)
	}
	assertNothingStaged(t, env)
}

// ─── Epoch floor: OTA is where a retired release key must stop working ───────

// TestUpdate_RejectsRetiredReleaseKey proves revocation reaches the OTA path.
// The device's floor has been raised past the cert's min_epoch — i.e. the root
// has since issued a cert retiring this key — so the key must no longer be
// usable, even though every signature on the channel is individually valid.
func TestUpdate_RejectsRetiredReleaseKey(t *testing.T) {
	env := newUpdateTestEnv(t)

	store, err := signing.NewEpochStore(filepath.Join(t.TempDir(), "epoch-floor.json"))
	if err != nil {
		t.Fatalf("NewEpochStore: %v", err)
	}
	if err := store.RaiseTo(7); err != nil {
		t.Fatalf("RaiseTo: %v", err)
	}

	imageBytes := []byte("squashfs-image-content-v09")
	files := env.channelFiles(t, "v09", imageBytes) // cert min_epoch = 1, floor = 7

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	cfg := env.cfg(t, srv, "v08")
	cfg.EpochStore = store
	upd, err := NewUpdater(cfg)
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}
	if err := upd.CheckAndUpdate(context.Background()); !errors.Is(err, ErrReleaseCertInvalid) {
		t.Fatalf("a release key retired by the epoch floor must be refused; got: %v", err)
	}
	if store.Current() != 7 {
		t.Errorf("floor moved to %d; a refused cert must never change it", store.Current())
	}
	assertNothingStaged(t, env)
}

// TestUpdate_RaisesEpochFloorFromCert proves the OTA path REMEMBERS what the
// root has retired. Without this the floor sits at 0 for life and issuing a
// cert with a higher -min-epoch revokes nothing at all.
func TestUpdate_RaisesEpochFloorFromCert(t *testing.T) {
	env := newUpdateTestEnv(t)

	store, err := signing.NewEpochStore(filepath.Join(t.TempDir(), "epoch-floor.json"))
	if err != nil {
		t.Fatalf("NewEpochStore: %v", err)
	}
	if store.Current() != 0 {
		t.Fatalf("fresh store floor = %d, want 0", store.Current())
	}

	imageBytes := []byte("squashfs-image-content-v09")
	files := env.channelFiles(t, "v09", imageBytes)
	// The root has issued a cert retiring everything below epoch 5.
	files[ReleaseCertBucketPath] = env.issueCert(t, env.rootPriv, env.releasePub, 5)

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	cfg := env.cfg(t, srv, "v08")
	cfg.EpochStore = store
	upd, err := NewUpdater(cfg)
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}
	// The manifest's own min_epoch is 1, below the floor the cert just set, so
	// this check must FAIL on the manifest — and the floor must still have
	// risen, because the raise is driven by the root-signed cert alone.
	err = upd.CheckAndUpdate(context.Background())
	if err == nil || !strings.Contains(err.Error(), ErrEpochTooLow.Error()) {
		t.Fatalf("a manifest below the newly raised floor must be refused; got: %v", err)
	}
	if store.Current() != 5 {
		t.Fatalf("epoch floor = %d after a root-signed cert with min_epoch 5; want 5 — "+
			"without this raise, -min-epoch revokes nothing", store.Current())
	}
	assertNothingStaged(t, env)
}

// assertNothingStaged is the fail-closed half of every rejection test: a
// refused update must leave no pending slot and no image in the inactive slot.
func assertNothingStaged(t *testing.T, env *updateTestEnv) {
	t.Helper()
	bs, err := env.sm.Load()
	if err != nil {
		t.Fatalf("load boot state: %v", err)
	}
	if bs.Pending != SlotNone {
		t.Errorf("Pending = %q after a refused update, want SlotNone", bs.Pending)
	}
	staged := filepath.Join(env.cacheDir, "slot-b", "os-core.squashfs")
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an image was staged despite a refused update (stat err=%v)", err)
	}
}

// ─── Standard test entry points ──────────────────────────────────────────────

func TestUpdate_HappyPath(t *testing.T)        { OSDIST04_TestUpdate_HappyPath(t) }
func TestUpdate_AlreadyUpToDate(t *testing.T)  { OSDIST04_TestUpdate_AlreadyUpToDate(t) }
func TestUpdate_HashMismatch(t *testing.T)     { OSDIST04_TestUpdate_HashMismatch(t) }
func TestUpdate_ImageSigMismatch(t *testing.T) { OSDIST04_TestUpdate_ImageSigMismatch(t) }
func TestUpdate_PoisonedManifest(t *testing.T) { OSDIST04_TestUpdate_PoisonedManifest(t) }
func TestUpdate_MirrorFailover(t *testing.T)   { OSDIST04_TestUpdate_MirrorFailover(t) }
func TestNewUpdater_Validation(t *testing.T)   { OSDIST04_TestNewUpdater_Validation(t) }
func TestUpdate_BothVerificationsRequired(t *testing.T) {
	OSDIST04_TestUpdate_BothVerificationsRequired(t)
}
func TestRun_ContextCancellation(t *testing.T) { OSDIST04_TestRun_ContextCancellation(t) }
func TestAnchorKeyRoundTrip(t *testing.T)      { OSDIST04_TestAnchorKeyRoundTrip(t) }

// Ensure unused import doesn't cause a compile error — fmt is used in bucketHandler indirectly.
var _ = fmt.Sprintf

// TestAutoUpdateEnabled_DefaultOnOptOut asserts the phone-home posture of the
// background OS auto-update loop: it is ON by default (unset env) so security
// updates flow, and turns OFF only when the operator explicitly sets a disable
// value. A mis-spelled value must fail SAFE (updates stay on), never silently
// disable. This is the ONLY default outbound connection a fresh self-host box
// makes, so its default-on / explicit-opt-out contract is security-relevant.
func TestAutoUpdateEnabled_DefaultOnOptOut(t *testing.T) {
	// Default: env unset → enabled.
	os.Unsetenv(AutoUpdateEnvKey)
	if !AutoUpdateEnabled() {
		t.Fatalf("AutoUpdateEnabled() = false with %s unset; want true (default on)", AutoUpdateEnvKey)
	}

	// Explicit disable values → disabled.
	for _, v := range []string{"0", "off", "OFF", "false", "no", "disable", "disabled", "none", " off "} {
		t.Setenv(AutoUpdateEnvKey, v)
		if AutoUpdateEnabled() {
			t.Errorf("AutoUpdateEnabled() = true with %s=%q; want false (opt-out)", AutoUpdateEnvKey, v)
		}
	}

	// Any other / truthy / garbage value → stays enabled (fail-safe).
	for _, v := range []string{"1", "on", "true", "yes", "", "please-disable", "0x0"} {
		t.Setenv(AutoUpdateEnvKey, v)
		if !AutoUpdateEnabled() {
			t.Errorf("AutoUpdateEnabled() = false with %s=%q; want true (fail-safe on)", AutoUpdateEnvKey, v)
		}
	}
}
