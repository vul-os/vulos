// Package kms implements customer-held encryption keys (BYO KMS) for the
// vulos.cloud control plane — COMPLY-04.
//
// Threat model: Vulos stores only wrapped DEKs and ciphertext. The customer
// holds the Key Encryption Key (KEK) in their own KMS. Without the customer's
// KEK, Vulos cannot decrypt any bucket object: there is no plaintext path.
//
// Envelope-encryption flow:
//
//	Encrypt:
//	  1. Generate a random 32-byte DEK (Data Encryption Key).
//	  2. Encrypt the plaintext with DEK via AES-256-GCM → ciphertext.
//	  3. Call Provider.WrapDEK(DEK) → wrappedDEK (customer KMS holds the KEK).
//	  4. Store: bucket-object = ciphertext; DB row = wrappedDEK.
//
//	Decrypt:
//	  1. Fetch wrappedDEK from DB.
//	  2. Call Provider.UnwrapDEK(wrappedDEK) → DEK (customer KMS decrypts).
//	  3. Decrypt ciphertext with DEK.
//
//	Key rotation (without data loss):
//	  1. Rotate: re-wrap every stored DEK under the new KEK version.
//	  2. The ciphertext (data) is NEVER re-encrypted — only the DEK wrapping changes.
//	  3. Both old and new KEK versions must be valid until rotation is complete.
//
// Tier gate: BYO KMS is a Pro/Team/Enterprise feature. Free accounts are
// rejected with ErrTierRequired.
//
// Provider interface: intentionally narrow (WrapDEK / UnwrapDEK). Two
// bundled implementations:
//   - SymmetricProvider: customer supplies a 32-byte AES key (local; for tests
//     and "self-hosted KMS" mode where key material is transmitted once at
//     config time and stored server-side encrypted under STORAGE_KEK).
//   - HTTPProvider: customer provides a URL (e.g. HashiCorp Vault transit,
//     AWS KMS proxy, or any HTTP-based KMS) with a Bearer token. Vulos calls
//     the customer's endpoint; the key never leaves their infrastructure.
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
	// ErrTierRequired is returned when a Free-tier account attempts to use BYO KMS.
	ErrTierRequired = errors.New("kms: BYO KMS requires Pro tier or higher")
	// ErrUnknownAccount is returned when no KMS config exists for the account.
	ErrUnknownAccount = errors.New("kms: unknown account")
	// ErrUnknownKey is returned when no wrapped-DEK row matches the given ID.
	ErrUnknownKey = errors.New("kms: unknown key id")
	// ErrProviderFailed is returned when the KMS provider call fails.
	ErrProviderFailed = errors.New("kms: provider operation failed")
	// ErrKeyRevoked is returned when the DEK row has been marked revoked.
	ErrKeyRevoked = errors.New("kms: key has been revoked")
)

// ---------------------------------------------------------------------------
// Provider — the customer-side KMS interface
// ---------------------------------------------------------------------------

// Provider is the customer KMS abstraction. Implementations must be
// concurrency-safe. Only two operations are needed: wrap and unwrap a DEK.
//
// The DEK is always a 32-byte (256-bit) random value. WrapDEK returns an
// opaque []byte blob (the provider's ciphertext / wrapped key material).
// UnwrapDEK recovers the original 32-byte DEK.
type Provider interface {
	// WrapDEK encrypts plainDEK (32 bytes) with the customer KEK.
	// Returns the wrapped DEK blob, which is safe to store in Vulos DB.
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
	// KindSymmetric uses AES-256-GCM with customer-supplied key material.
	// The key material is stored server-side encrypted under STORAGE_KEK.
	KindSymmetric ProviderKind = "symmetric"
	// KindHTTP delegates wrap/unwrap to a customer-operated HTTP KMS endpoint.
	// Vulos never sees the customer KEK; it only transmits DEK material to the
	// customer's endpoint over HTTPS.
	KindHTTP ProviderKind = "http"
)

// Config holds the KMS configuration for one account. It is persisted in
// the kms_configs table.
//
// EncryptedKeyMaterial contains the AES-GCM ciphertext of the customer's
// symmetric key material (KindSymmetric) or the bearer token (KindHTTP),
// encrypted under the Vulos STORAGE_KEK. It is NEVER returned in API responses.
type Config struct {
	AccountID            string       `json:"account_id"`
	Tier                 string       `json:"tier"` // "pro" | "team" | "enterprise"
	Kind                 ProviderKind `json:"kind"`
	Endpoint             string       `json:"endpoint,omitempty"` // KindHTTP only
	EncryptedKeyMaterial string       `json:"-"`                  // hex(nonce||ciphertext); never in JSON
	KEKVersion           int          `json:"kek_version"`        // customer KEK version (for rotation)
	CreatedAt            time.Time    `json:"created_at"`
	UpdatedAt            time.Time    `json:"updated_at"`
}

// DEKRecord is a wrapped DEK stored in the kms_deks table. One DEK per
// logical object (or per-account default). Wrapped = customer-KMS-encrypted.
// Vulos cannot decrypt the data without the customer's cooperation.
type DEKRecord struct {
	ID         string    `json:"id"` // ULID or UUID assigned at insert
	AccountID  string    `json:"account_id"`
	ObjectRef  string    `json:"object_ref"`  // e.g. "bucket/key" or arbitrary label
	WrappedDEK []byte    `json:"-"`           // opaque blob; never in API JSON
	KEKVersion int       `json:"kek_version"` // KEK version that wrapped this DEK
	Revoked    bool      `json:"revoked"`
	CreatedAt  time.Time `json:"created_at"`
}

// RotationResult summarises the outcome of a DEK rotation sweep.
type RotationResult struct {
	AccountID  string `json:"account_id"`
	Rotated    int    `json:"rotated"` // DEKs successfully re-wrapped
	Skipped    int    `json:"skipped"` // already at target version
	Failed     int    `json:"failed"`  // errors (provider failures)
	NewVersion int    `json:"new_version"`
}

// ---------------------------------------------------------------------------
// Store — KMS persistence contract
// ---------------------------------------------------------------------------

// Store is the persistence contract for KMS configs and wrapped DEKs.
// MemStore is the behavioural reference; SQLStore is the production backend.
type Store interface {
	// GetConfig returns the KMS config for an account.
	// Returns ErrUnknownAccount if no record exists.
	GetConfig(ctx context.Context, accountID string) (Config, error)

	// PutConfig upserts the KMS config for an account.
	PutConfig(ctx context.Context, c Config) error

	// DeleteConfig removes the KMS config and all associated DEKs.
	DeleteConfig(ctx context.Context, accountID string) error

	// PutDEK stores a new wrapped DEK record. The ID field must be set by the caller.
	PutDEK(ctx context.Context, d DEKRecord) error

	// GetDEK returns the DEKRecord by id.
	// Returns ErrUnknownKey if not found, ErrKeyRevoked if revoked.
	GetDEK(ctx context.Context, id string) (DEKRecord, error)

	// GetDEKByRef returns the most-recent non-revoked DEKRecord for an account+objectRef.
	// Returns ErrUnknownKey if not found.
	GetDEKByRef(ctx context.Context, accountID, objectRef string) (DEKRecord, error)

	// ListDEKs returns all DEKRecord rows for an account, newest first.
	// If onlyRevoked is false, includes all rows; if true, includes only revoked rows.
	ListDEKs(ctx context.Context, accountID string, includeRevoked bool) ([]DEKRecord, error)

	// UpdateWrappedDEK replaces the wrappedDEK blob and kek_version for an existing
	// DEK row. Used during key rotation.
	UpdateWrappedDEK(ctx context.Context, id string, wrappedDEK []byte, newVersion int) error

	// RevokeDEK marks a DEK as revoked (soft-delete). Revoked DEKs cannot be
	// used for new encryption but are kept for audit.
	RevokeDEK(ctx context.Context, id string) error

	Close() error
}
