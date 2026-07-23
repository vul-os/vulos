// localkek.go — KMS_STORAGE_KEK: the box-local key that protects the owner's
// KindSymmetric key material (and KindHTTP bearer token) AT REST in the
// kms_config table.
//
// This is a DIFFERENT key from the owner's own KEK (Config/Provider above):
// KMS_STORAGE_KEK never wraps a DEK and never touches customer data — it only
// keeps the config row from being plaintext-readable on disk. Losing it means
// losing the box's cached copy of the owner's key material (recoverable by
// re-registering the KEK reference); it does NOT expose the owner's KEK to
// anyone who does not already have this box's local secrets.
//
// The pattern mirrors services/integrations/selfhost's INTEGRATIONS_KEK
// exactly (see selfhost/crypto.go): fail-closed in prod, an all-zeros dev
// fallback off prod so a laptop checkout works without ceremony.
package kms

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"vulos/backend/services/env"
)

// StorageKEKEnv is the local key-encryption-key for KMS config material at
// rest. A base64-encoded 32-byte AES-256 key.
const StorageKEKEnv = "KMS_STORAGE_KEK"

// devFallbackStorageKEK is the all-zeros dev key, used ONLY off prod when the
// env var is unset. In prod an unset KEK is a hard error (fail-closed).
var devFallbackStorageKEK = make([]byte, 32)

// LoadKEK returns the 32-byte AES key from KMS_STORAGE_KEK (base64).
//
// FAIL-CLOSED IN PROD (activeEnv.IsProd()): an unset or malformed KEK returns
// an error so the caller refuses to register the feature under a
// known/predictable key. Off prod (local/dev) an unset KEK falls back to the
// all-zeros dev key.
func LoadKEK(activeEnv env.Env) ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(StorageKEKEnv))
	if raw == "" {
		if activeEnv.IsProd() {
			return nil, fmt.Errorf("kms: %s must be set in prod (base64-encoded 32-byte key) — refusing to register BYO-KMS under a default key", StorageKEKEnv)
		}
		return devFallbackStorageKEK, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("kms: %s is not valid base64: %w", StorageKEKEnv, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("kms: %s must decode to exactly 32 bytes, got %d", StorageKEKEnv, len(key))
	}
	return key, nil
}

// EncryptKeyMaterial encrypts raw owner key/token material under
// KMS_STORAGE_KEK. Returns hex(nonce||ciphertext). kek must be 32 bytes.
// Reuses the same aesGCMSeal primitive as the envelope layer (single package,
// no need to re-implement AES-GCM a second time).
func EncryptKeyMaterial(kek []byte, material string) (string, error) {
	if len(kek) != 32 {
		return "", fmt.Errorf("kms: KMS_STORAGE_KEK must be 32 bytes, got %d", len(kek))
	}
	return aesGCMSeal(kek, []byte(material))
}

// DecryptKeyMaterial decrypts a value produced by EncryptKeyMaterial.
func DecryptKeyMaterial(kek []byte, encoded string) (string, error) {
	if len(kek) != 32 {
		return "", fmt.Errorf("kms: KMS_STORAGE_KEK must be 32 bytes, got %d", len(kek))
	}
	plain, err := aesGCMOpen(kek, encoded)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
