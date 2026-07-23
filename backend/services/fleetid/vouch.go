// Package fleetid implements the fleet-identity peer-vouching KEYSTONE: a
// VerifyQuorum primitive by which a QUORUM of OTHER boxes vouch for a box's
// break-glass action (device enrollment, ssh recovery, identity recovery).
//
// # The hard rule (non-negotiable)
//
// A box must NEVER be able to authorize its OWN recovery / enrollment /
// ssh-backdoor. A full compromise of box S yields S's keys but NOT the power to
// self-authorize. VerifyQuorum is the single chokepoint that structurally
// guarantees this. It enforces, in order of importance:
//
//  1. SELF-EXCLUSION — any voucher cert whose VoucherVulaID == SubjectID is
//     rejected, even when its signature is perfectly valid. A compromised box
//     cannot vouch for itself, so it can never reach quorum on its own.
//  2. DISTINCT, VERIFIED, ROSTERED counting — only vouchers that (a) are members
//     of THIS box's own verified roster, (b) verify against that roster's key for
//     the voucher, (c) are not revoked, and (d) are DISTINCT (deduped by
//     VoucherVulaID) count toward quorum.
//  3. THRESHOLD FLOOR — distinct_valid must be >= max(threshold, 2). At least two
//     OTHER boxes must vouch; a threshold below 2 is silently raised to 2.
//
// # Relationship to existing primitives (reused, not reinvented)
//
//   - peering/identity_lifecycle.go: VouchCert is signed/verified with the same
//     canonical Ed25519 discipline as RotationCert/RevocationCert/RecoveryCert
//     (signing.Canonical over the cert with its Sig field cleared). VerifyQuorum
//     is the M-of-N generalization of RevocationCert.Verify's authorization gate
//     ("the signer must be the identity itself OR a trusted anchor, never an
//     unrelated key") — here the "trusted set" is an M-of-N quorum of OTHER
//     rostered boxes.
//   - internal/multiinstance CRDT-QUORUM-01 (appsync.go): the distinct-origin,
//     verified, self-hosted-roster counting discipline. Each box keeps its OWN
//     roster (the Roster interface here) so a compromised box cannot fabricate
//     voucher identities; a self-asserted key is never trusted — the signature
//     must verify against the ROSTER's key for the voucher. Unlike CRDT-QUORUM-01
//     there is NO small-fleet waiver: waiving the rule here would break the hard
//     rule, so small fleets simply cannot meet quorum (see below).
//
// # Small-fleet policy
//
// Peer-vouching is the ADDITIONAL primitive that applies at >= 3 boxes. For 1-2
// box fleets there are not >= 2 OTHER boxes, so quorum can never be met by
// design: VerifyQuorum returns ErrInsufficientQuorum. The caller then falls back
// to the EXISTING off-box mnemonic recovery anchor (peering/recovery_anchor.go +
// DeriveRecoveryAnchor). The rule is NOT waived for small fleets — the anchor is
// itself an off-box authority, so a box still cannot self-authorize.
package fleetid

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"vulos/backend/services/peering"
	"vulos/backend/services/signing"
)

// ─── Constants ─────────────────────────────────────────────────────────────────

// VouchCertType tags every fleet-vouch certificate.
const VouchCertType = "fleet-vouch"

// Allowed break-glass actions a vouch quorum may authorize.
const (
	ActionDeviceEnroll     = "device-enroll"
	ActionSSHRecovery      = "ssh-recovery"
	ActionIdentityRecovery = "identity-recovery"
)

// MinThreshold is the hard floor on the quorum threshold. The hard rule requires
// at least two OTHER boxes to vouch, so a caller-supplied threshold below this is
// raised to it. It is NEVER lowered.
const MinThreshold = 2

// Freshness window for a co-signing session. A vouch cert is only counted when
// its IssuedAt lies within [now-VouchMaxAge, now+ClockSkew]. Break-glass vouches
// are gathered in a single short session; a stale cert must not be replayable.
const (
	VouchMaxAge = 15 * time.Minute
	ClockSkew   = 2 * time.Minute
)

// ─── Errors ────────────────────────────────────────────────────────────────────

// ErrInsufficientQuorum is returned (alongside a fully-populated Result) when the
// number of distinct valid vouchers is below the effective threshold — including
// the small-fleet case where quorum cannot be met by design. Callers branch on
// errors.Is(err, ErrInsufficientQuorum) to fall back to the mnemonic anchor path.
var ErrInsufficientQuorum = errors.New("fleetid: insufficient vouch quorum")

// ErrInvalidRequest is returned for programmer misuse of VerifyQuorum (empty or
// unknown action, empty/undecodable subject, empty payload hash, nil roster).
// It is distinct from ErrInsufficientQuorum so the caller does NOT silently fall
// back to mnemonic recovery on a bug.
var ErrInvalidRequest = errors.New("fleetid: invalid quorum request")

// ─── Certificate ───────────────────────────────────────────────────────────────

// VouchCert is one peer box's signed attestation that SubjectID may perform
// Action over the payload identified by PayloadHash. It is signed by the voucher
// box's identity key over the canonical bytes of the cert with Sig cleared.
type VouchCert struct {
	// Type is always VouchCertType.
	Type string `json:"type"`
	// Action is the break-glass action being vouched for (one of the Action*
	// constants). A cert bound to a different action never counts.
	Action string `json:"action"`
	// SubjectID is the Vula ID of the box the action is FOR (the box being
	// enrolled / recovered). A voucher may never equal the subject (self-exclusion).
	SubjectID string `json:"subject_id"`
	// PayloadHash is the base64url (raw) hash of the exact action payload being
	// authorized (e.g. the new device pubkey, the ssh CA request, the recovery
	// target key). Binding to it stops a cert for one payload authorizing another.
	PayloadHash string `json:"payload_hash"`
	// VoucherVulaID is the identity of the vouching box (the signer).
	VoucherVulaID string `json:"voucher_vula_id"`
	// IssuedAt is the RFC3339 UTC issue time, checked against the freshness window.
	IssuedAt string `json:"issued_at"`
	// Nonce is random per cert; it defends against cross-protocol confusion and
	// makes otherwise-identical certs distinguishable on the wire.
	Nonce string `json:"nonce"`
	// Sig is the base64url (raw) Ed25519 signature by VoucherVulaID's key over the
	// canonical bytes of this cert with Sig empty.
	Sig string `json:"sig"`
}

// ─── Roster ────────────────────────────────────────────────────────────────────

// Roster is THIS box's own verified fleet roster — the authority on which peer
// identities may vouch. It is intentionally tiny so it is testable without the
// real multiinstance.Registry and so VerifyQuorum reuses the box's OWN roster (a
// compromised peer cannot fabricate members of someone else's roster).
//
// IsRostered returns the roster's recorded Ed25519 public key for vulaID and
// whether it is a member. VerifyQuorum verifies the cert signature against THIS
// key — never the key self-asserted by the cert — which is the CRDT-QUORUM-01
// discipline: the roster, not the observation, is the source of truth.
type Roster interface {
	IsRostered(vulaID string) (pubKey ed25519.PublicKey, ok bool)
}

// RevocationOracle is an OPTIONAL capability a Roster may also implement to
// exclude revoked voucher identities (mirroring peering's isVulaIDRevoked /
// LifecycleStore.IsRevoked). When the Roster passed to VerifyQuorum implements
// it, a voucher for which IsRevoked reports true is dropped and does not count.
// When the Roster does not implement it, revocation exclusion is skipped (the
// roster is assumed to already omit revoked members).
type RevocationOracle interface {
	IsRevoked(vulaID string) bool
}

// ─── Result / audit ────────────────────────────────────────────────────────────

// Drop reasons, returned per-cert for auditability.
const (
	DropSelfVouch         = "self-vouch: voucher is the subject"
	DropWrongType         = "wrong cert type"
	DropWrongAction       = "action mismatch"
	DropWrongSubject      = "subject mismatch"
	DropWrongPayload      = "payload-hash mismatch"
	DropBadTimestamp      = "unparseable issued_at"
	DropNotYetValid       = "issued in the future (outside skew)"
	DropExpired           = "issued outside freshness window"
	DropUnrostered        = "voucher not in roster"
	DropRosterKeyMismatch = "roster key does not match voucher id"
	DropRevoked           = "voucher revoked"
	DropBadSignature      = "signature invalid"
	DropDuplicate         = "duplicate of an already-counted voucher"
	DropMalformed         = "malformed cert fields"
)

// DroppedVouch records why one cert did not count toward quorum.
type DroppedVouch struct {
	Index         int    // position in the input certs slice
	VoucherVulaID string // as claimed by the cert (may be untrusted)
	Reason        string // one of the Drop* constants
}

// Result is the full, auditable outcome of a VerifyQuorum evaluation.
type Result struct {
	// OK is true iff DistinctValid >= Threshold. When false, VerifyQuorum also
	// returns ErrInsufficientQuorum.
	OK bool
	// Action / SubjectID echo the request being evaluated.
	Action    string
	SubjectID string
	// Threshold is the EFFECTIVE threshold applied (always >= MinThreshold).
	Threshold int
	// DistinctValid is the number of distinct, verified, rostered, non-revoked,
	// non-self, correctly-bound, fresh vouchers that counted.
	DistinctValid int
	// Counted lists the distinct voucher Vula IDs that counted (order of first
	// appearance).
	Counted []string
	// Dropped lists every cert that did NOT count and why.
	Dropped []DroppedVouch
}

// ─── VerifyQuorum: the chokepoint ──────────────────────────────────────────────

// VerifyQuorum is the single authorization chokepoint for peer-vouched
// break-glass actions. It returns a fully-populated Result for audit in ALL
// non-misuse cases. err is:
//
//   - ErrInvalidRequest      → programmer misuse (bad action/subject/payload/roster).
//   - ErrInsufficientQuorum  → distinct_valid < effective threshold (includes the
//     small-fleet case). Result is still populated; caller may fall back to the
//     off-box mnemonic anchor path.
//   - nil                    → OK; Result.Counted lists the vouchers that counted.
//
// Enforcement of the hard rule (self-exclusion, distinct/verified/rostered
// counting, threshold floor >= 2) is documented inline at each check.
func VerifyQuorum(action, subjectID string, payloadHash []byte, certs []VouchCert, roster Roster, threshold int, now time.Time) (Result, error) {
	// ── Input validation (misuse ⇒ ErrInvalidRequest, never a silent fallback) ──
	if !validAction(action) {
		return Result{}, fmt.Errorf("%w: unknown action %q", ErrInvalidRequest, action)
	}
	if _, err := peering.PublicKeyForVulaID(subjectID); err != nil {
		return Result{}, fmt.Errorf("%w: subject id: %v", ErrInvalidRequest, err)
	}
	if len(payloadHash) == 0 {
		return Result{}, fmt.Errorf("%w: empty payload hash", ErrInvalidRequest)
	}
	if roster == nil {
		return Result{}, fmt.Errorf("%w: nil roster", ErrInvalidRequest)
	}

	// THRESHOLD FLOOR: never below MinThreshold (2). The hard rule requires at
	// least two OTHER boxes; a caller passing 1 (or 0) is raised to 2, never below.
	effThreshold := threshold
	if effThreshold < MinThreshold {
		effThreshold = MinThreshold
	}

	revoker, _ := roster.(RevocationOracle)

	res := Result{
		Action:    action,
		SubjectID: subjectID,
		Threshold: effThreshold,
	}
	seen := make(map[string]bool) // distinct dedupe over vouchers that already counted

	for i, c := range certs {
		drop := func(reason string) {
			res.Dropped = append(res.Dropped, DroppedVouch{Index: i, VoucherVulaID: c.VoucherVulaID, Reason: reason})
		}

		// Structural / binding checks first. These are cheap and their reasons are
		// the most useful for audit; a cert that fails binding never reaches sig
		// verification.
		if c.Type != VouchCertType {
			drop(DropWrongType)
			continue
		}
		if c.VoucherVulaID == "" || c.SubjectID == "" {
			drop(DropMalformed)
			continue
		}
		// HARD RULE #1 — SELF-EXCLUSION. A voucher may never be the subject, even
		// with a perfectly valid signature. Checked BEFORE roster/sig so that a
		// compromised box holding its own valid key can never self-authorize.
		if c.VoucherVulaID == subjectID {
			drop(DropSelfVouch)
			continue
		}
		// A cert must be bound to THIS action / subject / payload. A cert minted for
		// a different action/subject/payload (e.g. replayed by a compromised box for
		// a different break-glass request) does not count.
		if c.Action != action {
			drop(DropWrongAction)
			continue
		}
		if c.SubjectID != subjectID {
			drop(DropWrongSubject)
			continue
		}
		gotHash, err := base64.RawURLEncoding.DecodeString(c.PayloadHash)
		if err != nil || !bytes.Equal(gotHash, payloadHash) {
			drop(DropWrongPayload)
			continue
		}

		// Freshness window.
		issued, err := time.Parse(time.RFC3339, c.IssuedAt)
		if err != nil {
			drop(DropBadTimestamp)
			continue
		}
		if issued.After(now.Add(ClockSkew)) {
			drop(DropNotYetValid)
			continue
		}
		if issued.Before(now.Add(-VouchMaxAge)) {
			drop(DropExpired)
			continue
		}

		// ROSTER MEMBERSHIP (CRDT-QUORUM-01): the voucher must be a member of THIS
		// box's own verified roster. A self-asserted identity that is not rostered
		// cannot count — a compromised peer cannot fabricate roster members.
		pub, ok := roster.IsRostered(c.VoucherVulaID)
		if !ok {
			drop(DropUnrostered)
			continue
		}
		// The roster's key for the voucher must match the key embedded in the
		// voucher's Vula ID; otherwise the roster and the claimed identity disagree
		// and we cannot safely attribute the cert. Fail closed.
		embedded, err := peering.PublicKeyForVulaID(c.VoucherVulaID)
		if err != nil || !bytes.Equal(embedded, pub) {
			drop(DropRosterKeyMismatch)
			continue
		}

		// Revocation exclusion (optional oracle). A revoked voucher never counts.
		if revoker != nil && revoker.IsRevoked(c.VoucherVulaID) {
			drop(DropRevoked)
			continue
		}

		// SIGNATURE VERIFICATION against the ROSTER's key (never the self-asserted
		// one). An unverifiable cert is dropped, exactly as CRDT-QUORUM-01 drops an
		// unsigned/bad-signature observation from counting toward quorum.
		if err := verifyCanonical(c, c.Sig, pub); err != nil {
			drop(DropBadSignature)
			continue
		}

		// DISTINCT counting: N certs from the SAME voucher count as one. Only the
		// first fully-valid cert from a given voucher is counted; the rest are
		// dropped as duplicates.
		if seen[c.VoucherVulaID] {
			drop(DropDuplicate)
			continue
		}
		seen[c.VoucherVulaID] = true
		res.Counted = append(res.Counted, c.VoucherVulaID)
	}

	res.DistinctValid = len(res.Counted)
	res.OK = res.DistinctValid >= effThreshold
	if !res.OK {
		return res, fmt.Errorf("%w: %d distinct valid < threshold %d", ErrInsufficientQuorum, res.DistinctValid, effThreshold)
	}
	return res, nil
}

// ─── Signer helper ─────────────────────────────────────────────────────────────

// NewVouchCert builds and signs a fleet-vouch cert with voucherPriv, for
// subjectID over payloadHash. voucherPriv's public key becomes VoucherVulaID.
//
// NewVouchCert does NOT itself refuse a self-vouch (voucher == subject): the hard
// rule is enforced at the chokepoint (VerifyQuorum), which rejects a self-vouch
// even when perfectly signed. Keeping the builder permissive lets tests construct
// a genuinely-signed self-vouch to prove the chokepoint — not the builder — is
// the guarantee.
func NewVouchCert(voucherPriv ed25519.PrivateKey, action, subjectID string, payloadHash []byte, now time.Time) (*VouchCert, error) {
	if len(voucherPriv) != ed25519.PrivateKeySize {
		return nil, errors.New("fleetid: voucher private key wrong size")
	}
	if !validAction(action) {
		return nil, fmt.Errorf("fleetid: unknown action %q", action)
	}
	if _, err := peering.PublicKeyForVulaID(subjectID); err != nil {
		return nil, fmt.Errorf("fleetid: subject id: %w", err)
	}
	if len(payloadHash) == 0 {
		return nil, errors.New("fleetid: empty payload hash")
	}
	voucherVulaID := peering.EncodeVulaID(voucherPriv.Public().(ed25519.PublicKey))
	c := &VouchCert{
		Type:          VouchCertType,
		Action:        action,
		SubjectID:     subjectID,
		PayloadHash:   base64.RawURLEncoding.EncodeToString(payloadHash),
		VoucherVulaID: voucherVulaID,
		IssuedAt:      now.UTC().Format(time.RFC3339),
		Nonce:         nonce(),
	}
	sig, err := signCanonical(c, voucherPriv)
	if err != nil {
		return nil, err
	}
	c.Sig = sig
	return c, nil
}

// ─── Canonical sign/verify (mirrors peering.lcSign/lcVerifyCanonical) ───────────

// signCanonical signs the canonical bytes of v (with its Sig field cleared by the
// caller-supplied value already having Sig=="") using priv. For a VouchCert the
// caller passes the cert with Sig=="" (NewVouchCert does exactly this before
// setting Sig).
func signCanonical(c *VouchCert, priv ed25519.PrivateKey) (string, error) {
	unsigned := *c
	unsigned.Sig = ""
	payload, err := signing.Canonical(unsigned)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, payload)), nil
}

// verifyCanonical verifies sigB64 over the canonical bytes of c (with Sig
// cleared) using pub — the ROSTER's key for the voucher. Fails closed on any
// malformed input.
func verifyCanonical(c VouchCert, sigB64 string, pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("fleetid: bad roster public key")
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("fleetid: malformed signature")
	}
	c.Sig = ""
	payload, err := signing.Canonical(c)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return errors.New("fleetid: signature mismatch")
	}
	return nil
}

// ─── small helpers ─────────────────────────────────────────────────────────────

func validAction(a string) bool {
	switch a {
	case ActionDeviceEnroll, ActionSSHRecovery, ActionIdentityRecovery:
		return true
	default:
		return false
	}
}

func nonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
