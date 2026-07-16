package ota

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// EnvKeyProvider
// ─────────────────────────────────────────────────────────────────────────────

// EnvKeyProvider reads the ed25519 signing key pair from environment variables:
//
//	OTA_SIGNING_KEY_BASE64 — base64-encoded ed25519 seed (32 bytes) or full
//	                         private key (64 bytes). Required for Sign.
//	OTA_VERIFY_KEY_BASE64  — base64-encoded ed25519 public key (32 bytes).
//	                         If absent it is derived from the private key.
//
// Secrets are resolved lazily (on first call) and never cached — so a container
// restart picks up a secret rotation.
type EnvKeyProvider struct{}

func (e EnvKeyProvider) PrivateKey() (ed25519.PrivateKey, error) {
	raw := strings.TrimSpace(os.Getenv("OTA_SIGNING_KEY_BASE64"))
	if raw == "" {
		return nil, errors.New("ota: OTA_SIGNING_KEY_BASE64 not set")
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("ota: decode OTA_SIGNING_KEY_BASE64: %w", err)
	}
	switch len(b) {
	case ed25519.SeedSize: // 32 bytes — expand to full 64-byte key
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519.PrivateKeySize: // 64 bytes — use directly
		return ed25519.PrivateKey(b), nil
	default:
		return nil, fmt.Errorf("ota: OTA_SIGNING_KEY_BASE64 must be 32 or 64 bytes; got %d", len(b))
	}
}

func (e EnvKeyProvider) PublicKey() (ed25519.PublicKey, error) {
	raw := strings.TrimSpace(os.Getenv("OTA_VERIFY_KEY_BASE64"))
	if raw != "" {
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("ota: decode OTA_VERIFY_KEY_BASE64: %w", err)
		}
		if len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("ota: OTA_VERIFY_KEY_BASE64 must be 32 bytes; got %d", len(b))
		}
		return ed25519.PublicKey(b), nil
	}
	// Derive from private key.
	priv, err := e.PrivateKey()
	if err != nil {
		return nil, err
	}
	return priv.Public().(ed25519.PublicKey), nil
}

// compile-time assertion.
var _ KeyProvider = EnvKeyProvider{}

// ─────────────────────────────────────────────────────────────────────────────
// FileKeyProvider
// ─────────────────────────────────────────────────────────────────────────────

// FileKeyProvider reads the ed25519 key pair from files on disk.
// PrivKeyPath must be mode 0o600; violation returns an error.
// PublicKeyPath is optional; if empty the public key is derived from the private key.
type FileKeyProvider struct {
	PrivKeyPath   string // path to base64-encoded seed or private key file
	PublicKeyPath string // path to base64-encoded public key file (optional)
}

func (f FileKeyProvider) PrivateKey() (ed25519.PrivateKey, error) {
	if f.PrivKeyPath == "" {
		return nil, errors.New("ota: FileKeyProvider.PrivKeyPath is empty")
	}
	info, err := os.Stat(f.PrivKeyPath)
	if err != nil {
		return nil, fmt.Errorf("ota: stat private key file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("ota: private key file %s has insecure permissions %o; must be 0o600",
			f.PrivKeyPath, info.Mode().Perm())
	}
	data, err := os.ReadFile(f.PrivKeyPath)
	if err != nil {
		return nil, fmt.Errorf("ota: read private key file: %w", err)
	}
	raw := strings.TrimSpace(string(data))
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("ota: decode private key file: %w", err)
	}
	switch len(b) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(b), nil
	default:
		return nil, fmt.Errorf("ota: private key file must be 32 or 64 bytes; got %d", len(b))
	}
}

func (f FileKeyProvider) PublicKey() (ed25519.PublicKey, error) {
	if f.PublicKeyPath != "" {
		data, err := os.ReadFile(f.PublicKeyPath)
		if err != nil {
			return nil, fmt.Errorf("ota: read public key file: %w", err)
		}
		raw := strings.TrimSpace(string(data))
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("ota: decode public key file: %w", err)
		}
		if len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("ota: public key file must be 32 bytes; got %d", len(b))
		}
		return ed25519.PublicKey(b), nil
	}
	priv, err := f.PrivateKey()
	if err != nil {
		return nil, err
	}
	return priv.Public().(ed25519.PublicKey), nil
}

// compile-time assertion.
var _ KeyProvider = FileKeyProvider{}

// ─────────────────────────────────────────────────────────────────────────────
// KeyProviderSigner — wraps any KeyProvider into a Signer
// ─────────────────────────────────────────────────────────────────────────────

// KeyProviderSigner implements Signer using a KeyProvider.
// This is the bridge between the custody abstraction and the signing interface.
type KeyProviderSigner struct {
	Provider KeyProvider
}

func (k KeyProviderSigner) Sign(manifestJSON []byte) (string, error) {
	priv, err := k.Provider.PrivateKey()
	if err != nil {
		return "", fmt.Errorf("ota: signer get key: %w", err)
	}
	sig := ed25519.Sign(priv, manifestJSON)
	return base64.StdEncoding.EncodeToString(sig), nil
}

func (k KeyProviderSigner) Verify(manifestJSON []byte, sigB64 string) error {
	pub, err := k.Provider.PublicKey()
	if err != nil {
		return fmt.Errorf("ota: verify get key: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return ErrInvalidSignature
	}
	if !ed25519.Verify(pub, manifestJSON, sig) {
		return ErrInvalidSignature
	}
	return nil
}

// compile-time assertion.
var _ Signer = KeyProviderSigner{}

// ─────────────────────────────────────────────────────────────────────────────
// NewEnvSigner — convenience constructor used by main.go
// ─────────────────────────────────────────────────────────────────────────────

// NewEnvSigner constructs a Signer backed by EnvKeyProvider.
// Returns an error if OTA_SIGNING_KEY_BASE64 is not set (so the caller can
// degrade gracefully on admin endpoints while the feed endpoint still works).
func NewEnvSigner() (Signer, error) {
	p := EnvKeyProvider{}
	if _, err := p.PrivateKey(); err != nil {
		return nil, err
	}
	return KeyProviderSigner{Provider: p}, nil
}
