package billingport

import "context"

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

func (NoopResolver) EffectiveTierFor(context.Context, string) (string, error) {
	return TierSelfHost, nil
}

// MaxActiveUsersForTier returns 0 (no cap) for every tier — self-host never
// limits active users.
func (NoopResolver) MaxActiveUsersForTier(string) int { return 0 }

// CheckStorageQuota always allows: self-host / BYO storage is never capped.
func (NoopResolver) CheckStorageQuota(context.Context, string, int64, string) error {
	return nil
}

// Interface compliance.
var (
	_ BillingProvider     = (*NoopProvider)(nil)
	_ EntitlementResolver = (*NoopResolver)(nil)
)
