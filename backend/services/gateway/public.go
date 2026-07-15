package gateway

// public.go — PUBWEB-SEC-01: the anonymous public-web entrypoint.
//
// Finding 1 (HIGH): the public-visibility subdomain path and the custom-domain
// path previously routed the edge (Caddy/nginx) straight to the app's network
// namespace (127.0.0.1:hostPort), BYPASSING this :8080 auth gateway. That path
// never stripped attacker-supplied X-Vulos-* headers and never enforced
// visibility, so anyone on the public internet could set X-Vulos-User-ID and be
// trusted as any user by an identity-aware app, and could reach apps that were
// never published.
//
// The fix routes the public/custom-domain upstream THROUGH the gateway at this
// dedicated handler. It is mounted at appnet.PubwebPathPrefix and is the ONLY
// gateway entrypoint that serves without a session — and it does so with an
// explicit anonymous posture:
//
//   - it serves an app ONLY when publicVisibility reports it is "public"
//     (explicit per-app opt-in); private/local apps get 404;
//   - it strips ALL inbound X-Vulos-* headers (anti-spoof) and injects NONE —
//     no identity is ever attached, so an app on this path always sees an
//     anonymous request and cannot be tricked into trusting a forged user;
//   - it strips ALL X-Vulos-* from the response so an app cannot leak or forge
//     seam headers back through the edge;
//   - it rate-limits per app exactly like the authenticated path.
//
// Adopted external ports (adopt-a-port) are deliberately NOT reachable here:
// this handler resolves only real app namespaces (GetAnyForApp), never the
// externalUpstreams registry, so an adopted loopback service can never be
// exposed to the public web even if its visibility is flipped to "public".

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"vulos/backend/services/appnet"
)

// PublicHandler returns the anonymous public-web reverse proxy. It is mounted at
// appnet.PubwebPathPrefix (e.g. /__pubweb__/{appID}/...). The edge terminates
// public TLS and forwards here over loopback; this handler enforces the public
// opt-in and the anti-spoof stripping described above.
func (g *Gateway) PublicHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Parse "/__pubweb__/{appID}/{rest}".
		rest := strings.TrimPrefix(r.URL.Path, appnet.PubwebPathPrefix)
		if rest == r.URL.Path {
			// Prefix not present — not a public-web request.
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		appID := rest
		appPath := "/"
		if slash := strings.Index(rest, "/"); slash >= 0 {
			appID = rest[:slash]
			appPath = rest[slash:]
		}
		if appID == "" {
			http.Error(w, `{"error":"missing app id"}`, http.StatusBadRequest)
			return
		}

		// PUBWEB opt-in (fail closed): only apps explicitly published to the
		// public web may be served anonymously. Anything else is 404 — we do not
		// distinguish "private" from "does not exist" to avoid leaking which apps
		// are installed.
		g.mu.RLock()
		pub := g.publicVisibility
		g.mu.RUnlock()
		if pub == nil || !pub(appID) {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}

		// Rate limit per app (shared bucket with the authenticated path).
		if g.isRateLimited(appID) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
			return
		}

		// Resolve the owner's running namespace for this app. Anonymous public
		// traffic has no user, so we take any active namespace serving appID.
		ns, ok := g.netMgr.GetAnyForApp(appID)
		if !ok {
			http.Error(w, `{"error":"app not running"}`, http.StatusNotFound)
			return
		}

		upstream := fmt.Sprintf("http://%s:%d%s", ns.NSIP, ns.AppPort, appPath)
		if r.URL.RawQuery != "" {
			upstream += "?" + r.URL.RawQuery
		}

		// WebSocket upgrade — reuse the authenticated WS proxy but with a nil
		// session so applyPublicHeaders (below) is what runs. We cannot call
		// proxyWebSocket (it requires a session); public apps needing WS on the
		// public path are out of scope, so refuse the upgrade rather than leak an
		// identity-injecting path.
		if isWebSocketUpgrade(r) {
			http.Error(w, `{"error":"websockets are not served on the public-web path"}`, http.StatusBadGateway)
			return
		}

		proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, r.Body)
		if err != nil {
			http.Error(w, `{"error":"proxy error"}`, http.StatusInternalServerError)
			return
		}
		for k, vv := range r.Header {
			for _, v := range vv {
				proxyReq.Header.Add(k, v)
			}
		}
		// ANONYMOUS posture: strip ALL inbound X-Vulos-* (anti-spoof) and inject
		// NOTHING. The app sees no identity headers at all.
		stripInboundVulosHeaders(proxyReq.Header)
		proxyReq.Header.Del("Cookie")
		proxyReq.Header.Del("Host")
		proxyReq.Host = fmt.Sprintf("localhost:%d", ns.AppPort)

		resp, err := g.client.Do(proxyReq)
		if err != nil {
			log.Printf("[gateway/public] proxy to %s failed: %v", appID, err)
			http.Error(w, `{"error":"app unreachable"}`, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		// Strip any X-Vulos-* the app tried to set on the response so it can never
		// leak or forge seam headers back through the edge.
		stripInboundVulosHeaders(w.Header())
		w.Header().Del("Set-Cookie")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Vulos-App", appID)

		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}
