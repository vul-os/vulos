// wire_relayscale.go — mounts the relay-scaling DEMAND API + control loop, and
// exposes a gated operator VIEW over the same live state.
//
// The relay pool scales through the pkg/relayscale seam. Regardless of which
// provisioner is active (manual / external / kubernetes / firecracker / proxmox /
// a commercial managed multi-provider), the control plane PUBLISHES the desired
// per-region relay count so an operator's own scaler — or an observer — can read
// it. This wires:
//
//	POST /api/relay/scale/observe    (X-Relay-Auth: $CP_SHARED_SECRET) — relay PoPs
//	                                  / an aggregator push per-region load.
//	GET  /api/relay/scale/demand     — the published desired state (current +
//	                                  desired + reason per region), for an external
//	                                  scaler / dashboard to consume.
//	GET  /api/relay/scale/controller — the #41 control-loop snapshot (per-region
//	                                  current/desired/draining/last_action).
//
// The observe endpoint fails closed: with no CP_SHARED_SECRET configured it
// returns 503 (never accept anonymous load pushes). The demand + controller reads
// are unauthenticated operational telemetry (no tenant data — only aggregate
// per-region counts + saturation), mirroring the relay saturation gauge the relay
// already exposes on /metrics.
//
// The SAME live surface (one DemandStore, one running control loop) is ALSO
// surfaced to the OPERATOR console behind the RequireSuperAdmin gate via
// AdminView(), so the console renders exactly the state the loop is acting on —
// no second data source, no mock.
package cproutes

import (
	"context"
	"net/http"

	"github.com/vul-os/vulos-management/pkg/relayscale"
)

// relayScaleSurface bundles the demand API and the #41 control loop bound to one
// shared DemandStore, so the public endpoints, the running loop, and the gated
// operator view all read/write exactly the same live state.
type relayScaleSurface struct {
	api  *relayscale.DemandAPI
	ctrl *relayscale.Controller
	stop func()
}

// RelayScaleAdminView is the combined operator snapshot returned to the console:
// the #41 control-loop status plus the published demand. Both are computed from
// the live DemandStore the observe endpoint fills, so this is real data — never a
// mock.
type RelayScaleAdminView struct {
	Controller relayscale.ControllerStatus `json:"controller"`
	Demand     relayscale.DemandResponse   `json:"demand"`
}

// newRelayScaleSurface builds the demand API + control loop against the injected
// provisioner (its Name()/Enabled() tell consumers whether the CP actuates) + the
// policy from env, and starts the background control loop. A nil provisioner
// defaults to manual. The returned surface's stop() cancels the loop.
func newRelayScaleSurface(prov relayscale.RelayProvisioner) *relayScaleSurface {
	if prov == nil {
		prov = relayscale.NewManualProvisioner()
	}
	store := relayscale.NewDemandStore(0, nil)
	policy := relayscale.PolicyFromEnv()
	api := relayscale.NewDemandAPI(store, policy, prov.Name(), func() string {
		return secretOrEnv(context.Background(), "CP_SHARED_SECRET")
	})
	ctrl := relayscale.NewController(prov, policy, relayscale.SpecFromEnv(), store, relayscale.ControllerConfigFromEnv(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	go ctrl.Run(ctx)
	return &relayScaleSurface{api: api, ctrl: ctrl, stop: cancel}
}

// mountPublic registers the unauthenticated demand/observe/controller endpoints.
func (s *relayScaleSurface) mountPublic(mux *http.ServeMux) {
	s.api.Register(mux, "/api/relay/scale")  // GET /demand, POST /observe
	s.ctrl.Register(mux, "/api/relay/scale") // GET /controller
}

// AdminView snapshots the live control-loop status + published demand for the
// gated operator console. Same instance the loop drives — real data.
func (s *relayScaleSurface) AdminView() RelayScaleAdminView {
	return RelayScaleAdminView{
		Controller: s.ctrl.Status(),
		Demand:     s.api.Demand(),
	}
}
