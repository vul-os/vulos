// Package profile implements the cross-instance login broker for vulos.cloud.
//
// THIS FILE IS THE FROZEN CONTRACT. Do not edit it in feature branches —
// every profile agent builds against these types, this Store interface, the
// reference MemStore (which DEFINES Store semantics), and the reference
// MemSigner (which DEFINES Signer semantics). The SQL Store and real
// EnvSigner implementations must match MemStore/MemSigner behaviourally;
// handlers depend only on the interfaces.
//
// See ../../PROFILE_API.md for the full specification.
//
// Trust model: Cloud signs a short-lived authorisation token. The OS validates
// the signature against the broker pubkey baked at enrollment. Cloud does NOT
// see the OS session establishment — it just signs an authorisation.
//
// Key rotation: a Store tracks an active key and an optional previous key
// (overlap window). During overlap both keys are valid for verification;
// only the active key is used for signing. This mirrors the OTA offline-signer
// model (see internal/ota).
package profile

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────────

var (
	// ErrTokenExpired is returned when a LoginToken's expires_at is in the past.
	ErrTokenExpired = errors.New("profile: login token expired")
	// ErrBadSignature is returned when token signature verification fails.
	ErrBadSignature = errors.New("profile: bad token signature")
	// ErrSignerUnconfigured is returned when PROFILE_LOGIN_KEY_BASE64 is not set
	// and no key has been provided to the signer.
	ErrSignerUnconfigured = errors.New("profile: signer not configured (PROFILE_LOGIN_KEY_BASE64 not set)")
	// ErrNoActiveKey is returned when the Store has no active signing key.
	ErrNoActiveKey = errors.New("profile: no active broker key")
	// ErrKeyNotFound is returned when a key ID is not found in the Store.
	ErrKeyNotFound = errors.New("profile: broker key not found")
)

// ─────────────────────────────────────────────────────────────────────────────
// Domain types
// ─────────────────────────────────────────────────────────────────────────────

// LoginToken is the payload of a cloud-signed cross-instance login token.
// It is serialised to JSON, signed with Ed25519, and returned base64-encoded.
// The OS validates the signature against the broker pubkey baked at enrollment.
type LoginToken struct {
	AccountID   string    `json:"account_id"`
	ULID        string    `json:"ulid"`
	ProfileName string    `json:"profile_name"`
	ExpiresAt   time.Time `json:"expires_at"`
	Nonce       string    `json:"nonce"` // base64url-encoded 16 random bytes

	// ── UNIFIED-SIGNIN additive fields ────────────────────────────────────────
	// The OS-side verifier (vulos/backend/services/auth/cloudlogin.go) REQUIRES
	// account_id AND email in the signed payload, uses name for the local user's
	// display name, gates linking-onto-an-existing-local-user on email_verified,
	// and dedups replays on jti. These keys are ADDITIVE: every pre-existing
	// consumer of LoginToken keeps working (unknown JSON keys are ignored) and
	// the original fields above are unchanged, so this does not break the frozen
	// contract — it extends it.
	Email string `json:"email,omitempty"`
	// Name is the human display name for the OS session; the broker populates it
	// with the requested profile_name.
	Name     string    `json:"name,omitempty"`
	IssuedAt time.Time `json:"issued_at"`
	// EmailVerified reports whether this control plane has verified that the
	// account owns Email. The OS refuses to LINK a cloud login onto an existing
	// local user by email unless this is true (account-takeover guard).
	EmailVerified bool `json:"email_verified,omitempty"`
	// JTI is a unique token id for OS-side replay dedup. Set equal to Nonce.
	JTI string `json:"jti,omitempty"`
}

// IsExpired reports whether t is past its expires_at relative to now.
func (t LoginToken) IsExpired(now time.Time) bool {
	return now.After(t.ExpiresAt)
}

// BrokerKey holds an ed25519 public key and its opaque identifier.
// The ID is used to distinguish keys during the rotation overlap window.
type BrokerKey struct {
	ID        string            // opaque identifier (e.g., base64url of pubkey hash)
	PublicKey ed25519.PublicKey // 32 bytes
}

// ─────────────────────────────────────────────────────────────────────────────
// Signer interface
// ─────────────────────────────────────────────────────────────────────────────

// Signer signs and verifies login token payloads using Ed25519.
// The signing key is never exposed outside the Signer.
// Handlers call Sign/PublicKeyBytes only; verification is on the OS side.
type Signer interface {
	// Sign signs payload (canonicalised JSON of LoginToken) with the active
	// Ed25519 private key and returns the raw 64-byte signature.
	Sign(payload []byte) (signature []byte, err error)

	// PublicKeyBytes returns the raw 32-byte Ed25519 public key for the active
	// signing key. Used by GET /api/profile/broker/pubkey.
	PublicKeyBytes() ([]byte, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// Store interface — key rotation overlap window
// ─────────────────────────────────────────────────────────────────────────────

// Store manages the active and previous broker public keys.
// During key rotation both active and previous keys are valid for verification
// (the OS may have enrolled with the previous key). Only the active key is used
// for signing new tokens.
//
// MemStore is the behavioural source of truth.
type Store interface {
	// ActiveKey returns the currently active broker key.
	// Returns ErrNoActiveKey if no key has been set.
	ActiveKey(ctx context.Context) (BrokerKey, error)

	// PreviousKey returns the previous broker key (overlap window).
	// Returns ErrKeyNotFound if there is no previous key.
	PreviousKey(ctx context.Context) (BrokerKey, error)

	// RotateKey sets a new active key and promotes the current active key
	// to previous (evicting any older previous key).
	// The key ID is computed from the public key.
	RotateKey(ctx context.Context, pub ed25519.PublicKey) error
}

// ─────────────────────────────────────────────────────────────────────────────
// MemSigner — reference Signer implementation (dev / test)
// ─────────────────────────────────────────────────────────────────────────────

// MemSigner implements Signer with an ephemeral Ed25519 key pair.
// Intended for tests and local development. The key is generated once at
// construction; never persisted.
//
// Mirror of ota.MemSigner — same semantics, profile-scoped.
type MemSigner struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// NewMemSigner returns a MemSigner with a freshly generated Ed25519 key pair.
func NewMemSigner() *MemSigner {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic("profile: MemSigner key gen: " + err.Error())
	}
	return &MemSigner{priv: priv, pub: pub}
}

func (s *MemSigner) Sign(payload []byte) ([]byte, error) {
	return ed25519.Sign(s.priv, payload), nil
}

func (s *MemSigner) PublicKeyBytes() ([]byte, error) {
	b := make([]byte, len(s.pub))
	copy(b, s.pub)
	return b, nil
}

// compile-time assertion: MemSigner satisfies Signer.
var _ Signer = (*MemSigner)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// EnvSigner — production Signer backed by PROFILE_LOGIN_KEY_BASE64
// ─────────────────────────────────────────────────────────────────────────────

// EnvSigner implements Signer by reading the signing key from the environment
// variable PROFILE_LOGIN_KEY_BASE64 on every call (so key rotation via secret
// injection or container restart is picked up without a server restart).
//
// PROFILE_LOGIN_KEY_BASE64 must be a standard or URL-safe base64 encoding of:
//   - a 32-byte Ed25519 seed, or
//   - a 64-byte Ed25519 private key.
//
// In production this variable is populated from an HSM or offline key store;
// the pattern mirrors OTA's EnvKeyProvider.
type EnvSigner struct{}

// NewEnvSigner returns an EnvSigner and validates that PROFILE_LOGIN_KEY_BASE64
// is present and well-formed. Returns ErrSignerUnconfigured if the variable is
// absent or malformed.
func NewEnvSigner() (*EnvSigner, error) {
	s := &EnvSigner{}
	if _, err := s.privateKey(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *EnvSigner) privateKey() (ed25519.PrivateKey, error) {
	raw := strings.TrimSpace(os.Getenv("PROFILE_LOGIN_KEY_BASE64"))
	if raw == "" {
		return nil, ErrSignerUnconfigured
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		// Try URL-safe base64 without padding.
		b, err = base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return nil, ErrSignerUnconfigured
		}
	}
	switch len(b) {
	case ed25519.SeedSize: // 32 bytes — expand to full 64-byte key
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519.PrivateKeySize: // 64 bytes — use directly
		return ed25519.PrivateKey(b), nil
	default:
		return nil, ErrSignerUnconfigured
	}
}

func (s *EnvSigner) Sign(payload []byte) ([]byte, error) {
	priv, err := s.privateKey()
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(priv, payload), nil
}

func (s *EnvSigner) PublicKeyBytes() ([]byte, error) {
	priv, err := s.privateKey()
	if err != nil {
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	b := make([]byte, len(pub))
	copy(b, pub)
	return b, nil
}

// compile-time assertion: EnvSigner satisfies Signer.
var _ Signer = (*EnvSigner)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// MemStore — in-memory reference Store
// ─────────────────────────────────────────────────────────────────────────────

// MemStore is the in-memory reference Store. Concurrency-safe. It DEFINES the
// semantics every other implementation must match.
//
// Key IDs are the base64url-encoded raw public key bytes (no padding).
type MemStore struct {
	mu       sync.Mutex
	active   *BrokerKey
	previous *BrokerKey
}

// NewMemStore returns an empty MemStore with no keys set.
func NewMemStore() *MemStore {
	return &MemStore{}
}

// keyID returns a stable opaque ID for a public key: base64url, no padding.
func keyID(pub ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(pub)
}

func (m *MemStore) ActiveKey(_ context.Context) (BrokerKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return BrokerKey{}, ErrNoActiveKey
	}
	return *m.active, nil
}

func (m *MemStore) PreviousKey(_ context.Context) (BrokerKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.previous == nil {
		return BrokerKey{}, ErrKeyNotFound
	}
	return *m.previous, nil
}

func (m *MemStore) RotateKey(_ context.Context, pub ed25519.PublicKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	newKey := &BrokerKey{
		ID:        keyID(pub),
		PublicKey: append(ed25519.PublicKey(nil), pub...),
	}
	// Current active becomes previous; discard any older previous key.
	m.previous = m.active
	m.active = newKey
	return nil
}

// compile-time assertion: MemStore satisfies Store.
var _ Store = (*MemStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// NewNonce — random nonce for LoginToken
// ─────────────────────────────────────────────────────────────────────────────

// NewNonce generates a 16-byte cryptographically random nonce, returned as a
// base64url-encoded string (no padding). Used when minting a LoginToken.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
