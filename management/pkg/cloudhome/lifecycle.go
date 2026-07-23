// lifecycle.go — key lifecycle (REVOCATION) for cloud-home identities.
//
// This is the cell-side mirror of vulos backend/services/peering/
// identity_lifecycle.go, restricted to the operation that is tractable when the
// cell custodies the identity key on the account's behalf: account-driven
// SELF-REVOCATION.
//
// A cloud-home RevocationCert is byte-identical to the box host's: the same JSON
// field set/tags, the same canonical-JSON signing (sorted keys, no whitespace,
// Sig cleared before signing), and the same base64url Ed25519 signature. Because
// the cell holds the cloud-home identity private key, it can sign a SELF
// revocation (SignerVulaID == VulaID) on the account's behalf; vulos
// RevocationCert.Verify accepts a self-revocation regardless of trusted anchors,
// so every box/relay/peer that observes the published cert rejects the revoked
// cloud-home VulaID exactly as it would a revoked box VulaID.
//
// Recovery-anchor-signed revocation and rotation/recovery chains are DEFERRED:
// they require threading the account recovery-kit seed into the cell, which is a
// separate (cross-repo) seam. See the final report.
package cloudhome

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrRevoked is returned by directory/redeem paths for a cloud-home identity that
// the account has revoked (fail closed).
var ErrRevoked = errors.New("cloudhome: identity is revoked")

// openIdentityKey decrypts the cloud-home identity Ed25519 private key for r under
// the KEK version it was sealed with. This is the IDENTITY/signing key only — it
// signs revocation certs, fetch proofs, and federation messages; it is NEVER a
// content-decryption key. The caller MUST scrub the returned key.
func (s *Service) openIdentityKey(r record) (ed25519.PrivateKey, error) {
	ver := r.KEKVersion
	if ver < 1 {
		ver = 1
	}
	kek, ok := s.keyring[ver]
	if !ok {
		return nil, fmt.Errorf("cloudhome: no KEK for version %d (rotate keyring before dropping prior keys)", ver)
	}
	priv, err := openKEK(r.EncPrivKey, kek)
	if err != nil {
		return nil, err
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("cloudhome: decrypted identity key is %d bytes, want %d", len(priv), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(priv), nil
}

// scrub zeroes a byte slice (best-effort key hygiene).
func scrub(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// LifecycleCertType tags the kind of lifecycle certificate (matches vulos).
type LifecycleCertType string

const (
	// CertTypeRevocation matches vulos peering.CertTypeRevocation.
	CertTypeRevocation LifecycleCertType = "vula-revocation"
)

// RevocationCert retires an identity. Field set + JSON tags are byte-identical to
// vulos backend/services/peering/identity_lifecycle.go RevocationCert so a box or
// relay can verify a cloud-home revocation with the same code path.
type RevocationCert struct {
	Type LifecycleCertType `json:"type"` // CertTypeRevocation
	// VulaID is the identity being revoked.
	VulaID string `json:"vula_id"`
	// SignerVulaID is the key that signed this revocation (the identity itself for
	// a cell-driven self-revocation; an anchor in the box recovery case).
	SignerVulaID string `json:"signer_vula_id"`
	// Reason is a short human-readable reason (e.g. "compromised").
	Reason string `json:"reason,omitempty"`
	// IssuedAt is the issue time (RFC3339 UTC).
	IssuedAt string `json:"issued_at"`
	// Sig is the base64url Ed25519 signature by SignerVulaID's key over the
	// canonical bytes of this cert with Sig empty.
	Sig string `json:"sig"`
}

// Verify checks a self-revocation cert's structure and signature. It accepts ONLY
// self-revocations (SignerVulaID == VulaID) — the case the cell produces and the
// case vulos accepts without a pinned anchor. Fails closed on any malformed input.
func (c *RevocationCert) Verify() error {
	if c.Type != CertTypeRevocation {
		return fmt.Errorf("lifecycle: revocation cert wrong type %q", c.Type)
	}
	if c.VulaID == "" || c.SignerVulaID == "" {
		return errors.New("lifecycle: revocation cert missing ids")
	}
	if c.SignerVulaID != c.VulaID {
		return fmt.Errorf("lifecycle: revocation signer %q is not the identity (cloud-home supports self-revocation only)", c.SignerVulaID)
	}
	pub, err := ParseVulaID(c.SignerVulaID)
	if err != nil {
		return fmt.Errorf("lifecycle: bad signer vula id: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(c.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("lifecycle: malformed signature")
	}
	unsigned := *c
	unsigned.Sig = ""
	payload, err := canonicalJSON(unsigned)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return errors.New("lifecycle: signature mismatch")
	}
	return nil
}

// LifecycleDoc is the directory-relayed lifecycle view for a cloud-home identity.
// Revocation, when present, is the verbatim RevocationCert (base58 VulaIDs inside)
// a peer re-verifies and honors. Revoked is the convenience boolean.
type LifecycleDoc struct {
	IdentityVulaID string          `json:"identity_vula_id"`
	Revoked        bool            `json:"revoked"`
	Revocation     *RevocationCert `json:"revocation,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Service operations
// ─────────────────────────────────────────────────────────────────────────────

// RevokeIdentity self-revokes the account's cloud-home identity using the
// identity key the cell custodies, persists the verbatim cert, and audits the op.
// It is idempotent: a second call returns the already-recorded cert. After this,
// IsRevoked is true and Resolve/RedeemCapability fail closed for the
// identity, and the cert is published via the lifecycle directory so peers reject.
func (s *Service) RevokeIdentity(ctx context.Context, accountID, reason string) (RevocationCert, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return RevocationCert{}, ErrInvalidInput
	}
	r, err := s.store.GetByAccount(ctx, accountID)
	if err != nil {
		return RevocationCert{}, err // ErrNotFound → nothing to revoke
	}
	// Idempotent: return the existing cert if already revoked.
	if certJSON, ok, gerr := s.store.GetRevocation(ctx, r.VulaID); gerr != nil {
		return RevocationCert{}, gerr
	} else if ok {
		var existing RevocationCert
		if json.Unmarshal([]byte(certJSON), &existing) == nil {
			return existing, nil
		}
	}

	cert := RevocationCert{
		Type:         CertTypeRevocation,
		VulaID:       r.VulaID,
		SignerVulaID: r.VulaID, // self-revocation
		Reason:       reason,
		IssuedAt:     s.now().UTC().Format(time.RFC3339),
	}
	unsigned := cert // Sig == "" here
	payload, err := canonicalJSON(unsigned)
	if err != nil {
		return RevocationCert{}, err
	}
	idPriv, err := s.openIdentityKey(r)
	if err != nil {
		return RevocationCert{}, err
	}
	cert.Sig = base64.RawURLEncoding.EncodeToString(ed25519.Sign(idPriv, payload))
	scrub(idPriv)

	// Sanity: the cert we just built must verify (fail closed on any drift).
	if verr := cert.Verify(); verr != nil {
		return RevocationCert{}, fmt.Errorf("cloudhome: self-revocation failed self-verify: %w", verr)
	}

	raw, err := json.Marshal(cert)
	if err != nil {
		return RevocationCert{}, err
	}
	if err := s.store.PutRevocation(ctx, r.VulaID, r.AccountID, string(raw), s.now().UTC()); err != nil {
		return RevocationCert{}, err
	}
	s.auditKey(ctx, "revoke", r.AccountID, r.VulaID, map[string]string{"reason": reason})
	return cert, nil
}

// IsRevoked reports whether the cloud-home identity vulaID has a recorded
// revocation.
func (s *Service) IsRevoked(ctx context.Context, vulaID string) (bool, error) {
	_, ok, err := s.store.GetRevocation(ctx, vulaID)
	return ok, err
}

// Lifecycle returns the directory-relayed lifecycle view for a cloud-home
// identity (verbatim revocation cert if any). Unknown identities → ErrNotFound.
func (s *Service) Lifecycle(ctx context.Context, vulaID string) (LifecycleDoc, error) {
	if _, err := s.store.GetByVulaID(ctx, vulaID); err != nil {
		return LifecycleDoc{}, err
	}
	doc := LifecycleDoc{IdentityVulaID: vulaID}
	certJSON, ok, err := s.store.GetRevocation(ctx, vulaID)
	if err != nil {
		return LifecycleDoc{}, err
	}
	if ok {
		var cert RevocationCert
		if json.Unmarshal([]byte(certJSON), &cert) == nil {
			doc.Revoked = true
			doc.Revocation = &cert
		}
	}
	return doc, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Canonical JSON — ported verbatim from vulos backend/services/signing/signing.go
// (Canonical): sorted map keys at every level, no insignificant whitespace,
// numbers preserved. A cloud-home RevocationCert MUST canonicalise byte-for-byte
// identically to a box one so cross-repo signature verification holds.
// ─────────────────────────────────────────────────────────────────────────────

func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("cloudhome: canonical marshal: %w", err)
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, fmt.Errorf("cloudhome: canonical decode: %w", err)
	}
	return encodeCanonical(generic)
}

func encodeCanonical(v any) ([]byte, error) {
	switch val := v.(type) {
	case map[string]any:
		return encodeCanonicalObject(val)
	case []any:
		return encodeCanonicalArray(val)
	case json.Number:
		return []byte(val.String()), nil
	case string:
		return json.Marshal(val)
	case bool:
		return json.Marshal(val)
	case nil:
		return []byte("null"), nil
	default:
		return json.Marshal(val)
	}
}

func encodeCanonicalObject(m map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := encodeCanonical(m[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func encodeCanonicalArray(a []any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, e := range a {
		if i > 0 {
			buf.WriteByte(',')
		}
		b, err := encodeCanonical(e)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}
