package signing

// releasecert.go — the canonical implementation of the Vulos two-level PKI's
// release-key certificate (roadmap/SIGNING.md, docs/KEY-CEREMONY.md).
//
//	Offline ROOT key  (air-gapped / HSM — never online)
//	   │  signs (offline, at ceremony time)
//	   ▼
//	RELEASE-key certificate  (ReleaseCert — this file)
//	   │  signs (routinely, online)
//	   ▼
//	os-core.squashfs.sig · stable.json.sig · registry.json entry signatures
//
// The ROOT public key is the trust anchor baked into the image at
// DefaultAnchorPath.  A device validates the release cert against that anchor
// before trusting *anything* the release key signed.  This is what lets the
// root key stay offline: rotating the release key is an offline root operation
// that produces a new cert, and nothing else on the box has to change.
//
// cmd/sign (issuer) and cmd/verify (initramfs verifier) both delegate here so
// that there is exactly one definition of the signed byte range.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// DefaultReleaseCertPath is the canonical in-image path where the root-signed
// release certificate is installed alongside the trust anchor.  Callers that
// verify release-key signatures load the anchor from DefaultAnchorPath and the
// cert from here.
const DefaultReleaseCertPath = "/etc/vulos/release-cert.json"

// ReleaseCert is a root-signed certificate that authorises a release public key.
//
// The root key signs canonical(CertBody) — every field EXCEPT RootSig.
//
//   - ReleasePubKey — hex-encoded Ed25519 public key authorised to sign artifacts.
//   - KeyID         — opaque human-readable label (e.g. "release-2026-07").
//   - NotAfter      — wall-clock expiry, RFC 3339.  Validation fails once passed.
//   - MinEpoch      — minimum trusted epoch this cert was issued for.  Callers
//     that track an epoch floor MUST refuse a cert below it (rollback defence).
//   - RootSig       — base64 Ed25519 signature by the root key over canonical(CertBody).
type ReleaseCert struct {
	ReleasePubKey string `json:"release_pubkey"`
	KeyID         string `json:"key_id"`
	NotAfter      string `json:"not_after"`
	MinEpoch      int64  `json:"min_epoch"`
	RootSig       string `json:"root_sig"`
}

// CertBody is the subset of ReleaseCert covered by the root signature.
// It is the exact byte range the root key signs (via Canonical).
type CertBody struct {
	ReleasePubKey string `json:"release_pubkey"`
	KeyID         string `json:"key_id"`
	NotAfter      string `json:"not_after"`
	MinEpoch      int64  `json:"min_epoch"`
}

// BodyOf converts a ReleaseCert into its signable CertBody.
func BodyOf(c ReleaseCert) CertBody {
	return CertBody{
		ReleasePubKey: c.ReleasePubKey,
		KeyID:         c.KeyID,
		NotAfter:      c.NotAfter,
		MinEpoch:      c.MinEpoch,
	}
}

// IssueReleaseCert creates a root-signed release-key certificate.
//
// This is an OFFLINE ROOT OPERATION.  rootPriv must come from the air-gapped
// machine or HSM that holds the root key; it must never be present on a CI
// runner or any internet-connected host.  See docs/KEY-CEREMONY.md.
func IssueReleaseCert(
	rootPriv ed25519.PrivateKey,
	releasePub ed25519.PublicKey,
	keyID string,
	notAfter time.Time,
	minEpoch int64,
) (ReleaseCert, error) {
	if len(rootPriv) != ed25519.PrivateKeySize {
		return ReleaseCert{}, errors.New("signing: rootPriv must be a valid Ed25519 private key")
	}
	if len(releasePub) != ed25519.PublicKeySize {
		return ReleaseCert{}, errors.New("signing: releasePub must be a valid Ed25519 public key")
	}
	if keyID == "" {
		return ReleaseCert{}, errors.New("signing: keyID must not be empty")
	}
	if minEpoch < 0 {
		return ReleaseCert{}, errors.New("signing: minEpoch must be non-negative")
	}

	body := CertBody{
		ReleasePubKey: hex.EncodeToString(releasePub),
		KeyID:         keyID,
		NotAfter:      notAfter.UTC().Format(time.RFC3339),
		MinEpoch:      minEpoch,
	}

	canonical, err := Canonical(body)
	if err != nil {
		return ReleaseCert{}, fmt.Errorf("signing: canonical cert body: %w", err)
	}

	return ReleaseCert{
		ReleasePubKey: body.ReleasePubKey,
		KeyID:         body.KeyID,
		NotAfter:      body.NotAfter,
		MinEpoch:      body.MinEpoch,
		RootSig:       base64.StdEncoding.EncodeToString(Sign(rootPriv, canonical)),
	}, nil
}

// ValidateReleaseCert verifies a release-key certificate against the baked root
// public key.
//
// Fail-closed: any error (bad signature, expiry, malformed data) means the
// release key MUST NOT be trusted and the caller must refuse the artifact.
func ValidateReleaseCert(rootPub ed25519.PublicKey, cert ReleaseCert) error {
	return ValidateReleaseCertAt(rootPub, cert, time.Now().UTC())
}

// ValidateReleaseCertAt is ValidateReleaseCert with an injectable clock so
// tests can exercise the expiry path without sleeping.
//
// Checks, in order, every one of which fails closed:
//  1. rootPub length sanity.
//  2. NotAfter — the cert must not be expired relative to now.
//  3. MinEpoch — non-negative sanity (monotonic enforcement is the caller's job).
//  4. RootSig  — Ed25519 verification of canonical(CertBody) against rootPub.
func ValidateReleaseCertAt(rootPub ed25519.PublicKey, cert ReleaseCert, now time.Time) error {
	if len(rootPub) != ed25519.PublicKeySize {
		return fmt.Errorf("signing: rootPub must be %d bytes, got %d", ed25519.PublicKeySize, len(rootPub))
	}

	notAfter, err := time.Parse(time.RFC3339, cert.NotAfter)
	if err != nil {
		return fmt.Errorf("signing: invalid not_after %q: %w", cert.NotAfter, err)
	}
	if now.After(notAfter) {
		return fmt.Errorf("signing: release cert expired at %s (now %s)",
			cert.NotAfter, now.UTC().Format(time.RFC3339))
	}

	if cert.MinEpoch < 0 {
		return fmt.Errorf("signing: invalid min_epoch %d in release cert", cert.MinEpoch)
	}

	sigBytes, err := base64.StdEncoding.DecodeString(cert.RootSig)
	if err != nil {
		return fmt.Errorf("signing: base64 decode root_sig: %w", err)
	}

	canonical, err := Canonical(BodyOf(cert))
	if err != nil {
		return fmt.Errorf("signing: canonical cert body: %w", err)
	}

	if !Verify(rootPub, canonical, sigBytes) {
		return errors.New("signing: release cert root signature invalid")
	}
	return nil
}

// DecodePubKey extracts and decodes the Ed25519 release public key from the cert.
func (c ReleaseCert) DecodePubKey() (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(c.ReleasePubKey)
	if err != nil {
		return nil, fmt.Errorf("signing: decode release_pubkey hex: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("signing: release_pubkey has %d bytes, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

// ParseReleaseCert unmarshals a ReleaseCert from JSON and rejects any cert that
// is missing a required field.  A cert with an empty root_sig would otherwise
// sail past a naive unmarshal and only fail later.
func ParseReleaseCert(data []byte) (ReleaseCert, error) {
	var cert ReleaseCert
	if err := json.Unmarshal(data, &cert); err != nil {
		return ReleaseCert{}, fmt.Errorf("signing: unmarshal release cert: %w", err)
	}
	if cert.ReleasePubKey == "" || cert.KeyID == "" || cert.NotAfter == "" || cert.RootSig == "" {
		return ReleaseCert{}, errors.New(
			"signing: release cert missing required fields (release_pubkey/key_id/not_after/root_sig)")
	}
	return cert, nil
}

// LoadReleaseCert reads and parses the release cert at path.
func LoadReleaseCert(path string) (ReleaseCert, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ReleaseCert{}, fmt.Errorf("signing: read release cert %q: %w", path, err)
	}
	return ParseReleaseCert(data)
}

// ReleaseKeyFromCert is the full chain in one call: validate cert against the
// root anchor, then hand back the release public key it authorises.
//
// This is the only sanctioned way to obtain a release key from a cert — it is
// impossible to use the key without first proving the root signed for it.
func ReleaseKeyFromCert(rootPub ed25519.PublicKey, cert ReleaseCert) (ed25519.PublicKey, error) {
	if err := ValidateReleaseCert(rootPub, cert); err != nil {
		return nil, err
	}
	return cert.DecodePubKey()
}
