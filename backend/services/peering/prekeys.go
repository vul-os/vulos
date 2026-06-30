// prekeys.go — forward-secret content keys via an X3DH-style prekey scheme
// (PEER-37 FS hardening).
//
// # Problem
//
// crypto.go derives content keys with STATIC-STATIC X25519 ECDH
// (ConversationKey): both sides use their long-term identity key, so the shared
// key is the SAME for every message forever. A stolen identity key therefore
// retroactively decrypts ALL previously-captured ciphertext — there is no
// forward secrecy.
//
// # Scheme (X3DH-style, store-and-forward / sealed-sender compatible)
//
// Each identity PUBLISHES a prekey bundle (PreKeyBundlePublic):
//   - its identity key (the Ed25519 Vula ID — used only to SIGN, never as a
//     content secret),
//   - a medium-term SIGNED prekey (X25519), signed by the identity key,
//   - a pool of ONE-TIME prekeys (X25519), each consumed at most once.
//
// A sender derives a per-message key from an EPHEMERAL key plus the recipient's
// prekeys (one-time when available, else the signed prekey), exactly as in
// Signal's X3DH:
//
//	DH1 = DH(IK_send, SPK_recv)     // authenticates the sender's identity
//	DH2 = DH(EK_send, IK_recv)      // authenticates the recipient's identity
//	DH3 = DH(EK_send, SPK_recv)     // contributes forward secrecy
//	DH4 = DH(EK_send, OPK_recv)     // one-time; strongest forward secrecy
//	SK  = HKDF(0xFF*32 || DH1 || DH2 || DH3 || DH4)
//
// The identity key appears only in DH1/DH2, whose purpose is mutual
// AUTHENTICATION; the secrecy of SK rests on the EPHEMERAL key EK_send (discarded
// after use) and the recipient's one-time prekey OPK_recv (deleted on first use).
//
// # Forward-secrecy property (HONEST statement)
//
//   - With a ONE-TIME prekey: compromising the long-term identity key AND the
//     signed-prekey private key does NOT retroactively decrypt a captured message,
//     because decryption additionally requires the one-time prekey private key,
//     which the recipient DELETES immediately after first use, and the sender's
//     ephemeral key, which the sender discards. This is full forward secrecy for
//     that message.
//   - WITHOUT a one-time prekey (pool exhausted → signed-prekey fallback): forward
//     secrecy holds against identity-key compromise, but a message stays decryptable
//     by whoever holds the SIGNED-prekey private key until that prekey is rotated.
//     Rotating the signed prekey bounds this window.
//
// # LIMITATIONS (not implemented here, called out deliberately)
//
//   - No Double-Ratchet / per-message ratchet: this provides session-establishment
//     forward secrecy (one SK per initiation), not post-compromise security across
//     a long-lived session. A follow-up would layer a symmetric ratchet on SK.
//   - One-time prekey exhaustion falls back to the signed prekey (see above).
//   - Replay protection of the prekey handshake itself is delegated to the
//     envelope/relay nonce layer.
//
// # Versioning / backward-compat
//
// ContentCryptoVersion tags the wire format. v1 == legacy static-static
// (ConversationKey, no FS); v2 == this X3DH scheme. NegotiateContentVersion picks
// v2 when a recipient bundle is available and v1 otherwise, so a v2 sender can
// still reach a v1-only peer (with a clear loss-of-FS signal to the caller).
package peering

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// ContentCryptoVersion identifies the content-key derivation scheme on the wire.
type ContentCryptoVersion int

const (
	// ContentCryptoV1Legacy is the static-static X25519 ECDH scheme (crypto.go).
	// NO forward secrecy. Retained for backward compatibility with peers that do
	// not publish a prekey bundle.
	ContentCryptoV1Legacy ContentCryptoVersion = 1

	// ContentCryptoV2X3DH is the forward-secret X3DH-style prekey scheme below.
	ContentCryptoV2X3DH ContentCryptoVersion = 2
)

// x3dhHKDFInfo is the HKDF info label binding derived keys to this scheme/version.
const x3dhHKDFInfo = "vula-x3dh-content-v2"

// ─── Published bundle ──────────────────────────────────────────────────────────

// SignedPreKeyPublic is a medium-term X25519 prekey signed by the identity key.
type SignedPreKeyPublic struct {
	ID  string `json:"id"`  // opaque key id (random)
	Pub []byte `json:"pub"` // 32-byte X25519 public key
	Sig []byte `json:"sig"` // Ed25519 signature by the identity key over Pub
}

// OneTimePreKeyPublic is a single-use X25519 prekey.
type OneTimePreKeyPublic struct {
	ID  string `json:"id"`
	Pub []byte `json:"pub"` // 32-byte X25519 public key
}

// PreKeyBundlePublic is what an identity publishes (via wellknown/directory) so
// senders can establish a forward-secret session.
type PreKeyBundlePublic struct {
	IdentityVulaID string                `json:"identity_vula_id"`
	SignedPreKey   SignedPreKeyPublic    `json:"signed_prekey"`
	OneTimePreKeys []OneTimePreKeyPublic `json:"one_time_prekeys,omitempty"`
	UpdatedAt      string                `json:"updated_at"`
}

// VerifySignedPreKey checks that the bundle's signed prekey carries a valid
// signature by the identity key embedded in IdentityVulaID. Fails closed.
func (b *PreKeyBundlePublic) VerifySignedPreKey() error {
	pub, err := decodeVulaID(b.IdentityVulaID)
	if err != nil {
		return fmt.Errorf("prekeys: bundle identity: %w", err)
	}
	if len(b.SignedPreKey.Pub) != curve25519.PointSize {
		return errors.New("prekeys: signed prekey wrong size")
	}
	if !ed25519.Verify(pub, b.SignedPreKey.Pub, b.SignedPreKey.Sig) {
		return errors.New("prekeys: signed prekey signature invalid")
	}
	return nil
}

// ─── Handshake header (sender → recipient) ─────────────────────────────────────

// X3DHHeader travels alongside the ciphertext. It tells the recipient which of
// their prekeys to use and carries the sender's ephemeral public key. It contains
// no secrets and may be sent in the clear (sealed-sender compatible: SenderVulaID
// can itself be carried inside an encrypted sender-cert layer by the caller).
type X3DHHeader struct {
	Version         ContentCryptoVersion `json:"v"`
	SenderVulaID    string               `json:"from"`
	EphemeralPub    []byte               `json:"ek"`               // 32-byte X25519
	SignedPreKeyID  string               `json:"spk_id"`           // which SPK was used
	OneTimePreKeyID string               `json:"opk_id,omitempty"` // which OPK, if any
}

// ─── Sender side ───────────────────────────────────────────────────────────────

// X3DHInitiate derives a forward-secret content key SK for sending to the bundle's
// identity. senderEdPriv/senderVulaID are the sender's long-term identity (used
// for authentication only). It returns the per-message header (to transmit) and
// the derived SK (to feed EncryptForPeer). The signed prekey signature is verified
// before use; a one-time prekey is consumed when the bundle offers one.
func X3DHInitiate(senderEdPriv ed25519.PrivateKey, senderVulaID string, bundle *PreKeyBundlePublic) (X3DHHeader, SharedKey, error) {
	var zero SharedKey
	if bundle == nil {
		return X3DHHeader{}, zero, errors.New("prekeys: nil bundle")
	}
	if err := bundle.VerifySignedPreKey(); err != nil {
		return X3DHHeader{}, zero, err
	}

	// Sender identity X25519 (from Ed25519) and recipient identity X25519 (from id).
	ikSendPriv, _, err := DeriveX25519FromEd25519(senderEdPriv)
	if err != nil {
		return X3DHHeader{}, zero, err
	}
	recvEdPub, err := decodeVulaID(bundle.IdentityVulaID)
	if err != nil {
		return X3DHHeader{}, zero, err
	}
	ikRecvPub, err := Ed25519PubToX25519(recvEdPub)
	if err != nil {
		return X3DHHeader{}, zero, err
	}

	// Ephemeral key.
	ekPriv, ekPub, err := genX25519()
	if err != nil {
		return X3DHHeader{}, zero, err
	}

	spkPub := bundle.SignedPreKey.Pub

	// DH1 = DH(IK_send, SPK_recv); DH2 = DH(EK, IK_recv); DH3 = DH(EK, SPK_recv).
	dh1, err := x25519(ikSendPriv[:], spkPub)
	if err != nil {
		return X3DHHeader{}, zero, err
	}
	dh2, err := x25519(ekPriv, ikRecvPub[:])
	if err != nil {
		return X3DHHeader{}, zero, err
	}
	dh3, err := x25519(ekPriv, spkPub)
	if err != nil {
		return X3DHHeader{}, zero, err
	}

	hdr := X3DHHeader{
		Version:        ContentCryptoV2X3DH,
		SenderVulaID:   senderVulaID,
		EphemeralPub:   ekPub,
		SignedPreKeyID: bundle.SignedPreKey.ID,
	}

	concat := append(append(append([]byte{}, dh1...), dh2...), dh3...)

	// DH4 = DH(EK, OPK_recv) when a one-time prekey is offered (strongest FS).
	if len(bundle.OneTimePreKeys) > 0 {
		opk := bundle.OneTimePreKeys[0]
		if len(opk.Pub) == curve25519.PointSize {
			dh4, err := x25519(ekPriv, opk.Pub)
			if err != nil {
				return X3DHHeader{}, zero, err
			}
			concat = append(concat, dh4...)
			hdr.OneTimePreKeyID = opk.ID
		}
	}

	sk, err := x3dhKDF(concat, senderVulaID, bundle.IdentityVulaID)
	if err != nil {
		return X3DHHeader{}, zero, err
	}
	return hdr, sk, nil
}

// ─── Recipient side ────────────────────────────────────────────────────────────

// X3DHRespond derives the same SK on the recipient side from an inbound header.
// recipientEdPriv is the recipient's long-term identity key. store supplies the
// private signed/one-time prekeys; the consumed one-time prekey is DELETED before
// return (this is what provides forward secrecy). Fails closed if the referenced
// prekeys are unknown (e.g. a replayed one-time prekey that was already deleted).
func X3DHRespond(store *PreKeyStore, recipientEdPriv ed25519.PrivateKey, recipientVulaID string, hdr X3DHHeader) (SharedKey, error) {
	var zero SharedKey
	if hdr.Version != ContentCryptoV2X3DH {
		return zero, fmt.Errorf("prekeys: unsupported header version %d", hdr.Version)
	}
	if len(hdr.EphemeralPub) != curve25519.PointSize {
		return zero, errors.New("prekeys: bad ephemeral key")
	}
	if store == nil {
		return zero, errors.New("prekeys: nil store")
	}

	spkPriv, ok := store.signedPreKeyPriv(hdr.SignedPreKeyID)
	if !ok {
		return zero, fmt.Errorf("prekeys: unknown signed prekey %q", hdr.SignedPreKeyID)
	}

	ikRecvPriv, _, err := DeriveX25519FromEd25519(recipientEdPriv)
	if err != nil {
		return zero, err
	}
	senderEdPub, err := decodeVulaID(hdr.SenderVulaID)
	if err != nil {
		return zero, err
	}
	ikSendPub, err := Ed25519PubToX25519(senderEdPub)
	if err != nil {
		return zero, err
	}

	// Mirror the sender's DHs.
	dh1, err := x25519(spkPriv, ikSendPub[:]) // DH(SPK_recv, IK_send)
	if err != nil {
		return zero, err
	}
	dh2, err := x25519(ikRecvPriv[:], hdr.EphemeralPub) // DH(IK_recv, EK)
	if err != nil {
		return zero, err
	}
	dh3, err := x25519(spkPriv, hdr.EphemeralPub) // DH(SPK_recv, EK)
	if err != nil {
		return zero, err
	}
	concat := append(append(append([]byte{}, dh1...), dh2...), dh3...)

	if hdr.OneTimePreKeyID != "" {
		opkPriv, ok := store.oneTimePreKeyPriv(hdr.OneTimePreKeyID)
		if !ok {
			// Already-consumed or unknown one-time prekey: fail closed. This also
			// rejects replays of a one-time-prekey handshake.
			return zero, fmt.Errorf("prekeys: unknown/consumed one-time prekey %q", hdr.OneTimePreKeyID)
		}
		dh4, err := x25519(opkPriv, hdr.EphemeralPub)
		if err != nil {
			return zero, err
		}
		concat = append(concat, dh4...)
		// FORWARD SECRECY: delete the one-time prekey now so its private scalar
		// can never again derive this (or any) message key.
		if err := store.consumeOneTimePreKey(hdr.OneTimePreKeyID); err != nil {
			return zero, err
		}
	}

	return x3dhKDF(concat, hdr.SenderVulaID, recipientVulaID)
}

// ─── KDF ───────────────────────────────────────────────────────────────────────

// x3dhKDF runs HKDF-SHA256 over F||DHs with the IDs bound into the salt, matching
// both sides regardless of who is local/remote (sorted, like PeerSharedSecret).
func x3dhKDF(dhConcat []byte, idA, idB string) (SharedKey, error) {
	var sk SharedKey
	// X3DH prepends F = 0xFF * 32 to the DH concatenation (domain separation from
	// raw X25519 outputs / different curves).
	ikm := make([]byte, 32, 32+len(dhConcat))
	for i := range ikm {
		ikm[i] = 0xFF
	}
	ikm = append(ikm, dhConcat...)

	lo, hi := idA, idB
	if lo > hi {
		lo, hi = hi, lo
	}
	salt := []byte(lo + ":" + hi)
	r := hkdf.New(sha256.New, ikm, salt, []byte(x3dhHKDFInfo))
	if _, err := io.ReadFull(r, sk[:]); err != nil {
		return SharedKey{}, fmt.Errorf("prekeys: hkdf: %w", err)
	}
	return sk, nil
}

// ─── X25519 helpers ────────────────────────────────────────────────────────────

func genX25519() (priv []byte, pub []byte, err error) {
	priv = make([]byte, curve25519.ScalarSize)
	if _, err = rand.Read(priv); err != nil {
		return nil, nil, err
	}
	pub, err = curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, nil, err
	}
	return priv, pub, nil
}

func x25519(priv, pub []byte) ([]byte, error) {
	out, err := curve25519.X25519(priv, pub)
	if err != nil {
		return nil, fmt.Errorf("prekeys: x25519: %w", err)
	}
	return out, nil
}

// ─── Private prekey store ──────────────────────────────────────────────────────

// prekeyState is the JSON persisted to disk: the signed prekey + remaining
// one-time prekeys, all PRIVATE scalars. Consumed one-time prekeys are removed.
type prekeyState struct {
	IdentityVulaID  string            `json:"identity_vula_id"`
	SignedPreKeyID  string            `json:"signed_prekey_id"`
	SignedPreKeyPub []byte            `json:"signed_prekey_pub"`
	SignedPreKeySig []byte            `json:"signed_prekey_sig"`
	SignedPreKeyPrv []byte            `json:"signed_prekey_prv"`
	OneTimePriv     map[string][]byte `json:"one_time_priv"` // id → 32-byte X25519 priv
	OneTimePub      map[string][]byte `json:"one_time_pub"`
	UpdatedAt       string            `json:"updated_at"`
}

// PreKeyStore owns this node's PRIVATE prekey material and publishes the public
// bundle. It is goroutine-safe and persists to <dir>/prekeys.json.
type PreKeyStore struct {
	mu    sync.Mutex
	path  string
	state prekeyState
}

// NewPreKeyStore opens (or initializes) the prekey store for identityVulaID under
// dir. On first use it generates a signed prekey (signed by idPriv) and a pool of
// oneTimeCount one-time prekeys. idPriv must correspond to identityVulaID.
func NewPreKeyStore(dir, identityVulaID string, idPriv ed25519.PrivateKey, oneTimeCount int) (*PreKeyStore, error) {
	if !ed25519PrivMatchesVulaID(idPriv, identityVulaID) {
		return nil, errors.New("prekeys: identity private key does not match identity_vula_id")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("prekeys: mkdir: %w", err)
	}
	s := &PreKeyStore{path: filepath.Join(dir, "prekeys.json")}
	if raw, err := os.ReadFile(s.path); err == nil {
		var st prekeyState
		if json.Unmarshal(raw, &st) == nil && st.IdentityVulaID == identityVulaID && len(st.SignedPreKeyPrv) == curve25519.ScalarSize {
			s.state = st
			return s, nil
		}
	}
	// Fresh init.
	if err := s.initFresh(identityVulaID, idPriv, oneTimeCount); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *PreKeyStore) initFresh(identityVulaID string, idPriv ed25519.PrivateKey, oneTimeCount int) error {
	spkPriv, spkPub, err := genX25519()
	if err != nil {
		return err
	}
	st := prekeyState{
		IdentityVulaID:  identityVulaID,
		SignedPreKeyID:  newKeyID(),
		SignedPreKeyPub: spkPub,
		SignedPreKeySig: ed25519.Sign(idPriv, spkPub),
		SignedPreKeyPrv: spkPriv,
		OneTimePriv:     map[string][]byte{},
		OneTimePub:      map[string][]byte{},
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	for i := 0; i < oneTimeCount; i++ {
		p, pub, err := genX25519()
		if err != nil {
			return err
		}
		id := newKeyID()
		st.OneTimePriv[id] = p
		st.OneTimePub[id] = pub
	}
	s.state = st
	return s.saveLocked()
}

func (s *PreKeyStore) saveLocked() error {
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

// PublicBundle returns the publishable bundle (signed prekey + remaining one-time
// prekeys). Senders fetch this via wellknown/directory.
func (s *PreKeyStore) PublicBundle() *PreKeyBundlePublic {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := &PreKeyBundlePublic{
		IdentityVulaID: s.state.IdentityVulaID,
		SignedPreKey: SignedPreKeyPublic{
			ID:  s.state.SignedPreKeyID,
			Pub: append([]byte(nil), s.state.SignedPreKeyPub...),
			Sig: append([]byte(nil), s.state.SignedPreKeySig...),
		},
		UpdatedAt: s.state.UpdatedAt,
	}
	for id, pub := range s.state.OneTimePub {
		b.OneTimePreKeys = append(b.OneTimePreKeys, OneTimePreKeyPublic{ID: id, Pub: append([]byte(nil), pub...)})
	}
	return b
}

// OneTimePreKeyCount reports how many one-time prekeys remain (for replenishment).
func (s *PreKeyStore) OneTimePreKeyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.state.OneTimePriv)
}

// Replenish tops the one-time prekey pool back up to target.
func (s *PreKeyStore) Replenish(target int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.state.OneTimePriv) < target {
		p, pub, err := genX25519()
		if err != nil {
			return err
		}
		id := newKeyID()
		s.state.OneTimePriv[id] = p
		s.state.OneTimePub[id] = pub
	}
	return s.saveLocked()
}

func (s *PreKeyStore) signedPreKeyPriv(id string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.state.SignedPreKeyID {
		return nil, false
	}
	return s.state.SignedPreKeyPrv, true
}

func (s *PreKeyStore) oneTimePreKeyPriv(id string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.state.OneTimePriv[id]
	return p, ok
}

// consumeOneTimePreKey deletes a one-time prekey (forward secrecy) and persists.
func (s *PreKeyStore) consumeOneTimePreKey(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.state.OneTimePriv[id]; !ok {
		return fmt.Errorf("prekeys: one-time prekey %q already consumed", id)
	}
	delete(s.state.OneTimePriv, id)
	delete(s.state.OneTimePub, id)
	return s.saveLocked()
}

func newKeyID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ─── Version negotiation ───────────────────────────────────────────────────────

// NegotiateContentVersion selects the content-crypto version for a recipient.
// It returns V2 (X3DH, forward-secret) when a usable bundle is available, else V1
// (legacy static-static, NO forward secrecy) so a v2 sender can still reach a
// v1-only peer. The bool reports whether forward secrecy is achieved.
func NegotiateContentVersion(bundle *PreKeyBundlePublic) (ContentCryptoVersion, bool) {
	if bundle != nil && bundle.VerifySignedPreKey() == nil && len(bundle.SignedPreKey.Pub) == curve25519.PointSize {
		return ContentCryptoV2X3DH, true
	}
	return ContentCryptoV1Legacy, false
}

// ─── Publication hook (wellknown/directory) ────────────────────────────────────

// prekeyBundlePublisher, when set, supplies this node's public bundle for the
// wellknown endpoint. The live server wires it from a *PreKeyStore.
var (
	prekeyPublisherMu sync.RWMutex
	prekeyPublisher   func() *PreKeyBundlePublic
)

// SetPreKeyPublisher installs the prekey-bundle source for /.well-known/vula-id.
// Passing nil disables prekey publication (peers then fall back to v1/no-FS).
func SetPreKeyPublisher(f func() *PreKeyBundlePublic) {
	prekeyPublisherMu.Lock()
	prekeyPublisher = f
	prekeyPublisherMu.Unlock()
}

func wkCurrentPreKeyBundle() *PreKeyBundlePublic {
	prekeyPublisherMu.RLock()
	f := prekeyPublisher
	prekeyPublisherMu.RUnlock()
	if f == nil {
		return nil
	}
	return f()
}
