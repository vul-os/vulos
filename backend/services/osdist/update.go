package osdist

// update.go — Periodic OS update fetch loop (OSDIST-04).
//
// # Overview
//
// Updater runs a periodic loop that:
//
//  1. Fetches os/stable.json (+ detached .sig) via the SEED-02 Source with
//     mirror failover.
//  2. Parses and verifies the manifest signature against the baked trust-anchor
//     public key (OSDIST-01 ParseAndVerify).
//  3. Compares the manifest's "latest" version to the running slot version; if
//     already up to date the loop sleeps and retries.
//  4. Downloads os/vNN/os-core.squashfs (+ detached .sig) to the INACTIVE slot
//     directory via SlotManager.StageInto (OSDIST-02).  The file is never
//     executed from S3 — it is downloaded to local storage first.
//  5. Verifies the dm-verity root hash (hex-encoded SHA-256 of the squashfs
//     image) matches the manifest's RootHash field AND that the squashfs .sig
//     verifies with the anchor key.  Verification is performed BEFORE staging
//     is committed (fail-closed; poisoned images are rejected).
//  6. Calls SlotManager.SetPending (OSDIST-02) to mark the inactive slot as
//     the next boot target and fires the RebootPrompt callback so higher-level
//     code can surface a reboot notification to the user.
//
// # Trust model
//
// URL location and cryptographic trust are orthogonal.  A poisoned mirror or
// redirected DNS cannot serve a forged OS image because every fetched file is
// verified against the baked anchor key (SIGN-02).  See source.go for the
// mirror-failover Source design.
//
// # Non-goals
//
// - Updater does not apply the update or reboot the device; it only stages and
//   signals.  Reboot policy lives above this layer.
// - Updater does not run the squashfs image live from S3.  The image is always
//   downloaded to the inactive slot, verified, and only then marked pending.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vulos/backend/services/signing"
)

// ─── Sentinel errors ─────────────────────────────────────────────────────────

// ErrHashMismatch is returned when the SHA-256 hash of the downloaded squashfs
// image does not match the RootHash field in the verified manifest.  This
// indicates a poisoned/corrupted image and the staged file is removed.
var ErrHashMismatch = errors.New("osdist/update: dm-verity root hash mismatch")

// ErrImageBadSignature is returned when the detached .sig for the squashfs
// image does not verify against the trust-anchor public key.  The staged file
// is removed before this error is returned.
var ErrImageBadSignature = errors.New("osdist/update: image signature verification failed")

// ErrAlreadyUpToDate is returned by CheckAndUpdate when the running slot
// version already matches the manifest's latest version.
var ErrAlreadyUpToDate = errors.New("osdist/update: already up to date")

// ─── UpdaterConfig ────────────────────────────────────────────────────────────

// UpdaterConfig holds the static configuration for an Updater.
type UpdaterConfig struct {
	// Source is the SEED-02 Source used to fetch manifest and image files.
	// Mirror failover is handled transparently by Source.Fetch.
	Source *Source

	// SlotManager is the OSDIST-02 slot manager used to stage the downloaded
	// image into the inactive slot and mark it pending.
	SlotManager *SlotManager

	// AnchorPub is the Ed25519 public key used to verify all fetched signatures
	// (manifest .sig and image .sig).  It is supplied by the caller; SIGN-02
	// wires the baked anchor key at startup.
	AnchorPub ed25519.PublicKey

	// EpochFloor is the minimum trusted epoch.  Manifests whose min_epoch is
	// below this value are rejected.  Supplied by the caller; SIGN-04 wires the
	// persistent floor.
	EpochFloor int64

	// RunningVersion is the version string of the OS currently running (e.g.
	// "v08").  The update loop skips the download when the manifest's latest
	// field matches this value.
	RunningVersion string

	// Interval is the polling interval between update checks.
	// Defaults to DefaultUpdateInterval when zero or negative.
	Interval time.Duration

	// RebootPrompt is called (in its own goroutine) after a new OS image has
	// been successfully staged and SetPending has been called.  It is optional;
	// a nil value is silently ignored.
	RebootPrompt func()

	// Logger is used for structured logging.  Defaults to slog.Default() when
	// nil.
	Logger *slog.Logger
}

// DefaultUpdateInterval is the default polling interval for the update loop.
const DefaultUpdateInterval = 4 * time.Hour

// ─── Updater ──────────────────────────────────────────────────────────────────

// Updater runs the periodic OS update fetch loop.
//
// Construct with NewUpdater; start with Run (blocking) or go Run(ctx).
type Updater struct {
	cfg UpdaterConfig
	log *slog.Logger
}

// NewUpdater creates an Updater from cfg.  It returns an error if required
// fields (Source, SlotManager, AnchorPub) are missing.
func NewUpdater(cfg UpdaterConfig) (*Updater, error) {
	if cfg.Source == nil {
		return nil, errors.New("osdist/update: NewUpdater: Source must not be nil")
	}
	if cfg.SlotManager == nil {
		return nil, errors.New("osdist/update: NewUpdater: SlotManager must not be nil")
	}
	if len(cfg.AnchorPub) == 0 {
		return nil, errors.New("osdist/update: NewUpdater: AnchorPub must not be empty")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultUpdateInterval
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Updater{cfg: cfg, log: log}, nil
}

// Run starts the periodic update loop.  It blocks until ctx is cancelled.
// The first check is performed immediately on entry; subsequent checks occur
// every cfg.Interval.
func (u *Updater) Run(ctx context.Context) {
	u.log.Info("osdist/update: update loop started",
		"interval", u.cfg.Interval,
		"running_version", u.cfg.RunningVersion)

	// Perform the first check immediately.
	u.runOnce(ctx)

	ticker := time.NewTicker(u.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			u.log.Info("osdist/update: update loop stopped")
			return
		case <-ticker.C:
			u.runOnce(ctx)
		}
	}
}

// runOnce executes a single update check iteration, logging errors but not
// propagating them (the loop continues regardless).
func (u *Updater) runOnce(ctx context.Context) {
	if err := u.CheckAndUpdate(ctx); err != nil {
		if errors.Is(err, ErrAlreadyUpToDate) {
			u.log.Debug("osdist/update: up to date", "version", u.cfg.RunningVersion)
			return
		}
		u.log.Error("osdist/update: check failed", "error", err)
	}
}

// CheckAndUpdate performs a single update check.
//
// It returns ErrAlreadyUpToDate when the running version already matches the
// manifest's latest.  On a successful update the inactive slot is staged,
// SetPending is called, and the RebootPrompt callback is fired.
//
// The function is fail-closed: any hash or signature mismatch causes the
// staged file to be removed and an error to be returned without calling
// SetPending.
func (u *Updater) CheckAndUpdate(ctx context.Context) error {
	// ── 1. Fetch manifest ────────────────────────────────────────────────────
	manifest, err := u.fetchManifest(ctx)
	if err != nil {
		return fmt.Errorf("osdist/update: fetch manifest: %w", err)
	}

	u.log.Info("osdist/update: manifest fetched",
		"latest", manifest.Latest,
		"running", u.cfg.RunningVersion)

	// ── 2. Version comparison ────────────────────────────────────────────────
	if manifest.Latest == u.cfg.RunningVersion {
		return ErrAlreadyUpToDate
	}

	u.log.Info("osdist/update: new version available",
		"from", u.cfg.RunningVersion,
		"to", manifest.Latest)

	// ── 3. Load boot state and determine inactive slot ───────────────────────
	bs, err := u.cfg.SlotManager.Load()
	if err != nil {
		return fmt.Errorf("osdist/update: load boot state: %w", err)
	}

	inactive, err := u.cfg.SlotManager.InactiveSlot(bs)
	if err != nil {
		return fmt.Errorf("osdist/update: determine inactive slot: %w", err)
	}

	u.log.Info("osdist/update: staging to inactive slot", "slot", inactive)

	// ── 4. Download + stage squashfs into inactive slot ──────────────────────
	// We download the image into a temp file inside the slot directory, verify
	// hash and signature, and only then rename (via StageInto) to the final
	// destination.  If verification fails the temp file is removed.
	stagedPath, err := u.downloadToSlot(ctx, manifest, bs, inactive)
	if err != nil {
		return fmt.Errorf("osdist/update: download to slot: %w", err)
	}

	// ── 5. Mark pending + fire reboot prompt ─────────────────────────────────
	if _, err := u.cfg.SlotManager.SetPending(bs, inactive); err != nil {
		return fmt.Errorf("osdist/update: set pending slot %s: %w", inactive, err)
	}

	u.log.Info("osdist/update: update staged successfully",
		"version", manifest.Latest,
		"slot", inactive,
		"path", stagedPath)

	if u.cfg.RebootPrompt != nil {
		go u.cfg.RebootPrompt()
	}

	return nil
}

// ─── fetchManifest ────────────────────────────────────────────────────────────

// fetchManifest fetches os/stable.json and os/stable.json.sig from the Source,
// verifies the signature, and returns the parsed manifest.
func (u *Updater) fetchManifest(ctx context.Context) (*StableManifest, error) {
	// Fetch manifest JSON.
	manifestRC, err := u.cfg.Source.Fetch(ctx, "os/stable.json")
	if err != nil {
		return nil, fmt.Errorf("fetch os/stable.json: %w", err)
	}
	manifestData, err := io.ReadAll(manifestRC)
	manifestRC.Close()
	if err != nil {
		return nil, fmt.Errorf("read os/stable.json: %w", err)
	}

	// Fetch detached manifest signature.
	sigRC, err := u.cfg.Source.Fetch(ctx, "os/stable.json.sig")
	if err != nil {
		return nil, fmt.Errorf("fetch os/stable.json.sig: %w", err)
	}
	sigFileData, err := io.ReadAll(sigRC)
	sigRC.Close()
	if err != nil {
		return nil, fmt.Errorf("read os/stable.json.sig: %w", err)
	}

	// Parse the detached .sig file format.
	detachedSig, err := signing.ParseSig(sigFileData)
	if err != nil {
		return nil, fmt.Errorf("parse manifest .sig: %w", err)
	}

	// ParseAndVerify: unmarshal + verify Ed25519 sig + enforce epoch floor.
	manifest, err := ParseAndVerify(manifestData, detachedSig.SigBytes, u.cfg.AnchorPub, u.cfg.EpochFloor)
	if err != nil {
		return nil, fmt.Errorf("verify manifest: %w", err)
	}

	return manifest, nil
}

// ─── downloadToSlot ──────────────────────────────────────────────────────────

// downloadToSlot downloads the squashfs image described in manifest to the
// inactive slot, verifies the dm-verity root hash and the detached image
// signature, then stages it atomically.
//
// It returns the final path of the staged file on success, or an error if any
// verification step fails (in which case the partial download is removed).
func (u *Updater) downloadToSlot(
	ctx context.Context,
	manifest *StableManifest,
	bs *BootState,
	slot Slot,
) (string, error) {
	imagePath := VersionPath(manifest.Latest)  // e.g. "os/v08/os-core.squashfs"
	sigPath := VersionSigPath(manifest.Latest) // e.g. "os/v08/os-core.squashfs.sig"

	// ── 4a. Download image to a local temp file ───────────────────────────────
	// We need to compute the hash while downloading, so we tee to a hasher.
	slotDir, err := u.cfg.SlotManager.SlotDir(slot)
	if err != nil {
		return "", fmt.Errorf("slot dir for %s: %w", slot, err)
	}

	imageRC, err := u.cfg.Source.Fetch(ctx, imagePath)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", imagePath, err)
	}
	defer imageRC.Close()

	// Write to a temp file, compute SHA-256 simultaneously.
	tmpFile, err := os.CreateTemp(slotDir, "os-core.squashfs.*.download")
	if err != nil {
		return "", fmt.Errorf("create temp for image: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Ensure cleanup on any failure path.
	cleanupTmp := func() {
		tmpFile.Close()
		os.Remove(tmpPath)
	}

	hasher := sha256.New()
	tee := io.TeeReader(imageRC, hasher)

	if _, err := io.Copy(tmpFile, tee); err != nil {
		cleanupTmp()
		return "", fmt.Errorf("download %s: %w", imagePath, err)
	}
	if err := tmpFile.Sync(); err != nil {
		cleanupTmp()
		return "", fmt.Errorf("sync downloaded image: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("close downloaded image: %w", err)
	}

	// ── 4b. Verify dm-verity root hash ────────────────────────────────────────
	gotHash := hex.EncodeToString(hasher.Sum(nil))
	wantHash := strings.ToLower(strings.TrimSpace(manifest.RootHash))
	if !strings.EqualFold(gotHash, wantHash) {
		os.Remove(tmpPath)
		return "", fmt.Errorf("%w: got %s, want %s", ErrHashMismatch, gotHash, wantHash)
	}

	// ── 4c. Fetch + verify detached image signature ───────────────────────────
	imgSigRC, err := u.cfg.Source.Fetch(ctx, sigPath)
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("fetch image sig %s: %w", sigPath, err)
	}
	imgSigData, err := io.ReadAll(imgSigRC)
	imgSigRC.Close()
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("read image sig: %w", err)
	}

	detachedImgSig, err := signing.ParseSig(imgSigData)
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("parse image .sig: %w", err)
	}

	// The image signature is over the raw image bytes.  Re-read the temp file
	// to get the canonical bytes for verification.
	imageBytes, err := os.ReadFile(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("read temp image for sig verification: %w", err)
	}

	if !signing.Verify(u.cfg.AnchorPub, imageBytes, detachedImgSig.SigBytes) {
		os.Remove(tmpPath)
		return "", ErrImageBadSignature
	}

	// ── 4d. Stage atomically into inactive slot ───────────────────────────────
	// StageInto streams a reader into the slot directory using temp-then-rename,
	// so we open the verified temp file as the source reader.
	verifiedReader := bytes.NewReader(imageBytes)
	if err := u.cfg.SlotManager.StageInto(bs, slot, "os-core.squashfs", verifiedReader); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("stage into slot %s: %w", slot, err)
	}

	// Remove the download temp file now that StageInto has placed the final copy.
	os.Remove(tmpPath)

	finalPath := filepath.Join(slotDir, "os-core.squashfs")
	return finalPath, nil
}
