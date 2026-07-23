// service.go — the KMS façade used by cmd/server/routes_kms.go: KEK
// registration/rotation + wrap/unwrap DEK operations, single-owner scope.
package kms

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
)

// Service ties a Store to the box's local KMS_STORAGE_KEK.
type Service struct {
	store Store
	// storageKEK is the 32-byte box-local key that protects the owner's
	// KindSymmetric key material / KindHTTP bearer token AT REST. If nil, key
	// material is stored unencrypted (dev mode only — LoadKEK never returns
	// nil, only a dev fallback key, so this only happens if a caller
	// constructs Service directly with a nil key).
	storageKEK []byte
}

// NewService builds a Service. storageKEK should come from LoadKEK.
func NewService(store Store, storageKEK []byte) *Service {
	return &Service{store: store, storageKEK: storageKEK}
}

// Status reports whether BYO-KMS is configured and, if so, the
// non-sensitive config fields (never the key material) plus the current DEK
// count — everything the Settings → Encryption panel needs to render.
type StatusInfo struct {
	Configured bool   `json:"configured"`
	Config     Config `json:"config,omitempty"`
	DEKCount   int    `json:"dek_count"`
}

// Status returns the current configuration status.
func (s *Service) Status(ctx context.Context) (StatusInfo, error) {
	cfg, err := s.store.GetConfig(ctx)
	if err == ErrNotConfigured {
		return StatusInfo{Configured: false}, nil
	}
	if err != nil {
		return StatusInfo{}, err
	}
	deks, err := s.store.ListDEKs(ctx, false)
	if err != nil {
		return StatusInfo{}, fmt.Errorf("kms: status: %w", err)
	}
	return StatusInfo{Configured: true, Config: cfg, DEKCount: len(deks)}, nil
}

// Configure registers the owner's KEK reference for the FIRST time. Returns
// ErrAlreadyConfigured if a config already exists — callers must use
// RotateKEK to change an existing KEK reference so that already-wrapped DEKs
// are re-wrapped rather than orphaned.
//
// If kind is KindSymmetric and rawKeyMaterial is empty, a fresh 32-byte key
// is generated and returned in generatedKeyHex (shown to the owner ONCE —
// the box does not display it again). The box's own copy is only ever kept
// encrypted under KMS_STORAGE_KEK.
func (s *Service) Configure(ctx context.Context, kind ProviderKind, endpoint, rawKeyMaterial string) (cfg Config, generatedKeyHex string, err error) {
	if _, gerr := s.store.GetConfig(ctx); gerr == nil {
		return Config{}, "", ErrAlreadyConfigured
	} else if gerr != ErrNotConfigured {
		return Config{}, "", gerr
	}

	material := rawKeyMaterial
	if kind == KindSymmetric && material == "" {
		dek, gerr := GenerateDEK()
		if gerr != nil {
			return Config{}, "", gerr
		}
		defer zeroBytes(dek)
		material = hex.EncodeToString(dek)
		generatedKeyHex = material
	}
	if kind == KindSymmetric {
		if _, verr := NewSymmetricProviderFromHex(material); verr != nil {
			return Config{}, "", verr
		}
	}

	ekm, err := s.encryptMaterial(material)
	if err != nil {
		return Config{}, "", err
	}
	newCfg := Config{Kind: kind, Endpoint: endpoint, EncryptedKeyMaterial: ekm, KEKVersion: 1}
	if err := s.store.PutConfig(ctx, newCfg); err != nil {
		return Config{}, "", err
	}
	saved, err := s.store.GetConfig(ctx)
	if err != nil {
		return Config{}, "", err
	}
	return saved, generatedKeyHex, nil
}

// providerFromConfig builds a Provider from a stored Config, decrypting its
// key material under KMS_STORAGE_KEK first.
func (s *Service) providerFromConfig(cfg Config) (Provider, error) {
	material, err := s.decryptMaterial(cfg.EncryptedKeyMaterial)
	if err != nil {
		return nil, fmt.Errorf("kms: decrypt key material: %w", err)
	}
	return s.providerFor(cfg.Kind, cfg.Endpoint, material)
}

// providerFor builds a Provider from raw (unencrypted) parameters. Used both
// for the current stored config and for a not-yet-persisted rotation target.
func (s *Service) providerFor(kind ProviderKind, endpoint, rawMaterial string) (Provider, error) {
	switch kind {
	case KindSymmetric:
		return NewSymmetricProviderFromHex(rawMaterial)
	case KindHTTP:
		return NewHTTPProvider(endpoint, rawMaterial), nil
	default:
		return nil, fmt.Errorf("kms: unknown provider kind %q", kind)
	}
}

// Provider returns the currently-configured owner Provider. Future
// consumers (files, mail, backups, …) that want to envelope-encrypt data
// under the owner's KEK call this. Returns ErrNotConfigured if BYO-KMS has
// not been registered yet.
func (s *Service) Provider(ctx context.Context) (Provider, error) {
	cfg, err := s.store.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	return s.providerFromConfig(cfg)
}

// WrapData generates a DEK, encrypts data, wraps the DEK via the owner's
// provider, and stores the DEKRecord. Returns the ciphertext and the new DEK
// record id. Callers persist the ciphertext themselves; the DEK id is stored
// as metadata so UnwrapData can retrieve the wrapped DEK later.
func (s *Service) WrapData(ctx context.Context, objectRef string, plaintext []byte) (ciphertext string, dekID string, err error) {
	cfg, err := s.store.GetConfig(ctx)
	if err != nil {
		return "", "", err
	}
	p, err := s.providerFromConfig(cfg)
	if err != nil {
		return "", "", err
	}
	ct, wrappedDEK, err := Encrypt(ctx, p, plaintext)
	if err != nil {
		return "", "", err
	}
	id := uuid.New().String()
	if err := s.store.PutDEK(ctx, DEKRecord{
		ID:         id,
		ObjectRef:  objectRef,
		WrappedDEK: wrappedDEK,
		KEKVersion: cfg.KEKVersion,
	}); err != nil {
		return "", "", fmt.Errorf("kms: store DEK: %w", err)
	}
	return ct, id, nil
}

// UnwrapData looks up the DEK record by dekID, unwraps it via the owner's
// provider, and decrypts ciphertext. Returns ErrKeyRevoked if the DEK was
// revoked, ErrUnknownKey if it does not exist.
func (s *Service) UnwrapData(ctx context.Context, dekID, ciphertext string) ([]byte, error) {
	dek, err := s.store.GetDEK(ctx, dekID)
	if err != nil {
		return nil, err // ErrUnknownKey or ErrKeyRevoked
	}
	cfg, err := s.store.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.providerFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	return Decrypt(ctx, p, ciphertext, dek.WrappedDEK)
}

// ListDEKs returns all DEK records (newest first), never including the
// wrapped bytes (DEKRecord.WrappedDEK is json:"-").
func (s *Service) ListDEKs(ctx context.Context, includeRevoked bool) ([]DEKRecord, error) {
	return s.store.ListDEKs(ctx, includeRevoked)
}

// RevokeDEK marks a DEK as revoked (soft-delete, kept for audit).
func (s *Service) RevokeDEK(ctx context.Context, id string) error {
	return s.store.RevokeDEK(ctx, id)
}

// RotateKEK re-wraps all active DEKs under a NEW owner KEK, described by
// (newKind, newEndpoint, newRawKeyMaterial). Requires an existing config
// (ErrNotConfigured otherwise — use Configure first).
//
//  1. Loads the current config + builds the OLD provider.
//  2. Builds the NEW provider from the supplied parameters (not yet persisted).
//  3. Unwraps every non-revoked DEK with the old provider, re-wraps with the
//     new provider, and persists the new wrapped bytes + new KEK version.
//  4. Only once the sweep completes does it persist the new config — so a
//     mid-rotation crash leaves the OLD config (and therefore OLD wrapped
//     DEKs, which are all still valid) in place rather than a mismatched
//     half-state.
//
// The ciphertext (data) is NEVER re-encrypted — only the DEK wrapping
// changes. If newKind is KindSymmetric and newRawKeyMaterial is empty, a
// fresh key is generated and returned once in generatedKeyHex, exactly like
// Configure.
func (s *Service) RotateKEK(ctx context.Context, newKind ProviderKind, newEndpoint, newRawKeyMaterial string) (result RotationResult, generatedKeyHex string, err error) {
	cfg, err := s.store.GetConfig(ctx)
	if err != nil {
		return RotationResult{}, "", err
	}
	oldProvider, err := s.providerFromConfig(cfg)
	if err != nil {
		return RotationResult{}, "", err
	}

	material := newRawKeyMaterial
	if newKind == KindSymmetric && material == "" {
		dek, gerr := GenerateDEK()
		if gerr != nil {
			return RotationResult{}, "", gerr
		}
		defer zeroBytes(dek)
		material = hex.EncodeToString(dek)
		generatedKeyHex = material
	}
	newProvider, err := s.providerFor(newKind, newEndpoint, material)
	if err != nil {
		return RotationResult{}, "", err
	}

	newVersion := cfg.KEKVersion + 1
	result = RotationResult{NewVersion: newVersion}

	deks, err := s.store.ListDEKs(ctx, false)
	if err != nil {
		return result, "", fmt.Errorf("kms: rotation list DEKs: %w", err)
	}
	for _, d := range deks {
		if d.KEKVersion >= newVersion {
			result.Skipped++
			continue
		}
		plainDEK, uerr := oldProvider.UnwrapDEK(ctx, d.WrappedDEK)
		if uerr != nil {
			result.Failed++
			continue
		}
		newWrapped, werr := newProvider.WrapDEK(ctx, plainDEK)
		zeroBytes(plainDEK)
		if werr != nil {
			result.Failed++
			continue
		}
		if uerr := s.store.UpdateWrappedDEK(ctx, d.ID, newWrapped, newVersion); uerr != nil {
			result.Failed++
			continue
		}
		result.Rotated++
	}

	ekm, err := s.encryptMaterial(material)
	if err != nil {
		return result, generatedKeyHex, fmt.Errorf("kms: encrypt new key material: %w", err)
	}
	if err := s.store.PutConfig(ctx, Config{
		Kind: newKind, Endpoint: newEndpoint, EncryptedKeyMaterial: ekm, KEKVersion: newVersion,
	}); err != nil {
		return result, generatedKeyHex, fmt.Errorf("kms: persist rotated config: %w", err)
	}
	return result, generatedKeyHex, nil
}

// encryptMaterial encrypts key/token material under KMS_STORAGE_KEK. Empty
// material (e.g. an HTTP provider with no bearer token) encrypts to "".
func (s *Service) encryptMaterial(material string) (string, error) {
	if material == "" {
		return "", nil
	}
	if len(s.storageKEK) != 32 {
		return material, nil // dev mode only: LoadKEK never returns a short key
	}
	return EncryptKeyMaterial(s.storageKEK, material)
}

// decryptMaterial reverses encryptMaterial.
func (s *Service) decryptMaterial(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	if len(s.storageKEK) != 32 {
		return encoded, nil
	}
	return DecryptKeyMaterial(s.storageKEK, encoded)
}
