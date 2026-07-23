package appsplatform

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Token / secret minting.
//
// Security contract:
//   - The app token is a Bearer secret. Only its sha256 HASH is stored at rest
//     (HashToken); the plaintext is shown ONCE to the operator at create/rotate.
//   - The signing secret is stored as-is because the platform must reproduce it
//     to sign outbound events (see signing.go).
//   - The incoming-webhook id is itself the secret for the unauthenticated
//     incoming-webhook URL, so it is generated with the same CSPRNG.

const (
	tokenPrefix  = "vat_" // Vulos App Token
	secretPrefix = "vas_" // Vulos App Secret (signing)
)

// randHex returns n cryptographically-random bytes hex-encoded (2n chars).
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand should never fail; if it does, fail loudly rather than
		// returning a predictable value.
		panic("appsplatform: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// GenerateToken mints a new app token: "vat_" + 32 random bytes hex.
func GenerateToken() string { return tokenPrefix + randHex(32) }

// GenerateSecret mints a new signing secret: "vas_" + 32 random bytes hex.
func GenerateSecret() string { return secretPrefix + randHex(32) }

// GenerateWebhookID mints a random incoming-webhook id (16 random bytes hex).
func GenerateWebhookID() string { return randHex(16) }

// GenerateAppID mints a random app id (16 random bytes hex).
func GenerateAppID() string { return randHex(16) }

// HashToken returns the sha256 hex of an app token. This is what is stored and
// what token auth looks tokens up by — the plaintext token is never persisted.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ---- Signing-secret at-rest encryption (KEK pattern) ------------------------

// encPrefix marks a ciphertext produced by AESGCMEncryptor in the storage
// column. Values without this prefix are treated as legacy plaintext.
const encPrefix = "enc:"

// SigningSecretEncryptor encrypts the vas_ signing secret before it is written
// to durable storage and decrypts it on read. The in-memory App always carries
// the plaintext; encryption is a storage-boundary concern only.
//
// Implementations MUST be safe for concurrent use.
type SigningSecretEncryptor interface {
	// Encrypt returns an opaque, prefix-marked ciphertext for the given plaintext.
	Encrypt(plaintext string) (string, error)
	// Decrypt returns the plaintext. If ciphertext has no enc: prefix it is
	// returned unchanged (plaintext passthrough for backward compatibility).
	Decrypt(ciphertext string) (string, error)
}

// AESGCMEncryptor envelope-encrypts signing secrets with AES-256-GCM.
// Each call to Encrypt uses a freshly generated random nonce so the same
// plaintext produces distinct ciphertexts. The KEK must be 16, 24, or 32 bytes.
type AESGCMEncryptor struct{ kek []byte }

// NewAESGCMEncryptor constructs an AESGCMEncryptor from the given
// key-encryption-key. Returns an error if the key length is invalid.
func NewAESGCMEncryptor(kek []byte) (*AESGCMEncryptor, error) {
	switch len(kek) {
	case 16, 24, 32:
	default:
		return nil, errors.New("appsplatform: AES-GCM KEK must be 16, 24, or 32 bytes")
	}
	cp := make([]byte, len(kek))
	copy(cp, kek)
	return &AESGCMEncryptor{kek: cp}, nil
}

// Encrypt seals plaintext with AES-GCM (random nonce) and returns
// "enc:" + base64(nonce || ciphertext).
func (e *AESGCMEncryptor) Encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(e.kek)
	if err != nil {
		return "", fmt.Errorf("appsplatform: encrypt signing secret: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("appsplatform: encrypt signing secret: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("appsplatform: encrypt signing secret: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil) // nonce prepended
	return encPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. If ciphertext does not carry the enc: prefix it is
// returned as-is so legacy plaintext rows continue to work without migration.
func (e *AESGCMEncryptor) Decrypt(ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, encPrefix) {
		return ciphertext, nil // plaintext passthrough
	}
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(ciphertext, encPrefix))
	if err != nil {
		return "", fmt.Errorf("appsplatform: decrypt signing secret: %w", err)
	}
	block, err := aes.NewCipher(e.kek)
	if err != nil {
		return "", fmt.Errorf("appsplatform: decrypt signing secret: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("appsplatform: decrypt signing secret: %w", err)
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return "", errors.New("appsplatform: decrypt signing secret: ciphertext too short")
	}
	out, err := gcm.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("appsplatform: decrypt signing secret: %w", err)
	}
	return string(out), nil
}
