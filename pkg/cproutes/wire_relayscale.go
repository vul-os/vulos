// wire_relayscale.go — mounts the relay-scaling DEMAND API.
//
// The relay pool scales through the pkg/relayscale seam. Regardless of which
// provisioner is active (manual / external / kubernetes / firecracker / proxmox /
// a commercial managed multi-provider), the control plane PUBLISHES the desired
// per-region relay count so an operator's own scaler — or an observer — can read
// it. This wires:
//
//	POST /api/relay/scale/observe   (X-Relay-Auth: $CP_SHARED_SECRET) — relay PoPs
//	                                 / an aggregator push per-region load.
//	GET  /api/relay/scale/demand    — the published desired state (current +
//	                                 desired + reason per region), for an external
//	                                 scaler / dashboard to consume.
//
// The observe endpoint fails closed: with no CP_SHARED_SECRET configured it
// returns 503 (never accept anonymous load pushes). The demand read is
// unauthenticated operational telemetry (no tenant data — only aggregate
// per-region counts + saturation), mirroring the relay saturation gauge the relay
// already exposes on /metrics.
package cproutes

import (
	"context"
	"net/http"

	"github.com/vul-os/vulos-management/pkg/relayscale"
)

// wireRelayScaleDemand mounts the demand API using the injected provisioner (its
// Name() tells consumers whether the CP actuates) + the policy from env. A nil
// provisioner defaults to manual. The DemandStore lives for the process lifetime;
// there is no closer (in-memory, bounded to one row per region).
func wireRelayScaleDemand(mux *http.ServeMux, prov relayscale.RelayProvisioner) {
	if prov == nil {
		prov = relayscale.NewManualProvisioner()
	}
	store := relayscale.NewDemandStore(0, nil)
	api := relayscale.NewDemandAPI(store, relayscale.PolicyFromEnv(), prov.Name(), func() string {
		return secretOrEnv(context.Background(), "CP_SHARED_SECRET")
	})
	api.Register(mux, "/api/relay/scale")
}
