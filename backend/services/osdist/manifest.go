// Package osdist implements the Vulos OS distribution manifest schema,
// canonical serialisation, and signature+epoch verification for the public OS
// bucket.
//
// # Public bucket layout
//
// The OS bucket is public-read; security comes from signing, not from access
// control.  The layout is:
//
//	os/
//	├── release-cert.json    – root-signed cert authorising the release key
//	├── stable.json          – release-key-signed manifest (this package)
//	├── stable.json.sig      – detached signature over stable.json
//	├── v07/
//	│   ├── os-core.squashfs
//	│   ├── os-core.squashfs.sig
//	│   └── os-core.hashtree
//	├── v08/
//	│   ├── os-core.squashfs
//	│   ├── os-core.squashfs.sig
//	│   └── os-core.hashtree
//	└── ...
//
// Use [VersionPath], [VersionSigPath] and [VersionHashtreePath] to obtain the
// canonical paths for a given version string.
//
// # Verification
//
// [ParseAndVerify] is the single entry point: unmarshal, verify the Ed25519
// signature via the supplied signer public key, and enforce the epoch floor.
// Neither the key nor the epoch floor are read from disk here — callers supply
// them.
//
// The key to supply is the RELEASE key, obtained by validating
// os/release-cert.json against the baked ROOT anchor
// (signing.ReleaseKeyFromCert).  The root anchor signs the certificate and
// nothing else: `cmd/sign sign-manifest` requires -release-priv, and no command
// in this repository signs a manifest with the root key.  Passing the anchor
// here therefore cannot verify any manifest this project can produce — that
// mistake is what update.go's header documents.
package osdist

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"vulos/backend/services/signing"
)

// ─── Sentinel errors ─────────────────────────────────────────────────────────

// ErrMalformed is returned when the manifest JSON cannot be parsed or is
// structurally incomplete (missing required fields).
var ErrMalformed = errors.New("osdist: malformed manifest")

// ErrBadSignature is returned when the Ed25519 signature does not verify
// against the supplied anchor public key.
var ErrBadSignature = errors.New("osdist: bad signature")

// ErrEpochTooLow is returned when the manifest's MinEpoch is below the
// supplied epoch floor, indicating a potential downgrade/rollback attack.
var ErrEpochTooLow = errors.New("osdist: min_epoch below floor")

// ─── Schema ───────────────────────────────────────────────────────────────────

// StableManifest is the in-memory representation of os/stable.json.
//
// The JSON field names are fixed by the bucket schema; do not rename them.
// The "sig" field is intentionally absent from this struct so that
// [Canonical] produces bytes that can be verified against the detached
// stable.json.sig (i.e. the signature is over the manifest content, not over
// a document that already contains its own signature).
//
// Example on-disk JSON:
//
//	{
//	  "channel":     "stable",
//	  "latest":      "v08",
//	  "min_epoch":   3,
//	  "roothash":    "<hex dm-verity root hash>",
//	  "size":        734003200,
//	  "released_at": "2026-05-20T09:00:00Z",
//	  "path":        "os/v08/os-core.squashfs"
//	}
type StableManifest struct {
	// Channel is the update channel, e.g. "stable" or "edge".
	Channel string `json:"channel"`

	// Latest is the version identifier for the current release, e.g. "v08".
	// Use [VersionPath] to derive the bucket paths for this version.
	Latest string `json:"latest"`

	// MinEpoch is the minimum trusted epoch.  A device that has seen a higher
	// epoch must reject this manifest.  This provides free rollback/downgrade
	// protection without clocks or CRLs (see SIGNING.md).
	MinEpoch int64 `json:"min_epoch"`

	// RootHash is the hex-encoded dm-verity root hash of the squashfs image
	// identified by Path.  Every block of the image is verified at runtime via
	// this hash.
	RootHash string `json:"roothash"`

	// Size is the byte length of the squashfs image at Path.
	Size int64 `json:"size"`

	// ReleasedAt is the release timestamp in UTC.
	ReleasedAt time.Time `json:"released_at"`

	// Path is the bucket-relative path to the squashfs image,
	// e.g. "os/v08/os-core.squashfs".
	Path string `json:"path"`
}

// ─── Canonical bytes ─────────────────────────────────────────────────────────

// Canonical returns the deterministic, signing-canonical JSON encoding of m.
// It delegates to [signing.Canonical] so that key ordering and whitespace rules
// are identical to those used by the signing toolchain.
//
// The returned bytes are what the release key signs (and what [ParseAndVerify]
// verifies against).  Because StableManifest has no "sig" field, the signature
// is always over the pure manifest content.
func (m *StableManifest) Canonical() ([]byte, error) {
	b, err := signing.Canonical(m)
	if err != nil {
		return nil, fmt.Errorf("osdist: canonical: %w", err)
	}
	return b, nil
}

// ─── Parse + verify ──────────────────────────────────────────────────────────

// ParseAndVerify unmarshals data as a StableManifest, verifies sig against
// signerPub using Ed25519 over the manifest's canonical bytes, and enforces
// that the manifest's MinEpoch is not below epochFloor.
//
// Parameters:
//   - data       raw bytes of stable.json (any valid JSON encoding)
//   - sig        raw 64-byte Ed25519 signature over the canonical bytes
//   - signerPub  the public key that signed the manifest.  This is the RELEASE
//     key, obtained by validating the root-signed release certificate against
//     the baked anchor (signing.ReleaseKeyFromCert) — NOT the anchor itself.
//     The parameter was called anchorPub, and both callers duly passed the
//     anchor, which no signer in this repository matches.
//   - epochFloor the highest min_epoch the device has previously accepted
//     (supplied by the caller; signing.EpochStore holds the persistent floor)
//
// The function is fail-closed: any verification failure returns nil and a
// typed sentinel error ([ErrMalformed], [ErrBadSignature], [ErrEpochTooLow]).
func ParseAndVerify(data []byte, sig []byte, signerPub ed25519.PublicKey, epochFloor int64) (*StableManifest, error) {
	// 1. Unmarshal.
	var m StableManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}

	// 2. Basic structural validation — required string fields must be non-empty.
	if m.Channel == "" || m.Latest == "" || m.RootHash == "" || m.Path == "" {
		return nil, fmt.Errorf("%w: missing required fields (channel/latest/roothash/path)", ErrMalformed)
	}

	// 3. Compute canonical bytes for verification.
	canonical, err := m.Canonical()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}

	// 4. Signature verification.  Fail closed: any problem is ErrBadSignature.
	if !signing.Verify(signerPub, canonical, sig) {
		return nil, ErrBadSignature
	}

	// 5. Epoch floor enforcement.
	if m.MinEpoch < epochFloor {
		return nil, fmt.Errorf("%w: manifest min_epoch %d < floor %d", ErrEpochTooLow, m.MinEpoch, epochFloor)
	}

	return &m, nil
}

// ─── Path helpers ─────────────────────────────────────────────────────────────

// Bucket-relative paths for the channel-level artifacts.
const (
	// ReleaseCertBucketPath is the root-signed certificate authorising the
	// release key.  Serving it beside the manifest is what lets a release key
	// be rotated without re-flashing every box's seed; it is inert until it
	// validates against the pinned anchor.
	ReleaseCertBucketPath = "os/release-cert.json"

	// ManifestBucketPath is the signed channel manifest.
	ManifestBucketPath = "os/stable.json"

	// ManifestSigBucketPath is its detached signature.
	ManifestSigBucketPath = ManifestBucketPath + ".sig"
)

// VersionPath returns the bucket-relative path for the squashfs image of the
// given version (e.g. "v08" → "os/v08/os-core.squashfs").
//
// The companion detached-signature file is at [VersionPath] + ".sig", and the
// dm-verity hash tree at [VersionHashtreePath].
func VersionPath(version string) string {
	return "os/" + version + "/os-core.squashfs"
}

// VersionHashtreePath returns the bucket-relative path for the dm-verity Merkle
// hash tree of the given version's image
// (e.g. "v08" → "os/v08/os-core.hashtree").
//
// This is the file scripts/verity/gen-verity.sh writes beside the image
// (build.sh VERITY-01).  Without it a downloaded image's root hash cannot be
// computed, and the signed ImagePayload cannot be bound to the bytes that
// arrived — which is why the updater refuses to stage when it is absent.
func VersionHashtreePath(version string) string {
	return "os/" + version + "/os-core.hashtree"
}

// VersionSigPath returns the bucket-relative path for the detached signature
// of the squashfs image for the given version
// (e.g. "v08" → "os/v08/os-core.squashfs.sig").
func VersionSigPath(version string) string {
	return VersionPath(version) + ".sig"
}
