// rotation.go — device-key ROTATION (normal + break-glass).
//
// # Two authorization paths, exactly one mechanism each
//
//   - NORMAL rotation (RotationMethodSelf): the box still holds its current
//     device key. That key signs a RotationCert binding old→new BEFORE the
//     new key becomes active, proving possession of the key being retired.
//     Entry point: KeyStore.Rotate (software.go / tpm.go).
//
//   - BREAK-GLASS rotation (RotationMethodBreakGlass): the old key is lost or
//     compromised and therefore CANNOT be trusted to authorize anything —
//     signing with it would prove nothing (an attacker who stole it could
//     sign too). Authorization instead comes from a QUORUM of OTHER boxes
//     vouching for the recovery via fleetid.VerifyQuorum — the SAME keystone
//     used for device-enroll and ssh-recovery break-glass. Entry point:
//     BreakGlassRotate, below.
//
// # The hard rule
//
// A box must NEVER be able to authorize its own break-glass rotation. This is
// enforced STRUCTURALLY, not just by convention:
//
//   - BreakGlassRotate calls fleetid.VerifyQuorum FIRST. On any error
//     (including ErrInsufficientQuorum) it returns without touching key
//     material at all.
//   - The only primitive capable of installing a new identity WITHOUT the old
//     key's signature is KeyStore.forceInstallIdentity, which is UNEXPORTED.
//     Because Go requires a type to be declared in the same package to
//     implement an interface with unexported methods, forceInstallIdentity
//     can only ever be invoked from code inside package devicekey — and the
//     only such call site is right here, after VerifyQuorum has already
//     succeeded. There is no HTTP handler, no CLI, no test helper anywhere
//     that can reach forceInstallIdentity without going through this
//     function and its quorum check.
package devicekey

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"vulos/backend/services/fleetid"
	"vulos/backend/services/signing"
)

// ErrActiveKeyChanged is returned by forceInstallIdentity (and surfaced by
// BreakGlassRotate/BreakGlassRevoke) when the active device key no longer
// matches the key the quorum was verified against — a concurrent rotation
// swapped it in the gap between the caller's identity read and the install.
// The operation installs/records nothing; the caller should re-read the
// current key, gather a fresh quorum over it, and retry.
var ErrActiveKeyChanged = errors.New("devicekey: active identity key changed between quorum verification and install (concurrent rotation?); nothing installed — retry with a fresh quorum over the current key")

// checkExpectedOldKey is the compare-and-swap guard shared by both backends'
// forceInstallIdentity: it fails closed (ErrActiveKeyChanged) unless the
// currently-active key exactly equals the key the caller verified the quorum
// against. Caller must already hold the store lock so current cannot change
// under it. A nil/empty expected is itself a failure — break-glass callers
// always know the old key.
func checkExpectedOldKey(current, expected []byte) error {
	if len(expected) == 0 || !bytes.Equal(current, expected) {
		return ErrActiveKeyChanged
	}
	return nil
}

// RotationOverlap is the grace window during which the PREVIOUS identity key
// is still considered valid by a verifier that checks PreviousIdentity() —
// mirrors internal/multiinstance's DefaultRotationOverlap (FABRIC-KEY-01).
const RotationOverlap = 24 * time.Hour

// Rotation method tags recorded on a RotationCert.
const (
	RotationMethodSelf       = "self"
	RotationMethodBreakGlass = "break-glass"
)

// rotationCertType domain-tags every RotationCert to prevent cross-protocol
// signature confusion with other signed artifacts in this codebase.
const rotationCertType = "devicekey-rotation-v1"

// RotationCert binds a device's OLD identity public key to a NEW one.
type RotationCert struct {
	Type   string `json:"type"`   // always rotationCertType
	Method string `json:"method"` // RotationMethodSelf | RotationMethodBreakGlass

	// OldPubKey / NewPubKey are base64-standard PKIX DER ECDSA P-256 public keys.
	OldPubKey string `json:"old_pub_key"`
	NewPubKey string `json:"new_pub_key"`

	Reason   string `json:"reason,omitempty"`
	IssuedAt string `json:"issued_at"` // RFC3339 UTC
	Nonce    string `json:"nonce"`

	// ── Break-glass authorization evidence (Method == RotationMethodBreakGlass only) ──
	//
	// QuorumSubjectID is the Vula ID of THIS box's fleet-fabric identity (the
	// subject the quorum vouches FOR — NOT the device key itself, which uses a
	// different algorithm/encoding and has no Vula ID). QuorumRequestID is the
	// caller-chosen nonce the vouchers signed the payload hash over; binding to
	// it stops a vouch bundle gathered for one recovery request from being
	// replayed against a different one.
	QuorumSubjectID string              `json:"quorum_subject_id,omitempty"`
	QuorumRequestID string              `json:"quorum_request_id,omitempty"`
	QuorumCerts     []fleetid.VouchCert `json:"quorum_certs,omitempty"`

	// Sig is the base64-standard ASN.1 ECDSA signature over the canonical
	// bytes of this cert with Sig cleared.
	//   - self:        signed by the OLD device key (proves possession).
	//   - break-glass: signed by the NEW device key (proves possession of the
	//     freshly installed key; the AUTHORIZATION itself is QuorumCerts,
	//     independently re-verified by VerifyRotationCert — the signer here is
	//     never trusted for authorization, only for key-possession).
	Sig string `json:"sig"`
}

func newRotationCert(method string, oldPubDER, newPubDER []byte, reason string) *RotationCert {
	return &RotationCert{
		Type:      rotationCertType,
		Method:    method,
		OldPubKey: base64.StdEncoding.EncodeToString(oldPubDER),
		NewPubKey: base64.StdEncoding.EncodeToString(newPubDER),
		Reason:    reason,
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
		Nonce:     randNonce(),
	}
}

func (c *RotationCert) setSig(sig []byte) {
	c.Sig = base64.StdEncoding.EncodeToString(sig)
}

// rotationSigningDigest returns the SHA-256 digest of the canonical bytes of
// cert with Sig cleared — the exact bytes both signing and verification
// operate over.
func rotationSigningDigest(cert *RotationCert) ([]byte, error) {
	unsigned := *cert
	unsigned.Sig = ""
	payload, err := signing.Canonical(unsigned)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(payload)
	return h[:], nil
}

func randNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ─── Verification ──────────────────────────────────────────────────────────────

// VerifyRotationCert checks a RotationCert's structure and authorization.
//
//   - Method == self:        Sig must verify against OldPubKey.
//   - Method == break-glass: Sig must verify against NewPubKey (key
//     possession), AND QuorumCerts must independently satisfy
//     fleetid.VerifyQuorum for action=IdentityRecovery, subject=
//     QuorumSubjectID, payload=hash(QuorumRequestID || OldPubKey || NewPubKey).
//     roster/threshold/now are passed straight through to VerifyQuorum.
//
// Fails closed on any structurally invalid, mistyped, or unverifiable input.
func VerifyRotationCert(cert *RotationCert, roster fleetid.Roster, threshold int, now time.Time) error {
	if cert == nil {
		return errors.New("devicekey: nil rotation cert")
	}
	if cert.Type != rotationCertType {
		return fmt.Errorf("devicekey: rotation cert wrong type %q", cert.Type)
	}
	oldPub, err := decodeECDSAPub(cert.OldPubKey)
	if err != nil {
		return fmt.Errorf("devicekey: rotation cert old pubkey: %w", err)
	}
	newPub, err := decodeECDSAPub(cert.NewPubKey)
	if err != nil {
		return fmt.Errorf("devicekey: rotation cert new pubkey: %w", err)
	}
	digest, err := rotationSigningDigest(cert)
	if err != nil {
		return fmt.Errorf("devicekey: rotation cert digest: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(cert.Sig)
	if err != nil {
		return errors.New("devicekey: rotation cert malformed signature")
	}

	switch cert.Method {
	case RotationMethodSelf:
		if !ecdsa.VerifyASN1(oldPub, digest, sig) {
			return errors.New("devicekey: rotation cert signature does not verify against old key")
		}
		return nil

	case RotationMethodBreakGlass:
		if !ecdsa.VerifyASN1(newPub, digest, sig) {
			return errors.New("devicekey: rotation cert signature does not verify against new key")
		}
		if len(cert.QuorumCerts) == 0 {
			return errors.New("devicekey: break-glass rotation cert carries no quorum certs")
		}
		payloadHash := breakGlassPayloadHash(cert.QuorumRequestID, cert.OldPubKey, cert.NewPubKey)
		if _, err := fleetid.VerifyQuorum(fleetid.ActionIdentityRecovery, cert.QuorumSubjectID, payloadHash, cert.QuorumCerts, roster, threshold, now); err != nil {
			return fmt.Errorf("devicekey: break-glass rotation quorum: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("devicekey: rotation cert unknown method %q", cert.Method)
	}
}

// breakGlassPayloadHash is the exact payload a quorum of peer boxes vouches
// for: it binds the request nonce (single-use, stops an old vouch bundle
// being replayed for a later rotation) to the specific old/new key pair being
// transitioned, so a vouch minted for one rotation can never authorize
// another.
func breakGlassPayloadHash(requestID, oldPubB64, newPubB64 string) []byte {
	h := sha256.New()
	h.Write([]byte("vulos:devicekey:break-glass-rotation:v1"))
	h.Write([]byte{0})
	h.Write([]byte(requestID))
	h.Write([]byte{0})
	h.Write([]byte(oldPubB64))
	h.Write([]byte{0})
	h.Write([]byte(newPubB64))
	return h.Sum(nil)
}

// BreakGlassPayloadHash exposes breakGlassPayloadHash so operator tooling
// gathering peer vouches ahead of a BreakGlassRotate call can compute the
// EXACT payload hash the vouchers must sign over. oldPubDER/newPubDER are
// PKIX DER bytes (the same encoding GenerateCandidateKey / DeviceIdentity
// return); this handles the base64 encoding internally.
func BreakGlassPayloadHash(requestID string, oldPubDER, newPubDER []byte) []byte {
	return breakGlassPayloadHash(requestID,
		base64.StdEncoding.EncodeToString(oldPubDER),
		base64.StdEncoding.EncodeToString(newPubDER))
}

func decodeECDSAPub(b64 string) (*ecdsa.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("bad base64: %w", err)
	}
	pubAny, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse PKIX: %w", err)
	}
	pub, ok := pubAny.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("not an ECDSA public key")
	}
	if pub.Curve != elliptic.P256() {
		return nil, errors.New("not a P-256 key")
	}
	return pub, nil
}

// ─── Break-glass entry point ────────────────────────────────────────────────────

// GenerateCandidateKey mints a fresh ECDSA P-256 keypair suitable for a
// break-glass rotation. Operator tooling generates the candidate, computes
// BreakGlassPayloadHash(requestID, oldPubDER, candidatePub), gathers a quorum
// of VouchCerts over that hash from OTHER boxes, and then calls
// BreakGlassRotate with the same candidate key and the collected certs.
func GenerateCandidateKey() (*ecdsa.PrivateKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("devicekey: GenerateCandidateKey: %w", err)
	}
	return priv, nil
}

// BreakGlassRotate is the SOLE sanctioned way to rotate a device identity
// without the old key's signature. It:
//
//  1. Reads the current (old) device public key.
//  2. Verifies quorum via fleetid.VerifyQuorum — action=IdentityRecovery,
//     subject=subjectID, payload=BreakGlassPayloadHash(requestID, old, new).
//     On ANY failure (including insufficient quorum) it returns the error
//     immediately and NEVER touches key material — self-authorization is
//     impossible because there is no path from here to installing a key
//     without this check passing first.
//  3. Only on success, installs candidatePriv as the active identity via the
//     UNEXPORTED KeyStore.forceInstallIdentity primitive.
//  4. Signs the resulting RotationCert with the NEW key (proves possession;
//     the authorization itself is the already-verified quorum) and returns it.
//
// subjectID is the Vula ID of this box's fleet-fabric identity (NOT the
// device key) — the identity the OTHER boxes' vouches are actually about.
// requestID is a caller-chosen, single-use nonce that must match what the
// vouchers signed over (see GenerateCandidateKey's doc comment for the
// gather-vouches flow). roster/threshold are passed straight through to
// fleetid.VerifyQuorum.
func BreakGlassRotate(ks KeyStore, candidatePriv *ecdsa.PrivateKey, reason, subjectID, requestID string, certs []fleetid.VouchCert, roster fleetid.Roster, threshold int, now time.Time) (*RotationCert, error) {
	if ks == nil {
		return nil, errors.New("devicekey: BreakGlassRotate: nil key store")
	}
	if candidatePriv == nil {
		return nil, errors.New("devicekey: BreakGlassRotate: nil candidate key")
	}
	oldPubDER, err := ks.DeviceIdentity()
	if err != nil {
		return nil, fmt.Errorf("devicekey: BreakGlassRotate: read current identity: %w", err)
	}
	newPubDER, err := x509.MarshalPKIXPublicKey(&candidatePriv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("devicekey: BreakGlassRotate: marshal candidate pubkey: %w", err)
	}

	// ── THE CHOKEPOINT ──────────────────────────────────────────────────────
	// Quorum MUST verify before any key material is touched. VerifyQuorum
	// itself enforces self-exclusion (a voucher can never equal subjectID), so
	// even if a caller mistakenly passed a self-vouch it would not count.
	payloadHash := BreakGlassPayloadHash(requestID, oldPubDER, newPubDER)
	if _, err := fleetid.VerifyQuorum(fleetid.ActionIdentityRecovery, subjectID, payloadHash, certs, roster, threshold, now); err != nil {
		return nil, fmt.Errorf("devicekey: BreakGlassRotate: %w", err)
	}

	// Quorum verified — now (and only now) install the new identity. This is
	// the ONLY call site of forceInstallIdentity in the entire codebase.
	// Pass the old key the quorum was verified against so the install is a
	// compare-and-swap: if a concurrent rotation changed the active key in the
	// gap since ks.DeviceIdentity() above, this fails closed (ErrActiveKeyChanged)
	// rather than binding the cert to a key the quorum never signed over.
	gotOldPubDER, err := ks.forceInstallIdentity(candidatePriv, oldPubDER)
	if err != nil {
		return nil, fmt.Errorf("devicekey: BreakGlassRotate: install: %w", err)
	}

	cert := newRotationCert(RotationMethodBreakGlass, gotOldPubDER, newPubDER, reason)
	cert.QuorumSubjectID = subjectID
	cert.QuorumRequestID = requestID
	cert.QuorumCerts = certs
	digest, err := rotationSigningDigest(cert)
	if err != nil {
		return nil, fmt.Errorf("devicekey: BreakGlassRotate: digest: %w", err)
	}
	sig, err := ks.Sign(digest, crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("devicekey: BreakGlassRotate: sign with new key: %w", err)
	}
	cert.setSig(sig)
	return cert, nil
}

// ─── Shared previous-key persistence (used by both software.go and tpm.go) ──────
//
// Both backends need the exact same "remember what I rotated away from, for a
// grace window" bookkeeping (mirrors FABRIC-KEY-01's overlap window in
// internal/multiinstance). Factored here once so the two backends cannot
// silently drift apart on this security-relevant detail.

func prevPubKeyPath(keyDir string) string { return filepath.Join(keyDir, "device_key.prev.pub") }
func prevExpiryPath(keyDir string) string { return filepath.Join(keyDir, "device_key.prev_expiry") }

// writePrevKeyState persists the identity key rotated AWAY FROM plus a grace
// expiry of now+RotationOverlap. Best-effort (mirrors atomicWrite's existing
// non-fatal writes elsewhere): a failure here only narrows what a verifier
// will later accept, never widens it.
func writePrevKeyState(keyDir string, oldPubDER []byte) time.Time {
	expiresAt := time.Now().UTC().Add(RotationOverlap)
	_ = atomicWrite(prevPubKeyPath(keyDir), oldPubDER, 0644)
	_ = atomicWrite(prevExpiryPath(keyDir), []byte(expiresAt.Format(time.RFC3339)), 0644)
	return expiresAt
}

// loadPrevKeyState restores rotation-overlap state left from a previous
// process's rotation. Returns (nil, zero-time) on any read/parse failure or
// absence — narrowing, never widening, what is accepted.
func loadPrevKeyState(keyDir string) (pubDER []byte, expiresAt time.Time) {
	pub, err := os.ReadFile(prevPubKeyPath(keyDir))
	if err != nil || len(pub) == 0 {
		return nil, time.Time{}
	}
	raw, err := os.ReadFile(prevExpiryPath(keyDir))
	if err != nil {
		return nil, time.Time{}
	}
	exp, err := time.Parse(time.RFC3339, string(raw))
	if err != nil {
		return nil, time.Time{}
	}
	return pub, exp
}

// PreviousIdentityProvider is implemented by KeyStore backends that retain
// the identity key rotated AWAY FROM during the FABRIC-KEY-01-style grace
// window (see RotationOverlap). Both software.go and tpm.go implement it;
// callers should type-assert rather than adding it to the core KeyStore
// interface (it is an optional capability, not core to Seal/Sign/Unseal).
type PreviousIdentityProvider interface {
	PreviousIdentity() (pubKeyDER []byte, expiresAt time.Time)
}
