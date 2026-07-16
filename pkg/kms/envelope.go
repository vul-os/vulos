// envelope.go — pure-Go AES-256-GCM envelope encryption primitives + Provider
// implementations for vulos.cloud BYO KMS (COMPLY-04).
//
// All crypto is stdlib (crypto/aes, crypto/cipher, crypto/rand). No CGO.
package kms

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// DEK generation
// ---------------------------------------------------------------------------

// GenerateDEK returns a cryptographically random 32-byte Data Encryption Key.
// This is the key used to encrypt customer data. It is immediately wrapped by
// the customer's Provider; Vulos never stores the plaintext DEK.
func GenerateDEK() ([]byte, error) {
	dek := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("kms: generate DEK: %w", err)
	}
	return dek, nil
}

// ---------------------------------------------------------------------------
// Envelope encrypt / decrypt (data layer)
// ---------------------------------------------------------------------------

// Encrypt performs envelope encryption:
//  1. Generates a fresh DEK.
//  2. Encrypts plaintext with DEK via AES-256-GCM.
//  3. Wraps the DEK via the customer's Provider.
//
// Returns (ciphertext, wrappedDEK, error).
// ciphertext is hex(nonce||gcmCiphertext). wrappedDEK is the opaque blob
// returned by provider.WrapDEK; store it in the kms_deks table.
func Encrypt(ctx context.Context, p Provider, plaintext []byte) (ciphertext string, wrappedDEK []byte, err error) {
	dek, err := GenerateDEK()
	if err != nil {
		return "", nil, err
	}
	defer zeroBytes(dek)

	ct, err := aesGCMSeal(dek, plaintext)
	if err != nil {
		return "", nil, fmt.Errorf("kms: envelope encrypt: %w", err)
	}

	wdek, err := p.WrapDEK(ctx, dek)
	if err != nil {
		return "", nil, fmt.Errorf("%w: WrapDEK: %v", ErrProviderFailed, err)
	}
	return ct, wdek, nil
}

// Decrypt performs envelope decryption:
//  1. Unwraps the DEK via the customer's Provider.
//  2. Decrypts ciphertext with DEK via AES-256-GCM.
//
// ciphertext must be the hex(nonce||gcmCiphertext) value returned by Encrypt.
func Decrypt(ctx context.Context, p Provider, ciphertext string, wrappedDEK []byte) ([]byte, error) {
	dek, err := p.UnwrapDEK(ctx, wrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("%w: UnwrapDEK: %v", ErrProviderFailed, err)
	}
	defer zeroBytes(dek)

	plain, err := aesGCMOpen(dek, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("kms: envelope decrypt: %w", err)
	}
	return plain, nil
}

// ---------------------------------------------------------------------------
// AES-256-GCM helpers
// ---------------------------------------------------------------------------

// aesGCMSeal encrypts plaintext with key (32 bytes) via AES-256-GCM.
// Returns hex(nonce || gcmCiphertext).
func aesGCMSeal(key, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return hex.EncodeToString(sealed), nil
}

// aesGCMOpen decrypts a hex(nonce || gcmCiphertext) produced by aesGCMSeal.
func aesGCMOpen(key []byte, encoded string) ([]byte, error) {
	data, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("kms: decode ciphertext: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return nil, errors.New("kms: ciphertext too short")
	}
	return gcm.Open(nil, data[:ns], data[ns:], nil)
}

// zeroBytes overwrites b with zeros (best-effort DEK hygiene).
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ---------------------------------------------------------------------------
// SymmetricProvider — AES-256-GCM wrap/unwrap using customer's key material
// ---------------------------------------------------------------------------

// SymmetricProvider wraps/unwraps DEKs using a customer-supplied 32-byte
// AES key. The key is transmitted once at config time and stored server-side
// encrypted under the Vulos STORAGE_KEK. This is the "local KMS" mode.
//
// Security note: Vulos holds the customer key encrypted under STORAGE_KEK.
// For stronger guarantees use HTTPProvider where the key never leaves the
// customer's infrastructure.
type SymmetricProvider struct {
	key []byte // 32-byte AES key (customer KEK)
}

// NewSymmetricProvider returns a SymmetricProvider using the given 32-byte key.
// Returns an error if key is not exactly 32 bytes.
func NewSymmetricProvider(key []byte) (*SymmetricProvider, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("kms: symmetric provider key must be 32 bytes, got %d", len(key))
	}
	k := make([]byte, 32)
	copy(k, key)
	return &SymmetricProvider{key: k}, nil
}

// NewSymmetricProviderFromHex decodes a 64-character hex string into a 32-byte
// key and returns a SymmetricProvider.
func NewSymmetricProviderFromHex(h string) (*SymmetricProvider, error) {
	b, err := hex.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("kms: symmetric provider hex key invalid: %w", err)
	}
	return NewSymmetricProvider(b)
}

// WrapDEK encrypts plainDEK (32 bytes) with the customer's AES key.
// Returns hex(nonce||gcmCiphertext).
func (p *SymmetricProvider) WrapDEK(_ context.Context, plainDEK []byte) ([]byte, error) {
	enc, err := aesGCMSeal(p.key, plainDEK)
	if err != nil {
		return nil, fmt.Errorf("kms: SymmetricProvider WrapDEK: %w", err)
	}
	return []byte(enc), nil
}

// UnwrapDEK decrypts a wrapped DEK blob produced by WrapDEK.
func (p *SymmetricProvider) UnwrapDEK(_ context.Context, wrappedDEK []byte) ([]byte, error) {
	plain, err := aesGCMOpen(p.key, string(wrappedDEK))
	if err != nil {
		return nil, fmt.Errorf("kms: SymmetricProvider UnwrapDEK: %w", err)
	}
	return plain, nil
}

// compile-time assertion.
var _ Provider = (*SymmetricProvider)(nil)

// ---------------------------------------------------------------------------
// HTTPProvider — delegates wrap/unwrap to customer-operated HTTP KMS endpoint
// ---------------------------------------------------------------------------

// HTTPProvider calls the customer's KMS endpoint (e.g. HashiCorp Vault transit,
// AWS KMS proxy, or a custom HTTPS service). The customer KEK never leaves the
// customer's infrastructure; Vulos only sees wrapped blobs.
//
// Expected HTTP contract (POST to endpoint):
//   - Wrap:   POST <endpoint>/wrap   body: {"dek":"<hex>"} → {"wrapped":"<hex>"}
//   - Unwrap: POST <endpoint>/unwrap body: {"wrapped":"<hex>"} → {"dek":"<hex>"}
//
// The customer's service must present a valid TLS certificate. Requests include
// "Authorization: Bearer <token>" when a token is configured.
type HTTPProvider struct {
	endpoint string
	token    string
	client   *http.Client
}

// NewHTTPProvider returns an HTTPProvider that calls the given endpoint with
// the given bearer token (may be empty). The endpoint should be the base URL,
// e.g. "https://kms.customer.example/v1/transit".
func NewHTTPProvider(endpoint, token string) *HTTPProvider {
	return &HTTPProvider{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

type httpWrapReq struct {
	DEK string `json:"dek"`
}
type httpWrapResp struct {
	Wrapped string `json:"wrapped"`
}
type httpUnwrapReq struct {
	Wrapped string `json:"wrapped"`
}
type httpUnwrapResp struct {
	DEK string `json:"dek"`
}

// WrapDEK sends plainDEK (hex-encoded) to <endpoint>/wrap and returns the
// wrapped blob from the customer's KMS.
func (p *HTTPProvider) WrapDEK(ctx context.Context, plainDEK []byte) ([]byte, error) {
	body, err := json.Marshal(httpWrapReq{DEK: hex.EncodeToString(plainDEK)})
	if err != nil {
		return nil, fmt.Errorf("kms: HTTPProvider marshal wrap request: %w", err)
	}
	resp, err := p.doRequest(ctx, "/wrap", body)
	if err != nil {
		return nil, fmt.Errorf("%w: HTTPProvider WrapDEK: %v", ErrProviderFailed, err)
	}
	var r httpWrapResp
	if err := json.Unmarshal(resp, &r); err != nil {
		return nil, fmt.Errorf("kms: HTTPProvider wrap response decode: %w", err)
	}
	if r.Wrapped == "" {
		return nil, fmt.Errorf("%w: HTTPProvider WrapDEK: empty wrapped field in response", ErrProviderFailed)
	}
	return []byte(r.Wrapped), nil
}

// UnwrapDEK sends the wrapped blob to <endpoint>/unwrap and returns the
// plain DEK bytes.
func (p *HTTPProvider) UnwrapDEK(ctx context.Context, wrappedDEK []byte) ([]byte, error) {
	body, err := json.Marshal(httpUnwrapReq{Wrapped: string(wrappedDEK)})
	if err != nil {
		return nil, fmt.Errorf("kms: HTTPProvider marshal unwrap request: %w", err)
	}
	resp, err := p.doRequest(ctx, "/unwrap", body)
	if err != nil {
		return nil, fmt.Errorf("%w: HTTPProvider UnwrapDEK: %v", ErrProviderFailed, err)
	}
	var r httpUnwrapResp
	if err := json.Unmarshal(resp, &r); err != nil {
		return nil, fmt.Errorf("kms: HTTPProvider unwrap response decode: %w", err)
	}
	decoded, err := hex.DecodeString(r.DEK)
	if err != nil {
		return nil, fmt.Errorf("kms: HTTPProvider unwrap DEK hex decode: %w", err)
	}
	if len(decoded) != 32 {
		return nil, fmt.Errorf("%w: HTTPProvider UnwrapDEK: expected 32-byte DEK, got %d", ErrProviderFailed, len(decoded))
	}
	return decoded, nil
}

func (p *HTTPProvider) doRequest(ctx context.Context, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from KMS endpoint", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// compile-time assertion.
var _ Provider = (*HTTPProvider)(nil)
