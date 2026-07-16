// Package middleware provides HTTP middleware for the vulos.cloud control-plane.
//
// All middlewares implement func(http.Handler) http.Handler so they compose
// uniformly via Chain. Order of wrapping in Chain determines order of execution
// (outermost first, i.e. Chain(h, A, B, C) → A → B → C → h).
package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/vul-os/vulos-management/pkg/env"
)

// ctxKey is an unexported type for context keys in this package to avoid
// collision with other packages.
type ctxKey int

const (
	ctxRequestID ctxKey = iota
)

// Chain wraps h with the given middlewares. The first middleware is outermost
// (runs first on the way in, last on the way out).
//
//	Chain(h, A, B, C)  →  A(B(C(h)))
func Chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// RequestIDFromContext returns the request ID stored by RequestID middleware,
// or empty string if not present.
func RequestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxRequestID).(string)
	return v
}

// RequestID echoes the incoming X-Request-ID header (if non-empty) or
// generates a fresh UUID v4. The value is set on both the response header and
// the request context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), ctxRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code for
// logging purposes.
type responseWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (rw *responseWriter) WriteHeader(status int) {
	if !rw.wrote {
		rw.status = status
		rw.wrote = true
	}
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wrote {
		rw.status = http.StatusOK
		rw.wrote = true
	}
	return rw.ResponseWriter.Write(b)
}

// Unwrap exposes the underlying ResponseWriter for http.ResponseController.
func (rw *responseWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

func statusCode(rw *responseWriter) int {
	if rw.status == 0 {
		return http.StatusOK
	}
	return rw.status
}

// PanicRecovery catches any panic in downstream handlers, logs the stack trace,
// and returns a 500 JSON response. The process is NOT crashed.
func PanicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				reqID := RequestIDFromContext(r.Context())
				log.Printf("[cp] PANIC req_id=%s method=%s path=%s: %v\n%s",
					reqID, r.Method, r.URL.Path, p, debug.Stack())
				// Only write the error if headers have not been sent yet.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "internal server error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// AccessLog emits a structured single-line log entry for every request:
//
//	[cp] method=GET path=/healthz status=200 dur=1.23ms req_id=<uuid>
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 0}
		next.ServeHTTP(rw, r)
		dur := time.Since(start)
		reqID := RequestIDFromContext(r.Context())
		log.Printf("[cp] method=%s path=%s status=%d dur=%s req_id=%s",
			r.Method, r.URL.Path, statusCode(rw), fmt.Sprintf("%.2fms", float64(dur.Nanoseconds())/1e6), reqID)
	})
}

// CORS applies cross-origin headers.
//
// Security policy:
//   - GET/HEAD: wildcard origin allowed (safe, read-only).
//   - OPTIONS preflight for GET/HEAD: wildcard origin allowed.
//   - OPTIONS preflight for POST/PUT/DELETE: no wildcard origin. The
//     browser will use the reflected origin only when the caller supplies an
//     explicit Origin header that the server has pre-approved (not done here by
//     default). State-changing auth endpoints are additionally protected by
//     SameSite=Lax session cookies, which the browser will not send on
//     cross-site requests, providing effective CSRF protection on modern browsers.
//   - POST/PUT/DELETE/PATCH (non-preflight): no wildcard origin.
//
// Individual handlers that genuinely need cross-origin POST support (e.g. the
// public billing rates endpoint) set their own Access-Control-Allow-Origin
// header before writing the response body.
func CORS(next http.Handler) http.Handler {
	return CORSWithOrigins(nil)(next)
}

// CORSWithOrigins is like CORS but additionally reflects the Origin header back
// as Access-Control-Allow-Origin when the request origin matches one of the
// explicitly allowed origins. This enables cross-origin POST support for
// trusted first-party frontends (e.g. http://localhost:5173 in local dev or
// https://app.vulos.org in prod) without opening a wildcard for all origins.
//
// Pass nil (or an empty slice) to use the default behaviour (wildcard for safe
// methods only, no allowed origins for state-changing methods).
func CORSWithOrigins(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Always advertise the allowed methods so browsers can plan.
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")

			origin := r.Header.Get("Origin")

			// If the request comes from an explicitly allowed origin, reflect it
			// back so state-changing cross-origin requests can succeed. This
			// covers both preflight OPTIONS and actual mutation methods.
			if origin != "" && allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Vary", "Origin")
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			switch r.Method {
			case http.MethodGet, http.MethodHead:
				// Safe methods: allow any origin to read public API data.
				w.Header().Set("Access-Control-Allow-Origin", "*")

			case http.MethodOptions:
				// Preflight: grant wildcard only for safe-method preflights.
				reqMethod := r.Header.Get("Access-Control-Request-Method")
				if reqMethod == http.MethodGet || reqMethod == http.MethodHead || reqMethod == "" {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				}
				// For POST/PUT/DELETE preflights we do NOT set a wildcard origin.
				// The browser will block the subsequent request unless the caller is
				// on an explicitly allowed origin (none here — by design).
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// POST/PUT/DELETE/PATCH: no wildcard CORS. State-changing endpoints are
			// protected by SameSite=Lax cookies (CSRF mitigation).
			next.ServeHTTP(w, r)
		})
	}
}

// SecureHeaders adds defensive HTTP response headers to every response:
//   - X-Content-Type-Options: nosniff  — prevents MIME-type sniffing
//   - X-Frame-Options: DENY           — prevents clickjacking via iframes
//   - Referrer-Policy: strict-origin-when-cross-origin
//   - Content-Security-Policy: default-src 'self'
//
// Handlers that serve richer content (e.g. the SPA shell) may override CSP by
// setting Content-Security-Policy before calling next.ServeHTTP.
func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// Strict CSP for API responses. The SPA's index.html is served by the
		// frontend (Vite/CDN) and sets its own CSP; this covers the control-plane
		// API server which only emits JSON.
		if h.Get("Content-Security-Policy") == "" {
			h.Set("Content-Security-Policy", "default-src 'none'")
		}
		next.ServeHTTP(w, r)
	})
}

// IsAllowedOrigin reports whether origin is permitted for state-changing
// requests. It applies two rules:
//
//  1. Exact match against the explicit allowlist (existing behaviour).
//  2. Subdomain-of-parentDomain match when parentDomain is non-empty: any
//     HTTPS (or HTTP in dev) origin whose host is a subdomain of parentDomain
//     is accepted. For example, parentDomain="vulos.org" permits
//     https://mail.vulos.org, https://app.vulos.org, etc.
//
// The scheme rule is: only "https" origins satisfy the parent-domain rule
// unless VULOS_ENV is not "prod" (where "http" is also accepted for local
// testing).
func IsAllowedOrigin(origin string, allowlist []string, parentDomain string) bool {
	if origin == "" {
		return false
	}
	// Trim a trailing slash on the Origin for symmetry with configured values.
	origin = strings.TrimSuffix(origin, "/")

	// Rule 1: explicit allowlist (exact match or scheme://host prefix of a Referer URL).
	for _, o := range allowlist {
		if o == "" {
			continue
		}
		if origin == o {
			return true
		}
	}
	// Rule 1b: full URL (Referer) — compare scheme://host only.
	u, err := url.Parse(origin)
	if err == nil && u.Scheme != "" && u.Host != "" {
		schemeHost := u.Scheme + "://" + u.Host
		for _, o := range allowlist {
			if schemeHost == o {
				return true
			}
		}
	}

	// Rule 2: subdomain of parentDomain.
	// VULOS_COOKIE_DOMAIN is documented (auth/session.go) as validly taking a
	// LEADING DOT (".vulos.org") as well as a bare apex. Normalise it: without this,
	// a leading-dot value makes the checks below `host == ".vulos.org"` (never true)
	// and suffix `"..vulos.org"` (never matches), silently rejecting every
	// legitimate subdomain origin and CSRF-blocking the whole app on a valid config.
	parentDomain = strings.TrimPrefix(strings.TrimSpace(parentDomain), ".")
	if parentDomain == "" {
		return false
	}
	if u == nil || err != nil {
		return false
	}
	if u.Scheme == "" || u.Host == "" {
		return false
	}
	// Scheme restriction: require https in prod; allow http elsewhere for localhost.
	// Prod is decided by the single authoritative, fail-safe resolver.
	isProd := env.IsProdResolved()
	if isProd && u.Scheme != "https" {
		return false
	}

	// Strip port from host if present.
	host := u.Host
	if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = h
	}

	// Accept exact apex or any subdomain.
	if host == parentDomain {
		return true
	}
	if strings.HasSuffix(host, "."+parentDomain) {
		return true
	}
	return false
}

// CSRFOriginCheck guards state-changing requests (POST/PUT/PATCH/DELETE) by
// requiring that either the Origin or Referer header matches one of the
// configured allowed origins. This is the cheap modern-browser-safe CSRF
// defence: every fetch initiated by JavaScript carries Origin, and every
// browser-driven form POST carries at least a Referer. Requests without either
// header are accepted only for read-only methods.
//
// Mismatches are logged (with the offending Origin/Referer) and the request is
// refused with 403. The middleware is a no-op for GET/HEAD/OPTIONS so that
// browser preflights and public reads continue to work.
//
// The middleware also accepts any subdomain of VULOS_COOKIE_DOMAIN when that
// env var is set (cross-subdomain SSO support).
//
// Pass nil or an empty slice to disable the check (e.g. for local dev when no
// stable origin can be enumerated). When disabled the middleware logs a single
// warning at construction time and otherwise behaves as a pass-through.
// csrfExemptPaths are POST endpoints that are EXCLUDED from the CSRF
// Origin/Referer requirement because they are server-to-server device
// endpoints with NO cookie authentication — the RFC 8628 device grant
// (device_code / device_pubkey) IS the credential, so there is no ambient
// browser credential for a cross-site request to ride on (CSRF does not
// apply). A Vulos box's enrollment client (vulos/backend/services/cloudenroll)
// legitimately sends neither Origin nor Referer; without this exemption a box
// can NEVER enroll against a CSRF-enabled CP (UNIFIED-SIGNIN E2E finding).
//
// Deliberately NOT exempted: /enroll/approve and /enroll/deny — those are
// session-cookie-authenticated owner actions and must keep full CSRF cover.
//
// /api/billing/webhook is exempt for exactly the same reason, and its absence was
// a REAL BUG: Paystack is a server-to-server caller that sends NEITHER Origin nor
// Referer, so this middleware rejected every webhook with a 403 before the handler
// ever ran — in any environment with CORSOrigins configured (i.e. prod). The CP
// would never have learned about a single payment: no tier grant on charge.success,
// no dunning on charge.failed. (Found by dev/smoke.sh, which now self-signs a
// webhook against the local stack.)
//
// CSRF does not apply to it: there is no cookie/ambient browser credential for a
// cross-site request to ride on. The CREDENTIAL IS THE SIGNATURE — an HMAC-SHA512
// over the raw body, verified fail-closed in billing.WebhookHandler (an unset
// secret 503s rather than accepting HMAC(key=""), and a bad signature 400s). A
// cross-site POST here gains an attacker nothing they could not already do by
// curling the endpoint directly, and without the secret it is rejected either way.
var csrfExemptPaths = map[string]bool{
	"/enroll/start":        true,
	"/enroll/poll":         true,
	"/api/billing/webhook": true,
	// Cookie-less server-to-server relay/PoP endpoints. Each is authenticated by
	// an HMAC-SHA256 signature keyed on CP_SHARED_SECRET (X-Pop-Sig) and fails
	// CLOSED when the secret is unset, so — exactly like the enroll/webhook
	// exemptions above — a cross-site POST here gains an attacker nothing they
	// could not do by curling directly, while a legitimate relay PoP (curl-style,
	// no Origin/Referer) would otherwise be rejected and could never register or
	// meter usage. See routes_relayusage.go / routes_routing_dns.go.
	"/api/relay/usage":         true,
	"/api/pop/heartbeat":       true,
	"/api/relay/pop/register":  true,
	"/api/relay/pop/heartbeat": true,
}

func CSRFOriginCheck(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o != "" {
			allowed[o] = true
		}
	}

	if len(allowed) == 0 {
		if env.IsProdResolved() {
			log.Fatalf("[csrf] CSRFOriginCheck cannot be enabled in prod with empty CORSOrigins")
		}
		log.Println("[csrf] WARNING: no allowed origins configured — CSRF Origin/Referer check is DISABLED")
		return func(next http.Handler) http.Handler { return next }
	}

	parentDomain := os.Getenv("VULOS_COOKIE_DOMAIN")

	checkOrigin := func(raw string) bool {
		return IsAllowedOrigin(raw, allowedOrigins, parentDomain)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}

			// Cookie-less S2S device endpoints (see csrfExemptPaths). Compare
			// against the CLEANED path so "/enroll/../x" cannot ride the
			// exemption (mirrors the OS session middleware's traversal guard).
			if csrfExemptPaths[path.Clean(r.URL.Path)] {
				next.ServeHTTP(w, r)
				return
			}

			origin := r.Header.Get("Origin")
			referer := r.Header.Get("Referer")

			// Same-origin requests from non-browser clients (curl, server-to-
			// server) may legitimately omit both headers. We require AT LEAST
			// one to be present and match — refuse otherwise.
			if origin == "" && referer == "" {
				reqID := RequestIDFromContext(r.Context())
				log.Printf("[csrf] reject method=%s path=%s reason=no-origin-or-referer req_id=%s",
					r.Method, r.URL.Path, reqID)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "missing Origin/Referer header"})
				return
			}

			ok := false
			if origin != "" && checkOrigin(origin) {
				ok = true
			}
			if !ok && referer != "" && checkOrigin(referer) {
				ok = true
			}
			if !ok {
				reqID := RequestIDFromContext(r.Context())
				log.Printf("[csrf] reject method=%s path=%s origin=%q referer=%q req_id=%s",
					r.Method, r.URL.Path, origin, referer, reqID)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "origin not allowed"})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RateLimitConfig holds tunables for RateLimit.
type RateLimitConfig struct {
	// RequestsPerSecond is the sustained fill rate of the token bucket per IP.
	RequestsPerSecond float64
	// Burst is the maximum burst size (bucket capacity).
	Burst int
}

// DefaultRateLimitConfig is a sane default: 20 req/s sustained, burst 50.
var DefaultRateLimitConfig = RateLimitConfig{
	RequestsPerSecond: 20,
	Burst:             50,
}

// bucket is a per-IP token bucket.
type bucket struct {
	mu       sync.Mutex
	tokens   float64
	lastSeen time.Time
}

// allow returns true if the request should be allowed and consumes one token.
func (b *bucket) allow(rps float64, burst int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.lastSeen = now
	b.tokens += elapsed * rps
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// RateLimit returns a per-IP token-bucket rate-limiting middleware. IPs that
// exceed the configured rate receive 429 Too Many Requests with a JSON body.
// Buckets are garbage-collected lazily (entries older than 5 minutes removed on
// each new request from a different IP).
func RateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	var (
		mu      sync.Mutex
		buckets = map[string]*bucket{}
		lastGC  time.Time
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}

			mu.Lock()
			// Lazy GC: evict buckets not seen for > 5 minutes.
			now := time.Now()
			if now.Sub(lastGC) > 5*time.Minute {
				for k, bkt := range buckets {
					bkt.mu.Lock()
					idle := now.Sub(bkt.lastSeen)
					bkt.mu.Unlock()
					if idle > 5*time.Minute {
						delete(buckets, k)
					}
				}
				lastGC = now
			}
			bkt, ok := buckets[ip]
			if !ok {
				bkt = &bucket{tokens: float64(cfg.Burst), lastSeen: now}
				buckets[ip] = bkt
			}
			mu.Unlock()

			if !bkt.allow(cfg.RequestsPerSecond, cfg.Burst) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// IPRateLimitFromDDoS wraps a ddos-package IPRateLimit middleware as a standard
// middleware.Chain-compatible func. This is a thin shim so callers can wire the
// DDoS sliding-window limiter without importing the ddos package directly in main.go.
//
// Usage in wire_ddos.go:
//
//	limiter := ddos.NewIPRateLimiter(ddos.DefaultPolicies)
//	mws = append(mws, middleware.IPRateLimitFromDDoS(ddos.IPRateLimit(limiter)))
func IPRateLimitFromDDoS(ddosMW func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return ddosMW
}
