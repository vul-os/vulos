// provision.go — per-tenant the S3 backend bucket provisioning.
//
// ProvisionManagedBucket is called at account signup (or lazily on first
// storage request) to:
//  1. Derive the canonical bucket name: "vulos-{ulid_lower}".
//  2. Call Provider.EnsureBucket (idempotent).
//  3. Upsert a storage_configs row with BYO=false and the bucket name.
//
// ProvisionBucketIfAbsent is the lazy variant: it checks whether a config row
// already exists and returns early if the bucket is already set.  This is safe
// to call on every request.
//
// COMPLY-01: both functions accept an optional region override via
// ProvisionOptions.  When Region is non-empty the bucket is PLACED there, and
// the stored row records the region that was actually enforced.  Previously the
// region was written to storage_configs but never passed to the provider — every
// bucket was created in the global the configured region ("auto") while the row claimed
// a residency the data did not have.  A region we cannot enforce is now an
// error, not a record.
package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vul-os/vulos-management/pkg/region"
	"github.com/vul-os/vulos-management/pkg/storageport"
)

// RegionalBucketProvider is the optional capability of creating a bucket in a
// SPECIFIC region.  It is separate from Provider so that a provider which cannot
// place regionally is unable to silently accept a residency requirement: callers
// type-assert, and fail closed when the assertion does not hold.
type RegionalBucketProvider interface {
	EnsureBucketInRegion(ctx context.Context, name, s3Region string) error
}

// ProvisionerFromProvider adapts a data-plane Provider (S3Provider, MemProvider)
// into a storageport.StorageProvisioner. Only a commercial composition root
// (vulos-cloud) uses it: it turns the managed Tigris provider into the bucket-
// creating seam management injects. Management itself never constructs one — its
// default provisioner is storageport.NoopProvisioner.
//
// EnsureBucketInRegion fails closed when the wrapped provider cannot place buckets
// regionally (it does not implement RegionalBucketProvider): recording an
// unenforced residency is worse than refusing to provision.
func ProvisionerFromProvider(p Provider) storageport.StorageProvisioner {
	return providerProvisioner{p: p}
}

type providerProvisioner struct{ p Provider }

func (a providerProvisioner) EnsureBucket(ctx context.Context, name string) error {
	return a.p.EnsureBucket(ctx, name)
}

func (a providerProvisioner) EnsureBucketInRegion(ctx context.Context, name, s3Region string) error {
	regional, ok := a.p.(RegionalBucketProvider)
	if !ok {
		return fmt.Errorf("%w: residency region %q requested but the storage provider cannot place buckets by region", ErrProviderFailed, s3Region)
	}
	return regional.EnsureBucketInRegion(ctx, name, s3Region)
}

func (providerProvisioner) Enabled() bool { return true }

var _ storageport.StorageProvisioner = providerProvisioner{}

// ensureBucketPlaced creates the bucket THROUGH the storageport seam and returns
// the canonical region it was actually placed in — "auto" when no residency
// requirement was given.
//
// When the injected provisioner is not enabled (the management/bring-your-own
// default, NoopProvisioner), NO bucket is created: the operator is expected to
// have created the bucket already, and we return a clear "create it first" error
// rather than silently attempting a create the control plane must never perform.
//
// A requested region must be (a) in the canonical region table and (b) placeable
// by the provisioner. Otherwise we refuse: recording an unenforced residency is
// worse than failing to provision, because the row is later read as a guarantee.
func ensureBucketPlaced(ctx context.Context, prov storageport.StorageProvisioner, bucket, want string) (string, error) {
	if !prov.Enabled() {
		// Bring-your-own-bucket: the control plane never creates a bucket. Bucket
		// creation on a managed backend is a commercial concern wired only in the
		// cloud composition root.
		return "", fmt.Errorf("%w: managed bucket %q not found — bring your own bucket: create it first (this control plane does not provision buckets)", storageport.ErrProvisioningDisabled, bucket)
	}
	if want == "" || want == "auto" {
		if err := prov.EnsureBucket(ctx, bucket); err != nil {
			return "", fmt.Errorf("EnsureBucket: %w", err)
		}
		return "auto", nil
	}

	r, err := region.Lookup(want)
	if err != nil {
		return "", err
	}
	if err := prov.EnsureBucketInRegion(ctx, bucket, r.S3); err != nil {
		return "", fmt.Errorf("EnsureBucketInRegion(%s): %w", r.S3, err)
	}
	return r.Key, nil
}

// ProvisionOptions carries optional overrides for bucket provisioning.
// The zero value is valid and applies defaults ("auto" region).
type ProvisionOptions struct {
	// Region overrides the bucket region. When empty, "auto" is used.
	// Should come from the org's residency.Store setting (COMPLY-01).
	Region string
}

// ProvisionManagedBucket ensures a managed object storage bucket exists for the given
// account and ULID.  It always calls EnsureBucket (idempotent at the S3 backend level)
// and always upserts the config row.  Use this at signup.
//
// bucket name convention: "vulos-{ulid_lower}".
// opts may be zero — pass ProvisionOptions{Region: orgRegion} from the
// residency store to honour COMPLY-01 per-org data residency.
func (svc *Service) ProvisionManagedBucket(ctx context.Context, accountID, ulid string, opts ProvisionOptions) (Config, error) {
	bucket := managedBucketName(ulid)
	placed, err := ensureBucketPlaced(ctx, svc.provisioner(), bucket, opts.Region)
	if err != nil {
		return Config{}, fmt.Errorf("storage: ProvisionManagedBucket: %w", err)
	}

	cfg := Config{
		AccountID: accountID,
		BYO:       false,
		Region:    placed,
		Bucket:    bucket,
	}
	if err := svc.Store.PutConfig(ctx, cfg); err != nil {
		return Config{}, fmt.Errorf("storage: ProvisionManagedBucket PutConfig: %w", err)
	}
	return cfg, nil
}

// backupBucketName returns the per-customer backup bucket name for a ULID,
// parallel to managedBucketName ("vulos-backup-{ulid_lower}"). NEVER shared across
// customers (STORAGE-ISOLATION.md §2.1 G1).
func backupBucketName(ulid string) string {
	return "vulos-backup-" + strings.ToLower(ulid)
}

// ProvisionBackupBucket ensures the per-customer backup bucket exists (§2, P4).
// It only calls EnsureBucket (idempotent) — it does NOT write a storage_configs
// row, because the backup bucket is not the account's primary config; it is a
// sibling single-tenant bucket the cell's scoped credential also covers (§2.3).
// Additive: called only when backups are enabled.
func (svc *Service) ProvisionBackupBucket(ctx context.Context, _, ulid string, _ ProvisionOptions) error {
	prov := svc.provisioner()
	if !prov.Enabled() {
		return fmt.Errorf("%w: backup bucket %q not found — bring your own bucket: create it first (this control plane does not provision buckets)", storageport.ErrProvisioningDisabled, backupBucketName(ulid))
	}
	if err := prov.EnsureBucket(ctx, backupBucketName(ulid)); err != nil {
		return fmt.Errorf("storage: ProvisionBackupBucket EnsureBucket: %w", err)
	}
	return nil
}

// ProvisionBucketIfAbsent is a lazy provisioner: if a config row already exists
// with a non-empty bucket, it returns immediately.  Otherwise it delegates to
// ProvisionManagedBucket.  Safe to call on every incoming storage request.
// opts is forwarded to ProvisionManagedBucket (see COMPLY-01 region wiring).
func (svc *Service) ProvisionBucketIfAbsent(ctx context.Context, accountID, ulid string, opts ProvisionOptions) (Config, error) {
	existing, err := svc.Store.GetConfig(ctx, accountID)
	if err == nil && existing.Bucket != "" {
		return existing, nil
	}
	if err != nil && !errors.Is(err, ErrUnknownAccount) {
		return Config{}, fmt.Errorf("storage: ProvisionBucketIfAbsent get: %w", err)
	}
	return svc.ProvisionManagedBucket(ctx, accountID, ulid, opts)
}
