// Package storageport is the provider-agnostic seam for object-storage bucket
// PROVISIONING — the one storage capability the operational control plane must
// never perform on its own.
//
// Serving objects (presign, put, get, list) is a first-class control-plane
// concern and lives in pkg/storage, which a self-hoster points at any
// S3-compatible endpoint (MinIO, Ceph, a cloud object store, …) they already
// run. But *creating* buckets on a managed multi-tenant backend is a commercial
// concern: it is where a distributor's account-to-bucket mapping, IAM scoping,
// and cost accounting live.
//
// So the split is:
//
//   - Self-host (default): NoopProvisioner. Bring-your-own-bucket. The control
//     plane assumes the operator has already created the bucket(s) it serves and
//     never issues a create/EnsureBucket call. Nothing is provisioned, nothing
//     phones home.
//   - Commercial distributor: injects a StorageProvisioner that creates and
//     scopes buckets on its managed backend, wired via the cpserver Deps struct.
//
// This package imports no storage implementation and names no vendor.
package storageport

import (
	"context"
	"errors"
)

// ErrProvisioningDisabled is returned by NoopProvisioner to signal that bucket
// provisioning is not available in this deployment (bring-your-own-bucket). The
// control plane treats it as "assume the bucket already exists", not a failure.
var ErrProvisioningDisabled = errors.New("storageport: bucket provisioning disabled (bring your own bucket)")

// StorageProvisioner creates object-storage buckets on a managed backend. Only a
// commercial distributor injects a real implementation; the self-host default is
// NoopProvisioner.
type StorageProvisioner interface {
	// EnsureBucket ensures a bucket with the given name exists (idempotent).
	EnsureBucket(ctx context.Context, name string) error

	// EnsureBucketInRegion ensures a bucket exists in a specific storage region.
	// A distributor with a single-region backend may ignore region.
	EnsureBucketInRegion(ctx context.Context, name, region string) error

	// Enabled reports whether this deployment provisions buckets at all. The
	// composition root uses it to skip provisioning entirely for bring-your-own
	// deployments rather than swallowing per-call errors.
	Enabled() bool
}

// NoopProvisioner is the bring-your-own-bucket default: it provisions nothing.
// EnsureBucket / EnsureBucketInRegion return ErrProvisioningDisabled so a caller
// can distinguish "not provisioned here" from a real backend failure, and
// Enabled() reports false so the composition root can skip provisioning cleanly.
type NoopProvisioner struct{}

// NewNoopProvisioner returns the bring-your-own-bucket default provisioner.
func NewNoopProvisioner() *NoopProvisioner { return &NoopProvisioner{} }

func (NoopProvisioner) EnsureBucket(context.Context, string) error {
	return ErrProvisioningDisabled
}

func (NoopProvisioner) EnsureBucketInRegion(context.Context, string, string) error {
	return ErrProvisioningDisabled
}

func (NoopProvisioner) Enabled() bool { return false }

var _ StorageProvisioner = (*NoopProvisioner)(nil)
