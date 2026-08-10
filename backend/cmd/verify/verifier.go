// Package verify implements per-boot-stage signature verification for the
// Vulos initramfs (VERITY-02).
//
// # Boot chain
//
// Every stage must be verified before execution.  This package is responsible
// for the initramfs half of the chain:
//
//	… kernel + initramfs (verified by bootloader)
//	     └─ verifies → OS squashfs (dm-verity root hash + detached .sig)
//	          └─ pivot_root → run the verified OS
//
// # PKI
//
// The two-level PKI (from roadmap/SIGNING.md and cmd/sign/pki.go):
//
//	Offline ROOT key  (baked as /etc/vulos/trust-anchor.pub by SEED-01)
//	   │  signs (offline, occasionally)
//	   ▼
//	RELEASE-key certificate  (ReleaseCert JSON on the filesystem or bucket)
//	   │  signs (per release)
//	   ▼
//	stable.json.sig  /  os-core.squashfs.sig
//
// The device validates the release cert against the baked root anchor before
// trusting any artifact the release key signed.  The squashfs dm-verity root
// hash in the manifest must match the runtime verity hash before pivot.
//
// # Fail-closed guarantee
//
// Every exported function returns a non-nil error on any failure path.
// Callers MUST treat a non-nil error as a hard boot halt — never attempt to
// continue past a verification failure.
package verify

import (
	"errors"
	"fmt"
	"os"

	"vulos/backend/services/signing"
)

// DefaultAnchorPath is the canonical location of the baked trust-anchor public
// key inside the initramfs and rootfs (installed by scripts/seed/embed-anchor.sh,
// SEED-01).
const DefaultAnchorPath = signing.DefaultAnchorPath

// ─── Release-key certificate ──────────────────────────────────────────────────
//
// The cert type and its validation live in backend/services/signing so that the
// issuer (cmd/sign), the initramfs verifier (here) and the app registry
// (services/appnet) all agree on exactly which bytes the root key signs.  The
// aliases below keep this package's API — and the boot chain that depends on
// it — unchanged.

// ReleaseCert is a root-signed certificate that authorises a release public key.
// See signing.ReleaseCert for the wire format and the signed byte range.
type ReleaseCert = signing.ReleaseCert

// certBody is the subset of ReleaseCert covered by the root signature.
type certBody = signing.CertBody

// bodyOf converts a ReleaseCert into its signable certBody.
func bodyOf(c ReleaseCert) certBody { return signing.BodyOf(c) }

// ValidateReleaseCert verifies a ReleaseCert against the baked root public key.
//
// Fail-closed: any error means the cert MUST NOT be trusted.
func ValidateReleaseCert(rootPub []byte, cert ReleaseCert) error {
	return signing.ValidateReleaseCert(rootPub, cert)
}

// ─── ImagePayload ─────────────────────────────────────────────────────────────

// ImagePayload is the structure the release key signs for an OS image artifact.
// It mirrors cmd/sign/pki.go:ImagePayload — the signing surface is canonical JSON
// of this struct.
type ImagePayload struct {
	Path       string `json:"path"`
	RootHash   string `json:"roothash"`
	Size       int64  `json:"size"`
	MinEpoch   int64  `json:"min_epoch"`
	ReleasedAt string `json:"released_at"` // RFC 3339, informational
}

// ─── Stage verification ───────────────────────────────────────────────────────

// VerifyStageFile verifies that the file at stagePath has a valid detached
// signature at stagePath+".sig", signed by releasePub.
//
// The signature is over the raw bytes of the file (not canonical JSON).
// This covers: shim, bootloader, kernel, initramfs, squashfs.
//
// Fail-closed: any error (missing sig file, bad sig, I/O failure) returns
// a non-nil error.  Callers MUST halt boot on any error.
func VerifyStageFile(releasePub []byte, stagePath string) error {
	if len(releasePub) != 32 {
		return fmt.Errorf("verify: releasePub must be 32 bytes, got %d", len(releasePub))
	}

	// Read the artifact.
	payload, err := os.ReadFile(stagePath)
	if err != nil {
		return fmt.Errorf("verify: read stage %q: %w", stagePath, err)
	}

	// Read the detached .sig file.
	sigPath := stagePath + ".sig"
	sigData, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("verify: read sig %q: %w", sigPath, err)
	}

	// Parse the .sig format.
	parsed, err := signing.ParseSig(sigData)
	if err != nil {
		return fmt.Errorf("verify: parse sig %q: %w", sigPath, err)
	}

	// Verify the raw-byte signature (stage files are not canonical JSON).
	if !signing.Verify(releasePub, payload, parsed.SigBytes) {
		return fmt.Errorf("verify: stage signature invalid for %q", stagePath)
	}

	return nil
}

// ─── Squashfs / dm-verity verification ───────────────────────────────────────

// SquashfsVerifyConfig holds the parameters for squashfs + dm-verity + sig
// verification before pivot_root.
type SquashfsVerifyConfig struct {
	// AnchorPath is the path to the baked trust-anchor public key.
	// Defaults to DefaultAnchorPath if empty.
	AnchorPath string

	// CertPath is the path to the release-key certificate JSON (ReleaseCert).
	// The cert is validated against the anchor before the image sig is checked.
	CertPath string

	// SquashfsPath is the path to the squashfs image file.
	SquashfsPath string

	// SigPath is the path to the detached .sig file for the squashfs image
	// (signed over an ImagePayload struct, not raw bytes).
	// If empty, SquashfsPath+".sig" is used.
	SigPath string

	// ImagePayloadForSig is the ImagePayload that the release key signed.
	// The verifier re-derives canonical(payload) and checks the sig against it.
	ImagePayloadForSig ImagePayload

	// BindRootHash proves that the OS this machine is actually running is the
	// image ImagePayloadForSig names.  REQUIRED by VerifySquashfsBeforePivot: a
	// nil binder is refused, not skipped.
	//
	// This field replaced an ExpectedRootHash string, and the replacement is the
	// whole point.  Both callers filled ExpectedRootHash from the SAME manifest
	// they filled ImagePayloadForSig from, so step 6 compared a value with
	// itself: a comparison that could not fail, that never touched the image,
	// and that then reported "root hash verified".  Only this package's own unit
	// tests ever passed two different values, which is why it looked alive.
	//
	// A signature over an ImagePayload covers a NAME, a SIZE and a ROOT HASH.  It
	// does not cover the image.  Something outside the manifest has to measure
	// the running system and say whether it is the one described — that is this
	// function, and there is deliberately no default: a caller with nothing to
	// measure must say so by calling VerifyManifestSignature instead.
	BindRootHash RootHashBinder

	// EpochFloor is a STATIC minimum trusted epoch, used only when EpochStore
	// is nil.  The release cert's MinEpoch must be >= the floor in force.
	EpochFloor int64

	// EpochStore is the device's PERSISTENT epoch floor.  When set it supplies
	// the floor in place of EpochFloor, and — this is the part that makes
	// revocation real — the floor is RAISED to the min_epoch of the root-signed
	// release cert once that cert validates.
	//
	// Without a store this gate is check-only, and a check-only floor never
	// moves: a device sits at 0 for life, every min_epoch >= 0 passes, and
	// issuing a release cert with a higher -min-epoch revokes nothing.  That was
	// true of every caller here until this field existed; see
	// signing.EpochStore.RaiseFromReleaseCert and commit 8846d61c.
	//
	// Only the ROOT-SIGNED cert may move the floor.  The manifest is signed by
	// the RELEASE key — the very key revocation exists to retire — so letting it
	// raise the floor would let a stolen key publish min_epoch=MaxInt64 and, as
	// the floor never falls, permanently brick the device.
	EpochStore *signing.EpochStore
}

// RootHashBinder proves that the OS this machine is actually running is the
// image whose dm-verity root hash the release key signed.  It is given the
// SIGNED root hash and must obtain the running system's own, independently of
// the manifest, and report whether they are the same.
//
// Fail-closed: a non-nil error means the signed payload has NOT been bound to
// anything, and the caller must halt.  Returning nil without having measured
// anything reintroduces the exact defect this type exists to remove.
type RootHashBinder func(signedRootHash string) error

// VerifyManifestSignature verifies everything that can be established from the
// signed artifacts alone:
//
//  1. Load the baked trust anchor from cfg.AnchorPath.
//  2. Load and validate the release cert (cfg.CertPath) against the anchor.
//  3. Enforce cert.MinEpoch >= the epoch floor in force, then RAISE that floor
//     to the cert's min_epoch when cfg.EpochStore is set.
//  4. Decode the release public key from the cert.
//  5. Verify the image signature — the release key over canonical(ImagePayload).
//
// It deliberately stops there.  What it establishes is that SOMEONE HOLDING THE
// CERTIFIED RELEASE KEY described an artifact: a path, a size and a dm-verity
// root hash.  It does not establish anything about the bytes in front of the
// caller, because nothing here has looked at them.
//
// Use this only where there is genuinely nothing to bind to — e.g. verifying a
// manifest before copying it onto a target disk, where the image it describes is
// not the image this process is running.  Where the running or about-to-run
// system IS the subject, call VerifySquashfsBeforePivot, which requires the
// binding.
//
// Fail-closed: returns a non-nil error on any failure.
func VerifyManifestSignature(cfg SquashfsVerifyConfig) error {
	return verifyManifestSignature(cfg)
}

// VerifySquashfsBeforePivot is VerifyManifestSignature plus the step that makes
// the signature mean something about an OS rather than about a description of
// one: cfg.BindRootHash must prove that the system this gate is protecting has
// the dm-verity root hash the release key signed.
//
// That step used to be a comparison between cfg.ExpectedRootHash and
// cfg.ImagePayloadForSig.RootHash.  Every production caller filled both from the
// same manifest file, so it compared a value with itself — it could not fail, it
// never touched an image, and it reported a verified root hash regardless.  The
// binding is now supplied by the caller and is MANDATORY: a nil BindRootHash is
// an error, never a skip, because a gate that cannot bind must refuse rather
// than quietly degrade to "the signature covers a name and a size".
//
// Fail-closed: returns a non-nil error on any failure.  Callers MUST halt boot.
func VerifySquashfsBeforePivot(cfg SquashfsVerifyConfig) error {
	if err := verifyManifestSignature(cfg); err != nil {
		return err
	}

	// 6. Bind the signed description to the running system.
	if cfg.ImagePayloadForSig.RootHash == "" {
		return errors.New("verify: signed payload names no root hash — nothing can bind it to an image")
	}
	if cfg.BindRootHash == nil {
		return errors.New("verify: no root-hash binder supplied — refusing to report an image verified when nothing measured it")
	}
	if err := cfg.BindRootHash(cfg.ImagePayloadForSig.RootHash); err != nil {
		return fmt.Errorf("verify: root hash not bound to the running image: %w", err)
	}

	return nil
}

func verifyManifestSignature(cfg SquashfsVerifyConfig) error {
	if cfg.AnchorPath == "" {
		cfg.AnchorPath = DefaultAnchorPath
	}
	sigPath := cfg.SigPath
	if sigPath == "" {
		sigPath = cfg.SquashfsPath + ".sig"
	}

	// 1. Load the baked trust anchor.
	anchorPub, err := signing.LoadAnchor(cfg.AnchorPath)
	if err != nil {
		return fmt.Errorf("verify: load anchor: %w", err)
	}

	// 2. Load and validate the release cert.
	certData, err := os.ReadFile(cfg.CertPath)
	if err != nil {
		return fmt.Errorf("verify: read release cert %q: %w", cfg.CertPath, err)
	}

	cert, err := parseReleaseCert(certData)
	if err != nil {
		return fmt.Errorf("verify: parse release cert: %w", err)
	}

	if err := ValidateReleaseCert(anchorPub, cert); err != nil {
		return fmt.Errorf("verify: release cert validation: %w", err)
	}

	// 3. Epoch floor enforcement — the gate that refuses a RETIRED release key.
	floor := cfg.EpochFloor
	if cfg.EpochStore != nil {
		floor = cfg.EpochStore.Current()
	}
	if cert.MinEpoch < floor {
		return fmt.Errorf("verify: release cert min_epoch %d is below device floor %d",
			cert.MinEpoch, floor)
	}

	// ...and the raise that gives that gate something to enforce.  It happens
	// here, as soon as the ROOT signature over the cert has been checked and
	// before any release-key-signed artifact is examined, so a hostile medium
	// cannot hold the floor down by pairing a good cert with a broken image
	// signature and then replaying the retired cert.
	//
	// Fail-closed on a persistence failure: booting an image while silently
	// failing to record the revocation that authorised it is the failure mode
	// this mechanism exists to prevent, and the next boot would measure the next
	// cert against a floor that never moved.
	if cfg.EpochStore != nil {
		if err := cfg.EpochStore.RaiseFromReleaseCert(anchorPub, cert); err != nil {
			return fmt.Errorf("verify: record the root-signed epoch floor: %w", err)
		}
	}

	// 4. Decode the release public key.
	releasePub, err := cert.DecodePubKey()
	if err != nil {
		return fmt.Errorf("verify: decode release pubkey from cert: %w", err)
	}

	// 5. Verify the squashfs image signature (over the ImagePayload, not raw bytes).
	//    The ImagePayload is canonical-JSON signed by the release key; the .sig
	//    file carries the signature bytes in the Vulos detached-sig format.
	sigData, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("verify: read squashfs sig %q: %w", sigPath, err)
	}

	parsedSig, err := signing.ParseSig(sigData)
	if err != nil {
		return fmt.Errorf("verify: parse squashfs sig: %w", err)
	}

	canonical, err := signing.Canonical(cfg.ImagePayloadForSig)
	if err != nil {
		return fmt.Errorf("verify: canonical image payload: %w", err)
	}

	if !signing.Verify(releasePub, canonical, parsedSig.SigBytes) {
		return errors.New("verify: squashfs image signature invalid")
	}

	return nil
}

// ─── JSON helpers ─────────────────────────────────────────────────────────────

// parseReleaseCert unmarshals a ReleaseCert from JSON bytes.
// Returns an error if the JSON is invalid or required fields are missing.
func parseReleaseCert(data []byte) (ReleaseCert, error) {
	return signing.ParseReleaseCert(data)
}
