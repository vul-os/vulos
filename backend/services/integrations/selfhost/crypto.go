// Package selfhost is the SOVEREIGN (no-cloud-CP) OAuth integrations provider
// for a Vulos OS box (self-host P2).
//
// # WHY THIS EXISTS
//
// A box can instead point at an operator-configured OAuth broker
// (INTEG-01/02, services/integrations/client.go): the broker custodies the
// fleet OAuth client secret + the user's refresh token under INTEGRATIONS_KEK,
// and the box mints short-lived access tokens over the device-HMAC channel.
// A sovereign box has no such broker configured.
//
// This package lets the OWNER connect their OWN accounts with the OWNER'S OWN
// OAuth app credentials, entirely on the box:
//
//   - The owner registers their own OAuth app (their own client_id/secret) with
//     each provider and sets GOOGLE_OAUTH_CLIENT_ID/SECRET (+ MICROSOFT_*,
//     DROPBOX_*) on the box.
//   - /api/integrations/{provider}/connect runs the standard auth-code flow ON
//     THE BOX (consent → callback → code→token exchange).
//   - The refresh token is custodied AT REST, AES-256-GCM under a LOCAL KEK
//     (INTEGRATIONS_KEK, fail-closed in prod exactly like the CP), and refreshed
//     locally. Short-lived access tokens are minted from it.
//
// The minted access token has the SAME downstream shape the assistant/mail
// already consume (services/integrations.Token), so no consumer changes.
//
// SELECTION: the composition root wires this provider ONLY when the CP is
// unconfigured (VULOS_CP_URL / VULOS_CLOUD_URL unset). When a CP IS configured
// the existing broker client (services/integrations.Client) is used unchanged —
// the cloud path is never broken.
package selfhost

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"vulos/backend/services/env"
)

// IntegrationsKEKEnv is the local key-encryption-key for refresh tokens at rest.
// It mirrors the CP's INTEGRATIONS_KEK: a base64-encoded 32-byte AES-256 key.
// Refresh tokens are the crown jewels of a self-host connection, so production
// MUST set it — a dev-only all-zeros fallback is allowed ONLY off prod.
const IntegrationsKEKEnv = "INTEGRATIONS_KEK"

// devFallbackKEK is the all-zeros dev key, used ONLY off prod when the env var is
// unset. In prod an unset KEK is a hard error (fail-closed), matching the CP and
// the OS STORAGE_KEK posture.
var devFallbackKEK = make([]byte, 32)

// LoadKEK returns the 32-byte AES key from INTEGRATIONS_KEK (base64).
//
// FAIL-CLOSED IN PROD (activeEnv.IsProd()): an unset or malformed KEK returns an
// error so the caller refuses to custody refresh tokens under a known/zero key.
// Off prod (local/dev) an unset KEK falls back to the all-zeros dev key so a
// laptop checkout works without ceremony — the SAME gate the CP and OS master-key
// paths use.
func LoadKEK(activeEnv env.Env) ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(IntegrationsKEKEnv))
	if raw == "" {
		if activeEnv.IsProd() {
			return nil, fmt.Errorf("selfhost: %s must be set in prod (base64-encoded 32-byte key) — refusing to custody refresh tokens under a default key", IntegrationsKEKEnv)
		}
		return devFallbackKEK, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("selfhost: %s is not valid base64: %w", IntegrationsKEKEnv, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("selfhost: %s must decode to exactly 32 bytes, got %d", IntegrationsKEKEnv, len(key))
	}
	return key, nil
}

// encrypt seals plain with AES-256-GCM under kek, returning base64(nonce||ct||tag).
// Empty plaintext encrypts to "" so "no cached access token" is stored cheaply.
// (Byte-identical scheme to the CP's integrations/crypto.go so a future migration
// between the two custody stores is straightforward.)
func encrypt(plain string, kek []byte) (string, error) {
	if plain == "" {
		return "", nil
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return "", fmt.Errorf("selfhost: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("selfhost: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("selfhost: rand nonce: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// decrypt reverses encrypt. Empty input decrypts to "". A wrong key or tampered
// ciphertext fails the AEAD tag check and returns an error — never partial data.
func decrypt(enc string, kek []byte) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("selfhost: base64 decode: %w", err)
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return "", fmt.Errorf("selfhost: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("selfhost: gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("selfhost: ciphertext too short")
	}
	plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("selfhost: gcm open: %w", err)
	}
	return string(plain), nil
}
