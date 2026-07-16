// pricing.go — the superadmin PRICING and REGIONS consoles.
//
// These are server-rendered HTML pages, not React, for the same reason the rest
// of this package is: the superadmin surface is the highest-value target in the
// system (it changes what customers are charged and where their machines run), so
// it gets the hardened, minimal, no-client-framework treatment — CSRF-protected
// POSTs, audit-logged mutations, no XHR, no bundler, nothing to XSS into.
//
// WHY A CONSOLE AT ALL: a price that can only change by deploying is a price that
// does not change. Opening a region, correcting a rate that Fly moved under us, or
// withdrawing a SKU are operator actions with immediate money consequences, and
// they should not require a release.
//
// WHAT THESE PAGES PRICE (BOX-MODEL-02): the CORE we operate — RELAY (bandwidth,
// per region, above a free allowance), COMPUTE (managed boxes, per class/region)
// and STORAGE (bytes we hold). Mail is bring-your-own (connect your Gmail/Outlook/
// IMAP) and never billed — never the account anchor — the account is free
// (email/OAuth) and the apps are self-hostable for $0.
//
// WHAT MAKES THE REGIONS PAGE MORE THAN A FORM: it shows, per region, what we are
// actually SPENDING there (from the Fly cost poller) against what we are CHARGING
// there, and how many accounts are on it. That is the number that tells you a
// region is underwater BEFORE you have fifty customers in it — which is exactly
// how the Africa-egress trap (6x Europe's cost) would otherwise have been found:
// slowly, from a bank statement.
package superadmin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// ── Provider seams ──────────────────────────────────────────────────────────
//
// The superadmin package does not import billing (it would be an import cycle,
// and it keeps this package a pure presentation layer). cmd/server injects these.

// PriceRowView is one catalog line as the page renders it.
type PriceRowView struct {
	SKU       string
	Name      string
	Unit      string
	USDCents  int
	USD       string // pre-formatted, e.g. "9.00"
	ZAR       string // at the live rate, e.g. "165.30" — what the card is ACTUALLY charged
	Custom    bool
	Active    bool
	UpdatedAt string
	UpdatedBy string
}

// RegionRowView is one region, joined with what it is costing and earning.
type RegionRowView struct {
	ID                string
	Name              string
	EgressCostCentsGB int
	RelayPriceCentsGB int
	FreeRelayGB       int
	ComputeMultBps    int
	ComputeMult       string // "1.8x"
	MarkupX           string // relay price / egress cost, e.g. "2.5x"
	Active            bool
	UpdatedAt         string
	UpdatedBy         string
	// Live operational figures. Zero when the cost poller has no data yet — the
	// template says "no data" rather than rendering a confident zero, because a
	// fabricated zero cost is worse than an honest blank.
	Accounts        int
	Boxes           int
	SpendUSD        string // what Fly actually billed us in this region (from the cost poller)
	HasSpendData    bool
	RelayGBMonth    float64
	EstRelayCostUSD string // relay GB x egress cost — what that traffic costs us
}

// PricingProvider returns the catalog for display.
type PricingProvider func(ctx context.Context) []PriceRowView

// SetPriceFunc updates one SKU. actor is the superadmin's email.
type SetPriceFunc func(ctx context.Context, sku string, usdCents int, active bool, actor string) error

// RegionsProvider returns every region joined with its live cost/usage figures.
type RegionsProvider func(ctx context.Context) []RegionRowView

// UpsertRegionFunc creates or updates a region. It MUST fail closed on a relay
// price at or below the region's egress cost — the store enforces that, and this
// page simply surfaces the error.
type UpsertRegionFunc func(ctx context.Context, id, name string, egressCostCentsGB, relayCentsGB, freeRelayGB, computeMultBps int, active bool, actor string) error

// SetPricingProviders wires the pricing console.
func (p *Pages) SetPricingProviders(list PricingProvider, set SetPriceFunc) {
	p.pricingFn = list
	p.setPriceFn = set
}

// SetRegionProviders wires the regions console.
func (p *Pages) SetRegionProviders(list RegionsProvider, upsert UpsertRegionFunc) {
	p.regionsFn = list
	p.upsertRegionFn = upsert
}

// ── Pricing page ────────────────────────────────────────────────────────────

type pricingData struct {
	Available bool // false on a self-hosted CP — pricing is a cloud-only subsystem
	Prices    []PriceRowView
	CSRFToken string
	Flash     string
	FlashErr  string
}

// Pricing renders the catalog. Every row shows BOTH the canonical USD price and
// the ZAR amount that will actually hit the customer's card, because Paystack
// settles in rands and a console that only shows dollars would let an operator
// set a price without seeing what is really charged.
func (p *Pages) Pricing(w http.ResponseWriter, r *http.Request) {
	var rows []PriceRowView
	if p.pricingFn != nil {
		rows = p.pricingFn(r.Context())
	}
	flash, flashErr := flashFromQuery(r)
	p.r.render(w, "Pricing", "pricing", pricingData{
		Available: p.pricingFn != nil,
		Prices:    rows,
		CSRFToken: p.csrf(w, r),
		Flash:     flash,
		FlashErr:  flashErr,
	})
}

// PricingSet handles a price change. Audit-logged: a price change is a money
// change and must be attributable to a person.
func (p *Pages) PricingSet(w http.ResponseWriter, r *http.Request) {
	if p.setPriceFn == nil {
		redirectErr(w, r, "/superadmin/pricing", "pricing is not wired")
		return
	}
	sku := strings.TrimSpace(r.FormValue("sku"))
	usd := strings.TrimSpace(r.FormValue("usd"))
	active := r.FormValue("active") == "1"

	cents, err := usdToCents(usd)
	if err != nil {
		redirectErr(w, r, "/superadmin/pricing", "price must be a plain amount like 9 or 9.00: "+err.Error())
		return
	}
	actor := p.actorEmail(r)
	if err := p.setPriceFn(r.Context(), sku, cents, active, actor); err != nil {
		redirectErr(w, r, "/superadmin/pricing", err.Error())
		return
	}
	if p.al != nil {
		_ = p.al.Record(r.Context(), actor, "pricing.set", sku, map[string]string{
			"usd_cents": strconv.Itoa(cents),
			"active":    strconv.FormatBool(active),
		})
	}
	redirectFlash(w, r, "/superadmin/pricing", "updated "+sku)
}

// ── Regions page ────────────────────────────────────────────────────────────

type regionsData struct {
	Available bool // false on a self-hosted CP — regions are a cloud-only subsystem
	Regions   []RegionRowView
	CSRFToken string
	Flash     string
	FlashErr  string
}

// Regions renders every region with its economics: what Fly charges us for egress
// there, what we charge, the resulting markup, how many accounts and boxes are on
// it, and what we have actually SPENT there this period.
func (p *Pages) Regions(w http.ResponseWriter, r *http.Request) {
	var rows []RegionRowView
	if p.regionsFn != nil {
		rows = p.regionsFn(r.Context())
	}
	flash, flashErr := flashFromQuery(r)
	p.r.render(w, "Regions", "regions", regionsData{
		Available: p.regionsFn != nil,
		Regions:   rows,
		CSRFToken: p.csrf(w, r),
		Flash:     flash,
		FlashErr:  flashErr,
	})
}

// RegionsUpsert opens a new region or edits an existing one.
//
// The one rule the operator cannot talk their way past (enforced in the store,
// surfaced here): the relay price must EXCEED the region's egress cost. Selling
// bandwidth below what Fly charges us is the exact mistake a flat cross-region
// price makes, and the whole reason regions are priced individually.
func (p *Pages) RegionsUpsert(w http.ResponseWriter, r *http.Request) {
	if p.upsertRegionFn == nil {
		redirectErr(w, r, "/superadmin/regions", "regions are not wired")
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	name := strings.TrimSpace(r.FormValue("name"))
	active := r.FormValue("active") == "1"

	egress, err1 := strconv.Atoi(strings.TrimSpace(r.FormValue("egress_cost_cents_gb")))
	relay, err2 := strconv.Atoi(strings.TrimSpace(r.FormValue("relay_cents_gb")))
	freeGB, err3 := strconv.Atoi(strings.TrimSpace(r.FormValue("free_relay_gb")))
	mult, err4 := strconv.Atoi(strings.TrimSpace(r.FormValue("compute_mult_bps")))
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		redirectErr(w, r, "/superadmin/regions", "every number must be a whole number (cents, GB, or basis points)")
		return
	}

	actor := p.actorEmail(r)
	if err := p.upsertRegionFn(r.Context(), id, name, egress, relay, freeGB, mult, active, actor); err != nil {
		redirectErr(w, r, "/superadmin/regions", err.Error())
		return
	}
	if p.al != nil {
		_ = p.al.Record(r.Context(), actor, "region.upsert", id, map[string]string{
			"egress_cost_cents_gb": strconv.Itoa(egress),
			"relay_cents_gb":       strconv.Itoa(relay),
			"free_relay_gb":        strconv.Itoa(freeGB),
			"compute_mult_bps":     strconv.Itoa(mult),
			"active":               strconv.FormatBool(active),
		})
	}
	redirectFlash(w, r, "/superadmin/regions", "saved region "+id)
}

// Price-parse failures. They are separate sentinels because the operator needs
// to know WHICH mistake they made: an empty box, a non-number, or a sub-cent
// amount we refuse to round.
var (
	errEmptyPrice = errors.New("price is empty")
	errBadPrice   = errors.New("price is not a number")
	errSubCent    = errors.New("price has more than two decimal places — we refuse to round a price nobody agreed to")
)

// usdToCents parses a human price ("9", "9.00", "0.05") into whole US cents.
//
// It rejects anything it cannot represent EXACTLY rather than rounding: a price
// silently rounded is a price nobody agreed to. Sub-cent inputs are refused for
// the same reason — if we ever need mills, that is a schema change, not a
// rounding decision made in a form parser.
func usdToCents(s string) (int, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	if s == "" {
		return 0, errEmptyPrice
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	w, err := strconv.Atoi(whole)
	if err != nil || w < 0 {
		return 0, errBadPrice
	}
	cents := w * 100
	if hasFrac {
		if len(frac) > 2 {
			return 0, errSubCent
		}
		for len(frac) < 2 {
			frac += "0"
		}
		f, err := strconv.Atoi(frac)
		if err != nil || f < 0 {
			return 0, errBadPrice
		}
		cents += f
	}
	return cents, nil
}
