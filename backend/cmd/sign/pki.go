// pki.go — Vulos two-level offline PKI implementation.
//
// Architecture:
//
//	Offline ROOT key (air-gapped / HSM)
//	   │  signs (offline, occasionally)
//	   ▼
//	RELEASE-key certificate  (fetched by device alongside the manifest)
//	   │  signs (per release)
//	   ▼
//	os-core.squashfs.sig  /  stable.json.sig
//
// Design constraints (roadmap/SIGNING.md):
//   - The root private key is NEVER online for routine releases.
//   - The release key cert is root-signed and fetched by the device.
//   - Device validates the cert against the baked root pubkey before trusting
//     any image the release key signed.
//   - Epoch monotonicity provides rollback/downgrade protection without CRLs.
//
// All cryptography uses crypto/ed25519 from the Go standard library.
// Reuses backend/services/signing for Canonical, Sign, Verify.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	// osdist owns the ONE definition of the release-severity surface
	// (is_security/severity/notes) that this signer and every verifier apply to
	// the same bytes. Importing it rather than restating the rules here is
	// deliberate: an independently maintained copy of a signing rule is how the
	// signed surface and its verifiers came to disagree in the first place.
	"vulos/backend/services/osdist"
	"vulos/backend/services/signing"
)

// ─── Release-key certificate ──────────────────────────────────────────────────
//
// The cert type, the byte range the root key signs, and the device-side
// validation all live in backend/services/signing so the issuer (here), the
// initramfs verifier (cmd/verify) and the app registry (services/appnet) can
// never drift apart.  The aliases below keep this package's API unchanged.

// ReleaseCert is a root-signed certificate that authorises a release public key.
// See signing.ReleaseCert for the wire format and the signed byte range.
type ReleaseCert = signing.ReleaseCert

// ─── Offline root operation ───────────────────────────────────────────────────

// IssueReleaseCert creates a root-signed release-key certificate.
//
// This is an OFFLINE ROOT OPERATION.  The root private key should be held on
// an air-gapped machine or inside an HSM and should never be copied online.
// Call this function only when rotating or issuing a new release key.
//
// Parameters:
//   - rootPriv    — root Ed25519 private key (air-gapped / HSM).
//   - releasePub  — the release key's Ed25519 public key to certify.
//   - keyID       — opaque human-readable label for the release key.
//   - notAfter    — certificate wall-clock expiry.
//   - minEpoch    — minimum trusted epoch at time of issuance.
func IssueReleaseCert(
	rootPriv ed25519.PrivateKey,
	releasePub ed25519.PublicKey,
	keyID string,
	notAfter time.Time,
	minEpoch int64,
) (ReleaseCert, error) {
	return signing.IssueReleaseCert(rootPriv, releasePub, keyID, notAfter, minEpoch)
}

// ─── Device-side validation ───────────────────────────────────────────────────

// ValidateReleaseCert verifies a release-key certificate against the baked root
// pubkey.
//
// This is the device-side path.  It fails closed: any error (bad sig, expiry,
// malformed data) returns a non-nil error and the caller MUST NOT trust the
// release key.
func ValidateReleaseCert(rootPub ed25519.PublicKey, cert ReleaseCert) error {
	return signing.ValidateReleaseCert(rootPub, cert)
}

// ─── Release-key signing helpers ─────────────────────────────────────────────

// ImagePayload is the data structure signed over an OS image artifact.
// The release key signs the canonical bytes of this struct.
//
// Fields:
//   - Path      — artifact path as it appears in the distribution bucket.
//   - RootHash  — dm-verity root hash (hex) for the squashfs image.
//   - Size      — byte length of the image file.
//   - MinEpoch  — must match the cert's MinEpoch or higher.
//   - ReleasedAt — ISO-8601 timestamp (informational; not used for trust decisions).
type ImagePayload struct {
	Path       string `json:"path"`
	RootHash   string `json:"roothash"`
	Size       int64  `json:"size"`
	MinEpoch   int64  `json:"min_epoch"`
	ReleasedAt string `json:"released_at"` // RFC 3339, informational
}

// SignImage signs an ImagePayload with the release private key.
// Returns a Signature suitable for serialisation with signing.MarshalSig.
//
// The caller MUST have validated the release cert (ValidateReleaseCert) before
// trusting output from this function on the verifier side.
func SignImage(releasePriv ed25519.PrivateKey, keyID string, payload ImagePayload) (signing.Signature, error) {
	if len(releasePriv) != ed25519.PrivateKeySize {
		return signing.Signature{}, errors.New("sign: releasePriv must be a valid Ed25519 private key")
	}
	canonical, err := signing.Canonical(payload)
	if err != nil {
		return signing.Signature{}, fmt.Errorf("sign: canonical image payload: %w", err)
	}
	return signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     keyID,
		SigBytes:  signing.Sign(releasePriv, canonical),
	}, nil
}

// ManifestPayload mirrors the stable.json structure signed per release.
// The release key signs the canonical bytes of this struct.
//
// Fields:
//   - Channel    — e.g. "stable".
//   - Latest     — version string, e.g. "v08".
//   - MinEpoch   — minimum trusted epoch; devices refuse anything below their floor.
//   - Path       — artifact path in the distribution bucket.
//   - ReleasedAt — ISO-8601 timestamp (informational).
//   - RootHash   — dm-verity root hash (hex).
//   - Size       — byte length of the image file.
//   - IsSecurity — this release fixes a security defect.
//   - Severity   — one of osdist.Severities; required with IsSecurity, forbidden without.
//   - Notes      — short single-line link-free release note shown to the box owner.
//
// # This struct IS the signed surface
//
// It must describe exactly the same JSON keys as osdist.StableManifest and
// services/ota.Manifest.  When it did not — the three severity fields existed on
// the box side and nowhere here — canonical(manifest) grew a key no signature
// covered, and the moment a publisher set one, no manifest could ever have
// verified again.  TestManifestPayloadMatchesTheVerifiers (manifestsurface_test.go)
// and services/ota's TestManifestSignedSurfaceMatchesSigner both fail if any one
// of the three moves alone.
//
// The severity fields carry `omitempty` so a release that sets none of them
// canonicalises to the byte-identical seven-key document it always did — every
// signature already issued keeps verifying.  See osdist.StableManifest's doc for
// the full compatibility argument.
type ManifestPayload struct {
	Channel    string `json:"channel"`
	Latest     string `json:"latest"`
	MinEpoch   int64  `json:"min_epoch"`
	Path       string `json:"path"`
	ReleasedAt string `json:"released_at"` // RFC 3339, informational
	RootHash   string `json:"roothash"`
	Size       int64  `json:"size"`
	IsSecurity bool   `json:"is_security,omitempty"`
	Severity   string `json:"severity,omitempty"`
	Notes      string `json:"notes,omitempty"`
}

// SignManifest signs a ManifestPayload with the release private key.
// Returns a Signature suitable for serialisation with signing.MarshalSig.
//
// It REFUSES to sign an inconsistent or unrenderable release-severity surface
// (osdist.ValidateSecuritySurface) — the same rule every verifier applies. A
// signer that will happily sign what no box will read produces artifacts that
// fail in the field instead of at the ceremony.
//
// The caller MUST have validated the release cert (ValidateReleaseCert) before
// trusting output from this function on the verifier side.
func SignManifest(releasePriv ed25519.PrivateKey, keyID string, payload ManifestPayload) (signing.Signature, error) {
	if len(releasePriv) != ed25519.PrivateKeySize {
		return signing.Signature{}, errors.New("sign: releasePriv must be a valid Ed25519 private key")
	}
	if err := osdist.ValidateSecuritySurface(payload.IsSecurity, payload.Severity, payload.Notes); err != nil {
		return signing.Signature{}, fmt.Errorf("sign: %w", err)
	}
	canonical, err := signing.Canonical(payload)
	if err != nil {
		return signing.Signature{}, fmt.Errorf("sign: canonical manifest payload: %w", err)
	}
	return signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     keyID,
		SigBytes:  signing.Sign(releasePriv, canonical),
	}, nil
}

// VerifyImage verifies that sig is a valid release-key signature over payload.
// The caller must supply the release public key (extracted from a validated cert).
func VerifyImage(releasePub ed25519.PublicKey, payload ImagePayload, sig signing.Signature) (bool, error) {
	canonical, err := signing.Canonical(payload)
	if err != nil {
		return false, fmt.Errorf("sign: canonical image payload: %w", err)
	}
	return signing.Verify(releasePub, canonical, sig.SigBytes), nil
}

// VerifyManifest verifies that sig is a valid release-key signature over payload.
// The caller must supply the release public key (extracted from a validated cert).
func VerifyManifest(releasePub ed25519.PublicKey, payload ManifestPayload, sig signing.Signature) (bool, error) {
	canonical, err := signing.Canonical(payload)
	if err != nil {
		return false, fmt.Errorf("sign: canonical manifest payload: %w", err)
	}
	return signing.Verify(releasePub, canonical, sig.SigBytes), nil
}

// ─── Key serialisation helpers ────────────────────────────────────────────────

// MarshalPrivateKey serialises an Ed25519 private key to a JSON object.
//
// NOTE: this is for LOCAL/DEV/OFFLINE use only.  The resulting file MUST be
// kept on the air-gapped machine or inside the HSM.  Never copy a root private
// key to an online system.
func MarshalPrivateKey(priv ed25519.PrivateKey) ([]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("sign: not a valid Ed25519 private key")
	}
	return json.Marshal(map[string]string{
		"algorithm":   "ed25519",
		"private_key": hex.EncodeToString(priv),
		"public_key":  hex.EncodeToString(priv.Public().(ed25519.PublicKey)),
	})
}

// MarshalPublicKey serialises an Ed25519 public key to a JSON object.
func MarshalPublicKey(pub ed25519.PublicKey) ([]byte, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("sign: not a valid Ed25519 public key")
	}
	return json.Marshal(map[string]string{
		"algorithm":  "ed25519",
		"public_key": hex.EncodeToString(pub),
	})
}

// ParsePrivateKey deserialises a private key produced by MarshalPrivateKey.
func ParsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("sign: parse private key JSON: %w", err)
	}
	privHex, ok := m["private_key"]
	if !ok {
		return nil, errors.New("sign: missing 'private_key' field")
	}
	b, err := hex.DecodeString(privHex)
	if err != nil {
		return nil, fmt.Errorf("sign: decode private_key hex: %w", err)
	}
	if len(b) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("sign: private key has %d bytes, want %d", len(b), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(b), nil
}

// GenerateKey generates a fresh Ed25519 keypair using crypto/rand.
// Intended for generating the offline root key and the online release key.
// The caller is responsible for protecting the returned private key.
func generateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// ParsePublicKey deserialises a public key from a JSON object that has a
// "public_key" hex field (produced by MarshalPublicKey or MarshalPrivateKey).
func ParsePublicKey(data []byte) (ed25519.PublicKey, error) {
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("sign: parse public key JSON: %w", err)
	}
	pubHex, ok := m["public_key"]
	if !ok {
		return nil, errors.New("sign: missing 'public_key' field")
	}
	b, err := hex.DecodeString(pubHex)
	if err != nil {
		return nil, fmt.Errorf("sign: decode public_key hex: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("sign: public key has %d bytes, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}
