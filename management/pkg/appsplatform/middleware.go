package appsplatform

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ctxKey is the unexported context key type for values the platform stores.
type ctxKey int

const ctxApp ctxKey = iota

// AdminAuthFunc lets a product reuse ITS OWN session auth for the management
// API. Given the request it returns the caller's account id, whether the caller
// is an admin (can manage every app, not just their own), and ok=false to reject
// (the platform then responds 401). The platform never sees the product's
// session mechanism — only this adapter.
type AdminAuthFunc func(r *http.Request) (ownerID string, isAdmin bool, ok bool)

// TokenAuth is the runtime Bearer-token middleware. It expects an
// "Authorization: Bearer <app-token>" header, looks the app up by the token's
// sha256 HASH in the registry (constant-time; the plaintext is never stored),
// and on success stashes the app in the request context. On any miss it responds
// 401; a registry error responds 503. Expired tokens (absolute TokenExpiresAt or
// age-based TokenTTL) are rejected with 401.
func TokenAuth(reg Registry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "app token required"})
			return
		}
		app, err := reg.GetByTokenHash(HashToken(token))
		if err != nil || app == nil {
			if err != nil && !errors.Is(err, ErrNotFound) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "app registry unavailable"})
				return
			}
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid app token"})
			return
		}
		// Absolute expiry: set explicitly at token generation / rotation time.
		if !app.TokenExpiresAt.IsZero() && time.Now().After(app.TokenExpiresAt) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "token expired"})
			return
		}
		// Age-based revocation: retroactive policy — any token older than
		// TokenTTL is rejected even if it was minted before the policy was set.
		if app.TokenTTL > 0 && !app.TokenIssuedAt.IsZero() && time.Since(app.TokenIssuedAt) > app.TokenTTL {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "token expired"})
			return
		}
		ctx := context.WithValue(r.Context(), ctxApp, app)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AppFromContext returns the authenticated app set by TokenAuth.
func AppFromContext(ctx context.Context) (*App, bool) {
	a, ok := ctx.Value(ctxApp).(*App)
	return a, ok
}

// bearerToken extracts a Bearer token from the Authorization header only. Unlike
// a session, the runtime API does not accept a cookie.
func bearerToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	return ""
}

// clientIP extracts the client IP from RemoteAddr, stripping the port.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- Rate limiting ----------------------------------------------------------

// RateLimiter limits request rates for the platform's runtime routes. Both
// methods must be safe for concurrent use.
type RateLimiter interface {
	// AllowToken returns true if the credential has not exceeded its per-credential
	// rate limit. The key is the sha256 hash of the secret presented by the caller
	// — the bearer app token on the runtime routes, or the incoming-webhook id
	// (which IS the webhook's secret) on the webhook route. The plaintext of
	// either is never handed to the limiter.
	AllowToken(tokenHash string) bool
	// AllowIP returns true if the remote IP has not exceeded its per-IP limit.
	AllowIP(ip string) bool
}

// NoopRateLimiter allows every request. Assign it to MountConfig.Limiter to
// disable built-in rate limiting entirely.
type NoopRateLimiter struct{}

func (NoopRateLimiter) AllowToken(string) bool { return true }
func (NoopRateLimiter) AllowIP(string) bool    { return true }

// tbucket is a single token-bucket used by TokenBucketLimiter.
type tbucket struct {
	mu         sync.Mutex
	tokens     float64
	lastRefill time.Time
	rate       float64 // tokens per second added
	capacity   float64 // maximum burst capacity
}

func newTBucket(rate, capacity float64) *tbucket {
	return &tbucket{tokens: capacity, lastRefill: time.Now(), rate: rate, capacity: capacity}
}

// allow consumes one token from the bucket. Returns true if the request is
// permitted (a token was available), false if the bucket was empty.
func (b *tbucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.lastRefill = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// TokenBucketLimiter is the built-in per-token and per-IP token-bucket limiter.
// Buckets are created on first use and evicted when the map exceeds maxBuckets
// (to bound memory in the face of many unique callers).
type TokenBucketLimiter struct {
	tokenRate, tokenBurst float64
	ipRate, ipBurst       float64

	mu           sync.Mutex
	tokenBuckets map[string]*tbucket
	ipBuckets    map[string]*tbucket
}

// maxBuckets caps the number of tracked keys before eviction.
const maxBuckets = 50_000

// NewDefaultRateLimiter returns a TokenBucketLimiter with sane defaults:
//   - Per-token: 20 requests/second, burst of 50
//   - Per-IP:    10 requests/second, burst of 30
func NewDefaultRateLimiter() *TokenBucketLimiter {
	return NewTokenBucketLimiter(20, 50, 10, 30)
}

// NewTokenBucketLimiter constructs a TokenBucketLimiter with explicit rates.
//   - tokenRate/tokenBurst: per-token requests/s and burst
//   - ipRate/ipBurst:       per-IP requests/s and burst
func NewTokenBucketLimiter(tokenRate, tokenBurst, ipRate, ipBurst float64) *TokenBucketLimiter {
	return &TokenBucketLimiter{
		tokenRate:    tokenRate,
		tokenBurst:   tokenBurst,
		ipRate:       ipRate,
		ipBurst:      ipBurst,
		tokenBuckets: make(map[string]*tbucket),
		ipBuckets:    make(map[string]*tbucket),
	}
}

func (l *TokenBucketLimiter) bucket(m map[string]*tbucket, key string, rate, capacity float64) *tbucket {
	b, ok := m[key]
	if !ok {
		// Evict a batch of entries when the map is too large.
		if len(m) >= maxBuckets {
			n := maxBuckets / 10
			for k := range m {
				delete(m, k)
				n--
				if n <= 0 {
					break
				}
			}
		}
		b = newTBucket(rate, capacity)
		m[key] = b
	}
	return b
}

// AllowToken implements RateLimiter.
func (l *TokenBucketLimiter) AllowToken(tokenHash string) bool {
	l.mu.Lock()
	b := l.bucket(l.tokenBuckets, tokenHash, l.tokenRate, l.tokenBurst)
	l.mu.Unlock()
	return b.allow()
}

// AllowIP implements RateLimiter.
func (l *TokenBucketLimiter) AllowIP(ip string) bool {
	l.mu.Lock()
	b := l.bucket(l.ipBuckets, ip, l.ipRate, l.ipBurst)
	l.mu.Unlock()
	return b.allow()
}

// PerIPRateLimit is a middleware that checks the per-IP rate limit before
// passing through to next. It writes 429 on exhaustion.
func PerIPRateLimit(limiter RateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.AllowIP(clientIP(r)) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// PerTokenRateLimit is a middleware that checks the per-token rate limit using
// the bearer token's sha256 hash (so the plaintext is never stored in the rate
// limiter). Requests without a bearer token skip the per-token check (they will
// be rejected by TokenAuth downstream). It writes 429 on exhaustion.
func PerTokenRateLimit(limiter RateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token := bearerToken(r); token != "" {
			if !limiter.AllowToken(HashToken(token)) {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// PerHookRateLimit is a middleware for the unauthenticated incoming-webhook
// route. The webhook id in the path IS the credential, so it is limited on the
// limiter's per-credential bucket keyed by the id's sha256 hash (never the
// plaintext id) — a leaked webhook URL is throttled like a leaked token. It must
// be mounted on a pattern with an "{id}" wildcard; a request with no id resolvable
// skips the per-hook check (it will be rejected by the handler downstream). It
// writes 429 on exhaustion.
func PerHookRateLimit(limiter RateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.PathValue("id"); id != "" {
			if !limiter.AllowToken(HashToken(id)) {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
