package osdist

// update_test.go — Unit tests for the OS update fetch loop (OSDIST-04).
//
// Test coverage:
//   - Update detected and downloaded to the inactive slot.
//   - Dm-verity root hash verified before staging; hash mismatch rejected.
//   - Detached image signature verified before staging; sig mismatch rejected.
//   - Both hash AND signature must pass before SetPending is called.
//   - Poisoned manifest (wrong manifest signature) is rejected.
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

	"vulos/backend/services/signing"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// updateTestEnv bundles everything needed by update tests.
type updateTestEnv struct {
	pub      ed25519.PublicKey
	priv     ed25519.PrivateKey
	sm       *SlotManager
	cacheDir string
}

// newUpdateTestEnv creates a fresh key pair and slot manager in a temp dir with
// slot-a as the active slot.
func newUpdateTestEnv(t *testing.T) *updateTestEnv {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
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
	return &updateTestEnv{pub: pub, priv: priv, sm: sm, cacheDir: dir}
}

// makeManifest constructs a StableManifest whose RootHash matches the provided
// imageBytes (SHA-256 hex) and whose Path matches latest.
func (e *updateTestEnv) makeManifest(t *testing.T, latest string, imageBytes []byte) StableManifest {
	t.Helper()
	hash := sha256.Sum256(imageBytes)
	return StableManifest{
		Channel:    "stable",
		Latest:     latest,
		MinEpoch:   1,
		RootHash:   hex.EncodeToString(hash[:]),
		Size:       int64(len(imageBytes)),
		ReleasedAt: time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC),
		Path:       VersionPath(latest),
	}
}

// signedManifestJSON returns the JSON bytes of m and a detached .sig file
// bytes produced by signing with priv.
func (e *updateTestEnv) signedManifestJSON(t *testing.T, m StableManifest) (jsonBytes []byte, sigFileBytes []byte) {
	t.Helper()
	jsonBytes, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal manifest: %v", err)
	}
	canonical, err := m.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	rawSig := signing.Sign(e.priv, canonical)
	sigBytes, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "test-key",
		SigBytes:  rawSig,
	})
	if err != nil {
		t.Fatalf("MarshalSig manifest: %v", err)
	}
	return jsonBytes, sigBytes
}

// signedImageSig returns the .sig file bytes for the raw image bytes, signed
// with priv.
func (e *updateTestEnv) signedImageSig(t *testing.T, imageBytes []byte) []byte {
	t.Helper()
	rawSig := signing.Sign(e.priv, imageBytes)
	sigBytes, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "test-key",
		SigBytes:  rawSig,
	})
	if err != nil {
		t.Fatalf("MarshalSig image: %v", err)
	}
	return sigBytes
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
	m := env.makeManifest(t, "v09", imageBytes)
	manifestJSON, manifestSigBytes := env.signedManifestJSON(t, m)
	imageSigBytes := env.signedImageSig(t, imageBytes)

	files := map[string][]byte{
		"os/stable.json":              manifestJSON,
		"os/stable.json.sig":          manifestSigBytes,
		"os/v09/os-core.squashfs":     imageBytes,
		"os/v09/os-core.squashfs.sig": imageSigBytes,
	}

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	rebootCalled := make(chan struct{}, 1)

	upd, err := NewUpdater(UpdaterConfig{
		Source:         buildSource(t, srv.Client(), srv.URL),
		SlotManager:    env.sm,
		AnchorPub:      env.pub,
		EpochFloor:     0,
		RunningVersion: "v08",
		RebootPrompt:   func() { rebootCalled <- struct{}{} },
	})
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
	m := env.makeManifest(t, "v08", imageBytes)
	manifestJSON, manifestSigBytes := env.signedManifestJSON(t, m)

	files := map[string][]byte{
		"os/stable.json":     manifestJSON,
		"os/stable.json.sig": manifestSigBytes,
		// image intentionally absent — should not be requested
	}

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(UpdaterConfig{
		Source:         buildSource(t, srv.Client(), srv.URL),
		SlotManager:    env.sm,
		AnchorPub:      env.pub,
		EpochFloor:     0,
		RunningVersion: "v08", // same as manifest
	})
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

// OSDIST04_TestUpdate_HashMismatch verifies that an image whose SHA-256 hash
// does not match the manifest's RootHash is rejected with ErrHashMismatch and
// SetPending is NOT called.
func OSDIST04_TestUpdate_HashMismatch(t *testing.T) {
	env := newUpdateTestEnv(t)

	// Real image that the manifest was signed for.
	realImage := []byte("squashfs-image-content-v09-real")
	m := env.makeManifest(t, "v09", realImage)
	manifestJSON, manifestSigBytes := env.signedManifestJSON(t, m)

	// Serve a DIFFERENT image (poisoned) at the image URL.
	poisonedImage := []byte("squashfs-image-content-v09-POISONED")
	poisonedSig := env.signedImageSig(t, poisonedImage) // sig matches poisoned, not real

	files := map[string][]byte{
		"os/stable.json":              manifestJSON,
		"os/stable.json.sig":          manifestSigBytes,
		"os/v09/os-core.squashfs":     poisonedImage, // wrong content
		"os/v09/os-core.squashfs.sig": poisonedSig,
	}

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(UpdaterConfig{
		Source:         buildSource(t, srv.Client(), srv.URL),
		SlotManager:    env.sm,
		AnchorPub:      env.pub,
		EpochFloor:     0,
		RunningVersion: "v08",
	})
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
// .sig does not verify against the anchor key is rejected with
// ErrImageBadSignature and SetPending is NOT called.
func OSDIST04_TestUpdate_ImageSigMismatch(t *testing.T) {
	env := newUpdateTestEnv(t)

	imageBytes := []byte("squashfs-image-content-v09")
	m := env.makeManifest(t, "v09", imageBytes)
	manifestJSON, manifestSigBytes := env.signedManifestJSON(t, m)

	// Sign with a DIFFERENT (attacker) key.
	_, attackerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	rawSig := signing.Sign(attackerPriv, imageBytes)
	attackerSigBytes, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "attacker-key",
		SigBytes:  rawSig,
	})
	if err != nil {
		t.Fatalf("MarshalSig: %v", err)
	}

	files := map[string][]byte{
		"os/stable.json":              manifestJSON,
		"os/stable.json.sig":          manifestSigBytes,
		"os/v09/os-core.squashfs":     imageBytes,
		"os/v09/os-core.squashfs.sig": attackerSigBytes, // wrong key
	}

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(UpdaterConfig{
		Source:         buildSource(t, srv.Client(), srv.URL),
		SlotManager:    env.sm,
		AnchorPub:      env.pub,
		EpochFloor:     0,
		RunningVersion: "v08",
	})
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
	m := env.makeManifest(t, "v09", imageBytes)
	manifestJSON, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// Sign manifest with a DIFFERENT (attacker) key.
	_, attackerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	canonical, err := m.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	rawSig := signing.Sign(attackerPriv, canonical)
	attackerSigBytes, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "attacker-key",
		SigBytes:  rawSig,
	})
	if err != nil {
		t.Fatalf("MarshalSig: %v", err)
	}

	files := map[string][]byte{
		"os/stable.json":     manifestJSON,
		"os/stable.json.sig": attackerSigBytes, // wrong key
	}

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(UpdaterConfig{
		Source:         buildSource(t, srv.Client(), srv.URL),
		SlotManager:    env.sm,
		AnchorPub:      env.pub,
		EpochFloor:     0,
		RunningVersion: "v08",
	})
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
	m := env.makeManifest(t, "v09", imageBytes)
	manifestJSON, manifestSigBytes := env.signedManifestJSON(t, m)
	imageSigBytes := env.signedImageSig(t, imageBytes)

	files := map[string][]byte{
		"os/stable.json":              manifestJSON,
		"os/stable.json.sig":          manifestSigBytes,
		"os/v09/os-core.squashfs":     imageBytes,
		"os/v09/os-core.squashfs.sig": imageSigBytes,
	}

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

	upd, err := NewUpdater(UpdaterConfig{
		Source:         src,
		SlotManager:    env.sm,
		AnchorPub:      env.pub,
		EpochFloor:     0,
		RunningVersion: "v08",
	})
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
				AnchorPub:   env.pub,
			},
		},
		{
			name: "nil SlotManager",
			cfg: UpdaterConfig{
				Source:      src,
				SlotManager: nil,
				AnchorPub:   env.pub,
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
// rejected when the hash matches but the signature does not (and vice versa the
// hash mismatch case is already covered by TestUpdate_HashMismatch).
// This test specifically exercises the "hash OK but sig bad" branch.
func OSDIST04_TestUpdate_BothVerificationsRequired(t *testing.T) {
	env := newUpdateTestEnv(t)

	// Hash will match (correct image bytes), but sig is wrong.
	imageBytes := []byte("squashfs-image-content-v09")
	m := env.makeManifest(t, "v09", imageBytes)
	manifestJSON, manifestSigBytes := env.signedManifestJSON(t, m)

	// Corrupt the image sig by flipping one byte of the raw sig before
	// wrapping in the .sig file format.
	rawSig := signing.Sign(env.priv, imageBytes)
	rawSig[0] ^= 0xFF // corrupt
	corruptedSigBytes, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "test-key",
		SigBytes:  rawSig,
	})
	if err != nil {
		t.Fatalf("MarshalSig: %v", err)
	}

	files := map[string][]byte{
		"os/stable.json":              manifestJSON,
		"os/stable.json.sig":          manifestSigBytes,
		"os/v09/os-core.squashfs":     imageBytes,        // hash will match
		"os/v09/os-core.squashfs.sig": corruptedSigBytes, // sig will fail
	}

	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(UpdaterConfig{
		Source:         buildSource(t, srv.Client(), srv.URL),
		SlotManager:    env.sm,
		AnchorPub:      env.pub,
		EpochFloor:     0,
		RunningVersion: "v08",
	})
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
	m := env.makeManifest(t, "v08", imageBytes)
	manifestJSON, manifestSigBytes := env.signedManifestJSON(t, m)

	files := map[string][]byte{
		"os/stable.json":     manifestJSON,
		"os/stable.json.sig": manifestSigBytes,
	}
	srv := httptest.NewServer(bucketHandler(files))
	t.Cleanup(srv.Close)

	upd, err := NewUpdater(UpdaterConfig{
		Source:         buildSource(t, srv.Client(), srv.URL),
		SlotManager:    env.sm,
		AnchorPub:      env.pub,
		EpochFloor:     0,
		RunningVersion: "v08",          // same → ErrAlreadyUpToDate, no download
		Interval:       24 * time.Hour, // very long — won't tick during test
	})
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
