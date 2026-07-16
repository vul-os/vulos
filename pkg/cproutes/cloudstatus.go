// cloudstatus.go — the two cloud-status surfaces (CLOUD-STATUS-01).
//
// The cloud today operates a small fixed set of services (Control Plane, the
// Relay EU + JHB PoPs, and Provisioning) while products run on the user's own
// box. These two endpoints reflect THAT architecture:
//
//	GET /api/cloud/status    — PUBLIC. Overall + per-component health of the cloud
//	                           services we operate. No tenant data, no secrets, no
//	                           internal URLs/IPs. Every check is fail-safe and the
//	                           response is cache-friendly.
//	GET /api/account/status  — AUTHENTICATED. The status of the things the signed-
//	                           in account owns: its boxes' reachability, its relay
//	                           usage/health, its provisioned services, recent
//	                           events. Session-gated (reuses authStore.RequireSession)
//	                           and strictly scoped to the caller's own account id —
//	                           no identifier is read from request input, so it
//	                           cannot be steered at another tenant (no IDOR).
//
// Health comes from REAL sources: the CP database (auth.Store.Ping), the relay
// PoP pool (routing.Store), the caller's relay usage (relayusage.Store), and the
// account's enrolled boxes (fleet.Store). Provisioning is probed only at a
// config-supplied URL, so a probe can never be abused as an SSRF oracle.
package cproutes

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cloudstatus"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/relayusage"
	"github.com/vul-os/vulos-management/pkg/routing"
)

// csPopLister is the optional routing-store extension that lists ALL PoPs
// (healthy or not). Both routing.SQLStore and routing.MemStore implement it.
type csPopLister interface {
	ListPoPs(ctx context.Context) ([]routing.PoP, error)
}

// ─── source adapters (bridge in-tree stores to cloudstatus interfaces) ───────

// csPoPAdapter exposes the relay PoP pool as a cloudstatus.PoPSource. It prefers
// the full ListPoPs view (so an all-unhealthy region can be shown as "down"); if
// the store only offers HealthyPoPs it falls back to that (healthy == total).
type csPoPAdapter struct{ store routing.Store }

func (a csPoPAdapter) ListPoPs(ctx context.Context) ([]cloudstatus.PoP, error) {
	if a.store == nil {
		return nil, nil
	}
	var pops []routing.PoP
	if lister, ok := a.store.(csPopLister); ok {
		if list, err := lister.ListPoPs(ctx); err == nil {
			pops = list
		} else {
			return nil, err
		}
	} else {
		list, err := a.store.HealthyPoPs(ctx)
		if err != nil {
			return nil, err
		}
		pops = list
	}
	out := make([]cloudstatus.PoP, 0, len(pops))
	for _, p := range pops {
		// Deliberately drop p.IP — the public page must not leak infra addresses.
		out = append(out, cloudstatus.PoP{ID: p.ID, Region: p.Region, Healthy: p.Healthy})
	}
	return out, nil
}

// csDeviceAdapter exposes an account's enrolled boxes as a cloudstatus.DeviceSource.
// Always account-scoped: it calls fleet.ListByAccount with the supplied id.
type csDeviceAdapter struct{ store fleet.Store }

func (a csDeviceAdapter) Devices(ctx context.Context, accountID string) ([]cloudstatus.Device, error) {
	if a.store == nil || strings.TrimSpace(accountID) == "" {
		return nil, nil
	}
	devices, err := a.store.ListByAccount(ctx, accountID, 200, 0)
	if err != nil {
		return nil, err
	}
	out := make([]cloudstatus.Device, 0, len(devices))
	for _, d := range devices {
		if d.Decommissioned {
			continue
		}
		out = append(out, cloudstatus.Device{
			ULID:          d.ULID,
			Name:          d.Name,
			Health:        d.Health,
			LastHeartbeat: d.LastHeartbeat,
		})
	}
	return out, nil
}

// csRelayAdapter exposes an account's current-month relay usage.
type csRelayAdapter struct{ store relayusage.Store }

func (a csRelayAdapter) Usage(ctx context.Context, accountID string) (cloudstatus.RelayUsage, error) {
	if a.store == nil || strings.TrimSpace(accountID) == "" {
		return cloudstatus.RelayUsage{}, nil
	}
	bytes, sessions, err := a.store.UsageThisMonth(ctx, accountID)
	if err != nil {
		return cloudstatus.RelayUsage{}, err
	}
	return cloudstatus.RelayUsage{Bytes: bytes, Sessions: sessions}, nil
}

// ─── wiring ──────────────────────────────────────────────────────────────────

// NewCloudStatusAggregator builds the aggregator from the in-scope stores + env.
// Any store may be nil; the aggregator degrades that section rather than failing.
//
// Env:
//
//	PROVISIONING_HEALTH_URL — optional provisioner health target ("" ⇒ tracks CP DB)
func NewCloudStatusAggregator(db cloudstatus.DBPinger, routingStore routing.Store, fleetStore fleet.Store, relayUsage relayusage.Store) *cloudstatus.Aggregator {
	return cloudstatus.New(cloudstatus.Config{
		DB:           db,
		PoPs:         csPoPAdapter{store: routingStore},
		Devices:      csDeviceAdapter{store: fleetStore},
		Relay:        csRelayAdapter{store: relayUsage},
		ProvisionURL: strings.TrimSpace(os.Getenv("PROVISIONING_HEALTH_URL")),
	})
}

// RegisterCloudStatus wires the public + authenticated status endpoints.
func RegisterCloudStatus(mux *http.ServeMux, agg *cloudstatus.Aggregator, authStore *auth.Store) {
	// PUBLIC — no tenant data, no secrets. Cache-friendly (short shared cache).
	mux.HandleFunc("GET /api/cloud/status", func(w http.ResponseWriter, r *http.Request) {
		snap := agg.PublicSnapshot(r.Context())
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "public, max-age=15, s-maxage=15")
		httpx.JSON(w, snap)
	})

	// AUTHENTICATED — session-gated + strictly account-scoped (no IDOR: the id
	// comes from the session User, never from request input).
	mux.HandleFunc("GET /api/account/status", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return // RequireSession already wrote 401.
		}
		snap := agg.AccountSnapshot(r.Context(), u.ID)
		w.Header().Set("Cache-Control", "no-store")
		httpx.JSON(w, snap)
	})
}
