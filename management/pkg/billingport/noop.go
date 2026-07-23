package billingport

import (
	"context"
	"time"
)

// NoopProvider is the default BillingProvider for a self-hosted control plane.
// It never contacts a payment network and never pretends a charge succeeded:
// every operation reports ErrUnsupported (or, for verification, a deterministic
// non-success state). A self-hoster has no billing, so nothing should ever call
// these paths; wiring the no-op keeps the composition root total and honest.
type NoopProvider struct{}

// NewNoopProvider returns the network-free default payment rail.
func NewNoopProvider() *NoopProvider { return &NoopProvider{} }

func (NoopProvider) Name() string { return "noop" }

func (NoopProvider) InitTransaction(context.Context, InitRequest) (*InitResponse, error) {
	return nil, ErrUnsupported
}

func (NoopProvider) VerifyTransaction(_ context.Context, reference string) (*VerifyResponse, error) {
	// Report a deterministic non-success so a caller never treats an
	// unconfigured rail as "paid".
	return &VerifyResponse{Reference: reference, Status: "failed"}, nil
}

func (NoopProvider) ChargeAuthorization(context.Context, ChargeAuthRequest) (*ChargeAuthResult, error) {
	return nil, ErrUnsupported
}

func (NoopProvider) RefundTransaction(context.Context, RefundRequest) (*RefundResult, error) {
	return nil, ErrUnsupported
}

func (NoopProvider) VerifyWebhookSignature(string, []byte, string) error {
	return ErrUnsupported
}

// NoopResolver is the default EntitlementResolver for a self-hosted control
// plane. Self-hosting is never tier-limited: every account gets the unlimited
// TierSelfHost entitlement, there is no seat cap, and managed storage is never
// capped. It records nothing and phones nowhere.
type NoopResolver struct{}

// NewNoopResolver returns the unlimited self-host entitlement resolver.
func NewNoopResolver() *NoopResolver { return &NoopResolver{} }

func (NoopResolver) TierFor(context.Context, string) (string, error) {
	return TierSelfHost, nil
}

func (NoopResolver) EffectiveTierFor(context.Context, string) (string, error) {
	return TierSelfHost, nil
}

// Tiers reports the single unlimited, $0 self-host plan — self-host has no
// priced tiers.
func (NoopResolver) Tiers(float64) []Tier {
	return []Tier{{ID: TierSelfHost, Name: "Self-host", MaxActiveUsers: 0}}
}

// QuotaFor grants an uncapped, always-allowed quota for every kind — self-host
// enforces no allowance.
func (NoopResolver) QuotaFor(_ context.Context, _, kind string) (Quota, error) {
	return unlimitedQuota(kind), nil
}

// QuotaWithUsage ignores usage and grants the same uncapped quota — self-host
// never exceeds a cap that does not exist.
func (NoopResolver) QuotaWithUsage(_ context.Context, _, kind string, _ RelayUsageSource) (Quota, error) {
	return unlimitedQuota(kind), nil
}

// QuotaForTier grants an uncapped, always-allowed quota for every (tier, kind).
func (NoopResolver) QuotaForTier(tier, kind string) Quota {
	q := unlimitedQuota(kind)
	q.Tier = tier
	return q
}

// unlimitedQuota is the self-host "everything allowed, nothing capped" quota.
func unlimitedQuota(kind string) Quota {
	return Quota{Tier: TierSelfHost, Kind: kind, Allowed: true}
}

// MaxActiveUsersForTier returns 0 (no cap) for every tier — self-host never
// limits active users.
func (NoopResolver) MaxActiveUsersForTier(string) int { return 0 }

// CheckStorageQuota always allows: self-host / BYO storage is never capped.
func (NoopResolver) CheckStorageQuota(context.Context, string, int64, string) error {
	return nil
}

// RelayAllowanceGB reports the region-default allowance for every region —
// self-host is never relay-capped, but callers use this as a display number.
func (NoopResolver) RelayAllowanceGB(context.Context, string) (int, error) {
	return noopFreeRelayGB, nil
}

// FreeRelayGBWithBox reports the region-default relay allowance.
func (NoopResolver) FreeRelayGBWithBox() int { return noopFreeRelayGB }

// DefaultRelayRegionID reports the single self-host region.
func (NoopResolver) DefaultRelayRegionID() string { return noopSelfHostRegionID }

// RegionByID reports the single self-host region for the default id and
// ErrRegionNotFound for any other — self-host sells no priced regions.
func (NoopResolver) RegionByID(_ context.Context, id string) (Region, error) {
	if id == "" || id == noopSelfHostRegionID {
		return noopSelfHostRegion(), nil
	}
	return Region{}, ErrRegionNotFound
}

// AccountRelayRegion always returns the single self-host region.
func (NoopResolver) AccountRelayRegion(context.Context, string) string {
	return noopSelfHostRegionID
}

// EmitSelfHostRelayChargeRegion charges nothing — self-host relay is $0.
func (NoopResolver) EmitSelfHostRelayChargeRegion(context.Context, string, string, int64, string, time.Time) error {
	return nil
}

// ResolveHostingKind reports HostingKindSelfHost — the no-op control plane is a
// self-hoster by definition; a self-host signal maps to self-host, anything else
// to cloud (which is still uncapped/uncharged here).
func (NoopResolver) ResolveHostingKind(selfHost bool) HostingKind {
	if selfHost {
		return HostingKindSelfHost
	}
	return HostingKindCloud
}

// MeteredBillingEnabled is always false — self-host has no card-gated wallet to
// opt into.
func (NoopResolver) MeteredBillingEnabled(context.Context, string) (bool, error) {
	return false, nil
}

// CountedThisMonthByBucket reports zero usage — the no-op resolver meters nothing.
func (NoopResolver) CountedThisMonthByBucket(context.Context, string, string, time.Time) (int64, error) {
	return 0, nil
}

// OverageCostThisMonthMicros reports zero cost — self-host is never billed.
func (NoopResolver) OverageCostThisMonthMicros(context.Context, string, string, time.Time) (int64, error) {
	return 0, nil
}

// Self-host defaults for the relay/region reads. The region is a display
// placeholder: self-host is structurally uncapped, so these numbers only ever
// surface in a usage VIEW, never gate anything.
const (
	noopFreeRelayGB      = 25
	noopSelfHostRegionID = "selfhost"
)

func noopSelfHostRegion() Region {
	return Region{ID: noopSelfHostRegionID, Name: "Self-host", FreeRelayGBWithBox: noopFreeRelayGB, ComputeMultBps: 10000, Active: true}
}

// IsNoopResolver reports whether r is the free self-host no-op entitlement
// resolver (unlimited tier, no quota/storage caps, no metering). A cloud
// composition MUST inject a real resolver; if this returns true in prod, every
// quota/storage cap is disabled and metering is off — a fail-open the caller
// should surface loudly (see cpserver's prod guard).
func IsNoopResolver(r EntitlementResolver) bool {
	_, ok := r.(*NoopResolver)
	return ok
}

// IsNoopProvider reports whether p is the free self-host no-op payment rail
// (never charges). Cloud injects a real rail; noop in prod means no charging.
func IsNoopProvider(p BillingProvider) bool {
	_, ok := p.(*NoopProvider)
	return ok
}

// ResolverRail names the wired entitlement resolver for observability
// (/version, /healthz): "noop" for the free self-host resolver, "custom" for an
// injected (commercial) one. Mirrors BillingProvider.Name() so a composition
// regression that drops the injection is visible on the operational endpoints.
func ResolverRail(r EntitlementResolver) string {
	if IsNoopResolver(r) {
		return "noop"
	}
	return "custom"
}

// Interface compliance.
var (
	_ BillingProvider     = (*NoopProvider)(nil)
	_ EntitlementResolver = (*NoopResolver)(nil)
)
