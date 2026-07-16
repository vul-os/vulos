// ratebucket.go -- a tiny per-key token-bucket rate limiter (per-IP, per-machine).
//
// This is the cheap FIRST line of login rate limiting. It lives in the auth
// package so the isolated idp service (internal/idp) can use it WITHOUT importing
// the monolith's copy in package main. It is deliberately identical in behaviour
// to that copy (authRateLimiter) so the two front doors throttle the same way.
//
// It is per-machine only: across an N-machine fleet an attacker gets N× this
// budget, which is exactly why the AUTHORITATIVE cap is the shared, DB-backed
// login-attempt limiter (see loginlimit.go, IDENTITY-SERVICE §2.4/§6). This
// bucket just cheaply sheds the obvious floods before they touch the DB.
package auth

import (
	"sync"
	"time"
)

// RateBucketSet is a set of per-key token buckets sharing a fill rate and burst.
type RateBucketSet struct {
	mu      sync.Mutex
	buckets map[string]*rateBucket
	rps     float64
	burst   int
}

type rateBucket struct {
	mu       sync.Mutex
	tokens   float64
	lastSeen time.Time
}

// NewRateBucketSet builds a limiter refilling at rps tokens/sec with the given
// burst capacity, keyed (typically) by client IP.
func NewRateBucketSet(rps float64, burst int) *RateBucketSet {
	return &RateBucketSet{
		buckets: make(map[string]*rateBucket),
		rps:     rps,
		burst:   burst,
	}
}

// Allow reports whether a request for key should proceed, consuming a token.
func (rl *RateBucketSet) Allow(key string) bool {
	rl.mu.Lock()
	bkt, ok := rl.buckets[key]
	if !ok {
		bkt = &rateBucket{tokens: float64(rl.burst), lastSeen: time.Now()}
		rl.buckets[key] = bkt
	}
	// Lazy GC of idle keys so the map does not grow unbounded.
	nowT := time.Now()
	for k, b := range rl.buckets {
		b.mu.Lock()
		idle := nowT.Sub(b.lastSeen)
		b.mu.Unlock()
		if idle > 10*time.Minute {
			delete(rl.buckets, k)
		}
	}
	rl.mu.Unlock()

	bkt.mu.Lock()
	defer bkt.mu.Unlock()
	elapsed := time.Since(bkt.lastSeen).Seconds()
	bkt.lastSeen = time.Now()
	bkt.tokens += elapsed * rl.rps
	if bkt.tokens > float64(rl.burst) {
		bkt.tokens = float64(rl.burst)
	}
	if bkt.tokens < 1 {
		return false
	}
	bkt.tokens--
	return true
}
