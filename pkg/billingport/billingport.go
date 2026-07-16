// Package billingport is the provider-agnostic seam between the operational
// control plane (this module) and any commercial billing layer that a
// distributor may wrap around it.
//
// The control plane NEVER charges money, resolves a price, or phones home. It
// only asks two questions and reports one fact:
//
//   - "what is this account entitled to?"      → EntitlementResolver
//   - "start / verify / charge a payment"      → BillingProvider (a payment rail)
//   - "here is some metered usage"             → the resolver reads it back via
//     RelayUsageSource when computing entitlements
//
// A self-hosted deployment wires the no-op defaults in this package: every
// account gets an unlimited "self-host" entitlement, storage is never capped,
// and the payment rail refuses to pretend a charge happened. A commercial
// distributor injects its own implementations (a real payment rail and a
// tier/quota resolver backed by its pricing engine) via the cpserver Deps
// struct — WITHOUT the control plane importing any billing code.
//
// This package must stay dependency-free: no imports of any billing, pricing,
// or payment implementation. The boundary test in pkg/billingport enforces it.
package billingport

import (
	"context"
	"errors"
	"time"
)

// ---------------------------------------------------------------------------
// Face 1 — payment rail (BillingProvider)
//
// A rail-agnostic surface over whatever payment processor a distributor uses.
// Amounts are in minor currency units (e.g. cents) and carry an explicit
// currency code so the interface is not tied to any one region or processor.
// ---------------------------------------------------------------------------

// InitRequest starts a hosted-checkout / authorisation flow with the rail.
type InitRequest struct {
	Email           string            // customer-visible email (receipt + auth)
	AmountMinor     int64             // amount in minor currency units (e.g. cents), > 0
	Currency        string            // ISO-4217 code, e.g. "ZAR", "USD"
	Reference       string            // caller-unique idempotency key
	CallbackURL     string            // optional post-checkout redirect
	Metadata        map[string]string // free-form; round-tripped to the webhook
}

// InitResponse is the rail-agnostic result of a successful InitTransaction.
type InitResponse struct {
	AuthorizationURL string // where to redirect the customer to complete payment
	Reference        string // echoes InitRequest.Reference
	AccessCode       string // rail-specific opaque resume handle; may be empty
}

// ChargeAuthRequest charges a previously-captured, reusable authorization with
// no customer interaction (recurring renewal / overage collection).
type ChargeAuthRequest struct {
	AuthorizationCode string // rail-specific reusable token (never a PAN)
	Email             string
	AmountMinor       int64
	Currency          string
	Reference         string
	Metadata          map[string]string
}

// ChargeAuthResult is the rail-agnostic result of ChargeAuthorization. Status
// is one of "success" / "failed" / "pending" / "abandoned".
type ChargeAuthResult struct {
	Reference      string
	Status         string
	AmountMinor    int64
	Currency       string
	PaidAt         string // RFC3339; empty for non-success states
	GatewayMessage string // human-readable decline reason, safe to log
}

// RefundRequest asks the rail to refund a previously-charged transaction.
// AmountMinor of 0 refunds the full transaction amount.
type RefundRequest struct {
	Transaction string
	AmountMinor int64
}

// RefundResult is the rail-agnostic result of RefundTransaction.
type RefundResult struct {
	Status      string // "processed" | "pending" | "failed"
	AmountMinor int64
	Currency    string
}

// VerifyResponse is the rail-agnostic result of VerifyTransaction. Status is one
// of "success" / "failed" / "pending" / "abandoned".
type VerifyResponse struct {
	Reference     string
	Status        string
	AmountMinor   int64
	Currency      string
	PaidAt        string // RFC3339; empty for non-success states
	CustomerEmail string
}

// ErrUnsupported is returned by a rail when the operation is not available on
// the underlying processor (e.g. webhook verify on the no-op rail).
var ErrUnsupported = errors.New("billingport: operation not supported by this provider")

// ErrBadSignature is returned by VerifyWebhookSignature when the signature does
// not match the body under the configured secret.
var ErrBadSignature = errors.New("billingport: webhook signature mismatch")

// BillingProvider is the narrow, provider-agnostic payment-rail surface. A
// distributor injects a real implementation; the default is NoopProvider, which
// never charges anything.
type BillingProvider interface {
	// Name returns a short diagnostic identifier, e.g. "noop" or "paystack".
	// Never branch on it in business logic.
	Name() string

	// InitTransaction starts a hosted-checkout / authorisation flow.
	InitTransaction(ctx context.Context, req InitRequest) (*InitResponse, error)

	// VerifyTransaction confirms the final state of a transaction by reference.
	VerifyTransaction(ctx context.Context, reference string) (*VerifyResponse, error)

	// ChargeAuthorization charges a saved, reusable authorization with no
	// customer interaction. A clean decline is reported via
	// ChargeAuthResult.Status; a transport/API failure via the returned error.
	ChargeAuthorization(ctx context.Context, req ChargeAuthRequest) (*ChargeAuthResult, error)

	// RefundTransaction refunds a previously-charged transaction (whole or partial).
	RefundTransaction(ctx context.Context, req RefundRequest) (*RefundResult, error)

	// VerifyWebhookSignature checks an inbound webhook signature against the raw
	// body. Returns nil on a valid signature, ErrBadSignature on a mismatch, or
	// ErrUnsupported when the rail does not sign webhooks.
	VerifyWebhookSignature(secretKey string, rawBody []byte, signatureHeader string) error
}

// ---------------------------------------------------------------------------
// Face 2 — entitlement / tier resolver
//
// The control plane gates optional capacity (relay/TURN allowances, included
// seats, managed storage quota) by an account's entitlement. Self-host grants
// everything; a commercial distributor resolves real tiers, seat caps, and
// storage quotas from its pricing engine.
// ---------------------------------------------------------------------------

// TierSelfHost is the entitlement the no-op resolver grants every account:
// self-hosting is never tier-limited.
const TierSelfHost = "selfhost"

// ErrQuotaExceeded is returned by EntitlementResolver.CheckStorageQuota when a
// managed-storage write would exceed the account's quota. The no-op resolver
// never returns it.
var ErrQuotaExceeded = errors.New("billingport: storage quota exceeded")

// Hosting kinds passed to CheckStorageQuota. Self-hosted / bring-your-own
// storage is never capped or billed. These stay UNTYPED string constants so
// existing callers can pass them where a plain `string` is expected (e.g.
// CheckStorageQuota); the typed HostingKind mirror below is what
// ResolveHostingKind returns.
const (
	HostingCloud    = "cloud"
	HostingBox      = "box"
	HostingSelfHost = "selfhost"
)

// HostingKind is the provider-neutral mirror of where a billed compute/storage/
// relay activity physically ran, independent of which subsystem emitted the
// usage record. It mirrors the commercial billing layer's HostingKind without
// the control plane importing it.
//
//   - HostingKindCloud    — vulos-hosted multi-tenant cloud.
//   - HostingKindBox      — a customer-provisioned dedicated box.
//   - HostingKindSelfHost — self-host / BYO-storage / peer-direct: STRUCTURALLY
//     $0 (never capped, never charged unless a shared relay is actually used).
type HostingKind string

const (
	HostingKindCloud    HostingKind = HostingCloud
	HostingKindBox      HostingKind = HostingBox
	HostingKindSelfHost HostingKind = HostingSelfHost
)

// ---------------------------------------------------------------------------
// Provider-neutral value types
//
// These mirror the shapes the commercial billing layer returns, so the control
// plane's operational routes can read tiers, quotas, and regions WITHOUT
// importing any billing implementation. A distributor's adapter translates its
// concrete types into these; the no-op resolver returns unlimited self-host
// values.
// ---------------------------------------------------------------------------

// Quota is the neutral view of an account's authoritative allowance for a given
// resource kind ("relay", "turn", "storage", "mail", "managed",
// "public_circuit", "mail_custom_domain"). It mirrors the commercial billing
// Quota. When resolved with a live usage source (QuotaWithUsage), the Used*/
// Exceeded fields report current-cycle consumption and over-cap state.
type Quota struct {
	Tier            string  `json:"tier"`
	Kind            string  `json:"kind"`
	AllowedGB       int64   `json:"allowed_gb,omitempty"`
	AllowedSessions int     `json:"allowed_sessions,omitempty"`
	ConcurrencyCap  int     `json:"concurrency_cap"`
	Allowed         bool    `json:"allowed"`
	Reason          string  `json:"reason,omitempty"`
	UsedGB          float64 `json:"used_gb,omitempty"`
	UsedSessions    int     `json:"used_sessions,omitempty"`
	Exceeded        bool    `json:"exceeded,omitempty"`
}

// Tier is the neutral view of a billing plan with USD and computed local-currency
// prices. It mirrors the commercial billing Tier. MaxActiveUsers of 0 means
// "no cap" (unlimited) — which is what self-host always reports.
type Tier struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	USDCents       int     `json:"usd_cents"`
	USD            float64 `json:"usd"`
	ZAR            int64   `json:"zar"`
	ZARSubunits    int64   `json:"zar_subunits"`
	MaxActiveUsers int     `json:"max_active_users,omitempty"`
}

// Region is the neutral view of a relay/compute region and its pricing basis. It
// mirrors the commercial billing Region (plus the operator Active flag), so the
// control plane can resolve a region's free relay allowance and overage rate
// without importing the pricing catalog. The no-op resolver reports a single
// unlimited self-host region.
type Region struct {
	ID                      string `json:"id"`
	Name                    string `json:"name"`
	EgressCostUSDCentsPerGB int    `json:"egress_cost_usd_cents_per_gb"`
	RelayUSDCentsPerGB      int    `json:"relay_usd_cents_per_gb"`
	FreeRelayGBWithBox      int    `json:"free_relay_gb_with_box"`
	ComputeMultBps          int    `json:"compute_mult_bps"`
	Active                  bool   `json:"active"`
}

// ErrRegionNotFound is returned by EntitlementResolver.RegionByID when the region
// id is not part of the priced catalog. The no-op resolver only knows its single
// self-host region.
var ErrRegionNotFound = errors.New("billingport: region not found")

// RelayUsageSource reports an account's current-month relay/TURN usage. The
// control plane's relay meter implements it; the resolver reads it back when
// computing over-cap enforcement.
type RelayUsageSource interface {
	UsageThisMonth(ctx context.Context, accountID string) (bytes int64, sessions int, err error)
}

// EntitlementResolver answers "what is this account entitled to?". It is the
// only coupling between the operational control plane and a commercial tier
// model. The no-op default (NoopResolver) grants an unlimited self-host tier.
type EntitlementResolver interface {
	// --- Tier / quota resolution -------------------------------------------

	// TierFor returns the account's PURCHASED plan id (billing display), default
	// "free" for an account with no subscription. Distinct from EffectiveTierFor,
	// which downgrades a payment-suspended account.
	TierFor(ctx context.Context, accountID string) (string, error)

	// EffectiveTierFor returns the account's current entitlement tier id — the
	// dunning-aware plan the quota gates enforce against.
	EffectiveTierFor(ctx context.Context, accountID string) (string, error)

	// Tiers returns every self-serve base plan with local-currency amounts
	// computed at the given USD→local rate.
	Tiers(rate float64) []Tier

	// QuotaFor returns the authoritative (dunning-aware) quota for accountID and
	// resource kind ("relay", "turn", "storage", "mail", "managed",
	// "public_circuit", "mail_custom_domain").
	QuotaFor(ctx context.Context, accountID, kind string) (Quota, error)

	// QuotaWithUsage returns QuotaFor joined with the account's current-cycle
	// relay/TURN usage from the supplied source, flipping Exceeded/Allowed once a
	// cap is crossed. A nil source (or a usage read error) returns the static
	// quota unchanged (fail open on a metering outage).
	QuotaWithUsage(ctx context.Context, accountID, kind string, usage RelayUsageSource) (Quota, error)

	// QuotaForTier is the static (no-account, no-DB) quota for a (tier, kind)
	// pair — the pure allowance table the account-aware resolvers delegate to.
	QuotaForTier(tier, kind string) Quota

	// MaxActiveUsersForTier returns the included-seat cap for a tier. A return
	// of 0 means "no cap" (unlimited) — which is what self-host always returns.
	MaxActiveUsersForTier(tier string) int

	// CheckStorageQuota reports whether writing bytes more of managed storage is
	// allowed for the account under hostingKind. It returns ErrQuotaExceeded when
	// the write would exceed a managed quota, or nil when allowed. Self-host /
	// BYO storage (HostingSelfHost) is always allowed.
	CheckStorageQuota(ctx context.Context, accountID string, bytes int64, hostingKind string) error

	// --- Relay / region ----------------------------------------------------

	// RelayAllowanceGB returns the free monthly relay allowance (GB) for a region.
	RelayAllowanceGB(ctx context.Context, regionID string) (int, error)

	// FreeRelayGBWithBox is the region-DEFAULT (EU basis) free monthly relay
	// allowance used as a fallback when a region-specific value is unavailable.
	FreeRelayGBWithBox() int

	// DefaultRelayRegionID is the region an account's relay allowance and overage
	// resolve against when it has no explicit region.
	DefaultRelayRegionID() string

	// RegionByID returns the priced region row for id, or ErrRegionNotFound when
	// id is not part of the catalog.
	RegionByID(ctx context.Context, id string) (Region, error)

	// AccountRelayRegion returns the account's primary relay region id, falling
	// back to DefaultRelayRegionID.
	AccountRelayRegion(ctx context.Context, accountID string) string

	// EmitSelfHostRelayChargeRegion meters shared-relay bytes consumed by a
	// self-hosted account in a specific region (the ONE relay charge a self-host
	// account can accrue). idempotencySuffix keeps repeated emits at-most-once.
	EmitSelfHostRelayChargeRegion(ctx context.Context, accountID, regionID string, bytes int64, idempotencySuffix string, now time.Time) error

	// --- Hosting-kind resolution -------------------------------------------

	// ResolveHostingKind turns the one signal a caller has on hand — "is this
	// account self-host / BYO storage" — into a HostingKind. Self-host is
	// structurally exempt; everything else defaults to cloud.
	ResolveHostingKind(selfHost bool) HostingKind

	// --- Metered-event reads -----------------------------------------------

	// MeteredBillingEnabled reports whether the account has opted into card-gated
	// metered overage billing (self-host always false).
	MeteredBillingEnabled(ctx context.Context, accountID string) (bool, error)

	// CountedThisMonthByBucket returns the account's current-cycle counted usage
	// total (e.g. relay bytes, mail sends) for a metered bucket.
	CountedThisMonthByBucket(ctx context.Context, accountID, bucket string, now time.Time) (int64, error)

	// OverageCostThisMonthMicros returns the account's current-cycle overage cost
	// (USD micros) recorded under a metered bucket.
	OverageCostThisMonthMicros(ctx context.Context, accountID, bucket string, now time.Time) (int64, error)
}
