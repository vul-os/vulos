package ddos

import (
	"encoding/json"
	"log"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"
)

// Policy defines a per-route sliding-window rate-limit rule.
type Policy struct {
	RouteGlob     string // e.g. "/api/auth/*" or "/**"
	WindowSeconds int    // sliding window size
	MaxRequests   int    // max requests in that window
}

// DefaultPolicies are the built-in per-route policies.
// More specific globs should be listed first.
var DefaultPolicies = []Policy{
	// Strict limits for unauthenticated sensitive routes (per-minute).
	{RouteGlob: "/api/auth/login", WindowSeconds: 60, MaxRequests: 5},
	{RouteGlob: "/api/auth/signup", WindowSeconds: 60, MaxRequests: 5},
	{RouteGlob: "/api/auth/handle-available", WindowSeconds: 60, MaxRequests: 30},
	{RouteGlob: "/api/auth/forgot-password", WindowSeconds: 60, MaxRequests: 5},
	// Expensive PoP usage-report write (RELAY-USAGE-01): a relay PoP flushes on a
	// timer (seconds–minutes), so a single source IP has no legitimate reason to
	// exceed ~1/s. Tighter than the generic /api/* bucket to blunt a replay/flood
	// of billable-usage writes. Listed before /api/* so it wins matchPolicy.
	{RouteGlob: "/api/relay/usage", WindowSeconds: 60, MaxRequests: 60},
	// Expensive session-gated org-admin dashboard plane (members/usage/quotas/
	// billing summary/backup-mode). Each call fans out across several stores on
	// single-writer SQLite, so a per-IP cap below the generic /api/* keeps a hot
	// dashboard loop (or an abusive scraper) from monopolising the writer.
	{RouteGlob: "/api/org/*", WindowSeconds: 60, MaxRequests: 120},
	{RouteGlob: "/api/billing/summary", WindowSeconds: 60, MaxRequests: 120},
	// Generous limits for authenticated API routes.
	{RouteGlob: "/api/*", WindowSeconds: 60, MaxRequests: 300},
	// Global fallback.
	{RouteGlob: "/**", WindowSeconds: 60, MaxRequests: 120},
}

// matchPolicy returns the first policy whose RouteGlob matches the request path.
func matchPolicy(policies []Policy, reqPath string) Policy {
	for _, p := range policies {
		g := p.RouteGlob
		if g == "/**" {
			return p
		}
		// Strip trailing wildcard for path.Match.
		if len(g) > 1 && g[len(g)-1] == '*' {
			g = g[:len(g)-1]
			if len(reqPath) >= len(g) && reqPath[:len(g)] == g {
				return p
			}
		} else {
			// Exact or path.Match.
			if ok, _ := path.Match(g, reqPath); ok {
				return p
			}
		}
	}
	return DefaultPolicies[len(DefaultPolicies)-1]
}

// ringBucket implements a sliding-window counter using a timestamp ring buffer.
type ringBucket struct {
	mu       sync.Mutex
	times    []int64 // Unix nanos of each request in this window
	head     int
	lastSeen time.Time
}

// newRingBucket creates a ring buffer of the given capacity.
func newRingBucket(capacity int) *ringBucket {
	return &ringBucket{
		times:    make([]int64, capacity),
		lastSeen: time.Now(),
	}
}

// countInWindow returns the number of requests within the last windowNs nanoseconds
// and appends now to the buffer. Returns true if the request is allowed (count < max).
func (rb *ringBucket) countInWindow(windowNs int64, max int) bool {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	now := time.Now()
	rb.lastSeen = now
	nowNs := now.UnixNano()
	cutoff := nowNs - windowNs

	// Count valid entries.
	count := 0
	for _, t := range rb.times {
		if t > cutoff {
			count++
		}
	}
	if count >= max {
		return false
	}
	// Record this request by overwriting the oldest slot.
	rb.times[rb.head] = nowNs
	rb.head = (rb.head + 1) % len(rb.times)
	return true
}

// peekInWindow reports whether a request WOULD be allowed, WITHOUT recording it —
// so a caller can consult the limiter (e.g. to scale captcha difficulty) without
// spending a token from the real budget. It does not touch lastSeen either.
func (rb *ringBucket) peekInWindow(windowNs int64, max int) bool {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	cutoff := time.Now().UnixNano() - windowNs
	count := 0
	for _, t := range rb.times {
		if t > cutoff {
			count++
		}
	}
	return count < max
}

// IPRateLimiter enforces per-IP sliding-window policies.
type IPRateLimiter struct {
	policies []Policy

	mu      sync.Mutex
	buckets map[string]map[string]*ringBucket // ip → routeGlob → bucket
	lastGC  time.Time
}

// NewIPRateLimiter creates a limiter with the given policies.
func NewIPRateLimiter(policies []Policy) *IPRateLimiter {
	if len(policies) == 0 {
		policies = DefaultPolicies
	}
	return &IPRateLimiter{
		policies: policies,
		buckets:  make(map[string]map[string]*ringBucket),
		lastGC:   time.Now(),
	}
}

// Allow returns true if the request is within the applicable policy's rate limit.
// It uses RealClientIP to extract the client IP.
// Peek reports whether r WOULD be allowed right now, without recording it or
// creating any bucket state. Use it to consult the limiter for a side decision
// (e.g. captcha difficulty) so that consultation never itself consumes budget.
func (l *IPRateLimiter) Peek(r *http.Request) bool {
	ip := RealClientIP(r)
	pol := matchPolicy(l.policies, r.URL.Path)

	l.mu.Lock()
	routes, ok := l.buckets[ip]
	if !ok {
		l.mu.Unlock()
		return true // no bucket yet ⇒ nothing recorded ⇒ would be allowed
	}
	rb, ok := routes[pol.RouteGlob]
	l.mu.Unlock()
	if !ok {
		return true
	}
	windowNs := int64(pol.WindowSeconds) * int64(time.Second)
	return rb.peekInWindow(windowNs, pol.MaxRequests)
}

func (l *IPRateLimiter) Allow(r *http.Request) bool {
	ip := RealClientIP(r)
	pol := matchPolicy(l.policies, r.URL.Path)

	l.mu.Lock()
	// Lazy GC every 5 minutes: evict IPs idle for >24h.
	now := time.Now()
	if now.Sub(l.lastGC) > 5*time.Minute {
		cutoff := now.Add(-24 * time.Hour)
		for ipKey, routes := range l.buckets {
			allIdle := true
			for _, rb := range routes {
				rb.mu.Lock()
				idle := rb.lastSeen.Before(cutoff)
				rb.mu.Unlock()
				if !idle {
					allIdle = false
					break
				}
			}
			if allIdle {
				delete(l.buckets, ipKey)
			}
		}
		l.lastGC = now
	}

	routes, ok := l.buckets[ip]
	if !ok {
		routes = make(map[string]*ringBucket)
		l.buckets[ip] = routes
	}
	rb, ok := routes[pol.RouteGlob]
	if !ok {
		rb = newRingBucket(pol.MaxRequests + 1)
		routes[pol.RouteGlob] = rb
	}
	l.mu.Unlock()

	windowNs := int64(pol.WindowSeconds) * int64(time.Second)
	return rb.countInWindow(windowNs, pol.MaxRequests)
}

// IPRateLimit returns an http.Handler middleware that enforces per-IP
// sliding-window rate limits using the given limiter. Excess requests receive
// 429 with a Retry-After header.
func IPRateLimit(limiter *IPRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.Allow(r) {
				pol := matchPolicy(limiter.policies, r.URL.Path)
				log.Printf("[ddos/iprate] 429 ip=%s path=%s policy=%s window=%ds max=%d",
					RealClientIP(r), r.URL.Path, pol.RouteGlob, pol.WindowSeconds, pol.MaxRequests)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", strconv.Itoa(pol.WindowSeconds))
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
