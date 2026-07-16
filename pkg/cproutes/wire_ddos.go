// wire_ddos.go — wires the DDoS defense layer into the control-plane server.
//
// All DDoS logic lives in pkg/ddos. Layers wired:
//  1. TrustedIP header config (Cloudflare-portable)
//  2. Per-IP sliding-window rate limit (IPRateLimiter)
//  3. Per-IP concurrent-connection cap (ConnCap)
//  4. Body-size caps + slow-loris defense (BodySizeLimiter)
//  5. Adaptive PoW captcha (CaptchaStore + CaptchaGate)
//  6. CIDR/IP blocklist (BlocklistStore + IsBlocked middleware)
//  7. GeoIP filter (GeoIPFilter + GeoIPMiddleware)
//  8. Tarpit + honeypot (honeypot routes + TarpitMiddleware)
//  9. Cost circuit breaker (BudgetCircuitBreaker)
//  10. Super-admin DDoS dashboard (DDoSPages)
//  11. Metrics collection (MetricsCollector + MetricsMiddleware)
package cproutes

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/ddos"
	flycosting "github.com/vul-os/vulos-management/pkg/superadmin/costing/fly"
)

// ddosEgressReader reads the latest managed-egress GB sample for the budget
// circuit breaker. A nil reader means "no data" (budget reader returns 0).
//
// COORDINATOR: the concrete implementation is internal/costing/tigris.Store
// (*tigriscosting.Store) in vulos-cloud — inject it (it satisfies this
// interface) rather than importing the cloud-internal package here.
type ddosEgressReader interface {
	LatestEgressGB(ctx context.Context) (float64, error)
}

// DDoSResult holds all DDoS layer handles for use by the composition root.
// Exported (with exported fields) so a commercial composition root can append
// the middleware stack, run the pollers, and close on shutdown.
type DDoSResult struct {
	// Middlewares is the stack to append to the main chain (after CSRF check).
	Middlewares []func(http.Handler) http.Handler

	// Blocklist is exposed for StartDDoSPollers.
	Blocklist *ddos.BlocklistStore

	// Closer must be called on server shutdown.
	Closer func()

	// Budget is exposed so StartDDoSPollers can start the hourly loop.
	Budget *ddos.BudgetCircuitBreaker

	// Metrics is the request metrics collector.
	Metrics *ddos.MetricsCollector
}

// WireDDoS opens the DDoS SQLite store, initialises all layers, registers
// honeypot + captcha + super-admin dashboard routes on mux, and returns the
// middleware stack to insert into the main middleware chain.
//
// flyStore and tigrisStore may be nil (no-op mode — budget readers return 0).
// Call after WireAuditLog (so the audit logger is available) and after the cost
// stores are open.
func WireDDoS(
	mux *http.ServeMux,
	dbDir string,
	al *auditlog.Logger,
	flyStore *flycosting.Store,
	tigrisStore ddosEgressReader,
) DDoSResult {
	// ── Blocklist store (ddos.db) ─────────────────────────────────────────────
	// COORDINATOR: needs setDBDirIfUnset (composition-root helper, routes_developer_keys.go)
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[ddos] WARNING: could not set DB dir (%v) — blocklist unavailable", err)
	}
	var bl *ddos.BlocklistStore
	ddosDB, ddosDBErr := cpdb.Open("ddos")
	if ddosDBErr != nil {
		log.Printf("[ddos] WARNING: could not open ddos cpdb (%v) — blocklist unavailable", ddosDBErr)
	} else {
		var blErr error
		bl, blErr = ddos.OpenBlocklistStore(ddosDB)
		if blErr != nil {
			_ = ddosDB.Close()
			log.Printf("[ddos] WARNING: could not open blocklist store (%v) — blocklist unavailable", blErr)
			bl = nil
		} else {
			log.Printf("[ddos] store opened (backend=%s)", ddosDB.Backend())
		}
	}

	// ── Metrics collector ─────────────────────────────────────────────────────
	mc := ddos.NewMetricsCollector()

	// ── IP rate limiter (sliding window) ─────────────────────────────────────
	limiter := ddos.NewIPRateLimiter(ddos.DefaultPolicies)

	// ── Captcha store ─────────────────────────────────────────────────────────
	captchaStore := ddos.NewCaptchaStore(limiter)

	// ── GeoIP filter ─────────────────────────────────────────────────────────
	geoFilter, _ := ddos.OpenGeoIPFilter()

	// ── Budget circuit breaker ────────────────────────────────────────────────
	// Real readers backed by the fly/tigris cost stores.  When the stores are nil
	// (env vars unset) the functions return 0 so the breaker doesn't trip
	// erroneously.
	budgetReaders := ddos.BudgetReaders{
		FlyInstanceCount: func(ctx context.Context) (int, error) {
			if flyStore == nil {
				return 0, nil
			}
			return flyStore.LatestMachineCount(ctx)
		},
		ManagedEgressGB: func(ctx context.Context) (float64, error) {
			if tigrisStore == nil {
				return 0, nil
			}
			return tigrisStore.LatestEgressGB(ctx)
		},
	}
	budget := ddos.NewBudgetCircuitBreaker(ddos.DefaultBudgetConfig(), budgetReaders, bl, al)

	// ── Honeypot routes ───────────────────────────────────────────────────────
	if bl != nil {
		ddos.RegisterHoneypotRoutes(mux, bl, al)
	}

	// ── Captcha challenge endpoint ────────────────────────────────────────────
	mux.HandleFunc("GET /api/captcha/challenge", ddos.HandleCaptchaChallenge(captchaStore))
	log.Println("[ddos] captcha endpoint: GET /api/captcha/challenge")

	// ── DDoS pages bundle ─────────────────────────────────────────────────────
	pages := &ddos.DDoSPages{
		Metrics:   mc,
		Blocklist: bl,
		Captcha:   captchaStore,
		GeoIP:     geoFilter,
		Budget:    budget,
		AuditLog:  al,
	}

	// Super-admin DDoS dashboard — gated by RequireSuperAdminStub (same pattern
	// as wire_superadmin_data.go). Replace with real RequireSuperAdmin when
	// integrating with the superadmin core agent.
	// COORDINATOR: needs RequireSuperAdminStub (composition-root, wire_superadmin_data.go)
	mux.HandleFunc("GET /superadmin/ddos", RequireSuperAdminStub(pages.HandleDDoSDashboard))
	mux.HandleFunc("POST /superadmin/ddos/toggle-captcha",
		RequireSuperAdminStub(pages.HandleDDoSToggleCaptcha))
	mux.HandleFunc("POST /superadmin/ddos/toggle-halt",
		RequireSuperAdminStub(pages.HandleDDoSToggleHalt))
	mux.HandleFunc("GET /api/superadmin/ddos/blocklist",
		RequireSuperAdminStub(pages.HandleDDoSBlocklistList))
	mux.HandleFunc("POST /api/superadmin/ddos/blocklist",
		RequireSuperAdminStub(pages.HandleDDoSBlocklistAdd))
	mux.HandleFunc("DELETE /api/superadmin/ddos/blocklist/{cidr}",
		RequireSuperAdminStub(pages.HandleDDoSBlocklistRemove))
	mux.HandleFunc("POST /api/superadmin/ddos/geo-block",
		RequireSuperAdminStub(pages.HandleDDoSGeoBlockUpdate))

	// Form-based helpers (HTML form POST → redirect back to dashboard).
	mux.HandleFunc("POST /api/superadmin/ddos/blocklist-form",
		RequireSuperAdminStub(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			cidr := r.FormValue("cidr")
			reason := r.FormValue("reason")
			if bl != nil && cidr != "" {
				if addErr := bl.Add(r.Context(), cidr, reason, "superadmin", nil); addErr != nil {
					log.Printf("[ddos/blocklist] form add: %v", addErr)
				}
				if al != nil {
					_ = al.Record(r.Context(), "superadmin", "ddos.blocklist.add", cidr,
						map[string]string{"reason": reason})
				}
			}
			http.Redirect(w, r, "/superadmin/ddos", http.StatusSeeOther)
		}))
	mux.HandleFunc("POST /api/superadmin/ddos/geo-block-form",
		RequireSuperAdminStub(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			countries := r.FormValue("countries")
			codes := splitDDoSCSV(countries)
			geoFilter.SetBlockedCountries(codes)
			if al != nil {
				_ = al.Record(r.Context(), "superadmin", "ddos.geo_block.update", "geoip",
					map[string]string{"countries": countries})
			}
			http.Redirect(w, r, "/superadmin/ddos", http.StatusSeeOther)
		}))

	log.Println("[ddos] super-admin routes registered: /superadmin/ddos + API endpoints")
	log.Println("[ddos] all 11 DDoS defense layers initialised")

	// ── Middleware stack ───────────────────────────────────────────────────────
	// These are returned to the composition root to be appended to the global
	// middleware chain.
	// Order: metrics → blocklist → geoip → body-size → conncap → iprate → tarpit → captcha.
	mws := []func(http.Handler) http.Handler{
		ddos.MetricsMiddleware(mc),
		ddosBlocklistMiddleware(bl),
		ddos.GeoIPMiddleware(geoFilter),
		ddos.BodySizeLimiter(ddos.DefaultBodySizeConfig),
		ddos.ConnCap(ddos.DefaultConnCapConfig),
		ddos.IPRateLimit(limiter),
		ddos.TarpitMiddleware,
		ddos.CaptchaGate(captchaStore),
	}

	closer := func() {
		if bl != nil {
			if err := bl.Close(); err != nil {
				log.Printf("[ddos] blocklist close: %v", err)
			}
		}
		log.Println("[ddos] shutdown")
	}

	return DDoSResult{
		Middlewares: mws,
		Blocklist:   bl,
		Closer:      closer,
		Budget:      budget,
		Metrics:     mc,
	}
}

// StartDDoSPollers starts the budget circuit breaker and blocklist sweeper.
func StartDDoSPollers(ctx context.Context, r DDoSResult) {
	go r.Budget.RunHourly(ctx)
	log.Println("[ddos] budget circuit breaker hourly poller started")

	if r.Blocklist != nil {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := r.Blocklist.SweepExpired(ctx); err != nil {
						log.Printf("[ddos] blocklist sweep: %v", err)
					}
				}
			}
		}()
		log.Println("[ddos] blocklist expiry sweeper started (5min)")
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// ddosBlocklistMiddleware returns a middleware that short-circuits with 403 for blocked IPs.
func ddosBlocklistMiddleware(bl *ddos.BlocklistStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if bl != nil && bl.IsBlocked(ddos.RealClientIP(r)) {
				log.Printf("[ddos/blocklist] 403 ip=%s path=%s", ddos.RealClientIP(r), r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// splitDDoSCSV splits a comma-separated string, trimming whitespace.
func splitDDoSCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
