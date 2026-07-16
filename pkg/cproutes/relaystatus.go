// relaystatus.go — GET /api/relay/status (RELAY-STATUS-01). Moved verbatim from
// vulos-cloud backend/cp/cmd/server/routes_relaystatus.go into the operational
// route set.
//
// The peer-fabric status the Workspace Relay surface consumes. It aggregates
// from the data the CP actually has:
//
//   - pops:     the geo-DNS relay PoP pool (pkg/routing) — REAL.
//   - transfer: the caller's current-month relayed bytes + TURN sessions
//     (pkg/relayusage) — REAL.
//   - peers / connections: the CP does not model a live per-user peer mesh, so
//     these are returned as typed EMPTY arrays (never faked).
//   - health:   derived from PoP health — "ok" (≥1 healthy PoP), "degraded"
//     (PoPs exist but none healthy), or "not_available" (no PoP source).
//
// Session-gated: transfer is scoped to the authenticated account.
//
// COORDINATOR: the popLister interface is DEFINED here (moved with this file) but
// is ALSO referenced by cloud cmd/server/main.go, routes_cloudstatus.go,
// routes_relayusage.go and wire_superadmin_ops.go — those files will lose the
// symbol when routes_relaystatus.go is deleted; redeclare popLister in cloud (or
// promote it into pkg/routing) so the cloud package still compiles.
//
// NOTE: the original used the package-main `timeNow` test-clock hook
// (var timeNow = func() time.Time { return time.Now().UTC() }, from
// cmd/server/routes_auth.go). That hook is not part of this route's contract, so
// it is inlined here as time.Now().UTC().
package cproutes

import (
	"context"
	"net/http"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/relayusage"
	"github.com/vul-os/vulos-management/pkg/routing"
)

// popLister is the optional routing-store extension that lists ALL PoPs (healthy
// or not). Both routing.SQLStore and routing.MemStore implement it.
type popLister interface {
	ListPoPs(ctx context.Context) ([]routing.PoP, error)
}

// relayPeer is one fabric peer. No CP source today → the array is empty.
type relayPeer struct {
	ID      string  `json:"id"`
	Latency float64 `json:"latency"`
	State   string  `json:"state"`
}

// relayConnection is one active relay connection. No CP source today → empty.
type relayConnection struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// relayPoPView is a relay PoP in the status payload.
type relayPoPView struct {
	ID      string `json:"id"`
	Region  string `json:"region"`
	Healthy bool   `json:"healthy"`
}

// relayTransfer is the caller's current-month relayed traffic.
type relayTransfer struct {
	Bytes    int64  `json:"bytes"`
	Sessions int    `json:"sessions"`
	Period   string `json:"period"`
}

// relayStatusResponse is the GET /api/relay/status body.
type relayStatusResponse struct {
	Health      string            `json:"health"`
	Connections []relayConnection `json:"connections"`
	Peers       []relayPeer       `json:"peers"`
	PoPs        []relayPoPView    `json:"pops"`
	Transfer    relayTransfer     `json:"transfer"`
}

// RegisterRelayStatus wires GET /api/relay/status. routingStore and
// relayUsage may be nil (the route then reports empty/not_available rather than
// erroring).
func RegisterRelayStatus(mux *http.ServeMux, authStore *auth.Store, routingStore routing.Store, relayUsage relayusage.Store) {
	mux.HandleFunc("GET /api/relay/status", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}

		resp := relayStatusResponse{
			Health:      "not_available",
			Connections: []relayConnection{},
			Peers:       []relayPeer{},
			PoPs:        []relayPoPView{},
			Transfer:    relayTransfer{Period: relayusage.CurrentPeriod(time.Now().UTC())},
		}

		// PoPs (REAL): prefer the full list; fall back to the healthy-only view.
		var pops []routing.PoP
		haveSource := false
		if routingStore != nil {
			if lister, ok := routingStore.(popLister); ok {
				if list, err := lister.ListPoPs(r.Context()); err == nil {
					pops, haveSource = list, true
				}
			}
			if !haveSource {
				if list, err := routingStore.HealthyPoPs(r.Context()); err == nil {
					pops, haveSource = list, true
				}
			}
		}
		healthy := 0
		for _, p := range pops {
			if p.Healthy {
				healthy++
			}
			resp.PoPs = append(resp.PoPs, relayPoPView{ID: p.ID, Region: p.Region, Healthy: p.Healthy})
		}
		switch {
		case !haveSource:
			resp.Health = "not_available"
		case healthy > 0:
			resp.Health = "ok"
		default:
			resp.Health = "degraded"
		}

		// Transfer (REAL): the caller's current-month relayed bytes + TURN sessions.
		if relayUsage != nil {
			if bytes, sessions, err := relayUsage.UsageThisMonth(r.Context(), u.ID); err == nil {
				resp.Transfer.Bytes = bytes
				resp.Transfer.Sessions = sessions
			}
		}

		httpx.JSON(w, resp)
	})
}
