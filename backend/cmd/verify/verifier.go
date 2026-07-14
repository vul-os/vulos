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
	"time"

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

// validateReleaseCertAt is the core cert-validation logic.  Accepts an
// explicit 'now' so unit tests can exercise expiry paths without sleeping.
//
// Checks (in order, fail-closed on every path):
//  1. rootPub length sanity.
//  2. NotAfter — cert must not be expired relative to now.
//  3. MinEpoch non-negative sanity.
//  4. RootSig — Ed25519 verification of canonical(certBody) against rootPub.
func validateReleaseCertAt(rootPub []byte, cert ReleaseCert, now time.Time) error {
	return signing.ValidateReleaseCertAt(rootPub, cert, now)
}

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

	// ExpectedRootHash is the hex dm-verity root hash the device must see before
	// pivot.  This comes from stable.json (osdist.StableManifest.RootHash).
	// The verifier checks that the squashfs image's actual verity hash matches
	// this value AND that the release sig covers an ImagePayload whose RootHash
	// field also matches.
	ExpectedRootHash string

	// ImagePayloadForSig is the ImagePayload that the release key signed.
	// The verifier re-derives canonical(payload) and checks the sig against it.
	// Its RootHash field must equal ExpectedRootHash.
	ImagePayloadForSig ImagePayload

	// EpochFloor is the device's current minimum trusted epoch.
	// The release cert's MinEpoch must be >= EpochFloor.
	EpochFloor int64
}

// VerifySquashfsBeforePivot performs the full pre-pivot verification gate:
//
//  1. Load the baked trust anchor from cfg.AnchorPath.
//  2. Load and validate the release cert (cfg.CertPath) against the anchor.
//  3. Enforce cert.MinEpoch >= cfg.EpochFloor.
//  4. Decode the release public key from the cert.
//  5. Verify the squashfs image signature (ImagePayload signed by release key).
//  6. Check that ImagePayload.RootHash == cfg.ExpectedRootHash.
//
// dm-verity step (step 6): the root hash in the signed ImagePayload must match
// the hash the manifest pins.  The kernel then verifies every block at runtime
// via the dm-verity device.  This function is the pre-pivot gate; it does not
// invoke the kernel's dm-verity setup itself (that is done by the caller in the
// mount sequence).
//
// Fail-closed: returns a non-nil error on any failure.  Callers MUST halt boot.
func VerifySquashfsBeforePivot(cfg SquashfsVerifyConfig) error {
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

	// 3. Epoch floor enforcement.
	if cert.MinEpoch < cfg.EpochFloor {
		return fmt.Errorf("verify: release cert min_epoch %d is below device floor %d",
			cert.MinEpoch, cfg.EpochFloor)
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

	// 6. dm-verity root hash check.
	if cfg.ExpectedRootHash == "" {
		return errors.New("verify: expected root hash must not be empty")
	}
	if cfg.ImagePayloadForSig.RootHash != cfg.ExpectedRootHash {
		return fmt.Errorf("verify: root hash mismatch: manifest pins %q but image payload has %q",
			cfg.ExpectedRootHash, cfg.ImagePayloadForSig.RootHash)
	}

	return nil
}

// ─── JSON helpers ─────────────────────────────────────────────────────────────

// parseReleaseCert unmarshals a ReleaseCert from JSON bytes.
// Returns an error if the JSON is invalid or required fields are missing.
func parseReleaseCert(data []byte) (ReleaseCert, error) {
	return signing.ParseReleaseCert(data)
}
