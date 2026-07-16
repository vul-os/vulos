// adapters.go — bridges the resolver interfaces to the real cp packages.
//
// These thin wrappers let the resolver read existing storage data
// without importing the full packages (avoids circular dependencies and keeps
// the resolver testable with simple stubs).
package resolver

import (
	"context"
	"errors"
)

// ---------------------------------------------------------------------------
// StorageStoreAdapter
// ---------------------------------------------------------------------------

// StorageStore is the narrow slice of storage.Store that the adapter reads.
// storage.Store in github.com/vul-os/vulos-management/pkg/storage satisfies this.
type StorageStore interface {
	GetConfig(ctx context.Context, accountID string) (StorageConfig, error)
}

// StorageConfig is the minimal config the adapter needs.
type StorageConfig struct {
	BYO bool
}

// StorageStoreAdapter adapts a StorageStore to the resolver.StorageSource
// interface.
type StorageStoreAdapter struct {
	Store StorageStore
}

// IsSelfHost returns true when the account's storage config has BYO=true.
// Returns errStorageUnknown wrapped when the account is not found.
func (a *StorageStoreAdapter) IsSelfHost(ctx context.Context, accountID string) (bool, error) {
	cfg, err := a.Store.GetConfig(ctx, accountID)
	if err != nil {
		// Map "unknown account" sentinel from the storage package to our sentinel.
		if isUnknownAccount(err) {
			return false, errStorageUnknown
		}
		return false, err
	}
	return cfg.BYO, nil
}

// isUnknownAccount heuristically detects the storage.ErrUnknownAccount error
// without importing the storage package (avoids the circular-import risk and
// keeps the resolver package self-contained for tests).
func isUnknownAccount(err error) bool {
	if err == nil {
		return false
	}
	// Use errors.Is to match wrapped sentinels where possible.
	// The storage package sentinel message is "storage: unknown account".
	return errors.Is(err, errStorageUnknown) ||
		(err != nil && err.Error() == "storage: unknown account")
}

// ---------------------------------------------------------------------------
// BYOMailRegistryAdapter
// ---------------------------------------------------------------------------

// BYOMailRegistry is the narrow interface a BYO-enrollment registry must satisfy
// to be used as an EnrollmentSource adapter.
type BYOMailRegistry interface {
	GetEndpoint(ctx context.Context, accountID string) (BYOEndpointInfo, error)
}

// BYOEndpointInfo carries the fields the resolver needs from a registered BYO
// endpoint.
type BYOEndpointInfo struct {
	// EndpointURL is the registered HTTPS endpoint of the self-hosted box.
	EndpointURL string
}

// BYOMailRegistryAdapter adapts a BYOMailRegistry to the
// resolver.EnrollmentSource interface.
type BYOMailRegistryAdapter struct {
	Registry BYOMailRegistry
}

// FabricRoute derives the fabric route from the registered BYO endpoint URL.
// The fabric address is the endpoint URL itself — the relay/peering layer
// handles NAT traversal and the cloud proxy forwards bytes opaquely.
// Returns ErrNoEnrollment when no endpoint is registered or the entry is
// absent.
func (a *BYOMailRegistryAdapter) FabricRoute(ctx context.Context, accountID string) (string, error) {
	ep, err := a.Registry.GetEndpoint(ctx, accountID)
	if err != nil {
		// Map "not found" sentinels to ErrNoEnrollment.
		if isNotFound(err) {
			return "", ErrNoEnrollment
		}
		return "", err
	}
	if ep.EndpointURL == "" {
		return "", ErrNoEnrollment
	}
	return ep.EndpointURL, nil
}

// BoxID returns the registered box identifier for the account.  The registry
// stores one box per account, so the accountID itself is the
// stable box-id used in the LAN hostname `box.<box-id>.lan.vulos.org`.
// Returns ErrNoEnrollment when the account has no registered box.
func (a *BYOMailRegistryAdapter) BoxID(ctx context.Context, accountID string) (string, error) {
	_, err := a.Registry.GetEndpoint(ctx, accountID)
	if err != nil {
		if isNotFound(err) {
			return "", ErrNoEnrollment
		}
		return "", err
	}
	return accountID, nil
}

// isNotFound heuristically detects "not found" errors from enrollment registry
// implementations without importing them directly.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return msg == "resolver: no self-host enrollment"
}
