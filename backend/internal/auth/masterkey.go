// masterkey.go — the per-user MASTER KEY primitive (Tier-1 honest-privacy foundation).
//
// ─── What this is ─────────────────────────────────────────────────────────────
//
// Every account owns one random 256-bit MASTER KEY. It is the single root from
// which all future per-content encryption keys are derived (see DeriveContentKey
// and KEY HIERARCHY below). The master key itself is NEVER stored in the clear
// and NEVER logged; only two *wrapped* copies of it are persisted:
//
//	MASTER KEY ─┬─ AES-256-GCM wrap under a PBKDF2(password)-derived key   → env.Password
//	            └─ AES-256-GCM wrap under a HKDF(recovery-phrase)-derived key → env.Phrase
//
// Either wrap can reconstruct the same master key:
//   - normal unlock → UnwrapWithPassword (client-side at login, see src/lib/masterKey.js)
//   - password lost → UnwrapWithMnemonic (recovery-phrase path)
//   - password reset → RewrapPassword (unwrap via phrase, re-wrap the SAME master
//     key under the new password; the phrase wrap and therefore the master key
//     itself are unchanged, so previously-encrypted content stays decryptable).
//
// ─── KEY HIERARCHY (wave 3 builds on this) ────────────────────────────────────
//
//	recovery phrase (24-word BIP39)                password
//	        │ PBKDF2-HMAC-SHA512 (BIP39 seed)          │ PBKDF2-HMAC-SHA256
//	        │ + HKDF-SHA256                            │  (600k iters)
//	        ▼                                          ▼
//	  phraseWrapKey (32B)                        passwordWrapKey (32B)
//	        │  AES-256-GCM                              │  AES-256-GCM
//	        └──────────────► MASTER KEY (32B) ◄─────────┘
//	                              │ HKDF-SHA256, info="vulos-content:<domain>:<id>"
//	                              ▼
//	                        per-content key (32B)   ← WAVE 3 wraps content keys here
//
// ─── Crypto choices / WebCrypto parity ────────────────────────────────────────
//
// The password wrap uses PBKDF2-HMAC-SHA256 (not Argon2id) and AES-256-GCM (not
// XChaCha20-Poly1305) DELIBERATELY: both are natively available in the browser's
// WebCrypto SubtleCrypto with ZERO third-party JavaScript, which is what makes an
// honest, dependency-free CLIENT-SIDE unwrap possible (src/lib/masterKey.js). The
// envelope carries an explicit `kdf` tag and version so migrating the password
// KDF to Argon2id later (once an argon2 WASM module is bundled for the browser) is
// a forward-compatible change, not a breaking one. See SECURITY note in the PR.
//
// The recovery-phrase side REUSES the existing recovery.go BIP39 machinery
// (entropyToMnemonic / deriveRecoverySeed / ValidateMnemonic), so the master-key
// phrase is a real 24-word BIP39 phrase with the same properties as the rest of
// the recovery subsystem.
//
// Fail-closed: every unwrap authenticates via AES-GCM; a wrong password or wrong
// phrase fails the AEAD tag check and returns an error — it never returns partial
// or unauthenticated key material.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// ─── Parameters (part of the wire format; do not change without a version bump) ─

const (
	// MasterKeyLen is the size of the per-user master key in bytes (256 bits).
	MasterKeyLen = 32

	// mkPBKDF2Iters is the PBKDF2-HMAC-SHA256 iteration count for the password
	// wrap. 600k follows OWASP 2023 guidance for PBKDF2-HMAC-SHA256.
	mkPBKDF2Iters = 600_000

	// masterKeyEnvelopeVersion is the on-disk envelope schema version.
	masterKeyEnvelopeVersion = 1

	kdfPassword = "pbkdf2-sha256"
	kdfPhrase   = "bip39-hkdf-sha256"
)

// AEAD associated-data domain separators. Distinct tags for the two wraps mean a
// blob sealed for one slot cannot be silently substituted into the other.
var (
	aadPasswordWrap = []byte("vulos-mk-pw-v1")
	aadPhraseWrap   = []byte("vulos-mk-phrase-v1")
	// hkdfPhraseInfo derives the phrase wrap key from the BIP39 seed.
	hkdfPhraseInfo = []byte("vulos-masterkey-wrap-v1")
	// hkdfZeroSalt is an explicit zero salt so Go and WebCrypto HKDF agree byte
	// for byte (WebCrypto requires an explicit salt parameter).
	hkdfZeroSalt = make([]byte, sha256.Size)
)

// ─── Envelope wire format ─────────────────────────────────────────────────────

// wrapSlot is one AES-256-GCM-sealed copy of the master key.
type wrapSlot struct {
	KDF  string `json:"kdf"`
	Iter int    `json:"iter,omitempty"` // PBKDF2 iterations (password slot only)
	Salt []byte `json:"salt,omitempty"` // PBKDF2 salt (password slot only)
	IV   []byte `json:"iv"`             // 12-byte AES-GCM nonce
	CT   []byte `json:"ct"`             // ciphertext || 16-byte tag
}

// MasterKeyEnvelope is the opaque, server-storable object. It never contains the
// master key or the recovery phrase — only two wrapped copies of the key.
type MasterKeyEnvelope struct {
	Version  int      `json:"v"`
	Password wrapSlot `json:"pw"`
	Phrase   wrapSlot `json:"phrase"`
}

// Marshal serialises the envelope to JSON bytes for persistence.
func (e *MasterKeyEnvelope) Marshal() ([]byte, error) { return json.Marshal(e) }

// ParseMasterKeyEnvelope parses a stored envelope blob.
func ParseMasterKeyEnvelope(blob []byte) (*MasterKeyEnvelope, error) {
	var e MasterKeyEnvelope
	if err := json.Unmarshal(blob, &e); err != nil {
		return nil, fmt.Errorf("auth/masterkey: parse envelope: %w", err)
	}
	if e.Version != masterKeyEnvelopeVersion {
		return nil, fmt.Errorf("auth/masterkey: unsupported envelope version %d", e.Version)
	}
	return &e, nil
}

// PasswordEnvelopeJSON returns just the password wrap slot as a compact JSON
// object suitable for handing to the browser so it can unwrap the master key
// client-side. It intentionally omits the phrase slot (that path is recovery-only
// and never needed for a normal login).
func (e *MasterKeyEnvelope) PasswordEnvelopeJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"v":    e.Version,
		"kdf":  e.Password.KDF,
		"iter": e.Password.Iter,
		"salt": e.Password.Salt,
		"iv":   e.Password.IV,
		"ct":   e.Password.CT,
	})
}

// ─── Generation + wrapping ────────────────────────────────────────────────────

// NewMasterKey returns a fresh random 256-bit master key.
func NewMasterKey() ([]byte, error) {
	mk := make([]byte, MasterKeyLen)
	if _, err := io.ReadFull(rand.Reader, mk); err != nil {
		return nil, fmt.Errorf("auth/masterkey: generate: %w", err)
	}
	return mk, nil
}

// WrapMasterKey seals mk under BOTH the password and the recovery mnemonic and
// returns the persistable envelope. It does not retain mk.
func WrapMasterKey(mk []byte, password, mnemonic string) (*MasterKeyEnvelope, error) {
	if len(mk) != MasterKeyLen {
		return nil, fmt.Errorf("auth/masterkey: master key must be %d bytes", MasterKeyLen)
	}
	if password == "" {
		return nil, errors.New("auth/masterkey: password must not be empty")
	}
	if err := ValidateMnemonic(mnemonic); err != nil {
		return nil, fmt.Errorf("auth/masterkey: invalid mnemonic: %w", err)
	}

	pw, err := sealUnderPassword(mk, password)
	if err != nil {
		return nil, err
	}
	ph, err := sealUnderMnemonic(mk, mnemonic)
	if err != nil {
		return nil, err
	}
	return &MasterKeyEnvelope{Version: masterKeyEnvelopeVersion, Password: pw, Phrase: ph}, nil
}

// ProvisionMasterKey is the one-shot signup helper: it generates a fresh master
// key AND a fresh 24-word recovery phrase, wraps the key under both the password
// and that phrase, and returns the persistable envelope blob plus the phrase for
// ONE-TIME display to the user. The plaintext master key is zeroed before return;
// the phrase is returned to the caller (which must show it once and never persist
// it). Neither the master key nor the phrase is ever logged.
func ProvisionMasterKey(password string) (envelopeBlob []byte, mnemonic string, err error) {
	mk, err := NewMasterKey()
	if err != nil {
		return nil, "", err
	}
	defer zero(mk)

	entropy := make([]byte, 32)
	if _, err = io.ReadFull(rand.Reader, entropy); err != nil {
		return nil, "", fmt.Errorf("auth/masterkey: entropy: %w", err)
	}
	mnemonic, err = entropyToMnemonic(entropy)
	if err != nil {
		return nil, "", fmt.Errorf("auth/masterkey: mnemonic: %w", err)
	}

	env, err := WrapMasterKey(mk, password, mnemonic)
	if err != nil {
		return nil, "", err
	}
	blob, err := env.Marshal()
	if err != nil {
		return nil, "", fmt.Errorf("auth/masterkey: marshal: %w", err)
	}
	return blob, mnemonic, nil
}

// ─── Unwrapping ───────────────────────────────────────────────────────────────

// UnwrapWithPassword reconstructs the master key from the password wrap. This is
// the same operation the browser performs client-side at login; the Go version
// exists for server-side flows (reset) and for tests. A wrong password fails the
// AEAD tag check and returns an error (fail-closed).
func UnwrapWithPassword(e *MasterKeyEnvelope, password string) ([]byte, error) {
	if e == nil {
		return nil, errors.New("auth/masterkey: nil envelope")
	}
	if e.Password.KDF != kdfPassword {
		return nil, fmt.Errorf("auth/masterkey: unsupported password kdf %q", e.Password.KDF)
	}
	iter := e.Password.Iter
	if iter <= 0 {
		return nil, errors.New("auth/masterkey: invalid pbkdf2 iterations")
	}
	key := pbkdf2SHA256([]byte(password), e.Password.Salt, iter, 32)
	defer zero(key)
	return aesGCMOpen(key, e.Password.IV, e.Password.CT, aadPasswordWrap)
}

// UnwrapWithMnemonic reconstructs the master key from the recovery-phrase wrap.
func UnwrapWithMnemonic(e *MasterKeyEnvelope, mnemonic string) ([]byte, error) {
	if e == nil {
		return nil, errors.New("auth/masterkey: nil envelope")
	}
	if e.Phrase.KDF != kdfPhrase {
		return nil, fmt.Errorf("auth/masterkey: unsupported phrase kdf %q", e.Phrase.KDF)
	}
	if err := ValidateMnemonic(mnemonic); err != nil {
		return nil, fmt.Errorf("auth/masterkey: invalid mnemonic: %w", err)
	}
	key, err := phraseWrapKey(mnemonic)
	if err != nil {
		return nil, err
	}
	defer zero(key)
	return aesGCMOpen(key, e.Phrase.IV, e.Phrase.CT, aadPhraseWrap)
}

// RewrapPassword performs a phrase-based password reset: it unwraps the master
// key with the recovery mnemonic and re-wraps THE SAME master key under the new
// password. The phrase wrap is preserved unchanged, so the master key — and every
// piece of content ever encrypted under it — remains recoverable. A wrong
// mnemonic fails closed and no re-wrap happens.
func RewrapPassword(e *MasterKeyEnvelope, mnemonic, newPassword string) (*MasterKeyEnvelope, error) {
	if newPassword == "" {
		return nil, errors.New("auth/masterkey: new password must not be empty")
	}
	mk, err := UnwrapWithMnemonic(e, mnemonic)
	if err != nil {
		return nil, err
	}
	defer zero(mk)

	pw, err := sealUnderPassword(mk, newPassword)
	if err != nil {
		return nil, err
	}
	// Preserve the existing phrase slot verbatim: the master key is unchanged.
	return &MasterKeyEnvelope{Version: masterKeyEnvelopeVersion, Password: pw, Phrase: e.Phrase}, nil
}

// ─── Content-key derivation (wave 3 entry point) ──────────────────────────────

// DeriveContentKey derives a 256-bit per-content key from the master key using
// HKDF-SHA256 with a domain/id-scoped info string. Wave 3 (content encryption)
// wraps individual mail/file/etc. content keys under keys derived here, so that
// each content domain and item gets an independent key while everything remains
// rooted in the single master key. Deterministic: same (master, domain, id) →
// same key. domain and id MUST NOT contain the ':' separator ambiguously; they
// are length-prefixed to make the derivation injective.
func DeriveContentKey(masterKey []byte, domain, id string) ([]byte, error) {
	if len(masterKey) != MasterKeyLen {
		return nil, fmt.Errorf("auth/masterkey: master key must be %d bytes", MasterKeyLen)
	}
	info := make([]byte, 0, len(domain)+len(id)+32)
	info = append(info, []byte("vulos-content:")...)
	info = appendLenPrefixed(info, []byte(domain))
	info = appendLenPrefixed(info, []byte(id))
	hk := hkdf.New(sha256.New, masterKey, hkdfZeroSalt, info)
	out := make([]byte, 32)
	if _, err := io.ReadFull(hk, out); err != nil {
		return nil, fmt.Errorf("auth/masterkey: derive content key: %w", err)
	}
	return out, nil
}

// ─── Internals ────────────────────────────────────────────────────────────────

func sealUnderPassword(mk []byte, password string) (wrapSlot, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return wrapSlot{}, err
	}
	key := pbkdf2SHA256([]byte(password), salt, mkPBKDF2Iters, 32)
	defer zero(key)
	iv, ct, err := aesGCMSeal(key, mk, aadPasswordWrap)
	if err != nil {
		return wrapSlot{}, err
	}
	return wrapSlot{KDF: kdfPassword, Iter: mkPBKDF2Iters, Salt: salt, IV: iv, CT: ct}, nil
}

func sealUnderMnemonic(mk []byte, mnemonic string) (wrapSlot, error) {
	key, err := phraseWrapKey(mnemonic)
	if err != nil {
		return wrapSlot{}, err
	}
	defer zero(key)
	iv, ct, err := aesGCMSeal(key, mk, aadPhraseWrap)
	if err != nil {
		return wrapSlot{}, err
	}
	return wrapSlot{KDF: kdfPhrase, IV: iv, CT: ct}, nil
}

// phraseWrapKey derives the 32-byte AES key that wraps the master key under the
// recovery phrase: BIP39 seed (reused from recovery.go) → HKDF-SHA256.
func phraseWrapKey(mnemonic string) ([]byte, error) {
	seed := deriveRecoverySeed(normaliseMnemonic(mnemonic)) // recovery.go
	hk := hkdf.New(sha256.New, seed, hkdfZeroSalt, hkdfPhraseInfo)
	key := make([]byte, 32)
	if _, err := io.ReadFull(hk, key); err != nil {
		return nil, fmt.Errorf("auth/masterkey: derive phrase key: %w", err)
	}
	return key, nil
}

// aesGCMSeal returns (iv, ciphertext||tag). AES-256-GCM, 12-byte nonce — chosen
// for byte-for-byte parity with WebCrypto's AES-GCM.
func aesGCMSeal(key, plaintext, aad []byte) (iv, ct []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	iv = make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, iv); err != nil {
		return nil, nil, err
	}
	ct = gcm.Seal(nil, iv, plaintext, aad)
	return iv, ct, nil
}

func aesGCMOpen(key, iv, ct, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(iv) != gcm.NonceSize() {
		return nil, errors.New("auth/masterkey: bad nonce size")
	}
	pt, err := gcm.Open(nil, iv, ct, aad)
	if err != nil {
		// Uniform error: never distinguish "wrong key" from "tampered" — both are
		// simply "cannot decrypt" (fail-closed).
		return nil, errors.New("auth/masterkey: unwrap failed (wrong password or phrase?)")
	}
	if len(pt) != MasterKeyLen {
		zero(pt)
		return nil, errors.New("auth/masterkey: unwrapped key has wrong length")
	}
	return pt, nil
}

// pbkdf2SHA256 is an inline PBKDF2-HMAC-SHA256 (mirrors the SHA-512 variant in
// recovery.go) to avoid adding a golang.org/x/crypto/pbkdf2 import.
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	hashLen := sha256.Size
	blocks := (keyLen + hashLen - 1) / hashLen
	buf := make([]byte, 0, blocks*hashLen)
	for block := 1; block <= blocks; block++ {
		var blockInt [4]byte
		binary.BigEndian.PutUint32(blockInt[:], uint32(block))
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write(blockInt[:])
		u := mac.Sum(nil)
		xored := make([]byte, len(u))
		copy(xored, u)
		for i := 1; i < iter; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range xored {
				xored[j] ^= u[j]
			}
		}
		buf = append(buf, xored...)
	}
	return buf[:keyLen]
}

func appendLenPrefixed(dst, b []byte) []byte {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	dst = append(dst, n[:]...)
	return append(dst, b...)
}

// zero overwrites b so key material does not linger in memory longer than needed.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// constantTimeEqual is a small helper for tests / callers comparing key material.
func constantTimeEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
