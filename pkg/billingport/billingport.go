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
// storage is never capped or billed.
const (
	HostingCloud    = "cloud"
	HostingBox      = "box"
	HostingSelfHost = "selfhost"
)

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
	// EffectiveTierFor returns the account's current entitlement tier id.
	EffectiveTierFor(ctx context.Context, accountID string) (string, error)

	// MaxActiveUsersForTier returns the included-seat cap for a tier. A return
	// of 0 means "no cap" (unlimited) — which is what self-host always returns.
	MaxActiveUsersForTier(tier string) int

	// CheckStorageQuota reports whether writing bytes more of managed storage is
	// allowed for the account under hostingKind. It returns ErrQuotaExceeded when
	// the write would exceed a managed quota, or nil when allowed. Self-host /
	// BYO storage (HostingSelfHost) is always allowed.
	CheckStorageQuota(ctx context.Context, accountID string, bytes int64, hostingKind string) error
}
