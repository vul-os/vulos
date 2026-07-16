package oauthprovider

// crypto.go — at-rest encryption for the RS256 id_token signing private key
// (audit M6). The signing key is the crown jewel of the OIDC provider: anyone
// holding it can forge id_tokens for any user. It must never sit in the DB in
// plaintext.
//
// Custody mirrors internal/integrations (refresh-token KEK): a 32-byte AES-256
// key sourced from the existing INTEGRATIONS_KEK env var (base64), with an
// all-zeros dev fallback ONLY under VULOS_DEV. Reusing the broker KEK avoids
// introducing a new mandatory secret while keeping production keys wrapped.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/vul-os/vulos-management/pkg/env"
)

// devFallbackKEK is used only when VULOS_DEV=true and INTEGRATIONS_KEK is unset.
var devFallbackKEK = make([]byte, 32)

// ErrKEKRequired is returned by loadKEK when INTEGRATIONS_KEK is unset outside
// of dev. The id_token signing private key is the crown jewel of the OIDC
// provider; persisting it in plaintext is a critical exposure. We FAIL CLOSED in
// production: the OAuth provider refuses to boot rather than silently writing an
// unencrypted signing key to disk.
var ErrKEKRequired = fmt.Errorf("oauthprovider: INTEGRATIONS_KEK is required outside dev — refusing to persist the id_token signing key in plaintext (set INTEGRATIONS_KEK to base64 32 bytes, or VULOS_DEV=true for local development)")

// loadKEK returns the 32-byte AES key from INTEGRATIONS_KEK (base64).
//
// FAIL-CLOSED: when no key is configured and VULOS_DEV is not set, it returns
// ErrKEKRequired so the caller refuses to boot — never the previous "plaintext-
// compatibility" mode that wrote the signing key unencrypted with only a
// warning. Under VULOS_DEV=true an all-zeros dev key is used so local
// development needs no secret.
func loadKEK() ([]byte, error) {
	raw := os.Getenv("INTEGRATIONS_KEK")
	if raw == "" {
		// WAVE34: dev fallback requires VULOS_DEV truthy AND env!=prod (cross-check).
		if env.DevKEKFallbackAllowed() {
			return devFallbackKEK, nil
		}
		return nil, ErrKEKRequired
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: INTEGRATIONS_KEK is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("oauthprovider: INTEGRATIONS_KEK must decode to exactly 32 bytes, got %d", len(key))
	}
	return key, nil
}

// encryptKEK seals plain with AES-256-GCM under kek, returning base64(nonce||ct).
func encryptKEK(plain string, kek []byte) (string, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return "", fmt.Errorf("oauthprovider: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("oauthprovider: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("oauthprovider: rand nonce: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// decryptKEK reverses encryptKEK.
func decryptKEK(enc string, kek []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("oauthprovider: base64 decode: %w", err)
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return "", fmt.Errorf("oauthprovider: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("oauthprovider: gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("oauthprovider: ciphertext too short")
	}
	plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("oauthprovider: gcm open: %w", err)
	}
	return string(plain), nil
}

// isPlaintextPEM reports whether stored is a legacy unencrypted PKCS#8 PEM
// (rather than a base64 GCM ciphertext). Used to re-wrap legacy keys on load.
func isPlaintextPEM(stored string) bool {
	return strings.Contains(stored, "-----BEGIN")
}
