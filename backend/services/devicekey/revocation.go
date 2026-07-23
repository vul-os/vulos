// revocation.go — device-key REVOCATION.
//
// A revocation retires a device identity key permanently: once revoked, a
// pubkey is revoked forever (the set only ever grows — no "un-revoke" path,
// mirroring services/peering's LifecycleStore.Revoked and the epoch floor's
// monotonicity discipline in services/signing/epoch.go, which this store's
// on-disk persistence model is deliberately built to match).
//
// # Two ways a revocation is authorized
//
//   - SELF: the device key itself signs a DeviceRevocationCert retiring
//     itself (e.g. planned decommission while the key is still trusted).
//   - BREAK-GLASS: the key is lost/compromised and cannot sign anything
//     trustworthy, so a fleetid.VerifyQuorum quorum of OTHER boxes
//     authorizes the revocation instead — same keystone as break-glass
//     rotation (rotation.go), same hard rule (never self-authorized).
//
// # Admission gate
//
// IsDeviceKeyRevoked / SetRevocationChecker mirror services/peering's
// isVulaIDRevoked / SetRevocationChecker EXACTLY: a nil checker means the
// subsystem is entirely unwired (fail-open only in that narrow sense); once a
// checker is installed, every revoked fingerprint is rejected everywhere it is
// consulted. Wiring the process-wide checker to a live RevocationStore is a
// main.go-adjacent integration step — see the package doc / integration
// manifest returned alongside this change.
//
// # Propagation
//
// A DeviceRevocationCert is a SELF-VERIFYING artifact (see (*DeviceRevocationCert).Verify):
// any box that obtains one from ANY source — direct API call, or a peer's
// GET /api/auth/device/revocations pull (propagation.go) — can verify it
// against nothing but the cert's own contents (self) or ITS OWN roster
// (break-glass), then union it into its local store. This is why propagation
// needs no additional trust in the transport: a box that has not yet seen a
// given revocation simply doesn't have it yet, but the MOMENT it merges a
// valid cert from any gossip round, it fails closed on that key forever
// after — merge order and transport are both irrelevant to correctness.
package devicekey

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"vulos/backend/services/fleetid"
	"vulos/backend/services/signing"
)

// Revocation method tags, mirroring RotationMethodSelf / RotationMethodBreakGlass.
const (
	RevocationMethodSelf       = "self"
	RevocationMethodBreakGlass = "break-glass"
)

const deviceRevocationCertType = "devicekey-revocation-v1"

// Fingerprint returns the stable hex-SHA-256 fingerprint of a PKIX DER public
// key. This is the key under which a revocation is recorded and checked —
// full SHA-256 (not the 16-byte prefix Status.DeviceID uses) to keep
// collision risk negligible for a security-gating identifier.
func Fingerprint(pubKeyDER []byte) string {
	h := sha256.Sum256(pubKeyDER)
	return hex.EncodeToString(h[:])
}

// DeviceRevocationCert retires a device identity key. It is a self-contained,
// self-verifying artifact: see Verify.
type DeviceRevocationCert struct {
	Type        string `json:"type"`        // always deviceRevocationCertType
	Fingerprint string `json:"fingerprint"` // Fingerprint(PubKeyDER) — recomputed and checked by Verify, never trusted bare
	PubKeyDER   string `json:"pub_key_der"` // base64 PKIX DER of the revoked key
	Reason      string `json:"reason,omitempty"`
	Method      string `json:"method"` // RevocationMethodSelf | RevocationMethodBreakGlass
	IssuedAt    string `json:"issued_at"`
	Nonce       string `json:"nonce"`

	// Break-glass authorization evidence (Method == RevocationMethodBreakGlass only).
	// Mirrors RotationCert's fields exactly; see rotation.go for the rationale.
	QuorumSubjectID string              `json:"quorum_subject_id,omitempty"`
	QuorumRequestID string              `json:"quorum_request_id,omitempty"`
	QuorumCerts     []fleetid.VouchCert `json:"quorum_certs,omitempty"`

	// Sig is the base64 ASN.1 ECDSA signature over the canonical bytes of this
	// cert with Sig cleared.
	//   - self:        signed by the revoked key ITSELF (proves the holder of
	//     the key chose to retire it).
	//   - break-glass: left EMPTY. The whole premise of break-glass is that the
	//     key cannot be trusted to sign anything (lost or compromised); QuorumCerts
	//     alone carry the authorization.
	Sig string `json:"sig,omitempty"`
}

func newDeviceRevocationCert(method string, pubKeyDER []byte, reason string) *DeviceRevocationCert {
	return &DeviceRevocationCert{
		Type:        deviceRevocationCertType,
		Fingerprint: Fingerprint(pubKeyDER),
		PubKeyDER:   base64.StdEncoding.EncodeToString(pubKeyDER),
		Reason:      reason,
		Method:      method,
		IssuedAt:    time.Now().UTC().Format(time.RFC3339),
		Nonce:       randNonce(),
	}
}

func revocationSigningDigest(cert *DeviceRevocationCert) ([]byte, error) {
	unsigned := *cert
	unsigned.Sig = ""
	payload, err := signing.Canonical(unsigned)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(payload)
	return h[:], nil
}

func revocationBreakGlassPayloadHash(requestID, fingerprint string) []byte {
	h := sha256.New()
	h.Write([]byte("vulos:devicekey:break-glass-revocation:v1"))
	h.Write([]byte{0})
	h.Write([]byte(requestID))
	h.Write([]byte{0})
	h.Write([]byte(fingerprint))
	return h.Sum(nil)
}

// Verify checks a DeviceRevocationCert's structure and authorization,
// FAILING CLOSED on anything malformed, mistyped, or unverifiable.
//
//   - self:        Fingerprint must match sha256(PubKeyDER), and Sig must
//     verify against the embedded PubKeyDER.
//   - break-glass: QuorumCerts must independently satisfy fleetid.VerifyQuorum
//     for action=IdentityRecovery, subject=QuorumSubjectID, payload=
//     hash(QuorumRequestID || Fingerprint). roster/threshold/now flow straight
//     through to VerifyQuorum — a revoker can never invent its own roster.
func (c *DeviceRevocationCert) Verify(roster fleetid.Roster, threshold int, now time.Time) error {
	if c == nil {
		return errors.New("devicekey: nil revocation cert")
	}
	if c.Type != deviceRevocationCertType {
		return fmt.Errorf("devicekey: revocation cert wrong type %q", c.Type)
	}
	pubDER, err := base64.StdEncoding.DecodeString(c.PubKeyDER)
	if err != nil {
		return fmt.Errorf("devicekey: revocation cert bad pubkey encoding: %w", err)
	}
	if Fingerprint(pubDER) != c.Fingerprint {
		return errors.New("devicekey: revocation cert fingerprint does not match embedded pubkey")
	}

	switch c.Method {
	case RevocationMethodSelf:
		pubAny, err := x509.ParsePKIXPublicKey(pubDER)
		if err != nil {
			return fmt.Errorf("devicekey: revocation cert parse pubkey: %w", err)
		}
		pub, ok := pubAny.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("devicekey: revocation cert pubkey is not ECDSA")
		}
		digest, err := revocationSigningDigest(c)
		if err != nil {
			return fmt.Errorf("devicekey: revocation cert digest: %w", err)
		}
		sig, err := base64.StdEncoding.DecodeString(c.Sig)
		if err != nil {
			return errors.New("devicekey: revocation cert malformed signature")
		}
		if !ecdsa.VerifyASN1(pub, digest, sig) {
			return errors.New("devicekey: revocation cert signature does not verify")
		}
		return nil

	case RevocationMethodBreakGlass:
		if len(c.QuorumCerts) == 0 {
			return errors.New("devicekey: break-glass revocation cert carries no quorum certs")
		}
		payloadHash := revocationBreakGlassPayloadHash(c.QuorumRequestID, c.Fingerprint)
		if _, err := fleetid.VerifyQuorum(fleetid.ActionIdentityRecovery, c.QuorumSubjectID, payloadHash, c.QuorumCerts, roster, threshold, now); err != nil {
			return fmt.Errorf("devicekey: break-glass revocation quorum: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("devicekey: revocation cert unknown method %q", c.Method)
	}
}

// ─── Persistent, monotonic revocation store ─────────────────────────────────────

// revocationRecord is the on-disk JSON format.
type revocationRecord struct {
	Revoked map[string]*DeviceRevocationCert `json:"revoked"` // keyed by Fingerprint
}

// RevocationStore persists a monotonic set of revoked device-key fingerprints.
// Modeled directly on services/signing.EpochStore: atomic temp-file-then-rename
// writes, an in-memory cache kept consistent with disk, and a set that only
// ever grows. Safe for concurrent use.
type RevocationStore struct {
	mu   sync.RWMutex
	path string
	rec  revocationRecord

	// maxEntries bounds total stored revocations as a poisoning/DoS backstop.
	// A self-revocation cert only needs to match its OWN embedded key, so any
	// party can mint unlimited valid self-revocations for throwaway keys and
	// serve them via GET /api/auth/device/revocations; without a cap a single
	// rostered peer could grow every fleet box's store without bound (see
	// revsync.go's per-round cap for the companion rate limit). Once at
	// capacity MergeVerified refuses NEW fingerprints (already-present ones
	// stay idempotent). Generous relative to any real fleet's device count.
	// <= 0 disables the cap. Defaults to DefaultMaxRevocationEntries.
	maxEntries int
}

// DefaultMaxRevocationEntries is the default RevocationStore.maxEntries cap —
// far above any realistic fleet's lifetime device-key count, low enough to
// bound a poisoning peer's disk/CPU impact.
const DefaultMaxRevocationEntries = 10000

// revocationFileName is the on-disk file name under the directory passed to
// NewRevocationStore (typically the same directory devicekey.Open uses, e.g.
// ~/.vulos/auth/tpm/).
const revocationFileName = "revocations.json"

// NewRevocationStore opens (or creates) the revocation store at
// filepath.Join(dir, "revocations.json"). dir is typically the same directory
// passed to devicekey.Open (e.g. ~/.vulos/auth/tpm/).
func NewRevocationStore(dir string) (*RevocationStore, error) {
	if dir == "" {
		return nil, errors.New("devicekey: NewRevocationStore: empty dir")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("devicekey: NewRevocationStore: mkdir: %w", err)
	}
	s := &RevocationStore{
		path:       filepath.Join(dir, revocationFileName),
		rec:        revocationRecord{Revoked: map[string]*DeviceRevocationCert{}},
		maxEntries: DefaultMaxRevocationEntries,
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		if werr := s.writeLocked(); werr != nil {
			return nil, fmt.Errorf("devicekey: NewRevocationStore: init write: %w", werr)
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("devicekey: NewRevocationStore: read %q: %w", s.path, err)
	}
	var rec revocationRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		// FAIL CLOSED on a corrupt store: a store we cannot parse might be
		// hiding revocations we'd otherwise silently drop. Refuse to open
		// rather than start with an empty (falsely permissive) set.
		return nil, fmt.Errorf("devicekey: NewRevocationStore: corrupt store %q: %w", s.path, err)
	}
	if rec.Revoked == nil {
		rec.Revoked = map[string]*DeviceRevocationCert{}
	}
	s.rec = rec
	return s, nil
}

// IsRevoked reports whether fingerprint has a recorded revocation.
func (s *RevocationStore) IsRevoked(fingerprint string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.rec.Revoked[fingerprint]
	return ok
}

// IsPubKeyRevoked is IsRevoked(Fingerprint(pubKeyDER)).
func (s *RevocationStore) IsPubKeyRevoked(pubKeyDER []byte) bool {
	return s.IsRevoked(Fingerprint(pubKeyDER))
}

// Get returns the recorded revocation cert for fingerprint, or (nil, false).
func (s *RevocationStore) Get(fingerprint string) (*DeviceRevocationCert, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.rec.Revoked[fingerprint]
	return c, ok
}

// List returns a snapshot of every recorded revocation (for the
// GET /api/auth/device/revocations propagation endpoint).
func (s *RevocationStore) List() []*DeviceRevocationCert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*DeviceRevocationCert, 0, len(s.rec.Revoked))
	for _, c := range s.rec.Revoked {
		out = append(out, c)
	}
	return out
}

// MergeVerified verifies cert (dispatching on cert.Method exactly like
// (*DeviceRevocationCert).Verify) and, ONLY on success, unions it into the
// store (idempotent — merging an already-known fingerprint is a no-op).
//
// This is the single entry point used BOTH by the local mint path (belt and
// suspenders: never trust a cert just because this process just built it) AND
// by the gossip-ingest path (a peer's GET /api/auth/device/revocations pull —
// see propagation.go): the exact same fail-closed verification runs either
// way, so an invalid or forged cert can never enter the store regardless of
// where it came from.
//
// The set is monotonic: once a fingerprint is present, MergeVerified never
// removes it — there is no exported "unrevoke".
func (s *RevocationStore) MergeVerified(cert *DeviceRevocationCert, roster fleetid.Roster, threshold int, now time.Time) error {
	if cert == nil {
		return errors.New("devicekey: MergeVerified: nil cert")
	}
	if err := cert.Verify(roster, threshold, now); err != nil {
		return fmt.Errorf("devicekey: MergeVerified: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, already := s.rec.Revoked[cert.Fingerprint]; already {
		return nil // idempotent union
	}
	// Poisoning/DoS backstop: refuse NEW fingerprints once at capacity. A
	// self-revocation is valid for ANY key its own signature covers, so an
	// unbounded store is a griefing vector for a malicious rostered peer.
	if s.maxEntries > 0 && len(s.rec.Revoked) >= s.maxEntries {
		return fmt.Errorf("devicekey: MergeVerified: revocation store at capacity (%d entries); refusing new fingerprint %s (possible poisoning by a fleet peer — inspect %s)", s.maxEntries, cert.Fingerprint, s.path)
	}
	// Mark revoked in memory FIRST: any concurrent admission check
	// (IsRevoked/IsDeviceKeyRevoked) sees the revocation immediately even if
	// the subsequent disk write is slow or fails. A transient persistence
	// failure narrows safety (the revocation might not survive THIS process's
	// restart) but never widens it (this process stays fail-closed on the key
	// for its remaining lifetime either way).
	cp := *cert
	s.rec.Revoked[cert.Fingerprint] = &cp
	if err := s.writeLocked(); err != nil {
		return fmt.Errorf("devicekey: MergeVerified: persist: %w", err)
	}
	return nil
}

// writeLocked atomically persists the store. Caller must hold s.mu (write lock).
func (s *RevocationStore) writeLocked() error {
	data, err := json.MarshalIndent(&s.rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// ─── Minting helpers ────────────────────────────────────────────────────────────

// SelfRevoke signs and records a self-revocation of ks's CURRENT active
// device identity, using that same key. After this, the key's fingerprint is
// permanently revoked in store, and Status().Revoked reports true for as long
// as the process-wide checker (SetRevocationChecker) is wired to store.
func SelfRevoke(ks KeyStore, store *RevocationStore, reason string) (*DeviceRevocationCert, error) {
	if ks == nil {
		return nil, errors.New("devicekey: SelfRevoke: nil key store")
	}
	pubDER, err := ks.DeviceIdentity()
	if err != nil {
		return nil, fmt.Errorf("devicekey: SelfRevoke: read identity: %w", err)
	}
	cert := newDeviceRevocationCert(RevocationMethodSelf, pubDER, reason)
	digest, err := revocationSigningDigest(cert)
	if err != nil {
		return nil, fmt.Errorf("devicekey: SelfRevoke: digest: %w", err)
	}
	sig, err := ks.Sign(digest, crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("devicekey: SelfRevoke: sign: %w", err)
	}
	cert.Sig = base64.StdEncoding.EncodeToString(sig)
	if store != nil {
		// nil roster/threshold: RevocationMethodSelf never consults them.
		if err := store.MergeVerified(cert, nil, 0, time.Now()); err != nil {
			return nil, fmt.Errorf("devicekey: SelfRevoke: record: %w", err)
		}
	}
	return cert, nil
}

// BreakGlassRevoke is the SOLE sanctioned way to revoke ks's CURRENT active
// device identity WITHOUT that key's own signature — for exactly the case
// that matters (the key is lost or compromised and cannot be trusted to sign
// its own retirement). Authorization is fleetid.VerifyQuorum, verified inside
// DeviceRevocationCert.Verify (via MergeVerified) — there is no path here that
// skips it: an insufficient/invalid quorum simply means MergeVerified returns
// an error and the fingerprint is never recorded, exactly mirroring
// BreakGlassRotate's hard-rule enforcement in rotation.go.
func BreakGlassRevoke(ks KeyStore, store *RevocationStore, reason, subjectID, requestID string, certs []fleetid.VouchCert, roster fleetid.Roster, threshold int, now time.Time) (*DeviceRevocationCert, error) {
	if ks == nil {
		return nil, errors.New("devicekey: BreakGlassRevoke: nil key store")
	}
	if store == nil {
		return nil, errors.New("devicekey: BreakGlassRevoke: nil revocation store")
	}
	pubDER, err := ks.DeviceIdentity()
	if err != nil {
		return nil, fmt.Errorf("devicekey: BreakGlassRevoke: read identity: %w", err)
	}
	cert := newDeviceRevocationCert(RevocationMethodBreakGlass, pubDER, reason)
	cert.QuorumSubjectID = subjectID
	cert.QuorumRequestID = requestID
	cert.QuorumCerts = certs
	// MergeVerified independently re-verifies the quorum against roster — this
	// is the actual chokepoint; nothing above this call authorizes anything.
	if err := store.MergeVerified(cert, roster, threshold, now); err != nil {
		return nil, fmt.Errorf("devicekey: BreakGlassRevoke: %w", err)
	}
	return cert, nil
}

// ─── Global admission gate (mirrors services/peering's isVulaIDRevoked) ─────────

var (
	deviceRevocationCheckerMu sync.RWMutex
	deviceRevocationChecker   func(fingerprint string) bool
)

// SetRevocationChecker installs the process-wide device-key revocation gate.
// The live server wires this to a RevocationStore.IsRevoked so every
// admission/verify point that calls IsDeviceKeyRevoked honors revocations.
// Passing nil disables the gate.
func SetRevocationChecker(f func(fingerprint string) bool) {
	deviceRevocationCheckerMu.Lock()
	deviceRevocationChecker = f
	deviceRevocationCheckerMu.Unlock()
}

// IsDeviceKeyRevoked reports whether the installed checker (if any) marks
// fingerprint revoked. With no checker installed it returns false — fail-open
// ONLY when the subsystem is entirely absent, matching services/peering's
// isVulaIDRevoked convention exactly. Once wired, every revoked fingerprint is
// rejected.
func IsDeviceKeyRevoked(fingerprint string) bool {
	deviceRevocationCheckerMu.RLock()
	f := deviceRevocationChecker
	deviceRevocationCheckerMu.RUnlock()
	if f == nil {
		return false
	}
	return f(fingerprint)
}

// VerifyDeviceSignatureChecked verifies an ECDSA signature by pubKeyDER over
// digest AND rejects it if pubKeyDER's fingerprint is revoked — the
// device-key parallel to services/peering's VerifyVulaSignatureChecked. Any
// remote verifier of a device-key signature (cloud enrollment, integrations
// broker, etc.) should prefer this over a bare ecdsa.VerifyASN1 so a
// compromised-then-revoked device key can no longer authenticate anywhere the
// checker is wired.
func VerifyDeviceSignatureChecked(pubKeyDER, digest, sig []byte) error {
	if IsDeviceKeyRevoked(Fingerprint(pubKeyDER)) {
		return errors.New("devicekey: identity is revoked")
	}
	pubAny, err := x509.ParsePKIXPublicKey(pubKeyDER)
	if err != nil {
		return fmt.Errorf("devicekey: parse pubkey: %w", err)
	}
	pub, ok := pubAny.(*ecdsa.PublicKey)
	if !ok {
		return errors.New("devicekey: pubkey is not ECDSA")
	}
	if !ecdsa.VerifyASN1(pub, digest, sig) {
		return errors.New("devicekey: signature does not verify")
	}
	return nil
}

// ─── Self fail-closed admission (this box refusing to mint its OWN sigs) ────
//
// Everything above this point is about REMOTE verifiers rejecting a revoked
// key. That alone leaves a gap: a box whose own active device key is
// self-revoked (planned decommission) or break-glass-revoked (compromise
// confirmed by fleet quorum) would, without this guard, happily keep MINTING
// fresh signatures under that key for as long as the process stays up —
// Status().Revoked only REPORTS the fact (see its doc comment), it never
// stopped anything. ErrActiveKeyRevoked / assertActiveKeyNotRevoked close
// that gap: KeyStore.Sign (software.go, tpm.go) and KeyStore.Rotate's
// self-authorized path (which signs the RotationCert with the CURRENT active
// key, exactly like Sign) both call this before touching key material, so a
// revoked active key can mint no NEW authority-bearing signature anywhere in
// this process — not just at a remote verifier that happens to have pulled
// the revocation.
//
// This deliberately does NOT block forceInstallIdentity / BreakGlassRotate /
// BreakGlassRevoke: those are exactly the sanctioned recovery path for a
// revoked-and-untrusted active key, and BreakGlassRotate signs its resulting
// RotationCert with the NEW key (never the revoked old one), so a box stuck
// with a revoked active key is barred from minting MORE signatures under the
// bad key, never permanently bricked.
var ErrActiveKeyRevoked = errors.New("devicekey: active device identity key is revoked; refusing to mint a new signature (fail-closed; use break-glass rotation/revocation to recover)")

// assertActiveKeyNotRevoked fails closed (returns ErrActiveKeyRevoked) when
// activePubDER's fingerprint is known-revoked by the process-wide checker
// (SetRevocationChecker). With no checker installed it never blocks — the
// same "subsystem entirely unwired" convention IsDeviceKeyRevoked already
// documents.
func assertActiveKeyNotRevoked(activePubDER []byte) error {
	if IsDeviceKeyRevoked(Fingerprint(activePubDER)) {
		return ErrActiveKeyRevoked
	}
	return nil
}
