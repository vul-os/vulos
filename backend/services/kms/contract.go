// Package kms implements customer-held encryption keys (BYO-KMS) for a Vulos
// box — the Settings → Encryption feature.
//
// Threat model: the box stores only wrapped DEKs and ciphertext. The box
// OWNER holds the Key Encryption Key (KEK) — either directly (SymmetricProvider,
// "local KMS" mode) or via their own HTTP-reachable KMS endpoint (HTTPProvider,
// e.g. a self-hosted HashiCorp Vault transit engine). Without the owner's KEK,
// nothing on the box (including a future cloud sync target) can decrypt any
// object protected under this scheme: there is no plaintext path.
//
// This is a single-owner port of vulos.cloud's multi-tenant BYO-KMS control
// plane (management/pkg/kms): one box, one owner, one KEK reference — no
// account scoping, no billing-tier gate. Route access is gated by the box
// owner/admin role (see cmd/server/routes_kms.go), not by a subscription tier.
//
// Envelope-encryption flow:
//
//	Encrypt (WrapData):
//	  1. Generate a random 32-byte DEK (Data Encryption Key).
//	  2. Encrypt the plaintext with the DEK via AES-256-GCM → ciphertext.
//	  3. Call Provider.WrapDEK(DEK) → wrappedDEK (the owner's KMS holds the KEK).
//	  4. Persist: caller stores ciphertext; this package stores wrappedDEK.
//
//	Decrypt (UnwrapData):
//	  1. Fetch wrappedDEK from the DEK record.
//	  2. Call Provider.UnwrapDEK(wrappedDEK) → DEK (the owner's KMS decrypts).
//	  3. Decrypt ciphertext with the DEK.
//
//	Key rotation (without data loss):
//	  1. Rotate: re-wrap every stored DEK under the new KEK version.
//	  2. The ciphertext (data) is NEVER re-encrypted — only the DEK wrapping
//	     changes.
//	  3. Both the old and the new KEK must be valid for the duration of the
//	     rotation sweep.
//
// Provider interface: intentionally narrow (WrapDEK / UnwrapDEK). Two bundled
// implementations (see envelope.go):
//   - SymmetricProvider: the owner supplies (or has the box generate once and
//     display) a 32-byte AES key. The key material is transmitted once at
//     config time and stored on the box encrypted under the local
//     KMS_STORAGE_KEK ("local KMS" mode — weaker sovereignty guarantee than
//     HTTPProvider, since the box briefly holds the plaintext key at
//     configuration time; documented honestly, not hidden).
//   - HTTPProvider: the owner runs their own HTTP-based KMS (e.g. HashiCorp
//     Vault transit, or any compatible HTTPS service). The box calls the
//     owner's endpoint; the KEK never leaves the owner's infrastructure and
//     the box never observes it.
package kms

import (
	"context"
	"errors"
	"time"
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrNotConfigured is returned when no KMS config has been registered yet.
	ErrNotConfigured = errors.New("kms: not configured — register a KEK reference first")
	// ErrAlreadyConfigured is returned by Configure when a config already
	// exists; use RotateKEK to change an existing KEK reference so wrapped
	// DEKs are re-wrapped rather than orphaned.
	ErrAlreadyConfigured = errors.New("kms: already configured — use rotate, not configure, to change the KEK")
	// ErrUnknownKey is returned when no wrapped-DEK row matches the given ID.
	ErrUnknownKey = errors.New("kms: unknown key id")
	// ErrProviderFailed is returned when the owner's KMS provider call fails.
	ErrProviderFailed = errors.New("kms: provider operation failed")
	// ErrKeyRevoked is returned when the DEK row has been marked revoked.
	ErrKeyRevoked = errors.New("kms: key has been revoked")
)

// ---------------------------------------------------------------------------
// Provider — the owner-side KMS interface
// ---------------------------------------------------------------------------

// Provider is the owner KMS abstraction. Implementations must be
// concurrency-safe. Only two operations are needed: wrap and unwrap a DEK.
//
// The DEK is always a 32-byte (256-bit) random value. WrapDEK returns an
// opaque []byte blob (the provider's ciphertext / wrapped key material).
// UnwrapDEK recovers the original 32-byte DEK.
type Provider interface {
	// WrapDEK encrypts plainDEK (32 bytes) with the owner's KEK.
	// Returns the wrapped DEK blob, which is safe to store on the box.
	WrapDEK(ctx context.Context, plainDEK []byte) (wrappedDEK []byte, err error)

	// UnwrapDEK decrypts wrappedDEK to recover the 32-byte plainDEK.
	UnwrapDEK(ctx context.Context, wrappedDEK []byte) (plainDEK []byte, err error)
}

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// ProviderKind identifies which KMS provider implementation to use.
type ProviderKind string

const (
	// KindSymmetric uses AES-256-GCM with owner-supplied (or box-generated,
	// shown-once) key material. The key material is stored on the box
	// encrypted under KMS_STORAGE_KEK.
	KindSymmetric ProviderKind = "symmetric"
	// KindHTTP delegates wrap/unwrap to an owner-operated HTTP KMS endpoint.
	// The box never sees the owner's KEK; it only transmits DEK material to
	// the owner's endpoint over HTTPS.
	KindHTTP ProviderKind = "http"
)

// Config holds the singleton KMS configuration for this box's owner. It is
// persisted in the kms_config table (exactly one row).
//
// EncryptedKeyMaterial contains the KMS_STORAGE_KEK-encrypted copy of the
// owner's symmetric key material (KindSymmetric) or bearer token (KindHTTP).
// It is NEVER returned in an API response.
type Config struct {
	Kind                 ProviderKind `json:"kind"`
	Endpoint             string       `json:"endpoint,omitempty"` // KindHTTP only
	EncryptedKeyMaterial string       `json:"-"`                  // hex(nonce||ciphertext) under KMS_STORAGE_KEK
	KEKVersion           int          `json:"kek_version"`        // owner KEK version (for rotation)
	CreatedAt            time.Time    `json:"created_at"`
	UpdatedAt            time.Time    `json:"updated_at"`
}

// DEKRecord is a wrapped DEK stored in the kms_deks table. One DEK per
// logical object (or arbitrary caller-chosen reference). Wrapped = owner-KMS-
// encrypted; the box cannot decrypt the associated ciphertext without the
// owner's cooperation.
type DEKRecord struct {
	ID         string    `json:"id"`          // UUID assigned at insert
	ObjectRef  string    `json:"object_ref"`  // e.g. "bucket/key" or arbitrary label
	WrappedDEK []byte    `json:"-"`           // opaque blob; never in API JSON
	KEKVersion int       `json:"kek_version"` // KEK version that wrapped this DEK
	Revoked    bool      `json:"revoked"`
	CreatedAt  time.Time `json:"created_at"`
}

// RotationResult summarises the outcome of a DEK rotation sweep.
type RotationResult struct {
	Rotated    int `json:"rotated"` // DEKs successfully re-wrapped
	Skipped    int `json:"skipped"` // already at target version
	Failed     int `json:"failed"`  // errors (provider failures)
	NewVersion int `json:"new_version"`
}

// ---------------------------------------------------------------------------
// Store — KMS persistence contract
// ---------------------------------------------------------------------------

// Store is the persistence contract for the singleton KMS config and its
// wrapped DEKs. SQLStore (store.go) is the production backend.
type Store interface {
	// GetConfig returns the KMS config for this box's owner.
	// Returns ErrNotConfigured if no record exists yet.
	GetConfig(ctx context.Context) (Config, error)

	// PutConfig upserts the singleton KMS config.
	PutConfig(ctx context.Context, c Config) error

	// DeleteConfig removes the KMS config and all associated DEKs. Dangerous:
	// callers lose the ability to decrypt any existing wrapped DEK.
	DeleteConfig(ctx context.Context) error

	// PutDEK stores a new wrapped DEK record. The ID field must be set by the caller.
	PutDEK(ctx context.Context, d DEKRecord) error

	// GetDEK returns the DEKRecord by id.
	// Returns ErrUnknownKey if not found, ErrKeyRevoked if revoked.
	GetDEK(ctx context.Context, id string) (DEKRecord, error)

	// GetDEKByRef returns the most-recent non-revoked DEKRecord for objectRef.
	// Returns ErrUnknownKey if not found.
	GetDEKByRef(ctx context.Context, objectRef string) (DEKRecord, error)

	// ListDEKs returns all DEKRecord rows, newest first. If includeRevoked is
	// false, revoked rows are excluded.
	ListDEKs(ctx context.Context, includeRevoked bool) ([]DEKRecord, error)

	// UpdateWrappedDEK replaces the wrappedDEK blob and kek_version for an
	// existing DEK row. Used during key rotation.
	UpdateWrappedDEK(ctx context.Context, id string, wrappedDEK []byte, newVersion int) error

	// RevokeDEK marks a DEK as revoked (soft-delete). Revoked DEKs cannot be
	// used for new decryption but are kept for audit.
	RevokeDEK(ctx context.Context, id string) error

	Close() error
}
