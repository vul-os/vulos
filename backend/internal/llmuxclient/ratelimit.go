package llmuxclient

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"
)

// aiBucket holds per-account token state for aiRateLimiter.
type aiBucket struct {
	tokens     [3]int64  // [minTokens, hourTokens, lastRefillNano]
	lastAccess time.Time // wall-clock time of last allow() call
}

// aiRateLimiter is a per-account token-bucket rate limiter for AI endpoints.
//
// It maintains two buckets per account: per-minute and per-hour.
// Defaults: AIROUTER_RATE_PER_MIN (default 20) and AIROUTER_RATE_PER_HOUR (default 200).
// When either bucket is empty the handler returns 429 with Retry-After.
//
// A background goroutine (started by newAIRateLimiterWithCtx) sweeps the
// buckets map every 5 minutes and evicts entries idle for more than 24 hours.
// The goroutine terminates when the supplied context is cancelled.
type aiRateLimiter struct {
	mu sync.Mutex

	perMin  int
	perHour int

	// buckets maps accountKey → per-account token state.
	buckets map[string]*aiBucket

	// now is a clock override used in tests; nil means time.Now.
	now func() time.Time

	// sweepInterval and idleThreshold can be overridden in tests.
	sweepInterval time.Duration
	idleThreshold time.Duration
}

// newAIRateLimiterWithCtx builds a limiter and starts the background eviction
// goroutine.  The goroutine exits when ctx is cancelled.
func newAIRateLimiterWithCtx(ctx context.Context) *aiRateLimiter {
	perMin := aiEnvInt("AIROUTER_RATE_PER_MIN", 20)
	perHour := aiEnvInt("AIROUTER_RATE_PER_HOUR", 200)
	rl := &aiRateLimiter{
		perMin:        perMin,
		perHour:       perHour,
		buckets:       make(map[string]*aiBucket),
		sweepInterval: 5 * time.Minute,
		idleThreshold: 24 * time.Hour,
	}
	go rl.sweepLoop(ctx)
	return rl
}

func newAIRateLimiter() *aiRateLimiter {
	return newAIRateLimiterWithCtx(context.Background())
}

// sweepLoop runs in a goroutine and periodically evicts idle buckets.
func (rl *aiRateLimiter) sweepLoop(ctx context.Context) {
	nowFn := rl.now
	if nowFn == nil {
		nowFn = time.Now
	}
	ticker := time.NewTicker(rl.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rl.sweep(nowFn())
		}
	}
}

// sweep deletes bucket entries that have been idle for longer than idleThreshold.
func (rl *aiRateLimiter) sweep(now time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for key, b := range rl.buckets {
		if now.Sub(b.lastAccess) > rl.idleThreshold {
			delete(rl.buckets, key)
		}
	}
}

// allow returns (allowed, retryAfterSeconds).
func (rl *aiRateLimiter) allow(accountKey string) (bool, int) {
	nowFn := rl.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	nowNano := now.UnixNano()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[accountKey]
	if !ok {
		b = &aiBucket{
			tokens:     [3]int64{int64(rl.perMin), int64(rl.perHour), nowNano},
			lastAccess: now,
		}
		rl.buckets[accountKey] = b
	}

	b.lastAccess = now

	elapsed := time.Duration(nowNano - b.tokens[2])
	if elapsed > 0 {
		minRefill := int64(float64(elapsed) / float64(time.Minute) * float64(rl.perMin))
		b.tokens[0] += minRefill
		if b.tokens[0] > int64(rl.perMin) {
			b.tokens[0] = int64(rl.perMin)
		}
		hourRefill := int64(float64(elapsed) / float64(time.Hour) * float64(rl.perHour))
		b.tokens[1] += hourRefill
		if b.tokens[1] > int64(rl.perHour) {
			b.tokens[1] = int64(rl.perHour)
		}
		b.tokens[2] = nowNano
	}

	if b.tokens[0] <= 0 {
		secsPerToken := int(time.Minute/time.Second) / rl.perMin
		if secsPerToken < 1 {
			secsPerToken = 1
		}
		return false, secsPerToken
	}
	if b.tokens[1] <= 0 {
		secsPerToken := int(time.Hour/time.Second) / rl.perHour
		if secsPerToken < 1 {
			secsPerToken = 1
		}
		return false, secsPerToken
	}

	b.tokens[0]--
	b.tokens[1]--
	return true, 0
}

func aiEnvInt(key string, fallback int) int {
	if s := os.Getenv(key); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			return v
		}
	}
	return fallback
}
