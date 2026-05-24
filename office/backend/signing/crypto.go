// Package signing provides cryptographic primitives for OFFICE-44:
//   - Ed25519 key management (env-sourced, generate-if-missing for dev)
//   - Per-signature token generation (sign payload with server private key)
//   - SHA-256 hash-chain helpers (event hashing + chain verification)
//   - Document SHA-256 hashing
package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// -------------------------------------------------------------------------
// Key management
// -------------------------------------------------------------------------

var (
	mu         sync.Mutex
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
)

// LoadOrGenerateKey loads an Ed25519 private key from the SIGNING_PRIVATE_KEY
// environment variable (base64-encoded 64-byte seed+public-key or raw seed).
// If the env var is absent, a fresh key is generated in-memory (dev mode).
// Call this once at startup; it is safe to call multiple times (idempotent).
func LoadOrGenerateKey() error {
	mu.Lock()
	defer mu.Unlock()

	if privateKey != nil {
		return nil
	}

	raw := os.Getenv("SIGNING_PRIVATE_KEY")
	if raw != "" {
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return fmt.Errorf("signing: decode SIGNING_PRIVATE_KEY: %w", err)
		}
		switch len(decoded) {
		case ed25519.SeedSize: // 32 bytes — seed only
			privateKey = ed25519.NewKeyFromSeed(decoded)
		case ed25519.PrivateKeySize: // 64 bytes — full key
			privateKey = ed25519.PrivateKey(decoded)
		default:
			return fmt.Errorf("signing: SIGNING_PRIVATE_KEY must be 32 or 64 bytes, got %d", len(decoded))
		}
		publicKey = privateKey.Public().(ed25519.PublicKey)
		return nil
	}

	// Dev mode: generate a fresh key each run.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("signing: generate key: %w", err)
	}
	privateKey = priv
	publicKey = pub
	return nil
}

// PublicKey returns the server's Ed25519 public key (base64-encoded).
// LoadOrGenerateKey must have been called first.
func PublicKeyBase64() (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if publicKey == nil {
		return "", errors.New("signing: key not initialised; call LoadOrGenerateKey first")
	}
	return base64.StdEncoding.EncodeToString(publicKey), nil
}

// -------------------------------------------------------------------------
// Token payload + generation
// -------------------------------------------------------------------------

// TokenPayload is the JSON-serialised claims signed into the Ed25519 token.
type TokenPayload struct {
	EnvelopeID    string `json:"envelope_id"`
	SignerID      string `json:"signer_id"`
	FieldID       string `json:"field_id,omitempty"`
	DocHashBefore string `json:"doc_hash_before"`
	DocHashAfter  string `json:"doc_hash_after"`
	Timestamp     int64  `json:"ts"` // Unix seconds
	Identity      string `json:"identity,omitempty"`
}

// GenerateToken signs the payload with the server private key and returns a
// base64url-encoded "<payload_b64>.<signature_b64>" token.
func GenerateToken(p TokenPayload) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if privateKey == nil {
		return "", errors.New("signing: key not initialised; call LoadOrGenerateKey first")
	}

	payloadJSON, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("signing: marshal payload: %w", err)
	}

	sig := ed25519.Sign(privateKey, payloadJSON)

	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)
	return payloadB64 + "." + sigB64, nil
}

// VerifyToken validates a token previously produced by GenerateToken.
// Returns the decoded payload on success.
func VerifyToken(token string) (TokenPayload, error) {
	mu.Lock()
	defer mu.Unlock()
	if publicKey == nil {
		return TokenPayload{}, errors.New("signing: key not initialised; call LoadOrGenerateKey first")
	}

	// Split into payload + signature.
	dot := findLastDot(token)
	if dot < 0 {
		return TokenPayload{}, errors.New("signing: malformed token (no dot separator)")
	}
	payloadB64 := token[:dot]
	sigB64 := token[dot+1:]

	payloadJSON, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return TokenPayload{}, fmt.Errorf("signing: decode payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return TokenPayload{}, fmt.Errorf("signing: decode signature: %w", err)
	}

	if !ed25519.Verify(publicKey, payloadJSON, sig) {
		return TokenPayload{}, errors.New("signing: invalid token signature")
	}

	var p TokenPayload
	if err := json.Unmarshal(payloadJSON, &p); err != nil {
		return TokenPayload{}, fmt.Errorf("signing: unmarshal payload: %w", err)
	}
	return p, nil
}

func findLastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// -------------------------------------------------------------------------
// Document hashing
// -------------------------------------------------------------------------

// HashDocument computes the hex-encoded SHA-256 digest of raw document bytes.
func HashDocument(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// -------------------------------------------------------------------------
// Audit-event hash-chain
// -------------------------------------------------------------------------

// AuditChainInput is a stable projection of an audit event used for hashing.
// Only include fields that are set at write time (no mutable derived fields).
type AuditChainInput struct {
	ID            string    `json:"id"`
	EnvelopeID    string    `json:"envelope_id"`
	SignerID      string    `json:"signer_id,omitempty"`
	Action        string    `json:"action"`
	Timestamp     time.Time `json:"timestamp"`
	IP            string    `json:"ip,omitempty"`
	Identity      string    `json:"identity,omitempty"`
	DocHashBefore string    `json:"doc_hash_before,omitempty"`
	DocHashAfter  string    `json:"doc_hash_after,omitempty"`
	Token         string    `json:"token,omitempty"`
	PrevEventHash string    `json:"prev_event_hash,omitempty"`
}

// HashEvent computes a SHA-256 hash over the canonical JSON of the event's
// chain-input fields.  The returned hex string becomes the next prev_event_hash.
func HashEvent(inp AuditChainInput) (string, error) {
	// Canonical JSON: sorted keys guaranteed by struct field order.
	data, err := json.Marshal(inp)
	if err != nil {
		return "", fmt.Errorf("signing: hash event marshal: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// VerifyChain checks that the hash-chain of a slice of audit events is intact.
// Events must be passed in chronological order.
// Returns an error naming the first broken link.
func VerifyChain(events []AuditChainInput) error {
	prevHash := ""
	for i, ev := range events {
		if ev.PrevEventHash != prevHash {
			return fmt.Errorf("signing: chain broken at event[%d] id=%s: expected prev_hash %q got %q",
				i, ev.ID, prevHash, ev.PrevEventHash)
		}
		h, err := HashEvent(ev)
		if err != nil {
			return fmt.Errorf("signing: hash event[%d] id=%s: %w", i, ev.ID, err)
		}
		prevHash = h
	}
	return nil
}

// LatestEventHash returns the hash of the last event in a (possibly empty)
// chain — i.e. the value to use as prev_event_hash for the next event.
func LatestEventHash(events []AuditChainInput) (string, error) {
	if len(events) == 0 {
		return "", nil
	}
	return HashEvent(events[len(events)-1])
}
