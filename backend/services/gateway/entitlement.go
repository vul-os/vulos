package gateway

// entitlement.go — ENTITLE-01: billing/entitlement gating at app-dispatch.
//
// Before this, the gateway was all-or-nothing: any authenticated user could
// reach any installed app regardless of what their account is entitled to.
// This wires a per-app "required product" (from the app manifest's "product"
// field, via AllowApp) against the CP-introspected entitlements of the
// CURRENT request (auth.Handler.Middleware stamps
// X-Vulos-Entitlements-Products on a vk_-keyed request from the SAME
// apikey.Result the CP already returns — see internal/apikey).
//
// Gating is OFF by default (self-host/standalone stays fully open, matching
// today's behavior) and is enabled explicitly via SetEntitlementGating(true)
// — main.go wires this to true when DEPLOY_MODE is "os" or "cloud". When
// enabled, a request for an app that requires a product it does not carry is
// refused (fail CLOSED); a request with NO entitlement information at all
// (e.g. an ordinary browser session, which does not carry vk_-introspected
// products today) is allowed through for apps with NO required product, and
// refused for apps that DO require one — a plain session simply cannot prove
// an entitlement it was never given, so gated apps stay refused for it rather
// than silently trusting an unauthenticated claim.
//
// Header contract (internal seam, stripped before ever reaching an app — see
// stripInboundVulosHeaders/applyTrustedHeaders):
//
//	X-Vulos-Entitlements-Products: comma-separated product keys, e.g.
//	                                "os,mail,office-pro"

import (
	"net/http"
	"strings"
)

// entitlementsHeader carries the current request's CP-introspected products
// (set by auth.Handler.Middleware from a vk_ key's apikey.Result.Products).
// It lives in the X-Vulos-* namespace so it is ALWAYS stripped from the
// request the gateway forwards to an app (stripInboundVulosHeaders runs on
// every proxied request) — it is read here, before that stripping happens, by
// the gateway's own dispatch logic and never reaches app code.
const entitlementsHeader = "X-Vulos-Entitlements-Products"

// SetEntitlementGating turns app-dispatch entitlement gating on or off.
// Off (the default) leaves every app open to any authenticated user — the
// correct behavior for self-host/standalone. On (cloud/os deployments) an app
// with a required product (AllowApp) is refused to a request that doesn't
// carry it.
func (g *Gateway) SetEntitlementGating(enabled bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gateEntitlements = enabled
}

// AllowApp records the CP product/entitlement key required to use appID (from
// the app manifest's "product" field). An empty product means "no entitlement
// required" and is a no-op (the zero value already means open).
func (g *Gateway) AllowApp(appID, product string) {
	product = strings.TrimSpace(product)
	if appID == "" || product == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.appProducts == nil {
		g.appProducts = make(map[string]string)
	}
	g.appProducts[appID] = product
}

// ProductAllowed reports whether the current request carries the given CP
// product entitlement, gated the SAME way entitlementCheck is (off when
// gating is disabled; fail-closed otherwise). Unlike entitlementCheck it does
// not consult the appProducts registry — callers that already know the
// product they need (e.g. POST /api/store/install, checking the product
// declared in the manifest being installed, which isn't registered yet since
// the app isn't installed) pass it directly. An empty product always passes
// (no entitlement required).
func (g *Gateway) ProductAllowed(r *http.Request, product string) (bool, string) {
	product = strings.TrimSpace(product)
	if product == "" {
		return true, ""
	}
	g.mu.RLock()
	enabled := g.gateEntitlements
	g.mu.RUnlock()
	if !enabled {
		return true, ""
	}
	products := splitEntitlements(r.Header.Get(entitlementsHeader))
	for _, p := range products {
		if p == product {
			return true, ""
		}
	}
	return false, "requires product " + product
}

// entitlementCheck reports whether the current request is allowed to dispatch
// to appID, and a human-readable reason when it is not. It fails CLOSED: any
// ambiguity (gating enabled, app requires a product, request doesn't
// unambiguously carry it) refuses.
func (g *Gateway) entitlementCheck(r *http.Request, appID string) (bool, string) {
	g.mu.RLock()
	required, hasRequirement := g.appProducts[appID]
	g.mu.RUnlock()
	if !hasRequirement {
		return true, ""
	}
	return g.ProductAllowed(r, required)
}

func splitEntitlements(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
