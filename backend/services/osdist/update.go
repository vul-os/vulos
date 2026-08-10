package osdist

// update.go — Periodic OS update fetch loop (OSDIST-04).
//
// # The scheme this file verifies, and the one it used to verify
//
// Until this was corrected, the updater verified BOTH the manifest and the
// image against cfg.AnchorPub — the ROOT anchor — and it verified the image
// signature over the RAW IMAGE BYTES.  Nothing in this repository produces
// either shape, and nothing was going to:
//
//	cmd/sign issue-release-cert → ROOT key over canonical(CertBody)
//	cmd/sign sign-manifest      → RELEASE key over canonical(ManifestPayload)
//	cmd/sign sign-image         → RELEASE key over canonical(ImagePayload)
//	                              {path, roothash, size, min_epoch, released_at}
//
// `sign-manifest` and `sign-image` both REQUIRE -release-priv; there is no
// command anywhere that signs a manifest or an image with the root key.  The
// release workflow (.github/workflows/release.yml) publishes trust-anchor.pub
// and release-cert.json and instructs the maintainer to sign offline with
// `sign-image`.  So the two ends disagreed on the key at both steps, and on the
// bytes at the image step.
//
// A third disagreement sat between them: step 4b compared SHA-256 of the
// downloaded image to manifest.RootHash.  RootHash is a dm-verity root hash —
// `veritysetup format` over a Merkle tree with a salt (build.sh VERITY-01,
// scripts/verity/gen-verity.sh) — never the SHA-256 of the image.  That
// comparison could not succeed for a real release either.
//
// The consequence was not a hole, it was a WALL: for any artifact this project
// can sign, CheckAndUpdate fails at the manifest step with ErrBadSignature and
// no image is ever staged.  It was never observed because nothing constructs an
// Updater — cmd/server wires services/ota instead (main.go, routes_ota.go) —
// and because update_test.go signed the manifest, and the raw image bytes, with
// the anchor key itself, i.e. it proved a chain production cannot produce.
//
// # What is verified now
//
// The chain cmd/sign actually emits, in the order cmd/verify and cmd/init use:
//
//  1. Fetch os/release-cert.json (falling back to the baked seed copy at
//     cfg.ReleaseCertPath) and validate it against cfg.AnchorPub, the pinned
//     ROOT anchor: root signature, wall-clock expiry, and cert.MinEpoch not
//     below the device's epoch floor.  This yields the RELEASE key, and it is
//     the only way to obtain one.
//  2. Raise the epoch floor from that ROOT-SIGNED cert (EpochStore.
//     RaiseFromReleaseCert).  OTA is exactly where a retired release key must
//     stop working, and the floor only rises when a root-signed cert says so —
//     never from the release-key-signed manifest, which an attacker holding a
//     stolen release key could otherwise pin at MaxInt64 forever.
//  3. Fetch os/stable.json (+ .sig) and verify it with the RELEASE key,
//     enforcing the same floor (ParseAndVerify).
//  4. Download os/vNN/os-core.squashfs to the INACTIVE slot's temp area, and
//     verify os/vNN/os-core.squashfs.sig as a RELEASE-key signature over
//     canonical(ImagePayload) — the payload reconstructed from the manifest's
//     own bytes.
//  5. BIND that signed description to the bytes actually downloaded: the byte
//     length must equal ImagePayload.Size, and `veritysetup verify` must accept
//     ImagePayload.RootHash for the downloaded image against the sibling
//     os/vNN/os-core.hashtree.
//  6. Only then StageInto + SetPending, and fire RebootPrompt.
//
// # Why the verity step is mandatory rather than best-effort
//
// An ImagePayload signature covers a NAME, a SIZE and a ROOT HASH.  It does not
// cover the image.  Verifying it in isolation proves someone with the release
// key once described an artifact; it says nothing about the bytes that arrived.
// The root hash is the only thing that ties the two together, and the only way
// to compute it from bytes is the same Merkle-tree construction veritysetup
// performs.  A file of the right length is not authentication, so where
// veritysetup or the hash tree is unavailable this refuses to stage
// (ErrVerityUnavailable) rather than downgrading to a size check.
//
// # Trust model
//
// URL location and cryptographic trust are orthogonal.  A poisoned mirror or
// redirected DNS cannot serve a forged OS image because the release cert chains
// to the baked anchor (SIGN-02) and everything else chains to the cert.  See
// source.go for the mirror-failover Source design.
//
// # Non-goals
//
// - Updater does not apply the update or reboot the device; it only stages and
//   signals.  Reboot policy lives above this layer.
// - Updater does not run the squashfs image live from S3.  The image is always
//   downloaded to the inactive slot, verified, and only then marked pending.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	// cmd/verify is a library that happens to live under cmd/ ("Package verify
	// implements per-boot-stage signature verification"); cmd/init and
	// services/installer import it the same way.  Importing the ImagePayload
	// definition rather than copying it is deliberate: a third independently
	// maintained copy of the signing surface is how the two ends came to
	// disagree in the first place.
	"vulos/backend/cmd/verify"
	"vulos/backend/services/signing"
)

// ─── Sentinel errors ─────────────────────────────────────────────────────────

// ErrHashMismatch is returned when the signed ImagePayload does not describe
// the bytes that were downloaded — either the byte length differs from
// ImagePayload.Size, or `veritysetup verify` rejects ImagePayload.RootHash for
// the downloaded image.  The partial download is removed.
var ErrHashMismatch = errors.New("osdist/update: signed image payload does not describe the downloaded image")

// ErrImageBadSignature is returned when the detached .sig for the squashfs
// image does not verify as a release-key signature over canonical(ImagePayload).
// The staged file is removed before this error is returned.
var ErrImageBadSignature = errors.New("osdist/update: image signature verification failed against the certified release key")

// ErrAlreadyUpToDate is returned by CheckAndUpdate when the running slot
// version already matches the manifest's latest version.
var ErrAlreadyUpToDate = errors.New("osdist/update: already up to date")

// ErrReleaseCertMissing is returned when the release-key certificate can be
// obtained neither from the channel nor from the baked seed copy.  Without it
// the release key is unknown and nothing can be verified.
var ErrReleaseCertMissing = errors.New("osdist/update: release certificate unavailable — refusing to verify anything")

// ErrReleaseCertInvalid is returned when the release certificate does not
// validate against the pinned root anchor, has expired, or carries a min_epoch
// below the device's epoch floor (a retired release key).
var ErrReleaseCertInvalid = errors.New("osdist/update: release certificate rejected")

// ErrVerityUnavailable is returned when the dm-verity root hash of the
// downloaded image cannot be checked at all — no hash tree published beside the
// image, or no veritysetup on this box.  Fail-closed: nothing is staged,
// because the root hash is the only thing binding the signature to the bytes.
var ErrVerityUnavailable = errors.New("osdist/update: cannot verify the image's dm-verity root hash — refusing to stage an unbound image")

// ─── UpdaterConfig ────────────────────────────────────────────────────────────

// UpdaterConfig holds the static configuration for an Updater.
type UpdaterConfig struct {
	// Source is the SEED-02 Source used to fetch manifest and image files.
	// Mirror failover is handled transparently by Source.Fetch.
	Source *Source

	// SlotManager is the OSDIST-02 slot manager used to stage the downloaded
	// image into the inactive slot and mark it pending.
	SlotManager *SlotManager

	// AnchorPub is the pinned ROOT trust anchor (SIGN-02 wires the baked
	// /etc/vulos/trust-anchor.pub).  It signs exactly one thing: the release
	// certificate.  It does NOT verify manifests or images directly — those
	// carry RELEASE-key signatures, and the release key is only ever obtained
	// by validating a cert against this anchor.
	AnchorPub ed25519.PublicKey

	// ReleaseCertPath is the local, baked copy of the release certificate, used
	// when the channel does not serve one at ReleaseCertBucketPath.  Defaults to
	// signing.DefaultReleaseCertPath (/etc/vulos/release-cert.json), the same
	// file cmd/init reads, so a box and its updater trust one file rather than
	// two policies that can drift apart.
	ReleaseCertPath string

	// EpochFloor is a static minimum trusted epoch, used only when EpochStore is
	// nil.  Certs and manifests below it are rejected.
	EpochFloor int64

	// EpochStore, when non-nil, is the authority on the rollback floor: it
	// supplies the floor in place of EpochFloor, and it is RAISED to the
	// min_epoch of every root-signed release cert that validates
	// (RaiseFromReleaseCert).  That raise is what makes `issue-release-cert
	// -min-epoch N` actually retire a release key — without a store the floor
	// never moves and revocation revokes nothing.
	EpochStore *signing.EpochStore

	// VerityVerify proves that rootHash is the dm-verity root hash of the image
	// at imagePath, given the Merkle hash tree at hashtreePath.  Defaults to
	// verityVerifyWithTool, which shells out to `veritysetup verify` — the same
	// primitive services/installer uses and the same one the initramfs hook will
	// use at boot.  Injectable so tests can exercise the surrounding chain on
	// hosts without cryptsetup; production callers must leave it nil.
	VerityVerify func(ctx context.Context, imagePath, hashtreePath, rootHash string) error

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

	// Store, when non-nil, is updated with the outcome of every check
	// (RecordSuccess/RecordError) so the /api/os/update/status HTTP handler
	// (see handlers.go) reflects the periodic loop's real state instead of
	// staying permanently empty/"never checked".
	Store *StatusStore
}

// DefaultUpdateInterval is the default polling interval for the update loop.
const DefaultUpdateInterval = 4 * time.Hour

// AutoUpdateEnvKey is the operator opt-out for the background OS auto-update
// loop. The signed auto-update check (a read-only GET of os/stable.json from the
// OS distribution bucket) is the ONLY outbound connection a fresh, un-configured
// self-hosted box makes by default — it carries no usage/telemetry data, but it
// does reach Vulos-operated (or mirror) infrastructure. Privacy-maximal
// operators who prefer a box with zero default egress can turn it off here and
// pull updates manually via the OS update admin endpoint.
const AutoUpdateEnvKey = "VULOS_OS_AUTOUPDATE"

// AutoUpdateEnabled reports whether the background OS auto-update loop should
// run. It is ON by default (security updates matter) and only turns OFF when
// VULOS_OS_AUTOUPDATE is explicitly set to a disable value
// (0/off/false/no/disable/disabled/none — case-insensitive). Any other value,
// including unset, leaves auto-update enabled. Fail-safe direction: a
// mis-spelled value keeps updates flowing rather than silently disabling them.
func AutoUpdateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(AutoUpdateEnvKey))) {
	case "0", "off", "false", "no", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// ─── Updater ──────────────────────────────────────────────────────────────────

// Updater runs the periodic OS update fetch loop.
//
// Construct with NewUpdater; start with Run (blocking) or go Run(ctx).
type Updater struct {
	cfg UpdaterConfig
	log *slog.Logger

	mu         sync.Mutex
	lastLatest string // manifest.Latest from the most recent successful fetchManifest
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
	if cfg.ReleaseCertPath == "" {
		cfg.ReleaseCertPath = signing.DefaultReleaseCertPath
	}
	if cfg.VerityVerify == nil {
		cfg.VerityVerify = verityVerifyWithTool
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
// propagating them (the loop continues regardless). It also records the
// outcome into cfg.Store (when configured) so the status HTTP handler can
// report the periodic loop's real state.
func (u *Updater) runOnce(ctx context.Context) {
	err := u.CheckAndUpdate(ctx)
	if err == nil {
		if u.cfg.Store != nil {
			u.cfg.Store.RecordSuccess(u.getLastLatest())
		}
		return
	}
	if errors.Is(err, ErrAlreadyUpToDate) {
		u.log.Debug("osdist/update: up to date", "version", u.cfg.RunningVersion)
		if u.cfg.Store != nil {
			u.cfg.Store.RecordSuccess("") // "" == already up to date, per RecordSuccess's contract
		}
		return
	}
	u.log.Error("osdist/update: check failed", "error", err)
	if u.cfg.Store != nil {
		u.cfg.Store.RecordError(err)
	}
}

// setLastLatest/getLastLatest track the most recently fetched manifest's
// Latest field so runOnce can report it to Store on a successful stage
// (CheckAndUpdate itself only returns error, so this is how the version
// survives past the fetch step for RecordSuccess).
func (u *Updater) setLastLatest(v string) {
	u.mu.Lock()
	u.lastLatest = v
	u.mu.Unlock()
}

func (u *Updater) getLastLatest() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastLatest
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
	// ── 0. The release key, and the offline root's statement about it ────────
	// Nothing below can be verified without this, and this is the only step the
	// pinned anchor participates in directly.
	releasePub, floor, err := u.certifiedReleaseKey(ctx)
	if err != nil {
		return err
	}

	// ── 1. Fetch manifest ────────────────────────────────────────────────────
	manifest, manifestData, err := u.fetchManifest(ctx, releasePub, floor)
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
	stagedPath, err := u.downloadToSlot(ctx, manifest, manifestData, releasePub, bs, inactive)
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

// ─── certifiedReleaseKey ──────────────────────────────────────────────────────

// certifiedReleaseKey resolves the RELEASE public key everything else on the
// channel was signed with, and refuses to return one the pinned root anchor has
// not certified.  It also returns the epoch floor in force for this check.
//
// The cert is taken from the channel when it serves one, and from the baked
// seed copy otherwise.  Preferring the channel's copy is safe — the cert is
// inert until ValidateReleaseCert accepts it against the pinned anchor — and it
// is what allows a release key to be rotated without re-flashing every box.
//
// The floor is READ before the cert is validated and RAISED after, so a cert
// carrying a min_epoch below the floor is refused by this call rather than
// silently raising the floor to its own lower value (RaiseTo is monotonic, but
// refusing outright is the caller's job and this is that caller).
func (u *Updater) certifiedReleaseKey(ctx context.Context) (ed25519.PublicKey, int64, error) {
	floor := u.cfg.EpochFloor
	if u.cfg.EpochStore != nil {
		floor = u.cfg.EpochStore.Current()
	}

	certData, err := u.fetchFile(ctx, ReleaseCertBucketPath)
	if err != nil {
		// Fall back to the baked seed copy — a channel that publishes no cert
		// is still verifiable by a box whose image shipped one.
		local, lerr := os.ReadFile(u.cfg.ReleaseCertPath)
		if lerr != nil {
			return nil, 0, fmt.Errorf("%w: channel: %v; seed %s: %v",
				ErrReleaseCertMissing, err, u.cfg.ReleaseCertPath, lerr)
		}
		certData = local
	}

	cert, err := signing.ParseReleaseCert(certData)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: parse: %v", ErrReleaseCertInvalid, err)
	}
	if err := signing.ValidateReleaseCert(u.cfg.AnchorPub, cert); err != nil {
		return nil, 0, fmt.Errorf("%w: does not chain to the pinned anchor: %v", ErrReleaseCertInvalid, err)
	}
	// Downgrade defence at the KEY level: a cert issued before this device's
	// floor was raised must not resurrect a retired release key.
	if cert.MinEpoch < floor {
		return nil, 0, fmt.Errorf("%w: cert min_epoch %d is below the device epoch floor %d",
			ErrReleaseCertInvalid, cert.MinEpoch, floor)
	}
	releasePub, err := cert.DecodePubKey()
	if err != nil {
		return nil, 0, fmt.Errorf("%w: decode release pubkey: %v", ErrReleaseCertInvalid, err)
	}

	// Remember what the root has retired.  Only the ROOT-SIGNED cert may move
	// the floor (see signing.EpochStore.RaiseFromReleaseCert for why the
	// release-key-signed manifest must not).  A persistence failure is logged
	// rather than fatal: the checks above already refused anything below the
	// floor we did read.
	if u.cfg.EpochStore != nil {
		if err := u.cfg.EpochStore.RaiseFromReleaseCert(u.cfg.AnchorPub, cert); err != nil {
			u.log.Error("osdist/update: could not raise epoch floor from release cert", "error", err)
		} else if cert.MinEpoch > floor {
			u.log.Info("osdist/update: epoch floor raised by root-signed release cert",
				"from", floor, "to", cert.MinEpoch)
			floor = cert.MinEpoch
		}
	}

	return releasePub, floor, nil
}

// ─── fetchManifest ────────────────────────────────────────────────────────────

// fetchManifest fetches os/stable.json and os/stable.json.sig from the Source,
// verifies the signature with the CERTIFIED RELEASE KEY (not the root anchor —
// no tool in this repository signs a manifest with the root key), enforces the
// epoch floor, and returns the parsed manifest together with its raw bytes.
//
// The raw bytes matter: the image signature is over canonical(ImagePayload),
// and that payload is unmarshalled straight from these bytes rather than
// re-encoded from the parsed manifest.  Round-tripping released_at through
// time.Time would not reproduce the signer's bytes.  cmd/init does the same for
// the same reason.
func (u *Updater) fetchManifest(ctx context.Context, releasePub ed25519.PublicKey, floor int64) (*StableManifest, []byte, error) {
	manifestData, err := u.fetchFile(ctx, ManifestBucketPath)
	if err != nil {
		return nil, nil, err
	}
	sigFileData, err := u.fetchFile(ctx, ManifestSigBucketPath)
	if err != nil {
		return nil, nil, err
	}

	// Parse the detached .sig file format.
	detachedSig, err := signing.ParseSig(sigFileData)
	if err != nil {
		return nil, nil, fmt.Errorf("parse manifest .sig: %w", err)
	}

	// ParseAndVerify: unmarshal + verify Ed25519 sig + enforce epoch floor.
	manifest, err := ParseAndVerify(manifestData, detachedSig.SigBytes, releasePub, floor)
	if err != nil {
		return nil, nil, fmt.Errorf("verify manifest: %w", err)
	}

	u.setLastLatest(manifest.Latest)
	return manifest, manifestData, nil
}

// fetchFile reads one whole file from the Source, closing the body on every
// path.  Every artifact this updater consumes is small enough to hold in memory
// except the image itself, which is streamed to disk by downloadToSlot.
func (u *Updater) fetchFile(ctx context.Context, relPath string) ([]byte, error) {
	rc, err := u.cfg.Source.Fetch(ctx, relPath)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", relPath, err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", relPath, err)
	}
	return data, nil
}

// ─── downloadToSlot ──────────────────────────────────────────────────────────

// downloadToSlot downloads the squashfs image described in manifest to the
// inactive slot, verifies the release-key signature over the ImagePayload the
// manifest describes, BINDS that payload to the downloaded bytes (size +
// dm-verity root hash), then stages the file atomically.
//
// It returns the final path of the staged file on success, or an error if any
// verification step fails (in which case the partial download is removed and
// nothing is staged).
func (u *Updater) downloadToSlot(
	ctx context.Context,
	manifest *StableManifest,
	manifestData []byte,
	releasePub ed25519.PublicKey,
	bs *BootState,
	slot Slot,
) (string, error) {
	imagePath := VersionPath(manifest.Latest)            // e.g. "os/v08/os-core.squashfs"
	sigPath := VersionSigPath(manifest.Latest)           // e.g. "os/v08/os-core.squashfs.sig"
	hashtreePath := VersionHashtreePath(manifest.Latest) // e.g. "os/v08/os-core.hashtree"

	// ── 4a. The ImagePayload the release key signed ───────────────────────────
	// Unmarshalled STRAIGHT FROM THE MANIFEST BYTES, not re-encoded from the
	// parsed manifest: canonical() must reproduce the signer's bytes exactly.
	var payload verify.ImagePayload
	if err := json.Unmarshal(manifestData, &payload); err != nil {
		return "", fmt.Errorf("manifest does not describe an image payload: %w", err)
	}
	if payload.RootHash == "" || payload.Path == "" {
		return "", errors.New("manifest is missing roothash/path — nothing to bind a signature to")
	}
	// The signed payload must name the artifact we are about to fetch. Without
	// this a valid signature over a DIFFERENT (older, vulnerable) image could be
	// paired with a newer manifest.
	if payload.Path != imagePath {
		return "", fmt.Errorf("%w: signed payload names %q but this update fetches %q",
			ErrHashMismatch, payload.Path, imagePath)
	}

	// ── 4b. Download image to a local temp file ───────────────────────────────
	slotDir, err := u.cfg.SlotManager.SlotDir(slot)
	if err != nil {
		return "", fmt.Errorf("slot dir for %s: %w", slot, err)
	}

	imageRC, err := u.cfg.Source.Fetch(ctx, imagePath)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", imagePath, err)
	}
	defer imageRC.Close()

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

	written, err := io.Copy(tmpFile, imageRC)
	if err != nil {
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

	// ── 4c. Verify the detached image signature ───────────────────────────────
	// A RELEASE-key signature over canonical(ImagePayload) — the shape
	// `cmd/sign sign-image` emits.  It is NOT over the raw image bytes.
	imgSigData, err := u.fetchFile(ctx, sigPath)
	if err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	detachedImgSig, err := signing.ParseSig(imgSigData)
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("parse image .sig: %w", err)
	}
	canonicalPayload, err := signing.Canonical(payload)
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("canonical image payload: %w", err)
	}
	if !signing.Verify(releasePub, canonicalPayload, detachedImgSig.SigBytes) {
		os.Remove(tmpPath)
		return "", ErrImageBadSignature
	}

	// ── 4d. Bind the signed description to the downloaded bytes ───────────────
	// Size first: weak on its own but free, and it catches a truncated download
	// before the Merkle tree walk would.
	if written != payload.Size {
		os.Remove(tmpPath)
		return "", fmt.Errorf("%w: signed size %d, downloaded %d bytes", ErrHashMismatch, payload.Size, written)
	}
	if err := u.bindByVerity(ctx, tmpPath, hashtreePath, payload.RootHash, slotDir); err != nil {
		os.Remove(tmpPath)
		return "", err
	}

	// ── 4e. Stage atomically into inactive slot ───────────────────────────────
	// StageInto streams a reader into the slot directory using temp-then-rename,
	// so we open the verified temp file as the source reader.  Streaming from
	// the file rather than a []byte keeps a ~600 MiB image off the heap.
	verified, err := os.Open(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("reopen verified image: %w", err)
	}
	stageErr := u.cfg.SlotManager.StageInto(bs, slot, "os-core.squashfs", verified)
	verified.Close()
	if stageErr != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("stage into slot %s: %w", slot, stageErr)
	}

	// Remove the download temp file now that StageInto has placed the final copy.
	os.Remove(tmpPath)

	finalPath := filepath.Join(slotDir, "os-core.squashfs")
	return finalPath, nil
}

// ─── dm-verity binding ────────────────────────────────────────────────────────

// bindByVerity is the step that makes the release-key signature mean something
// about the IMAGE rather than about a description of one.
//
// It fetches the Merkle hash tree published beside the image and asks
// veritysetup whether rootHash — the value the release key signed — is the root
// of that tree over these exact bytes.  Equal root hash means the signed payload
// names this image and no other.
//
// Fail-closed by construction: a missing hash tree or an absent veritysetup
// returns ErrVerityUnavailable rather than falling back to the size check that
// already ran.  Size proves a file is the right length, not that it is the right
// file.
func (u *Updater) bindByVerity(ctx context.Context, imagePath, hashtreeRelPath, rootHash, workDir string) error {
	hashtreeData, err := u.fetchFile(ctx, hashtreeRelPath)
	if err != nil {
		return fmt.Errorf("%w: no hash tree at %s: %v", ErrVerityUnavailable, hashtreeRelPath, err)
	}

	htFile, err := os.CreateTemp(workDir, "os-core.hashtree.*.download")
	if err != nil {
		return fmt.Errorf("create temp for hash tree: %w", err)
	}
	htPath := htFile.Name()
	defer os.Remove(htPath)
	if _, err := htFile.Write(hashtreeData); err != nil {
		htFile.Close()
		return fmt.Errorf("write hash tree: %w", err)
	}
	if err := htFile.Close(); err != nil {
		return fmt.Errorf("close hash tree: %w", err)
	}

	// Whitespace matters: a garbled root hash handed to veritysetup is exactly
	// the failure mode that once panicked a kernel at boot (see build.sh
	// VERITY-01's note on reading the file, not stdout).
	return u.cfg.VerityVerify(ctx, imagePath, htPath, strings.ToLower(strings.TrimSpace(rootHash)))
}

// verityVerifyWithTool is the production VerityVerify: `veritysetup verify`
// re-reads every data block, rebuilds the Merkle tree, and asserts the stored
// root matches rootHash.  This is the same primitive services/installer uses
// before a netboot install and the same one the initramfs hook uses at boot —
// not a stand-in for it.
func verityVerifyWithTool(ctx context.Context, imagePath, hashtreePath, rootHash string) error {
	bin, err := exec.LookPath("veritysetup")
	if err != nil {
		return fmt.Errorf("%w: veritysetup not available: %v", ErrVerityUnavailable, err)
	}
	out, err := exec.CommandContext(ctx, bin, "verify", imagePath, hashtreePath, rootHash).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: veritysetup verify rejected roothash %s: %v: %s",
			ErrHashMismatch, rootHash, err, strings.TrimSpace(string(out)))
	}
	return nil
}
