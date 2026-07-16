// billing_recon.go — billing reconciliation view for the super-admin portal.
//
// The reconciliation view answers: for each tenant (or tier in aggregate):
//   - What usage did we meter?  (metered_events)
//   - What does that usage cost us? (grounded cost model: Fly / Tigris / SES /
//     Linode / LLM pass-through)
//   - What did we charge them? (subscriptions + billing_transactions)
//   - What is the gross margin?
//   - Is there a drift flag? (COGS > price, free accounts over cost budget,
//     past_due not collected, tier mis-pricing)
//
// Data is assembled by an injectable BillingReconProvider seam wired from
// cmd/server (which has the billing store). This package holds only the render
// types and handler.
//
// Cost model (grounded, 2026-06 published rates):
//
//	Fly.io machines  $0.0018/min shared-cpu-1x@256MB  (≈ $2.59/mo)
//	Tigris storage   $0.021/GB-month
//	Tigris egress    $0.05/GB
//	AWS SES          $0.10 / 1000 emails
//	Linode/Akamai    $6/mo per nanode (fallback compute)
//	LLM              pass-through (vendor cost per 1k tokens stored in events)
//	Meet             $0.01 / participant-minute (Livekit Cloud ceiling)
//
// These are the same rates used in billingmodel/model.py; keeping them in sync
// is the operator's responsibility (update both when rates change).
package superadmin

import (
	"context"
	"fmt"
	"net/http"
)

// ─────────────────────────────────────────────────────────────────────────────
// Cost model constants (grounded, matches billingmodel/model.py)
// ─────────────────────────────────────────────────────────────────────────────

// Published per-unit rates in USD micro-cents (1e-8 USD) for integer math.
// Use: cost_usd = units * rate / 1_000_000_000
const (
	// FlyMachineMinUSD is cost per Fly machine-minute for shared-cpu-1x@256 MB.
	FlyMachineMinUSD = 0.0018 // USD
	// TigrisStorageGBMonthUSD is Tigris storage cost per GB-month.
	TigrisStorageGBMonthUSD = 0.021 // USD
	// TigrisEgressGBUSD is Tigris egress cost per GB.
	TigrisEgressGBUSD = 0.05 // USD
	// SESPer1000USD is AWS SES cost per 1 000 outbound emails.
	SESPer1000USD = 0.10 // USD
	// MeetParticipantMinUSD is the Livekit Cloud ceiling cost per participant-minute.
	MeetParticipantMinUSD = 0.01 // USD
	// StorageGBMonthUSD is the general Tigris storage rate (alias for template use).
	StorageGBMonthUSD = TigrisStorageGBMonthUSD

	// Tier list prices in USD/user/month per TIERS.md (FINAL, 2026-06-28).
	// Tier IDs in the billing store: "free" | "pro" | "team".
	// "personal" is the marketing label for the $6 single-user tier (tier ID
	// "personal" in the subscription; absent from older DBs → falls back to
	// TierListPrice default $0).
	// "enterprise" is deprecated — no new subscribers; legacy rows only.
	TierPriceFreePU       = 0.00
	TierPricePersonalPU   = 6.00  // Personal: $6/mo — single user (TIERS.md)
	TierPriceProPU        = 10.00 // Pro: $10/user/mo (was $9 — TIERS.md fix)
	TierPriceTeamPU       = 12.00 // Team: $12/user/mo
	TierPriceEnterprisePU = 0.00  // Enterprise: DEPRECATED — no new subscribers

	// Tier COGS from model.py @100k-user scale (gross variable cost per user/mo).
	TierCOGSFreePU       = 0.32
	TierCOGSPersonalPU   = 2.10 // Personal: estimated (between free and pro)
	TierCOGSProPU        = 3.83
	TierCOGSTeamPU       = 3.81
	TierCOGSEnterprisePU = 42.62 // Kept for legacy row reconciliation
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// TenantReconRow is the per-tenant reconciliation line.
type TenantReconRow struct {
	AccountID string
	Email     string
	Tier      string
	State     string // "active" | "past_due" | "cancelled" | "suspended"
	// Revenue
	RevenueUSD float64 // actual collected (billing_transactions, this period)
	PriceUSD   float64 // expected list price for their tier (per month)
	// Cost
	COGSEstUSD float64 // estimated COGS from metered events + cost model
	// Derived
	MarginPct float64
	// Drift flags (non-empty = anomaly).
	DriftFlags []string
}

// TierSummary is the aggregate per-tier reconciliation.
type TierSummary struct {
	Tier            string
	AccountCount    int
	TotalRevenueUSD float64
	TotalCOGSUSD    float64
	MarginPct       float64
	// Expected margin from the billing model.
	ModelMarginPct float64
	DeltaPP        float64 // actual - model (negative = under-performing)
	Status         string  // GREEN / YELLOW / RED (|delta| < 5pp / 5-10 / >10)
}

// BillingReconResult is the full reconciliation data set.
type BillingReconResult struct {
	// Tenant-level rows (top N by COGS or revenue; capped for page performance).
	Tenants []TenantReconRow
	// Drift flagged rows (pre-filtered from Tenants).
	Drifted []TenantReconRow
	// Per-tier aggregates.
	ByTier []TierSummary
	// Fleet-wide blended margin.
	BlendedMarginPct float64
	ModelMarginPct   float64
	BlendedDeltaPP   float64
	BlendedStatus    string
	// How many past_due accounts have uncollected balance.
	UncollectedPastDue int
	// Total estimated fleet COGS (USD).
	TotalCOGSUSD float64
	// Total collected revenue (USD).
	TotalRevenueUSD float64
}

// BillingReconProvider assembles the reconciliation data at request time.
// Wired from cmd/server (billing + auth stores). May return a zero-value
// result when data is not yet available; the template handles that gracefully.
type BillingReconProvider func(ctx context.Context) BillingReconResult

// ─────────────────────────────────────────────────────────────────────────────
// Pages seam
// ─────────────────────────────────────────────────────────────────────────────

// SetBillingReconProvider wires the billing reconciliation assembler.
func (p *Pages) SetBillingReconProvider(fn BillingReconProvider) {
	p.billingReconFn = fn
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler
// ─────────────────────────────────────────────────────────────────────────────

type billingReconPageData struct {
	Result        BillingReconResult
	HasData       bool
	CostModelNote string
	CSRFToken     string
}

// BillingReconciliation renders GET /superadmin/billing-recon.
func (p *Pages) BillingReconciliation(w http.ResponseWriter, r *http.Request) {
	d := billingReconPageData{
		CSRFToken: p.csrf(w, r),
		CostModelNote: fmt.Sprintf(
			"Cost model: Fly $%.4f/machine-min · Tigris $%.3f/GB-mo · "+
				"SES $%.2f/1k emails · Meet $%.2f/participant-min · "+
				"LLM pass-through. Source: billingmodel/model.py (blended model margin %.1f%%).",
			FlyMachineMinUSD, TigrisStorageGBMonthUSD, SESPer1000USD,
			MeetParticipantMinUSD, 62.4,
		),
	}

	if p.billingReconFn != nil {
		d.Result = p.billingReconFn(r.Context())
		d.HasData = true
	}

	auditFromRequest(r, p.al, p.actorEmail(r), "admin.billing_recon.view", "", nil)
	p.r.render(w, "Billing Reconciliation", "billing_recon", d)
}

// ─────────────────────────────────────────────────────────────────────────────
// Reconciliation math helpers (used by the server-side assembler, exported so
// cmd/server can use them without re-implementing)
// ─────────────────────────────────────────────────────────────────────────────

// ReconcileTenant computes the per-tenant reconciliation given raw usage metrics.
// All USD amounts are float64. The function is pure / stateless.
//
//   - mailSends: count of outbound emails (SES cost)
//   - storageGBMo: GB-months of object storage (Tigris cost)
//   - egressGB: GB of object egress (Tigris egress cost)
//   - machineMinutes: Fly machine minutes consumed (Fly cost)
//   - meetMinutes: participant-minutes (Meet cost)
//   - llmCostUSD: direct LLM vendor cost (pass-through)
//   - revenueUSD: actual collected revenue this period
//   - priceUSD: list price for the account's tier (per month)
//   - tier, state: account tier + subscription state
func ReconcileTenant(
	accountID, email, tier, state string,
	mailSends, machineMinutes, meetMinutes int64,
	storageGBMo, egressGB, llmCostUSD, revenueUSD, priceUSD float64,
) TenantReconRow {
	// Estimated COGS from metered usage.
	sesCost := float64(mailSends) / 1000.0 * SESPer1000USD
	flyCost := float64(machineMinutes) * FlyMachineMinUSD
	tigrisCost := storageGBMo*TigrisStorageGBMonthUSD + egressGB*TigrisEgressGBUSD
	meetCost := float64(meetMinutes) * MeetParticipantMinUSD
	cogsTotalUSD := sesCost + flyCost + tigrisCost + meetCost + llmCostUSD

	// Gross margin.
	marginPct := 0.0
	if revenueUSD > 0 {
		marginPct = (revenueUSD - cogsTotalUSD) / revenueUSD * 100
	}

	// Drift flags.
	var flags []string
	if cogsTotalUSD > priceUSD && priceUSD > 0 {
		flags = append(flags, fmt.Sprintf("COGS $%.2f > price $%.2f", cogsTotalUSD, priceUSD))
	}
	if tier == "free" && cogsTotalUSD > TierCOGSFreePU*2 {
		// Free accounts costing more than 2x the model budget.
		flags = append(flags, fmt.Sprintf("free over budget (COGS $%.2f > 2×model $%.2f)", cogsTotalUSD, TierCOGSFreePU*2))
	}
	if state == "past_due" && revenueUSD == 0 {
		flags = append(flags, "past_due: no revenue collected")
	}
	if revenueUSD > 0 && marginPct < 0 {
		flags = append(flags, fmt.Sprintf("negative margin %.1f%%", marginPct))
	}

	return TenantReconRow{
		AccountID:  accountID,
		Email:      email,
		Tier:       tier,
		State:      state,
		RevenueUSD: revenueUSD,
		PriceUSD:   priceUSD,
		COGSEstUSD: cogsTotalUSD,
		MarginPct:  marginPct,
		DriftFlags: flags,
	}
}

// TierMarginStatus returns GREEN / YELLOW / RED based on |delta| from model.
func TierMarginStatus(actualPct, modelPct float64) string {
	delta := actualPct - modelPct
	if delta < 0 {
		delta = -delta
	}
	switch {
	case delta < 5:
		return "GREEN"
	case delta <= 10:
		return "YELLOW"
	default:
		return "RED"
	}
}

// TierModelMargin returns the billing-model expected margin % for the given tier.
// Source: billingmodel/TIERS.md (FINAL 2026-06-28).
func TierModelMargin(tier string) float64 {
	switch tier {
	case "free":
		return -100.0 // loss leader
	case "personal":
		// Personal $6/mo: similar margin profile to Pro (smaller allowances).
		return 55.0
	case "pro":
		return 57.4 // Pro $10/user (was $9; margin recalculated at $10 price)
	case "team":
		return 68.3
	case "enterprise":
		// Deprecated tier — margin kept for legacy row display.
		return 56.9
	default:
		return 62.4 // blended
	}
}

// TierListPrice returns the list price in USD/user/month for the given tier per
// billingmodel/TIERS.md (FINAL 2026-06-28). Tier IDs: free | personal | pro |
// team. "enterprise" is deprecated (returns 0 for new reconciliation rows; kept
// for legacy subscriber data only).
func TierListPrice(tier string) float64 {
	switch tier {
	case "free":
		return TierPriceFreePU
	case "personal":
		return TierPricePersonalPU
	case "pro":
		return TierPriceProPU
	case "team":
		return TierPriceTeamPU
	case "enterprise":
		// Deprecated tier — no new subscribers. Legacy rows still reconcile at $0
		// to flag them for migration in the drift check.
		return TierPriceEnterprisePU
	default:
		return 0
	}
}
