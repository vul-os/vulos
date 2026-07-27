package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"vulos/backend/services/reach"
	"vulos/backend/services/reach/tunnel"
	"vulos/backend/services/relayconfig"
)

// reachwire.go — starting Vulos's OWN reverse-tunnel agent inside the box.
//
// This is the wiring layer between three things that deliberately do not know
// about each other:
//
//   - services/reach holds the endpoint SET and its health state, and knows
//     nothing about tunnels.
//   - services/reach/tunnel holds the protocol and the agent, and knows
//     nothing about how endpoints are configured (it is compiled into the
//     relay too, which has no business linking box-side config).
//   - cmd/server owns the OS's http.Handler.
//
// Bringing them together here — rather than letting either package reach into
// the other — is what keeps the relay role free of box-side machinery and
// keeps the endpoint set reusable by the other consumers (peer resolve,
// notify fan-out) that never open a tunnel at all.
//
// # No sidecar
//
// The agent serves the OS's own handler IN PROCESS. There is no second binary
// to install, supervise, or version-skew against the OS, and no loopback
// listener for it to dial — which also means no loopback SSRF surface. A
// tunnelled request runs the exact same auth, session, CSRF, rate-limit and
// security-header chain as one arriving on the box's own listener.

// reachRuntime is what the wiring hands back to main for shutdown and status.
type reachRuntime struct {
	Agent *tunnel.Agent
	Set   *reach.Set
}

// Close stops the agent. Safe on a nil runtime, which is the common case (a
// box with no relays configured).
func (r *reachRuntime) Close() {
	if r == nil || r.Agent == nil {
		return
	}
	_ = r.Agent.Close()
}

// startReachAgent loads the relay endpoint set and, if any endpoints are
// configured, brings up one tunnel per relay serving handler.
//
// directEndpoint is this box's public https origin when the DIRECT-IP
// listener is up, or "" — relays that accept it will verify ownership and
// then advertise it so clients bypass the tunnel entirely.
//
// # Failure posture
//
// A CONFIGURATION error (malformed URL, unreadable file, world-readable
// secrets) is logged loudly and leaves the box with NO tunnels. It is
// deliberately not fatal: a box that refused to boot because its relay
// config was wrong would be unreachable by every path at once, including the
// LAN and console an operator needs in order to fix it. A box that boots
// LAN-reachable with a loud log line is recoverable.
//
// A CONNECTION error is not an error at all here — the agent retries forever
// in the background, which is the correct behaviour for a relay that is
// merely down.
func startReachAgent(ctx context.Context, mux *http.ServeMux, handler http.Handler, directEndpoint, version string) *reachRuntime {
	set, src, err := reach.FromEnv()
	if err != nil {
		log.Printf("[reach] relay endpoints NOT configured: %v", err)
		log.Printf("[reach] the box will run without any relay tunnel — reachable on the LAN, " +
			"and publicly only if the DIRECT-IP listener is enabled")
		registerReachStatus(mux, nil)
		return nil
	}

	if set.Len() == 0 {
		// The expected posture for a box with a public IP, or a LAN-only box.
		registerReachStatus(mux, &reachRuntime{Set: set})
		return &reachRuntime{Set: set}
	}

	targets := make([]tunnel.Target, 0, set.Len())
	for _, ep := range set.All() {
		targets = append(targets, tunnel.Target{
			WSURL: ep.WSURL(),
			Name:  ep.Name,
			Token: ep.Token,
			Label: ep.BaseURL,
		})
	}

	agent, err := tunnel.NewAgent(tunnel.AgentConfig{
		Targets:   targets,
		Handler:   handler,
		Direct:    directEndpoint,
		UserAgent: "vulos/" + version,
		Logf:      log.Printf,
		// Feed link state back into the endpoint set, so the OTHER consumers
		// of the set (peer resolve, notify fan-out) automatically prefer a
		// relay whose tunnel is actually up. One health signal, shared.
		OnState: func(st tunnel.LinkStatus) {
			switch st.State {
			case tunnel.LinkUp:
				set.MarkUp(st.Label)
			case tunnel.LinkBackoff, tunnel.LinkRefused:
				set.MarkDown(st.Label, nil)
			}
		},
	})
	if err != nil {
		log.Printf("[reach] tunnel agent NOT started: %v", err)
		registerReachStatus(mux, &reachRuntime{Set: set})
		return &reachRuntime{Set: set}
	}

	log.Printf("[reach] %s (source: %s)", set.Describe(), src)
	if set.Len() == 1 {
		log.Printf("[reach] NOTE: one relay configured. Listing two under different operators in %s "+
			"removes it as a single point of failure — the agent holds a tunnel to every one at once.",
			reach.EnvEndpoints)
	}
	agent.Start(ctx)

	rt := &reachRuntime{Agent: agent, Set: set}

	// Let relayconfig report the LIVE tunnel state instead of guessing. This
	// is the injection seam that keeps relayconfig — which nearly every other
	// service imports for ICE — free of a dependency on the tunnel.
	relayconfig.SetIngressReporter(rt.ingressDescriptor)

	registerReachStatus(mux, rt)
	return rt
}

// ingressDescriptor summarises how this box is currently reachable, for
// Settings and status surfaces.
//
// It reports what is TRUE, not what was configured: a box whose relays are
// all in backoff says so, rather than showing a URL nobody is answering.
// Confident-but-wrong status is worse than no status, because it sends an
// operator looking for the problem somewhere else.
func (r *reachRuntime) ingressDescriptor() relayconfig.IngressDescriptor {
	if r == nil || r.Agent == nil {
		return relayconfig.IngressDescriptor{}
	}
	var up []string
	for _, l := range r.Agent.Status() {
		if l.State == tunnel.LinkUp && l.PublicURL != "" {
			up = append(up, l.PublicURL)
		}
	}
	if len(up) == 0 {
		return relayconfig.IngressDescriptor{
			Mode:   "relay-tunnel",
			Detail: "configured, but no relay tunnel is currently up — see GET /api/network/reach",
		}
	}
	detail := up[0]
	if len(up) > 1 {
		detail = strings.Join(up, ", ")
	}
	return relayconfig.IngressDescriptor{Mode: "relay-tunnel", Detail: detail}
}

// registerReachStatus exposes the reachability view for Settings and for
// `curl`-level debugging.
//
// It is mounted on the session-authed mux, and it is TOKEN-FREE by
// construction: LinkStatus and reach.Status both embed only redacted values,
// and reach.Endpoint.Redact is what produces them. This route is the reason
// those types exist in redacted form rather than being marshalled directly.
func registerReachStatus(mux *http.ServeMux, rt *reachRuntime) {
	mux.HandleFunc("GET /api/network/reach", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		out := map[string]any{
			"enabled":   false,
			"endpoints": []any{},
			"links":     []any{},
		}
		if rt != nil && rt.Set != nil {
			out["endpoints"] = rt.Set.Statuses()
			out["enabled"] = rt.Set.Len() > 0
		}
		if rt != nil && rt.Agent != nil {
			out["links"] = rt.Agent.Status()
		}
		_ = json.NewEncoder(w).Encode(out)
	})
}

// reachRelayBaseURLs returns the configured relay base URLs, in preference
// order, for the consumers that make plain HTTP calls to a relay rather than
// holding a tunnel to it — peer-reachability resolve and cross-instance
// notify fan-out.
//
// It reloads from the environment rather than closing over the set built at
// startup, because these callers run before (and independently of) the tunnel
// agent, and because a configuration error here must degrade to "no relays"
// rather than propagate a startup failure into an unrelated subsystem.
func reachRelayBaseURLs() []string {
	set, _, err := reach.FromEnv()
	if err != nil || set.Len() == 0 {
		return nil
	}
	out := make([]string, 0, set.Len())
	for _, ep := range set.Ordered() {
		out = append(out, ep.BaseURL)
	}
	return out
}
