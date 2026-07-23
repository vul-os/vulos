// health.go -- fail-closed health + liveness for the isolated idp service
// (IDENTITY-SERVICE §2.5). Fly (or any orchestrator) uses these to decide
// whether a machine may serve login traffic.
//
//	GET /livez   — process is up and serving (no dependency check). Always 200
//	               unless the process is wedged. Used as the LIVENESS probe so a
//	               transient DB blip does not get a healthy machine killed.
//	GET /healzz? — no; the readiness probe is /healthz below.
//	GET /healthz — READINESS: can this machine actually authenticate right now?
//	               It pings the auth DB (the ONLY dependency idp has). FAIL-CLOSED:
//	               if the auth DB is unreachable the machine reports 503 and Fly
//	               routes login away from it, rather than accepting logins it
//	               cannot serve. This is the honest "designed to stay up" contract:
//	               a machine that cannot reach the auth schema removes itself.
package idp

import (
	"context"
	"net/http"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
)

// contextWithTimeout derives a bounded context from the request so a hung
// dependency ping cannot wedge the readiness probe.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// healthPingTimeout bounds the auth-DB readiness ping so a hung DB connection
// cannot wedge the probe (which would let Fly keep sending it traffic).
const healthPingTimeout = 2 * time.Second

// RegisterHealth wires /livez (liveness) and /healthz (fail-closed readiness)
// onto mux. st may be nil only in degenerate test setups; a nil store makes
// /healthz report 503 (fail-closed), never 200.
func RegisterHealth(mux *http.ServeMux, st *auth.Store) {
	// Liveness: the process is running and the mux is serving. No dependency
	// check — a DB blip must not cause the machine to be killed and lose its
	// warm state; readiness (/healthz) handles temporary un-serviceability.
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"alive"}`))
	})

	// Readiness: can we authenticate? Ping the auth DB (idp's only dependency).
	// FAIL-CLOSED — any error → 503, so an unhealthy machine is taken out of the
	// login rotation instead of accepting logins it cannot complete.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if st == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable","reason":"no auth store"}`))
			return
		}
		ctx, cancel := contextWithTimeout(r, healthPingTimeout)
		defer cancel()
		if err := st.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable","reason":"auth db unreachable"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
}
