// placer.go — residency-backed region placement (COMPLY-01 enforcement).
//
// Placer turns the per-org residency policy into a region placer:
// PlaceFor(account, kind) resolves the account → org → residency region, so
// managed-instance and bucket provisioning land in (and are constrained to) the
// org's chosen data-residency region instead of a client-supplied one.
//
// It exposes a PlaceFor(ctx, tenant, kind string) (string, error) placement
// method — the stable seam a future multi-region placer interface would adopt.
// (The Phase-0 internal/multiregion seam package was removed as a 0-import stub;
// callers depend on this concrete placer directly.)
package residency

import "context"

// OrgResolver maps an account ID to the org whose residency policy governs it.
// In the current single-cell deployment org_id == account_id, so the default
// (identity) resolver is used; multi-org deployments supply a real lookup.
type OrgResolver func(ctx context.Context, accountID string) (orgID string, err error)

// Placer resolves the residency region for a tenant's resources.
type Placer struct {
	store       Store
	orgResolver OrgResolver
	// defaultRegion is returned when the org has no residency row yet. "auto"
	// means "no residency constraint" (the managed store automatic placement); a client may
	// then request any region.
	defaultRegion string
}

// NewPlacer returns a Placer over store. orgResolver may be nil (identity:
// org == account). The no-policy default region is "auto".
func NewPlacer(store Store, orgResolver OrgResolver) *Placer {
	return &Placer{store: store, orgResolver: orgResolver, defaultRegion: "auto"}
}

// PlaceFor returns the residency region governing tenant's resources of the given
// kind. It resolves account → org → region; when the org has no residency policy
// it returns the no-policy default ("auto"). kind is currently advisory (all
// resource kinds follow the single org residency region).
func (p *Placer) PlaceFor(ctx context.Context, tenant, _ string) (string, error) {
	org := tenant
	if p.orgResolver != nil {
		o, err := p.orgResolver(ctx, tenant)
		if err != nil {
			return "", err
		}
		if o != "" {
			org = o
		}
	}
	r, err := p.store.GetRegion(ctx, org)
	if err == ErrUnknownOrg {
		def := p.defaultRegion
		if def == "" {
			def = "auto"
		}
		return def, nil
	}
	if err != nil {
		return "", err
	}
	if r.Region == "" {
		return "auto", nil
	}
	return r.Region, nil
}
