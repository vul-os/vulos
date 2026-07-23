// enum.go — anti-enumeration hardening.
//
//  1. LoginTimingMiddleware — wraps the login handler and pads response time
//     to a constant ~250ms regardless of user existence, preventing timing attacks.
//
//  2. HandleAvailableMiddleware — tightens the handle-available rate limit to
//     10/min/IP (down from 30) and returns pretend-200 (always "available") with
//     jitter after the limit is hit.
//
//  3. IsTakenMiddleware — same pretend-200 + jitter pattern for any "is taken"
//     endpoint.
package security

import (
	"crypto/rand"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// loginTargetDuration is the constant response time for login attempts.
const loginTargetDuration = 250 * time.Millisecond

// LoginTimingMiddleware pads the login response time to loginTargetDuration.
// If the handler finishes faster, we sleep the remainder.
// If it takes longer (e.g. bcrypt on high-load), we don't add extra delay.
func LoginTimingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		elapsed := time.Since(start)
		if remaining := loginTargetDuration - elapsed; remaining > 0 {
			time.Sleep(remaining)
		}
	})
}

// ─── Handle-available anti-enumeration ────────────────────────────────────────

// enumBucket is a per-IP sliding-window counter for anti-enumeration.
type enumBucket struct {
	mu    sync.Mutex
	times []int64
	head  int
}

func newEnumBucket(cap int) *enumBucket {
	return &enumBucket{times: make([]int64, cap)}
}

func (b *enumBucket) allow(windowNs int64, max int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now().UnixNano()
	cutoff := now - windowNs
	count := 0
	for _, t := range b.times {
		if t > cutoff {
			count++
		}
	}
	if count >= max {
		return false
	}
	b.times[b.head] = now
	b.head = (b.head + 1) % len(b.times)
	return true
}

// lastSeen returns the most recent recorded hit time (unix-nanos), or 0 if the
// bucket has never recorded a hit. Used by the eviction sweep to drop idle IPs.
func (b *enumBucket) lastSeen() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	var max int64
	for _, t := range b.times {
		if t > max {
			max = t
		}
	}
	return max
}

// enumLimiter is a per-IP rate limiter for anti-enumeration endpoints.
//
// LIMITER-EVICT-01: the buckets map is internet-facing (keyed by client IP), so
// without eviction it grows unbounded under IP churn/spoofing. allow() runs an
// amortised sweep that drops buckets whose newest hit is older than the window,
// bounding memory to roughly the count of IPs active within the last window.
type enumLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*enumBucket
	max       int
	window    time.Duration
	lastSweep time.Time
}

func newEnumLimiter(max int, window time.Duration) *enumLimiter {
	return &enumLimiter{
		buckets:   make(map[string]*enumBucket),
		max:       max,
		window:    window,
		lastSweep: time.Now(),
	}
}

func (l *enumLimiter) allow(ip string) bool {
	l.mu.Lock()
	l.sweepLocked()
	b, ok := l.buckets[ip]
	if !ok {
		b = newEnumBucket(l.max + 1)
		l.buckets[ip] = b
	}
	l.mu.Unlock()
	return b.allow(l.window.Nanoseconds(), l.max)
}

// sweepLocked evicts buckets with no hit inside the window. Runs at most once per
// window (amortised O(0) per request). Must be called with l.mu held.
func (l *enumLimiter) sweepLocked() {
	now := time.Now()
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now
	cutoff := now.Add(-l.window).UnixNano()
	for ip, b := range l.buckets {
		if b.lastSeen() <= cutoff {
			delete(l.buckets, ip)
		}
	}
}

// globalHandleAvailableLimiter: 10 req/min per IP (spec: tighter than existing 30/min).
var globalHandleAvailableLimiter = newEnumLimiter(10, time.Minute)

// HandleAvailableMiddleware wraps the handle-available endpoint.
// After 10 hits/min/IP it returns a pretend-200 {"available":true} with jitter.
func HandleAvailableMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)
		if !globalHandleAvailableLimiter.allow(ip) {
			addJitter()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"available":true,"reason":"available"}`)) //nolint:errcheck
			return
		}
		next.ServeHTTP(w, r)
	})
}

// IsTakenMiddleware wraps any "is taken" endpoint with the pretend-200 pattern.
// maxPerMin controls the per-IP limit; pass 10 for default.
func IsTakenMiddleware(maxPerMin int, next http.Handler) http.Handler {
	lim := newEnumLimiter(maxPerMin, time.Minute)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)
		if !lim.allow(ip) {
			addJitter()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"available":true}`)) //nolint:errcheck
			return
		}
		next.ServeHTTP(w, r)
	})
}

// addJitter sleeps for a random 10-60ms to prevent timing correlation.
func addJitter() {
	n, _ := rand.Int(rand.Reader, big.NewInt(50))
	time.Sleep(10*time.Millisecond + time.Duration(n.Int64())*time.Millisecond)
}

func extractIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return ip
}
