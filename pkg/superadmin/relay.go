// relay.go — the superadmin RELAY & USAGE console (item 5).
//
// The relay is the one bandwidth-bound service the cloud runs, and the one place
// a region can quietly go underwater (Africa egress costs 6x Europe). This page
// puts the two numbers that matter side by side: per-region PoP health (EU + JHB)
// and relay GB by account, with the accounts sorted by consumption so the whales
// — and any account past its allowance — are visible at a glance.
//
// Data is assembled by cmd/server (which holds the relay-usage store + the
// cloud-status PoP health aggregation) and injected via SetRelayProvider. This
// package stays a pure presentation layer.
package superadmin

import (
	"context"
	"net/http"
)

// RelayPoPRow is one region's PoP health for the relay page.
type RelayPoPRow struct {
	Region       string
	Healthy      int     // healthy PoPs in the region
	Total        int     // total PoPs in the region
	Status       string  // "up" | "degraded" | "down" — derived by the assembler
	RelayGBMonth float64 // GB relayed through this region this month (0 when unknown)
}

// RelayAccountRow is one account's relay consumption this month.
type RelayAccountRow struct {
	AccountID string
	Email     string
	GB        float64
	Sessions  int
	OverageGB float64 // GB above the account's allowance (0 when within allowance/unknown)
}

// RelayOverview is the whole relay rollup for the page.
type RelayOverview struct {
	PoPs           []RelayPoPRow
	Accounts       []RelayAccountRow
	TotalGBMonth   float64
	TotalOverageGB float64
	Period         string // "2026-07"
}

// RelayProvider assembles the relay overview at request time. When unset, the
// page shows a notice.
type RelayProvider func(ctx context.Context) RelayOverview

// SetRelayProvider wires the relay console's data assembler.
func (p *Pages) SetRelayProvider(fn RelayProvider) { p.relayFn = fn }

type relayData struct {
	Available bool
	Overview  RelayOverview
	Flash     string
	FlashErr  string
}

// Relay renders per-region PoP health and per-account relay usage.
func (p *Pages) Relay(w http.ResponseWriter, r *http.Request) {
	d := relayData{Available: p.relayFn != nil}
	if p.relayFn != nil {
		d.Overview = p.relayFn(r.Context())
	}
	d.Flash, d.FlashErr = flashFromQuery(r)
	p.r.render(w, "Relay & Usage", "relay", d)
}
