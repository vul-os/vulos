// Package multiinstance — FABRIC-KEY-01: key-at-rest encryption + rotation +
// revocation for the per-instance fabric signing key.
//
// Background
//
//	The fabric signing identity is a per-instance Ed25519 key (CRDT-QUORUM-01):
//	uninstall observations are SIGNED with it and peers verify them against the
//	rostered public key, so a single shared-secret holder can only ever sign as
//	itself. The legacy LoadOrCreateInstanceKey persisted the raw 32-byte seed in
//	a 0600 file — access-controlled by file perms only, NOT encrypted at rest.
//
// This file adds three things WITHOUT changing the signing/quorum semantics:
//
//   - Key-at-rest encryption (KeyringSealer): the seed is wrapped with
//     AES-256-GCM under a 32-byte root key sourced from the OS keyring /
//     environment (the same custody class the AI-router and identity stores
//     use). The on-disk file becomes an opaque sealed envelope; an attacker who
//     reads the data dir without the keyring key cannot recover the signing key.
//
//   - Key ROTATION with an overlap window (rotation.go): a new key is published
//     to the roster while the OLD public key is still ACCEPTED for a grace
//     period, so peers re-learn the new key without rejecting in-flight signed
//     observations made under the old key.
//
//   - REVOCATION (rotation.go): a compromised peer key is marked revoked in the
//     roster, so observations signed by it stop counting toward quorum
//     immediately (verification fails closed for a revoked key).
//
// Pure-Go: crypto/aes + crypto/cipher only (no CGO). Mirrors the AES-256-GCM
// envelope pattern in internal/airouter/config.go.
package multiinstance

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

// FabricKeyEnv is the environment variable holding the 32-byte (64 hex char)
// OS-keyring root key that wraps the fabric signing key at rest. In production
// (VULOS_ENV=prod) it MUST be set; otherwise SealedKeyFromEnv fails closed.
const FabricKeyEnv = "VULOS_FABRIC_KEY_HEX"

// sealedKeyMagic tags an encrypted-at-rest key file so we can distinguish a
// sealed envelope from a legacy bare-base64-seed file and transparently migrate.
const sealedKeyMagic = "vulos-fabric-sealed-v1"

// KeyringSealer wraps/unwraps the fabric signing seed with AES-256-GCM under a
// 32-byte root key held in the OS keyring (supplied as bytes here). It is the
// "key-at-rest" mechanism: the seed never touches disk in plaintext.
//
// The root key is the same custody class as the box's other at-rest keys
// (internal/airouter, internal/identity): derived from the OS keyring / an
// operator-supplied env secret, never persisted alongside the ciphertext.
type KeyringSealer struct {
	rootKey []byte // exactly 32 bytes (AES-256)
}

// NewKeyringSealer returns a sealer using rootKey, which must be exactly 32
// bytes (AES-256-GCM). The caller owns rootKey's secrecy.
func NewKeyringSealer(rootKey []byte) (*KeyringSealer, error) {
	if len(rootKey) != 32 {
		return nil, fmt.Errorf("multiinstance: KeyringSealer root key must be 32 bytes, got %d", len(rootKey))
	}
	cp := make([]byte, 32)
	copy(cp, rootKey)
	return &KeyringSealer{rootKey: cp}, nil
}

// SealedKeyFromEnv builds a KeyringSealer from FabricKeyEnv. The env value must
// be 64 hex chars (32 bytes). Fail-closed in production: when VULOS_ENV=prod and
// the var is unset, it returns an error rather than silently using a dev key.
// In dev (VULOS_ENV != prod) it derives a deterministic dev key and logs loudly,
// matching internal/airouter.NewStoreFromEnv so local builds/tests still work.
func SealedKeyFromEnv() (*KeyringSealer, error) {
	keyHex := strings.TrimSpace(os.Getenv(FabricKeyEnv))
	if keyHex != "" {
		raw, err := hex.DecodeString(keyHex)
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("multiinstance: %s must be 64 hex chars (32 bytes)", FabricKeyEnv)
		}
		return NewKeyringSealer(raw)
	}
	if os.Getenv("VULOS_ENV") == "prod" {
		return nil, fmt.Errorf("multiinstance: %s unset in VULOS_ENV=prod — refusing to seal the fabric key with the dev key", FabricKeyEnv)
	}
	log.Printf("[fabric] WARNING: %s unset — sealing fabric key with deterministic dev key (NEVER use in production; set VULOS_ENV=prod to enforce)", FabricKeyEnv)
	h := sha256.Sum256([]byte("vulos-fabric-keyring-dev-root-do-not-use-in-prod"))
	return NewKeyringSealer(h[:])
}

// sealedEnvelope is the on-disk JSON shape of an encrypted-at-rest key file.
type sealedEnvelope struct {
	Magic      string `json:"magic"`      // sealedKeyMagic
	Nonce      []byte `json:"nonce"`      // GCM nonce
	Ciphertext []byte `json:"ciphertext"` // AES-256-GCM(seed)
}

// Seal encrypts a 32-byte Ed25519 seed into an opaque envelope blob.
func (s *KeyringSealer) Seal(seed []byte) ([]byte, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("multiinstance: Seal seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	block, err := aes.NewCipher(s.rootKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, seed, []byte(sealedKeyMagic))
	return json.Marshal(sealedEnvelope{Magic: sealedKeyMagic, Nonce: nonce, Ciphertext: ct})
}

// Open decrypts an envelope produced by Seal back to the 32-byte seed.
func (s *KeyringSealer) Open(blob []byte) ([]byte, error) {
	var env sealedEnvelope
	if err := json.Unmarshal(blob, &env); err != nil {
		return nil, fmt.Errorf("multiinstance: Open: parse envelope: %w", err)
	}
	if env.Magic != sealedKeyMagic {
		return nil, fmt.Errorf("multiinstance: Open: unrecognised envelope magic %q", env.Magic)
	}
	block, err := aes.NewCipher(s.rootKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(env.Nonce) != gcm.NonceSize() {
		return nil, errors.New("multiinstance: Open: bad nonce length")
	}
	seed, err := gcm.Open(nil, env.Nonce, env.Ciphertext, []byte(sealedKeyMagic))
	if err != nil {
		return nil, fmt.Errorf("multiinstance: Open: decrypt failed (wrong keyring key?): %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("multiinstance: Open: decrypted seed has %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	return seed, nil
}

// LoadOrCreateSealedInstanceKey loads this box's persistent fabric signing key
// from path, transparently:
//
//   - reading an EXISTING sealed envelope (decrypting via the sealer), OR
//   - migrating a LEGACY bare-base64-seed file in place to a sealed envelope
//     (so an upgrade re-encrypts the at-rest key without changing the identity),
//     OR
//   - generating a fresh key on first use and writing it SEALED.
//
// The returned key is the same stable identity LoadOrCreateInstanceKey returned,
// but the on-disk representation is now encrypted at rest. The file is written
// 0600. When sealer is nil this behaves exactly like LoadOrCreateInstanceKey
// (unencrypted) so callers that have no keyring still work (with a warning).
func LoadOrCreateSealedInstanceKey(path string, sealer *KeyringSealer) (ed25519.PrivateKey, error) {
	if sealer == nil {
		// No keyring available — fall back to the legacy unencrypted path so the
		// fabric still has an identity (logged by the caller).
		return LoadOrCreateInstanceKey(path)
	}

	if b, err := os.ReadFile(path); err == nil {
		trimmed := strings.TrimSpace(string(b))
		// Sealed envelope?
		if strings.Contains(trimmed, sealedKeyMagic) {
			seed, oerr := sealer.Open([]byte(trimmed))
			if oerr != nil {
				return nil, fmt.Errorf("multiinstance: LoadOrCreateSealedInstanceKey: open sealed key: %w", oerr)
			}
			return ed25519.NewKeyFromSeed(seed), nil
		}
		// Legacy bare-base64-seed file → migrate in place to a sealed envelope.
		if seed, derr := base64.StdEncoding.DecodeString(trimmed); derr == nil && len(seed) == ed25519.SeedSize {
			if werr := writeSealedSeed(path, seed, sealer); werr != nil {
				return nil, fmt.Errorf("multiinstance: LoadOrCreateSealedInstanceKey: migrate legacy key: %w", werr)
			}
			log.Printf("[fabric] migrated legacy plaintext signing key at %s to encrypted-at-rest envelope", path)
			return ed25519.NewKeyFromSeed(seed), nil
		}
		// Corrupt/empty — fall through and regenerate.
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("multiinstance: LoadOrCreateSealedInstanceKey: generate: %w", err)
	}
	if err := writeSealedSeed(path, priv.Seed(), sealer); err != nil {
		return nil, fmt.Errorf("multiinstance: LoadOrCreateSealedInstanceKey: persist: %w", err)
	}
	return priv, nil
}

// writeSealedSeed seals seed and writes the envelope to path with 0600 perms.
func writeSealedSeed(path string, seed []byte, sealer *KeyringSealer) error {
	blob, err := sealer.Seal(seed)
	if err != nil {
		return err
	}
	return os.WriteFile(path, blob, 0o600)
}
