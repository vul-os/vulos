package georoute

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
)

// MiddlewareConfig tunes Middleware behaviour.
type MiddlewareConfig struct {
	// TenantHeader is the request header that identifies the tenant. Default
	// "X-Tenant-ID" (matches the rest of cp's per-tenant call sites).
	TenantHeader string
	// SyncKey is the per-tenant sync_key passed to SyncTrigger on failover.
	// Default "default" — callers with multiple stores can override.
	SyncKey string
	// SkipPaths is a list of URL path prefixes to bypass entirely (health
	// endpoints, static assets, etc.). Each prefix is matched with
	// strings.HasPrefix. Default skips: "/api/ha/health", "/healthz",
	// "/readyz", "/metrics" — these must NEVER be geo-redirected.
	SkipPaths []string
}

func (c *MiddlewareConfig) withDefaults() {
	if c.TenantHeader == "" {
		c.TenantHeader = "X-Tenant-ID"
	}
	if c.SyncKey == "" {
		c.SyncKey = "default"
	}
	if len(c.SkipPaths) == 0 {
		c.SkipPaths = []string{
			"/api/ha/health",
			"/healthz",
			"/readyz",
			"/metrics",
		}
	}
}

// Middleware wraps next with the geo-route 307 redirect.
//
// Behaviour for each request:
//
//  1. Skip if the path matches any cfg.SkipPaths prefix (health/metrics
//     endpoints must never bounce).
//  2. Skip if no X-Tenant-ID header (unauthenticated / pre-tenant routes —
//     signup, public APIs — are served locally).
//  3. Look up the tenant's effective region (Failover-aware). If the
//     effective region == LocalRegion: pass through to next.
//  4. Otherwise emit HTTP 307 Temporary Redirect to
//     {effectiveRegion.Endpoint}{r.URL.RequestURI()} with X-Georoute=WireProto.
//
// 307 (not 302) preserves the request method — see contract.go package doc.
//
// Errors from the Store / Trigger never fail the request: they log and pass
// through to next (fail-open). Geo-routing is an optimisation, not a
// correctness constraint — the underlying request handlers still work
// against the local region.
func Middleware(svc *Service, cfg MiddlewareConfig) func(http.Handler) http.Handler {
	cfg.withDefaults()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if shouldSkip(r.URL.Path, cfg.SkipPaths) {
				next.ServeHTTP(w, r)
				return
			}
			tenantID := r.Header.Get(cfg.TenantHeader)
			if tenantID == "" {
				// Pre-tenant request — pass through.
				next.ServeHTTP(w, r)
				return
			}
			ctx := r.Context()
			region, didFailover, err := svc.Failover(ctx, tenantID, cfg.SyncKey)
			if err != nil {
				// Trigger error is best-effort; only ErrUnknownTenant is a real
				// "no row" — log + pass through (fail-open).
				if !errors.Is(err, ErrUnknownTenant) {
					log.Printf("[georoute] failover %s: %v", tenantID, err)
				}
				next.ServeHTTP(w, r)
				return
			}
			if region == "" || region == svc.LocalRegion() {
				next.ServeHTTP(w, r)
				return
			}
			endpoint := svc.EndpointForRegion(region)
			if endpoint == "" {
				next.ServeHTTP(w, r)
				return
			}
			target := strings.TrimRight(endpoint, "/") + r.URL.RequestURI()
			w.Header().Set("X-Georoute", WireProto)
			w.Header().Set("X-Georoute-Region", region)
			if didFailover {
				w.Header().Set("X-Georoute-Failover", "1")
			}
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		})
	}
}

// shouldSkip reports whether path matches any prefix in skips.
func shouldSkip(path string, skips []string) bool {
	for _, p := range skips {
		if p != "" && strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// HandlerNoop returns a Middleware-compatible no-op wrapper used when
// CP_GEOROUTE_ENABLE is unset. This keeps the wiring code in main.go a
// straight call regardless of the opt-in flag.
func HandlerNoop() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}

// HomeRegionContextKey is the request-context key the middleware (when
// extended) can stash the resolved home region under. Kept here so handler
// code can pull it without import-cycling on a separate ctx package.
type homeRegionKey struct{}

// HomeRegionContextKey is the public sentinel value to look up under.
var HomeRegionContextKey = homeRegionKey{}

// WithHomeRegion returns ctx annotated with the resolved home region.
func WithHomeRegion(ctx context.Context, region string) context.Context {
	return context.WithValue(ctx, HomeRegionContextKey, region)
}

// HomeRegionFromContext returns the region annotated by WithHomeRegion, or
// "" when none was set.
func HomeRegionFromContext(ctx context.Context) string {
	v, _ := ctx.Value(HomeRegionContextKey).(string)
	return v
}
