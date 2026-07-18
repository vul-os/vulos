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

// wireRelayScaleDemand mounts the demand API AND starts the control loop, both
// bound to the injected provisioner (its Name()/Enabled() tell consumers whether
// the CP actuates) + the policy from env. A nil provisioner defaults to manual.
//
// The DemandAPI (POST /observe, GET /demand) and the Controller share ONE
// DemandStore, so the loop reconciles against exactly the load the PoPs push in.
// The Controller runs in the background and is ADVISORY for manual/external
// (computes + surfaces desired counts, actuates nothing); for an actuating
// provisioner it converges the fleet, draining before destroy and cooldown-gated.
//
// It also mounts the superadmin read surface GET /api/relay/scale/controller —
// per-region {current, desired, draining, last_action} — aggregate operational
// telemetry (no tenant data), consistent with the unauthenticated /demand read.
//
// Returns a closer that stops the background loop; the caller adds it to the
// operational closer set so it is cancelled on server shutdown.
func wireRelayScaleDemand(mux *http.ServeMux, prov relayscale.RelayProvisioner) func() {
	if prov == nil {
		prov = relayscale.NewManualProvisioner()
	}
	store := relayscale.NewDemandStore(0, nil)
	policy := relayscale.PolicyFromEnv()
	api := relayscale.NewDemandAPI(store, policy, prov.Name(), func() string {
		return secretOrEnv(context.Background(), "CP_SHARED_SECRET")
	})
	api.Register(mux, "/api/relay/scale")

	ctrl := relayscale.NewController(prov, policy, relayscale.SpecFromEnv(), store, relayscale.ControllerConfigFromEnv(), nil)
	ctrl.Register(mux, "/api/relay/scale")
	ctx, cancel := context.WithCancel(context.Background())
	go ctrl.Run(ctx)
	return func() { cancel() }
}
