// ownership.go — OwnershipResolver seam for fleet authorisation.
//
// OwnershipResolver lets fleet handlers verify that a ULID is owned by the
// expected account without importing the routing package directly. The
// production implementation wraps routing.Store; tests use a no-op stub.
package fleet

import (
	"context"

	"github.com/vul-os/vulos-management/pkg/routing"
)

// OwnershipResolver resolves the account that owns a given device ULID.
// Used by fleet authorisation to cross-check device ownership via the
// routing store without creating a hard circular import.
type OwnershipResolver interface {
	// OwnerOf returns the accountID that owns ulid, or an error if the
	// ULID is not enrolled.
	OwnerOf(ctx context.Context, ulid string) (accountID string, err error)
}

// RoutingOwnershipResolver implements OwnershipResolver using routing.Store.
type RoutingOwnershipResolver struct {
	Store routing.Store
}

// OwnerOf looks up the routing binding for ulid and returns the owning accountID.
func (r *RoutingOwnershipResolver) OwnerOf(ctx context.Context, ulid string) (string, error) {
	b, err := r.Store.GetBinding(ctx, ulid)
	if err != nil {
		return "", err
	}
	return b.AccountID, nil
}

// compile-time assertion: RoutingOwnershipResolver satisfies OwnershipResolver.
var _ OwnershipResolver = (*RoutingOwnershipResolver)(nil)
