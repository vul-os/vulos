// identity_lifecycle.go — VulaID key lifecycle: rotation, revocation, recovery.
//
// # Problem
//
// A Vula identity was historically a single static on-disk Ed25519 key with NO
// rotation, revocation, or recovery: losing the key lost the identity, and a
// compromised key could never be retired. This file adds the three missing
// lifecycle operations and the verification/admission plumbing that honors them.
//
// All identifiers here use the canonical base58 Vula ID encoding (encodeVulaID /
// decodeVulaID in identity.go), which is the encoding used by the envelope
// Verify path and VerifyVulaSignature — i.e. the identities that actually gate
// inbound traffic.
//
// # Operations
//
//   - ROTATION (RotationCert): the holder of the OLD identity key signs a
//     certificate naming the NEW key. Peers that pinned the old identity follow
//     the chain old→new and update their contact. Used for routine key hygiene
//     while the old key is still available.
//
//   - REVOCATION (RevocationCert): a certificate that retires an identity. It may
//     be signed by the identity key itself (self-revocation) OR by the account
//     recovery anchor (so a LOST or COMPROMISED key can still be revoked). Once a
//     revocation is observed, every verify/admission point rejects the identity.
//
//   - RECOVERY (RecoveryCert): account-anchored re-establishment. A long-lived
//     recovery ANCHOR key (derived deterministically from the account recovery
//     kit / mnemonic — see DeriveRecoveryAnchor) signs a certificate binding the
//     old identity to a brand-new identity key. Because the anchor survives the
//     loss/compromise of the identity key, a peer that pinned the anchor at first
//     contact can follow recovery even when the old identity key is gone. This is
//     what makes a lost key recoverable rather than terminal.
//
// # Trust model for following a chain
//
// A peer trusts a transition to a new key when it can verify the signature with a
// key it ALREADY trusts:
//   - rotation link: signed by the immediately-preceding identity key.
//   - recovery link: signed by the recovery anchor the peer pinned for this
//     identity (TOFU at first contact, or distributed out-of-band).
//
// A peer that has not pinned the anchor cannot be tricked into following a
// recovery it cannot verify — the resolver FAILS CLOSED on any unverifiable link.
package peering

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"crypto/sha256"
	"golang.org/x/crypto/hkdf"

	"vulos/backend/services/signing"
)

// ─── Certificate types ─────────────────────────────────────────────────────────

// LifecycleCertType tags the kind of lifecycle certificate.
type LifecycleCertType string

const (
	CertTypeRotation   LifecycleCertType = "vula-rotation"
	CertTypeRevocation LifecycleCertType = "vula-revocation"
	CertTypeRecovery   LifecycleCertType = "vula-recovery"
)

// RotationCert chains an old identity key to a new one. It is signed by the OLD
// key, proving the holder of the old key authorized the new key.
type RotationCert struct {
	Type LifecycleCertType `json:"type"` // CertTypeRotation
	// PrevVulaID is the identity being rotated away from (the signer).
	PrevVulaID string `json:"prev_vula_id"`
	// NewVulaID is the identity being rotated to.
	NewVulaID string `json:"new_vula_id"`
	// IssuedAt is the issue time (RFC3339 UTC).
	IssuedAt string `json:"issued_at"`
	// Nonce defends against any future cross-protocol confusion; random per cert.
	Nonce string `json:"nonce"`
	// Sig is the base64url Ed25519 signature by PrevVulaID's key over the
	// canonical bytes of this cert with Sig empty.
	Sig string `json:"sig"`
}

// RevocationCert retires an identity. Signer is either the identity itself
// (SignerVulaID == VulaID) or the recovery anchor (SignerVulaID == anchor id).
type RevocationCert struct {
	Type LifecycleCertType `json:"type"` // CertTypeRevocation
	// VulaID is the identity being revoked.
	VulaID string `json:"vula_id"`
	// SignerVulaID is the key that signed this revocation: the identity itself
	// (self-revocation) or the account recovery anchor.
	SignerVulaID string `json:"signer_vula_id"`
	// Reason is a short human-readable reason (e.g. "compromised", "rotated").
	Reason string `json:"reason,omitempty"`
	// IssuedAt is the issue time (RFC3339 UTC).
	IssuedAt string `json:"issued_at"`
	// Sig is the base64url Ed25519 signature by SignerVulaID's key.
	Sig string `json:"sig"`
}

// RecoveryCert re-establishes an identity onto a new key, signed by the account
// recovery anchor. Unlike a RotationCert it does NOT require the old identity key
// (which may be lost/compromised) — only the anchor.
type RecoveryCert struct {
	Type LifecycleCertType `json:"type"` // CertTypeRecovery
	// PrevVulaID is the identity being recovered (whose key was lost/compromised).
	PrevVulaID string `json:"prev_vula_id"`
	// NewVulaID is the freshly generated replacement identity.
	NewVulaID string `json:"new_vula_id"`
	// AnchorVulaID is the account recovery anchor that signs this cert. A peer
	// must already trust this anchor for this identity to follow the recovery.
	AnchorVulaID string `json:"anchor_vula_id"`
	// IssuedAt is the issue time (RFC3339 UTC).
	IssuedAt string `json:"issued_at"`
	// Sig is the base64url Ed25519 signature by AnchorVulaID's key.
	Sig string `json:"sig"`
}

// ─── Canonical signing helpers ─────────────────────────────────────────────────

// lcSign signs the canonical bytes of cert (with its Sig field cleared) using
// priv, returning the base64url signature. setSig clears the relevant Sig field
// before canonicalising; getCanonical returns the value to canonicalise.
func lcSignCanonical(v any, priv ed25519.PrivateKey) (string, error) {
	payload, err := signing.Canonical(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, payload)), nil
}

// lcVerifyCanonical verifies sigB64 over the canonical bytes of v using the key
// embedded in signerVulaID. Fails closed on any malformed input.
func lcVerifyCanonical(v any, sigB64, signerVulaID string) error {
	pub, err := decodeVulaID(signerVulaID)
	if err != nil {
		return fmt.Errorf("lifecycle: bad signer vula id: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("lifecycle: malformed signature")
	}
	payload, err := signing.Canonical(v)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return errors.New("lifecycle: signature mismatch")
	}
	return nil
}

func lcNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ─── Build certificates ────────────────────────────────────────────────────────

// NewRotationCert builds a rotation certificate signed by the OLD identity key.
// prevPriv must correspond to prevVulaID; newVulaID is the replacement identity.
func NewRotationCert(prevPriv ed25519.PrivateKey, prevVulaID, newVulaID string) (*RotationCert, error) {
	if _, err := decodeVulaID(prevVulaID); err != nil {
		return nil, fmt.Errorf("lifecycle: prev vula id: %w", err)
	}
	if _, err := decodeVulaID(newVulaID); err != nil {
		return nil, fmt.Errorf("lifecycle: new vula id: %w", err)
	}
	if !ed25519PrivMatchesVulaID(prevPriv, prevVulaID) {
		return nil, errors.New("lifecycle: prev private key does not match prev_vula_id")
	}
	c := &RotationCert{
		Type:       CertTypeRotation,
		PrevVulaID: prevVulaID,
		NewVulaID:  newVulaID,
		IssuedAt:   time.Now().UTC().Format(time.RFC3339),
		Nonce:      lcNonce(),
	}
	sig, err := lcSignCanonical(c, prevPriv)
	if err != nil {
		return nil, err
	}
	c.Sig = sig
	return c, nil
}

// Verify checks the rotation cert's structure and signature (by PrevVulaID).
func (c *RotationCert) Verify() error {
	if c.Type != CertTypeRotation {
		return fmt.Errorf("lifecycle: rotation cert wrong type %q", c.Type)
	}
	if c.PrevVulaID == "" || c.NewVulaID == "" || c.PrevVulaID == c.NewVulaID {
		return errors.New("lifecycle: rotation cert prev/new ids invalid")
	}
	unsigned := *c
	unsigned.Sig = ""
	return lcVerifyCanonical(unsigned, c.Sig, c.PrevVulaID)
}

// NewRevocationCert builds a revocation signed by signerPriv. When
// signerVulaID == vulaID this is a self-revocation; otherwise signerVulaID is the
// recovery anchor.
func NewRevocationCert(signerPriv ed25519.PrivateKey, vulaID, signerVulaID, reason string) (*RevocationCert, error) {
	if _, err := decodeVulaID(vulaID); err != nil {
		return nil, fmt.Errorf("lifecycle: revoked vula id: %w", err)
	}
	if !ed25519PrivMatchesVulaID(signerPriv, signerVulaID) {
		return nil, errors.New("lifecycle: signer private key does not match signer_vula_id")
	}
	c := &RevocationCert{
		Type:         CertTypeRevocation,
		VulaID:       vulaID,
		SignerVulaID: signerVulaID,
		Reason:       reason,
		IssuedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	sig, err := lcSignCanonical(c, signerPriv)
	if err != nil {
		return nil, err
	}
	c.Sig = sig
	return c, nil
}

// Verify checks the revocation signature. validAnchors, when non-nil, is the set
// of anchor Vula IDs trusted to revoke this identity (in addition to the identity
// itself). A revocation signed by neither the identity nor a trusted anchor is
// rejected — this prevents an unrelated key from revoking someone else's identity.
func (c *RevocationCert) Verify(validAnchors map[string]bool) error {
	if c.Type != CertTypeRevocation {
		return fmt.Errorf("lifecycle: revocation cert wrong type %q", c.Type)
	}
	if c.VulaID == "" || c.SignerVulaID == "" {
		return errors.New("lifecycle: revocation cert missing ids")
	}
	// Authorization: signer must be the identity itself or a trusted anchor.
	if c.SignerVulaID != c.VulaID && !validAnchors[c.SignerVulaID] {
		return fmt.Errorf("lifecycle: revocation signer %q is neither the identity nor a trusted anchor", c.SignerVulaID)
	}
	unsigned := *c
	unsigned.Sig = ""
	return lcVerifyCanonical(unsigned, c.Sig, c.SignerVulaID)
}

// NewRecoveryCert builds an account-anchored recovery cert signed by anchorPriv.
func NewRecoveryCert(anchorPriv ed25519.PrivateKey, prevVulaID, newVulaID, anchorVulaID string) (*RecoveryCert, error) {
	for _, id := range []string{prevVulaID, newVulaID, anchorVulaID} {
		if _, err := decodeVulaID(id); err != nil {
			return nil, fmt.Errorf("lifecycle: recovery cert id %q: %w", id, err)
		}
	}
	if !ed25519PrivMatchesVulaID(anchorPriv, anchorVulaID) {
		return nil, errors.New("lifecycle: anchor private key does not match anchor_vula_id")
	}
	c := &RecoveryCert{
		Type:         CertTypeRecovery,
		PrevVulaID:   prevVulaID,
		NewVulaID:    newVulaID,
		AnchorVulaID: anchorVulaID,
		IssuedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	sig, err := lcSignCanonical(c, anchorPriv)
	if err != nil {
		return nil, err
	}
	c.Sig = sig
	return c, nil
}

// Verify checks the recovery cert signature (by AnchorVulaID). The caller must
// SEPARATELY confirm it trusts AnchorVulaID for PrevVulaID (see ResolveCurrentKey).
func (c *RecoveryCert) Verify() error {
	if c.Type != CertTypeRecovery {
		return fmt.Errorf("lifecycle: recovery cert wrong type %q", c.Type)
	}
	if c.PrevVulaID == "" || c.NewVulaID == "" || c.AnchorVulaID == "" {
		return errors.New("lifecycle: recovery cert missing ids")
	}
	unsigned := *c
	unsigned.Sig = ""
	return lcVerifyCanonical(unsigned, c.Sig, c.AnchorVulaID)
}

// ─── Chain resolution ──────────────────────────────────────────────────────────

// LifecycleLink is one transition in an identity's published key history. Exactly
// one of Rotation / Recovery is set.
type LifecycleLink struct {
	Rotation *RotationCert `json:"rotation,omitempty"`
	Recovery *RecoveryCert `json:"recovery,omitempty"`
}

// ResolveCurrentKey follows an ordered chain of links starting from rootVulaID and
// returns the current (latest) Vula ID. It FAILS CLOSED:
//
//   - a rotation link must be signed by the current head key (the predecessor).
//   - a recovery link must be signed by trustedAnchor AND its PrevVulaID must be
//     the current head (so an anchor can only recover an identity it is bound to).
//   - trustedAnchor may be "" when the caller pinned no anchor; recovery links are
//     then un-followable and rejected.
//   - any structurally invalid or out-of-order link aborts resolution with error.
func ResolveCurrentKey(rootVulaID, trustedAnchor string, chain []LifecycleLink) (string, error) {
	if _, err := decodeVulaID(rootVulaID); err != nil {
		return "", fmt.Errorf("lifecycle: root vula id: %w", err)
	}
	head := rootVulaID
	for i, link := range chain {
		switch {
		case link.Rotation != nil && link.Recovery != nil:
			return "", fmt.Errorf("lifecycle: link %d sets both rotation and recovery", i)
		case link.Rotation != nil:
			rc := link.Rotation
			if rc.PrevVulaID != head {
				return "", fmt.Errorf("lifecycle: rotation link %d prev %q does not chain from head %q", i, rc.PrevVulaID, head)
			}
			if err := rc.Verify(); err != nil {
				return "", fmt.Errorf("lifecycle: rotation link %d: %w", i, err)
			}
			head = rc.NewVulaID
		case link.Recovery != nil:
			rec := link.Recovery
			if trustedAnchor == "" {
				return "", fmt.Errorf("lifecycle: recovery link %d but no trusted anchor pinned", i)
			}
			if rec.AnchorVulaID != trustedAnchor {
				return "", fmt.Errorf("lifecycle: recovery link %d anchor %q != trusted anchor %q", i, rec.AnchorVulaID, trustedAnchor)
			}
			if rec.PrevVulaID != head {
				return "", fmt.Errorf("lifecycle: recovery link %d prev %q does not chain from head %q", i, rec.PrevVulaID, head)
			}
			if err := rec.Verify(); err != nil {
				return "", fmt.Errorf("lifecycle: recovery link %d: %w", i, err)
			}
			head = rec.NewVulaID
		default:
			return "", fmt.Errorf("lifecycle: link %d is empty", i)
		}
	}
	return head, nil
}

// ─── Recovery anchor derivation ────────────────────────────────────────────────

// recoveryAnchorHKDFInfo separates the anchor key from any other key derived from
// the same recovery seed (e.g. the identity key in internal/auth/recovery.go,
// which uses a different label). This guarantees the anchor is an INDEPENDENT key
// so that compromising the identity key does not yield the anchor.
const recoveryAnchorHKDFInfo = "vula-recovery-anchor-ed25519-v1"

// DeriveRecoveryAnchor deterministically derives the account recovery ANCHOR
// Ed25519 keypair from the recovery-kit seed (the 64-byte BIP39 seed behind the
// user's 24-word mnemonic; see internal/auth/recovery.go RestoreFromMnemonic).
//
// Because the anchor is a deterministic function of the recovery seed, it is
// reproducible from the recovery kit alone — so a user who lost their identity
// key but still has their recovery kit can reconstruct the anchor, sign a
// RecoveryCert, and re-establish their identity on a new key. The anchor is a
// SEPARATE key from the identity key (distinct HKDF label), so an attacker who
// steals only the on-disk identity key cannot forge recoveries.
func DeriveRecoveryAnchor(recoverySeed []byte) (ed25519.PrivateKey, string, error) {
	if len(recoverySeed) < 16 {
		return nil, "", errors.New("lifecycle: recovery seed too short")
	}
	r := hkdf.New(sha256.New, recoverySeed, nil, []byte(recoveryAnchorHKDFInfo))
	seed := make([]byte, ed25519.SeedSize)
	if _, err := r.Read(seed); err != nil {
		return nil, "", fmt.Errorf("lifecycle: derive anchor: %w", err)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return priv, encodeVulaID(pub), nil
}

// ─── Persistent lifecycle state ────────────────────────────────────────────────

// lifecycleState is the JSON persisted to disk for THIS node's identity history
// and the revocations it has observed.
type lifecycleState struct {
	// RootVulaID is the original identity this node first published.
	RootVulaID string `json:"root_vula_id"`
	// AnchorVulaID is this node's account recovery anchor (public id only).
	AnchorVulaID string `json:"anchor_vula_id,omitempty"`
	// Chain is this node's own published transition history (rotation/recovery).
	Chain []LifecycleLink `json:"chain,omitempty"`
	// Revoked maps a revoked Vula ID → the verified revocation cert that retired
	// it. Consulted at every admission/verify point.
	Revoked map[string]*RevocationCert `json:"revoked,omitempty"`
	// PinnedAnchors maps a contact's ROOT Vula ID → the anchor this node trusts
	// to recover that contact (TOFU at first contact).
	PinnedAnchors map[string]string `json:"pinned_anchors,omitempty"`
}

// LifecycleStore is the goroutine-safe, persistent owner of lifecycle state. It
// is consulted by the admission/verify paths to reject revoked identities and to
// follow rotation/recovery chains.
type LifecycleStore struct {
	mu    sync.RWMutex
	path  string
	state lifecycleState
}

// NewLifecycleStore opens (or creates) the lifecycle store under dir
// (e.g. ~/.vulos/peering/identity). rootVulaID and anchorVulaID seed a fresh
// state; an existing state on disk takes precedence for those fields.
func NewLifecycleStore(dir, rootVulaID, anchorVulaID string) (*LifecycleStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("lifecycle: mkdir: %w", err)
	}
	s := &LifecycleStore{
		path: filepath.Join(dir, "lifecycle.json"),
		state: lifecycleState{
			RootVulaID:    rootVulaID,
			AnchorVulaID:  anchorVulaID,
			Revoked:       map[string]*RevocationCert{},
			PinnedAnchors: map[string]string{},
		},
	}
	if raw, err := os.ReadFile(s.path); err == nil {
		var st lifecycleState
		if json.Unmarshal(raw, &st) == nil && st.RootVulaID != "" {
			if st.Revoked == nil {
				st.Revoked = map[string]*RevocationCert{}
			}
			if st.PinnedAnchors == nil {
				st.PinnedAnchors = map[string]string{}
			}
			s.state = st
		}
	}
	return s, nil
}

// saveLocked persists state. Caller holds s.mu.
func (s *LifecycleStore) saveLocked() error {
	raw, err := json.MarshalIndent(&s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// IsRevoked reports whether vulaID has an observed, verified revocation.
func (s *LifecycleStore) IsRevoked(vulaID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.state.Revoked[vulaID]
	return ok
}

// PinAnchor records (TOFU) the anchor trusted to recover a contact's root id.
// It will not overwrite an already-pinned, differing anchor (which would be a
// downgrade/takeover attempt) — that returns an error.
func (s *LifecycleStore) PinAnchor(rootVulaID, anchorVulaID string) error {
	if _, err := decodeVulaID(rootVulaID); err != nil {
		return err
	}
	if _, err := decodeVulaID(anchorVulaID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.state.PinnedAnchors[rootVulaID]; ok && existing != anchorVulaID {
		return fmt.Errorf("lifecycle: anchor for %q already pinned to a different key", rootVulaID)
	}
	s.state.PinnedAnchors[rootVulaID] = anchorVulaID
	return s.saveLocked()
}

// TrustedAnchorFor returns the pinned anchor for a contact's root id, or "".
func (s *LifecycleStore) TrustedAnchorFor(rootVulaID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.PinnedAnchors[rootVulaID]
}

// RecordRevocation verifies cert against the anchors trusted for cert.VulaID and,
// if valid, records it. After this, IsRevoked(cert.VulaID) is true and the global
// admission gate (when this store is wired via SetRevocationChecker) rejects it.
func (s *LifecycleStore) RecordRevocation(cert *RevocationCert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Build the set of anchors trusted for this identity: the pinned anchor for
	// the identity's root, plus this node's own anchor when revoking self.
	anchors := map[string]bool{}
	if a := s.state.PinnedAnchors[cert.VulaID]; a != "" {
		anchors[a] = true
	}
	if s.state.AnchorVulaID != "" && (cert.VulaID == s.state.RootVulaID || s.headLocked() == cert.VulaID) {
		anchors[s.state.AnchorVulaID] = true
	}
	if err := cert.Verify(anchors); err != nil {
		return err
	}
	s.state.Revoked[cert.VulaID] = cert
	return s.saveLocked()
}

// SetAnchor installs (or confirms) this node's account recovery anchor public
// VulaID. It is the LIVE seam that turns recovery on after boot: the box derives
// the anchor public id from the account recovery seed only when that seed is
// transiently in hand (recovery-kit generation / restore), then calls SetAnchor
// so subsequent well-known publications carry the anchor and peers can TOFU-pin
// it. Only the PUBLIC anchor id is ever stored on the box; the anchor PRIVATE key
// (the recovery signing power) lives solely in the off-box recovery kit.
//
// Fails closed on a takeover attempt: once an anchor is set, SetAnchor refuses to
// replace it with a DIFFERENT id (which would let a box compromise swap the
// recovery anchor). Setting the same id, or setting from empty, is allowed.
func (s *LifecycleStore) SetAnchor(anchorVulaID string) error {
	if anchorVulaID == "" {
		return errors.New("lifecycle: empty anchor id")
	}
	if _, err := decodeVulaID(anchorVulaID); err != nil {
		return fmt.Errorf("lifecycle: anchor vula id: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.AnchorVulaID == anchorVulaID {
		return nil
	}
	if s.state.AnchorVulaID != "" {
		return fmt.Errorf("lifecycle: recovery anchor already set to a different key; refusing to replace")
	}
	s.state.AnchorVulaID = anchorVulaID
	return s.saveLocked()
}

// AnchorVulaID returns this node's configured recovery anchor public id, or "".
func (s *LifecycleStore) AnchorVulaID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.AnchorVulaID
}

// headLocked returns this node's current identity id (end of its own chain).
func (s *LifecycleStore) headLocked() string {
	head := s.state.RootVulaID
	for _, l := range s.state.Chain {
		switch {
		case l.Rotation != nil:
			head = l.Rotation.NewVulaID
		case l.Recovery != nil:
			head = l.Recovery.NewVulaID
		}
	}
	return head
}

// Head returns this node's current (latest) identity Vula ID.
func (s *LifecycleStore) Head() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.headLocked()
}

// AppendOwnRotation rotates THIS node's identity to newVulaID, signing the link
// with the current head key (oldPriv). The new link is appended to the published
// chain so peers can follow it.
func (s *LifecycleStore) AppendOwnRotation(oldPriv ed25519.PrivateKey, newVulaID string) (*RotationCert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head := s.headLocked()
	cert, err := NewRotationCert(oldPriv, head, newVulaID)
	if err != nil {
		return nil, err
	}
	s.state.Chain = append(s.state.Chain, LifecycleLink{Rotation: cert})
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return cert, nil
}

// AppendOwnRecovery re-establishes THIS node's identity on newVulaID via the
// account anchor (anchorPriv), without needing the lost identity key.
func (s *LifecycleStore) AppendOwnRecovery(anchorPriv ed25519.PrivateKey, newVulaID string) (*RecoveryCert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.AnchorVulaID == "" {
		return nil, errors.New("lifecycle: no recovery anchor configured")
	}
	head := s.headLocked()
	cert, err := NewRecoveryCert(anchorPriv, head, newVulaID, s.state.AnchorVulaID)
	if err != nil {
		return nil, err
	}
	s.state.Chain = append(s.state.Chain, LifecycleLink{Recovery: cert})
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return cert, nil
}

// OwnChain returns a copy of this node's published transition chain (for the
// wellknown endpoint to publish).
func (s *LifecycleStore) OwnChain() (root, anchor string, chain []LifecycleLink) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]LifecycleLink, len(s.state.Chain))
	copy(cp, s.state.Chain)
	return s.state.RootVulaID, s.state.AnchorVulaID, cp
}

// RevocationList returns a snapshot of observed revocations (for publishing /
// gossip to peers).
func (s *LifecycleStore) RevocationList() []*RevocationCert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*RevocationCert, 0, len(s.state.Revoked))
	for _, c := range s.state.Revoked {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VulaID < out[j].VulaID })
	return out
}

// ─── Global admission gate ─────────────────────────────────────────────────────

// revocationChecker is the process-wide hook consulted by InboundMiddleware and
// VerifyVulaSignatureChecked to reject revoked identities. nil ⇒ no revocation
// enforcement (the historical behavior; safe because absence of a checker means
// no lifecycle subsystem is wired).
var (
	revocationCheckerMu sync.RWMutex
	revocationChecker   func(vulaID string) bool
)

// SetRevocationChecker installs the process-wide revocation gate. The LIVE server
// wires this to a LifecycleStore.IsRevoked so every admission/verify point honors
// revocations. Passing nil disables the gate.
func SetRevocationChecker(f func(vulaID string) bool) {
	revocationCheckerMu.Lock()
	revocationChecker = f
	revocationCheckerMu.Unlock()
}

// isVulaIDRevoked reports whether the installed checker (if any) marks vulaID
// revoked. With no checker installed it returns false (fail-open ONLY when the
// subsystem is entirely absent; once wired, every revoked id is rejected).
func isVulaIDRevoked(vulaID string) bool {
	revocationCheckerMu.RLock()
	f := revocationChecker
	revocationCheckerMu.RUnlock()
	if f == nil {
		return false
	}
	return f(vulaID)
}

// VerifyVulaSignatureChecked is VerifyVulaSignature plus a revocation gate: it
// rejects a signature from a revoked identity even if the signature is otherwise
// valid. Cross-box capability/proof checks should prefer this over the bare
// VerifyVulaSignature so a compromised-then-revoked key cannot be used.
func VerifyVulaSignatureChecked(vulaID string, msg, sig []byte) error {
	if isVulaIDRevoked(vulaID) {
		return fmt.Errorf("peering: identity %q is revoked", vulaID)
	}
	return VerifyVulaSignature(vulaID, msg, sig)
}

// ─── Small helpers ─────────────────────────────────────────────────────────────

// ed25519PrivMatchesVulaID reports whether priv's public key equals the key
// embedded in vulaID.
func ed25519PrivMatchesVulaID(priv ed25519.PrivateKey, vulaID string) bool {
	if len(priv) != ed25519.PrivateKeySize {
		return false
	}
	want, err := decodeVulaID(vulaID)
	if err != nil {
		return false
	}
	got := priv.Public().(ed25519.PublicKey)
	return bytes.Equal(got, want)
}
