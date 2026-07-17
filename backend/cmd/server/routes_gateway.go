package main

// routes_gateway.go — GATEWAY-01: owner-configurable control-plane ("gateway")
// URL surface. Lets a self-hoster point the OS at their own OSS control plane
// (vulos-management) from first-boot and from Settings, while managed users
// keep defaulting to Vulos Cloud with zero friction.
//
// Endpoints:
//
//	GET    /api/gateway        — current gateway {url, source, default, env_url, configured}
//	                             (public read: the CP base URL is not a secret and
//	                             the first-boot wizard reads it pre-session).
//	POST   /api/gateway/check  — validate + reachability-test a candidate URL
//	                             WITHOUT persisting. SSRF-guarded (gwurl.CheckReachable
//	                             dials through the shared safe dialer). Body {"url"}.
//	POST   /api/gateway        — SET + persist the override. Body {"url"}. GATED:
//	                             owner/admin session required once the box has an
//	                             owner; allowed unauthenticated ONLY during first-boot
//	                             (no owner yet), the same trust window as /api/setup/*
//	                             and /api/auth/register.
//	DELETE /api/gateway        — clear the override (revert to env/default). Same gate.
//
// Why the SET gate matters (security): a persisted gateway repoints EVERY CP
// broker — cloud login, signup, enrollment, identity claim. If a low-priv
// session could set it to an attacker's host, that host would harvest the
// owner's Vulos Cloud credentials on the next sign-in (a phishing/credential
// theft primitive). So SET is owner-only after setup. The reachability probe is
// additionally SSRF-scoped (https + public host unless VULOS_GATEWAY_ALLOW_PRIVATE).

import (
	"encoding/json"
	"net/http"

	"vulos/backend/services/gwurl"
)

// gatewaySetGate decides whether the caller may SET/CLEAR the gateway.
//
//   - isOwner reports whether the session user id is the box owner/admin.
//   - hasOwner reports whether ANY owner/admin account exists yet. During
//     first-boot (before an owner exists) SET is allowed unauthenticated so the
//     setup wizard can persist the chosen gateway before the cloud login runs —
//     the same open window setup itself uses. Once an owner exists, only that
//     owner may repoint the OS.
type gatewaySetGate struct {
	isOwner  func(userID string) bool
	hasOwner func() bool
}

func (g gatewaySetGate) allow(userID string) bool {
	if g.hasOwner != nil && !g.hasOwner() {
		return true // first-boot: no owner exists yet
	}
	return g.isOwner != nil && g.isOwner(userID)
}

// gatewayCheckMaxBody bounds the tiny JSON body for check/set.
const gatewayCheckMaxBody = 4 << 10

// registerGatewayRoutes wires the gateway configuration endpoints.
func registerGatewayRoutes(mux *http.ServeMux, gate gatewaySetGate) {
	mux.HandleFunc("GET /api/gateway", func(w http.ResponseWriter, r *http.Request) {
		url, src := gwurl.Resolved()
		writeJSON(w, map[string]any{
			"url":        url,
			"source":     string(src), // "default" | "env" | "configured"
			"default":    gwurl.Default,
			"env_url":    gwurl.EnvURL(),     // env var value, if any (diagnostic)
			"configured": gwurl.Configured(), // persisted override, if any
		})
	})

	// POST /api/gateway/check — dry-run validate + reachability probe. Public so
	// the first-boot wizard (no session yet) can test a candidate; the probe is
	// SSRF-scoped inside gwurl.CheckReachable.
	mux.HandleFunc("POST /api/gateway/check", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, gatewayCheckMaxBody)).Decode(&req); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "invalid request body"})
			return
		}
		clean, err := gwurl.CheckReachable(r.Context(), req.URL)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "url": clean})
	})

	// POST /api/gateway — persist the override (owner-gated after setup).
	mux.HandleFunc("POST /api/gateway", func(w http.ResponseWriter, r *http.Request) {
		if !gate.allow(r.Header.Get("X-User-ID")) {
			writeErr(w, http.StatusForbidden, "owner only")
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, gatewayCheckMaxBody)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := gwurl.Set(r.Context(), req.URL); err != nil {
			// Validation / unreachable / persist failures are the client's to fix.
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		url, src := gwurl.Resolved()
		writeJSON(w, map[string]any{"ok": true, "url": url, "source": string(src)})
	})

	// DELETE /api/gateway — revert to env/default (owner-gated after setup).
	mux.HandleFunc("DELETE /api/gateway", func(w http.ResponseWriter, r *http.Request) {
		if !gate.allow(r.Header.Get("X-User-ID")) {
			writeErr(w, http.StatusForbidden, "owner only")
			return
		}
		if err := gwurl.Clear(); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		url, src := gwurl.Resolved()
		writeJSON(w, map[string]any{"ok": true, "url": url, "source": string(src)})
	})
}
