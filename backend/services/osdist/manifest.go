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
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
//	  "path":        "os/v08/os-core.squashfs",
//	  "is_security": true,
//	  "severity":    "critical",
//	  "notes":       "Fixes CVE-2026-1234 in the boot verifier."
//	}
//
// # The release-severity surface, and why it is `omitempty`
//
// is_security/severity/notes are INSIDE the signed surface — they are part of
// the same canonical bytes the release key signs (`cmd/sign sign-manifest`
// -security/-severity/-notes).  Anything a box shows its owner about a release
// has to be, or it is attacker-appendable to a legitimately signed document.
//
// All three carry `omitempty`, and that is what makes the addition compatible
// rather than breaking: [signing.Canonical] drops a zero value entirely, so a
// manifest that sets none of them canonicalises to EXACTLY the seven-key bytes
// it did before these fields existed.  Every signature already issued keeps
// verifying, byte for byte, with no format version, no dual-verify path and no
// re-signing ceremony.  A box that meets an old-shape manifest reads
// is_security=false / severity="" / notes="" — the honest report of a release
// that says nothing about severity.
//
// The corollary, stated plainly because it looks like a hole and is not: an
// attacker CAN append `"is_security": false` (or `"severity": ""`, or
// `"notes": ""`) to a signed seven-key document and the signature still
// verifies, because those canonicalise away.  That is sound.  The canonical
// form is what is authenticated, and every document sharing a canonical form
// has the same meaning.  Any value that CHANGES the meaning changes the
// canonical bytes, and then the signature fails — including flipping a signed
// is_security from true to false, which is the interesting direction.
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

	// IsSecurity marks this release as fixing a security defect.  It is what
	// raises the OS-update panel's banner and fires the box owner's priority
	// notification (cmd/server/routes_ota.go), so it is signed like everything
	// else the owner is shown.
	IsSecurity bool `json:"is_security,omitempty"`

	// Severity classifies that defect.  It is a CLOSED SET
	// ([SeverityLow]..[SeverityCritical]) rather than free text: the string is
	// rendered to the owner beside the word "Security update", and a closed set
	// is the difference between a badge and an attacker-chosen sentence.
	// Required when IsSecurity, and forbidden without it — "critical" on a
	// release that fixes no vulnerability is exactly the scare copy this pairing
	// rule removes.
	Severity string `json:"severity,omitempty"`

	// Notes is a SHORT, SINGLE-LINE, LINK-FREE release note, rendered verbatim
	// to the box owner in Settings → OS Update and used as the body of the
	// security push notification.  See [ValidateReleaseNotes] for the exact
	// constraints and the reasoning; the short version is that a signed field a
	// human reads and acts on is still a social-engineering surface, so it is
	// bounded, printable and cannot carry a link.
	Notes string `json:"notes,omitempty"`
}

// ─── The release-severity surface ────────────────────────────────────────────

// Severity values for [StableManifest.Severity].  This is the complete set;
// anything else is refused by [ValidateSecuritySurface] at the signer and at
// every verifier.
const (
	SeverityLow      = "low"
	SeverityMedium   = "medium"
	SeverityHigh     = "high"
	SeverityCritical = "critical"
)

// Severities is the closed set of accepted [StableManifest.Severity] values, in
// increasing order.
var Severities = []string{SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical}

// MaxNotesBytes bounds [StableManifest.Notes].  It is sized for a push
// notification body — the surface the string actually lands on — not for a
// changelog.  A release with more to say publishes it where the box does not
// render it unread.
const MaxNotesBytes = 200

// ErrSecuritySurface is returned when is_security/severity/notes are internally
// inconsistent, or when notes carries something that must never be rendered to
// the box owner.  Signers refuse to sign it; verifiers refuse to read it.
var ErrSecuritySurface = errors.New("osdist: invalid release-severity surface")

// ValidateSecuritySurface enforces the is_security/severity/notes rules that
// `cmd/sign sign-manifest`, [ParseAndVerify] and the services/ota client all
// apply to the SAME bytes.  It lives here, in one place, because three
// independently maintained copies of a signing rule is precisely how this
// manifest's signed surface came to disagree with its signer in the first place.
//
// The rules:
//
//   - is_security ⇒ severity must be one of [Severities].
//   - !is_security ⇒ severity must be empty.
//   - notes must satisfy [ValidateReleaseNotes].
//
// Being inside the signed surface is necessary but not sufficient. A signature
// proves the release key said it; it does not make an unbounded attacker-chosen
// string safe to put in front of the box owner. These rules are what make the
// two fields renderable.
func ValidateSecuritySurface(isSecurity bool, severity, notes string) error {
	switch {
	case isSecurity && !validSeverity(severity):
		return fmt.Errorf("%w: is_security requires severity to be one of %s, got %q",
			ErrSecuritySurface, strings.Join(Severities, "/"), severity)
	case !isSecurity && severity != "":
		return fmt.Errorf("%w: severity %q on a release that is not marked is_security",
			ErrSecuritySurface, severity)
	}
	return ValidateReleaseNotes(notes)
}

func validSeverity(s string) bool {
	for _, v := range Severities {
		if s == v {
			return true
		}
	}
	return false
}

// ValidateReleaseNotes constrains the one free-text field the box renders to its
// owner.
//
// The threat is not markup — Settings renders the string as text, and React
// escapes it — the threat is the HUMAN. This string is shown beside a "Download
// & stage update" button and is used verbatim as the body of a high-priority
// push notification. Whoever writes it is telling the box owner what to do next.
// Signing it makes it attributable to the release key; it does not make it safe.
// So:
//
//   - at most [MaxNotesBytes] bytes, and valid UTF-8 — one notification body,
//     bounded before it is ever allocated or rendered;
//   - no control characters, so it cannot flood the panel with blank lines
//     (Settings renders it `whitespace-pre-wrap`) or truncate a log line;
//   - no Unicode format characters (category Cf: bidi overrides, zero-width
//     joiners), which are how a string is made to read as something other than
//     its bytes;
//   - NO LINKS — no "://" and no "www.". This is the one rule that removes a
//     capability rather than tidying a string. The only legitimate action for an
//     OS update is the button the box draws itself; an update note that hands
//     the owner somewhere else to go is a phishing primitive whether or not the
//     signature is good, and it is worth exactly nothing to a real release.
func ValidateReleaseNotes(notes string) error {
	if notes == "" {
		return nil
	}
	if len(notes) > MaxNotesBytes {
		return fmt.Errorf("%w: notes is %d bytes, the limit is %d",
			ErrSecuritySurface, len(notes), MaxNotesBytes)
	}
	if !utf8.ValidString(notes) {
		return fmt.Errorf("%w: notes is not valid UTF-8", ErrSecuritySurface)
	}
	for _, r := range notes {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("%w: notes contains the non-printing character %U — "+
				"it must be a single line of printable text", ErrSecuritySurface, r)
		}
	}
	for _, link := range []string{"://", "www."} {
		if strings.Contains(strings.ToLower(notes), link) {
			return fmt.Errorf("%w: notes contains %q — an update note must never "+
				"send the box owner to a link", ErrSecuritySurface, link)
		}
	}
	return nil
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

	// 2b. The release-severity surface.  Refused BEFORE the signature check on
	//     purpose: a valid signature over an unrenderable note is still an
	//     unrenderable note, and this is the gate that says so regardless of who
	//     signed it.  A compromised release key must not be able to put a link
	//     in front of the box owner just because it can sign.
	if err := ValidateSecuritySurface(m.IsSecurity, m.Severity, m.Notes); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
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
