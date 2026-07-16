// byo.go — Bring-Your-Own (BYO) object storage: connect / validate / disconnect.
//
// This is the user-facing path that lets a Vulos Cloud customer point their
// per-user unified bucket at THEIR OWN S3-compatible object storage instead of
// our managed object storage bucket. When a BYO config is stored (byo=true) the
// Service.ProviderForAccount resolver (wired in cmd/server) builds a per-account
// provider from the stored credentials, so the customer's data never lives in
// our infrastructure — the core "no lock-in / easy escape" guarantee.
//
// Credentials are encrypted at rest by the SQLStore (AES-GCM under STORAGE_KEK),
// exactly like the managed-secret path; the plaintext secret key is never logged
// or serialised to JSON (Config.SecretKey has json:"-").
//
// NOTE: This file intentionally lives OUTSIDE the frozen contract.go. It only
// adds new package-level helpers and Service methods; it does not change any
// existing type, interface, or sentinel.
package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrBYOValidation is returned when a candidate BYO bucket fails the live
// reachability/credential check (HEAD/list against the user's endpoint).
var ErrBYOValidation = errors.New("storage: BYO bucket validation failed")

// BYOInput carries the user-supplied parameters to connect a BYO bucket.
// SecretKey is plaintext on the wire (TLS) and encrypted at rest by the Store.
type BYOInput struct {
	AccountID string
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
}

// byoProviderFactory builds a Provider from a candidate BYO Config for live
// validation. It is a package var so tests can substitute an in-memory provider
// (no network). Production uses NewBYOProvider.
var byoProviderFactory = NewBYOProvider

// SetBYOProviderFactoryForTest swaps the BYO provider factory and returns a
// restore func. Test-only; callers must defer the returned func.
func SetBYOProviderFactoryForTest(f func(Config) (Provider, error)) func() {
	prev := byoProviderFactory
	byoProviderFactory = f
	return func() { byoProviderFactory = prev }
}

// NewBYOProvider constructs a Provider for an account's BYO bucket from its
// stored Config. BYO buckets are S3-compatible, so we reuse the same S3 driver
// that backs managed object storage (S3Provider) pointed at the user's endpoint.
func NewBYOProvider(cfg Config) (Provider, error) {
	region := cfg.Region
	if region == "" {
		region = "auto"
	}
	return NewS3Provider(S3Config{
		AccessKeyID:     cfg.AccessKey,
		SecretAccessKey: cfg.SecretKey,
		Endpoint:        cfg.Endpoint,
		Region:          region,
	})
}

// normalizeBYO validates the shape of a BYOInput and returns a Config with
// defaults applied. It does NOT touch the network.
func normalizeBYO(in BYOInput) (Config, error) {
	endpoint := strings.TrimSpace(in.Endpoint)
	bucket := strings.TrimSpace(in.Bucket)
	region := strings.TrimSpace(in.Region)
	access := strings.TrimSpace(in.AccessKey)
	secret := in.SecretKey // never trim a secret

	if in.AccountID == "" {
		return Config{}, fmt.Errorf("%w: account_id is required", ErrBYOValidation)
	}
	if endpoint == "" {
		return Config{}, fmt.Errorf("%w: endpoint is required", ErrBYOValidation)
	}
	if !strings.HasPrefix(endpoint, "https://") {
		return Config{}, fmt.Errorf("%w: endpoint must use https scheme", ErrBYOValidation)
	}
	if bucket == "" {
		return Config{}, fmt.Errorf("%w: bucket is required", ErrBYOValidation)
	}
	if access == "" || secret == "" {
		return Config{}, fmt.Errorf("%w: access_key and secret_key are required", ErrBYOValidation)
	}
	if region == "" {
		region = "auto"
	}
	return Config{
		AccountID: in.AccountID,
		BYO:       true,
		Endpoint:  endpoint,
		Region:    region,
		Bucket:    bucket,
		AccessKey: access,
		SecretKey: secret,
	}, nil
}

// ValidateBYO performs a live reachability + credential check against the
// candidate bucket by listing a single object. A non-existent bucket or bad
// credentials surface as ErrBYOValidation. An empty bucket is valid.
func ValidateBYO(ctx context.Context, cfg Config) error {
	p, err := byoProviderFactory(cfg)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBYOValidation, err)
	}
	if _, err := p.ListBucket(ctx, cfg.Bucket, "", 1); err != nil {
		return fmt.Errorf("%w: %v", ErrBYOValidation, err)
	}
	return nil
}

// ConnectBYO validates a candidate BYO bucket live, then persists it with
// byo=true (secret encrypted at rest). On success the per-account provider
// resolver will route all of this account's storage to the user's bucket.
// Returns the stored Config (with SecretKey blanked).
func (s *Service) ConnectBYO(ctx context.Context, in BYOInput) (Config, error) {
	cfg, err := normalizeBYO(in)
	if err != nil {
		return Config{}, err
	}
	if err := ValidateBYO(ctx, cfg); err != nil {
		return Config{}, err
	}
	if err := s.Store.PutConfig(ctx, cfg); err != nil {
		return Config{}, fmt.Errorf("storage: ConnectBYO put config: %w", err)
	}
	cfg.SecretKey = ""
	return cfg, nil
}

// DisconnectBYO removes the account's BYO config. Storage then reverts to the
// managed bucket, which is (re-)provisioned lazily on the next request via
// ProvisionBucketIfAbsent. Idempotent.
func (s *Service) DisconnectBYO(ctx context.Context, accountID string) error {
	if accountID == "" {
		return ErrUnknownAccount
	}
	return s.Store.DeleteConfig(ctx, accountID)
}
