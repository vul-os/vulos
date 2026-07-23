package integrations

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/vul-os/vulos-management/pkg/env"
)

// KEK custody mirrors auth.LoadTOTPKEK: a 32-byte AES-256 key from a base64 env
// var, with an all-zeros dev fallback ONLY under VULOS_DEV. Refresh tokens are
// the crown jewels of the broker, so production MUST set INTEGRATIONS_KEK.

// devFallbackKEK is used only when the dev-KEK gate is open (VULOS_DEV truthy AND
// not prod — see env.DevKEKFallbackAllowed) and INTEGRATIONS_KEK is unset.
var devFallbackKEK = make([]byte, 32)

// LoadKEK returns the 32-byte AES key from INTEGRATIONS_KEK (base64).
func LoadKEK() ([]byte, error) {
	raw := os.Getenv("INTEGRATIONS_KEK")
	if raw == "" {
		// WAVE34: gate the all-zeros dev fallback on env.DevKEKFallbackAllowed so a
		// stray VULOS_DEV on a prod box can never activate a zero KEK.
		if env.DevKEKFallbackAllowed() {
			return devFallbackKEK, nil
		}
		return nil, fmt.Errorf("integrations: INTEGRATIONS_KEK must be set (base64-encoded 32-byte key)")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("integrations: INTEGRATIONS_KEK is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("integrations: INTEGRATIONS_KEK must decode to exactly 32 bytes, got %d", len(key))
	}
	return key, nil
}

// encrypt seals plain with AES-256-GCM under kek, returning base64(nonce||ct||tag).
// Empty plaintext encrypts to "" so we can store "no cached access token" cheaply.
func encrypt(plain string, kek []byte) (string, error) {
	if plain == "" {
		return "", nil
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return "", fmt.Errorf("integrations: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("integrations: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("integrations: rand nonce: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// decrypt reverses encrypt. Empty input decrypts to "".
func decrypt(enc string, kek []byte) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("integrations: base64 decode: %w", err)
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return "", fmt.Errorf("integrations: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("integrations: gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("integrations: ciphertext too short")
	}
	plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("integrations: gcm open: %w", err)
	}
	return string(plain), nil
}
