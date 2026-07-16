// relay.go — the superadmin RELAY HEALTH console.
//
// The relay is the one bandwidth-bound service the fleet runs. This page is a
// pure OPERATIONAL view: per-region PoP health (up / degraded / down) and the
// throughput moving through each region, so an operator can see at a glance
// whether the data plane is healthy. Per-account billing, allowances and
// overage are a commercial concern and live in the commercial module, not here.
//
// Data is assembled by cmd/server (which holds the cloud-status PoP health
// aggregation) and injected via SetRelayProvider. This package stays a pure
// presentation layer.
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

// RelayOverview is the whole relay health rollup for the page.
type RelayOverview struct {
	PoPs         []RelayPoPRow
	TotalGBMonth float64
	Period       string // "2026-07"
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

// Relay renders per-region PoP health and per-region throughput.
func (p *Pages) Relay(w http.ResponseWriter, r *http.Request) {
	d := relayData{Available: p.relayFn != nil}
	if p.relayFn != nil {
		d.Overview = p.relayFn(r.Context())
	}
	d.Flash, d.FlashErr = flashFromQuery(r)
	p.r.render(w, "Relay health", "relay", d)
}
