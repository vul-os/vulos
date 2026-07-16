// service.go — tier-gated KMS service: config management + DEK rotation.
//
// Service ties a Store to a tier-resolution function and the Vulos STORAGE_KEK.
// All mutating operations require Pro tier or higher (ErrTierRequired otherwise).
package kms

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// TierSource resolves the billing tier for an account. billing.Store satisfies
// this interface; a test double can too.
type TierSource interface {
	// AccountTier returns the current tier string for the account:
	// "free" | "pro" | "team" | "enterprise".
	// Returns "free" (not an error) for unknown/unsubscribed accounts.
	AccountTier(ctx context.Context, accountID string) (string, error)
}

// Service is the KMS façade used by HTTP handlers.
type Service struct {
	Store
	// KEK is the 32-byte Vulos STORAGE_KEK used to encrypt customer key material
	// at rest. If nil, key material is stored unencrypted (dev mode only).
	KEK []byte
	// Tiers resolves the billing tier for tier-gating. If nil, all accounts are
	// treated as "pro" (useful for integration tests without a billing store).
	Tiers TierSource
}

// allowedTiers is the set of tiers that may use BYO KMS.
var allowedTiers = map[string]bool{
	"pro":        true,
	"team":       true,
	"enterprise": true,
}

// checkTier returns ErrTierRequired if the account is not on an allowed tier.
func (s *Service) checkTier(ctx context.Context, accountID string) error {
	if s.Tiers == nil {
		return nil // dev mode: skip tier check
	}
	tier, err := s.Tiers.AccountTier(ctx, accountID)
	if err != nil {
		return fmt.Errorf("kms: tier lookup: %w", err)
	}
	if !allowedTiers[tier] {
		return ErrTierRequired
	}
	return nil
}

// ConfigureKMS stores (or updates) the KMS configuration for an account.
// On Pro/Team/Enterprise only. key material is encrypted under KEK before
// storage; if KEK is nil (dev mode) it is stored plaintext.
func (s *Service) ConfigureKMS(ctx context.Context, accountID, tier string, kind ProviderKind,
	endpoint, rawKeyMaterial string) error {
	if err := s.checkTier(ctx, accountID); err != nil {
		return err
	}

	ekm := rawKeyMaterial
	if len(s.KEK) == 32 && rawKeyMaterial != "" {
		enc, err := EncryptKeyMaterial(s.KEK, rawKeyMaterial)
		if err != nil {
			return fmt.Errorf("kms: encrypt key material: %w", err)
		}
		ekm = enc
	}

	return s.Store.PutConfig(ctx, Config{
		AccountID:            accountID,
		Tier:                 tier,
		Kind:                 kind,
		Endpoint:             endpoint,
		EncryptedKeyMaterial: ekm,
		KEKVersion:           1,
	})
}

// ProviderForAccount loads the customer's KMS config and returns an
// initialised Provider. Returns ErrUnknownAccount if no config exists.
func (s *Service) ProviderForAccount(ctx context.Context, accountID string) (Provider, error) {
	cfg, err := s.Store.GetConfig(ctx, accountID)
	if err != nil {
		return nil, err
	}

	// Decrypt the key material if a KEK is set.
	rawMaterial := cfg.EncryptedKeyMaterial
	if len(s.KEK) == 32 && rawMaterial != "" {
		dec, err := DecryptKeyMaterial(s.KEK, rawMaterial)
		if err != nil {
			return nil, fmt.Errorf("kms: decrypt key material for account %s: %w", accountID, err)
		}
		rawMaterial = dec
	}

	switch cfg.Kind {
	case KindSymmetric:
		p, err := NewSymmetricProviderFromHex(rawMaterial)
		if err != nil {
			return nil, fmt.Errorf("kms: build symmetric provider: %w", err)
		}
		return p, nil
	case KindHTTP:
		return NewHTTPProvider(cfg.Endpoint, rawMaterial), nil
	default:
		return nil, fmt.Errorf("kms: unknown provider kind %q", cfg.Kind)
	}
}

// NewEncryptedDEK generates a DEK, encrypts data, wraps the DEK via the
// customer's provider, and stores the DEKRecord. Returns the ciphertext
// and the new DEK record id.
//
// Callers store the ciphertext in the object store; the DEK record id is
// stored as object metadata so Decrypt can retrieve the wrapped DEK.
func (s *Service) NewEncryptedDEK(ctx context.Context, accountID, objectRef string, plaintext []byte) (
	ciphertext string, dekID string, err error,
) {
	if err := s.checkTier(ctx, accountID); err != nil {
		return "", "", err
	}
	p, err := s.ProviderForAccount(ctx, accountID)
	if err != nil {
		return "", "", err
	}
	ct, wrappedDEK, err := Encrypt(ctx, p, plaintext)
	if err != nil {
		return "", "", err
	}
	cfg, err := s.Store.GetConfig(ctx, accountID)
	if err != nil {
		return "", "", err
	}
	id := uuid.New().String()
	if err := s.Store.PutDEK(ctx, DEKRecord{
		ID:         id,
		AccountID:  accountID,
		ObjectRef:  objectRef,
		WrappedDEK: wrappedDEK,
		KEKVersion: cfg.KEKVersion,
	}); err != nil {
		return "", "", fmt.Errorf("kms: store DEK: %w", err)
	}
	return ct, id, nil
}

// DecryptWithDEK looks up the DEK record by dekID, unwraps it via the
// customer's provider, and decrypts ciphertext. Returns ErrKeyRevoked if the
// DEK was revoked.
func (s *Service) DecryptWithDEK(ctx context.Context, accountID, dekID, ciphertext string) ([]byte, error) {
	dek, err := s.Store.GetDEK(ctx, dekID)
	if err != nil {
		return nil, err // ErrUnknownKey or ErrKeyRevoked
	}
	if dek.AccountID != accountID {
		return nil, ErrUnknownKey // ownership check
	}
	p, err := s.ProviderForAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return Decrypt(ctx, p, ciphertext, dek.WrappedDEK)
}

// RotateKEK re-wraps all active DEKs under a new customer KEK. The caller
// must have already updated the provider config (via ConfigureKMS with the
// new key material) and incremented the KEK version. RotateKEK:
//
//  1. Loads all non-revoked DEKs for the account.
//  2. Unwraps each DEK using oldProvider (old KEK).
//  3. Re-wraps using newProvider (new KEK).
//  4. Updates the DB row with the new wrapped DEK + new version.
//
// This is the "key rotation without data loss" guarantee: ciphertext is never
// touched; only the wrapped DEK changes. Both providers must be valid during
// rotation. Returns a RotationResult summary.
func (s *Service) RotateKEK(ctx context.Context, accountID string,
	oldProvider, newProvider Provider, newVersion int,
) (RotationResult, error) {
	result := RotationResult{AccountID: accountID, NewVersion: newVersion}

	deks, err := s.Store.ListDEKs(ctx, accountID, false)
	if err != nil {
		return result, fmt.Errorf("kms: rotation list DEKs: %w", err)
	}

	for _, d := range deks {
		if d.KEKVersion >= newVersion {
			result.Skipped++
			continue
		}
		// Unwrap with old provider.
		plainDEK, err := oldProvider.UnwrapDEK(ctx, d.WrappedDEK)
		if err != nil {
			result.Failed++
			continue
		}
		// Re-wrap with new provider.
		newWrapped, err := newProvider.WrapDEK(ctx, plainDEK)
		zeroBytes(plainDEK)
		if err != nil {
			result.Failed++
			continue
		}
		// Persist the new wrapped DEK.
		if err := s.Store.UpdateWrappedDEK(ctx, d.ID, newWrapped, newVersion); err != nil {
			result.Failed++
			continue
		}
		result.Rotated++
	}
	return result, nil
}
