// ratelimit.go — brute-force throttle for the super-admin login endpoint.
//
// Failed POST /superadmin/login attempts are counted per source IP AND per
// target account (email). After a threshold of failures within a rolling
// window the key is locked out for an (exponentially growing) cool-off period
// during which all attempts are rejected with 429 — regardless of whether the
// supplied credentials are correct, so an attacker gains no signal.
//
// The limiter is in-memory and therefore per-instance: behind multiple CP
// replicas the effective limit scales with the replica count. That is an
// accepted trade-off (no shared store dependency); the IP allowlist + WebAuthn
// hardware key remain the primary controls.
package superadmin

import (
	"strings"
	"sync"
	"time"
)

const (
	// loginMaxFailures is the number of failures within loginWindow allowed
	// before a key is locked out.
	loginMaxFailures = 5
	// loginWindow is the rolling window over which failures are counted.
	loginWindow = 15 * time.Minute
	// loginBaseLockout is the first lockout duration; it doubles on each
	// successive lockout (capped at loginMaxLockout).
	loginBaseLockout = 1 * time.Minute
	loginMaxLockout  = 1 * time.Hour
)

type attemptRecord struct {
	failures    int
	windowStart time.Time
	lockedUntil time.Time
	lockoutLvl  int // number of times this key has been locked out
}

// loginLimiter is a thread-safe per-key brute-force limiter.
type loginLimiter struct {
	mu  sync.Mutex
	rec map[string]*attemptRecord
	now func() time.Time // injectable clock for tests
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{rec: make(map[string]*attemptRecord), now: time.Now}
}

// locked reports whether key is currently locked out, and if so for how long.
func (l *loginLimiter) locked(key string) (bool, time.Duration) {
	key = normKey(key)
	if key == "" {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.rec[key]
	if r == nil {
		return false, 0
	}
	now := l.now()
	if now.Before(r.lockedUntil) {
		return true, r.lockedUntil.Sub(now)
	}
	return false, 0
}

// anyLocked reports whether any of the given keys is currently locked out.
func (l *loginLimiter) anyLocked(keys ...string) (bool, time.Duration) {
	var maxD time.Duration
	var any bool
	for _, k := range keys {
		if locked, d := l.locked(k); locked {
			any = true
			if d > maxD {
				maxD = d
			}
		}
	}
	return any, maxD
}

// recordFailure registers a failed attempt for key and returns true if this
// failure triggered (or extended) a lockout.
func (l *loginLimiter) recordFailure(key string) bool {
	key = normKey(key)
	if key == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	r := l.rec[key]
	if r == nil {
		r = &attemptRecord{windowStart: now}
		l.rec[key] = r
	}
	// Reset the rolling window if it has elapsed.
	if now.Sub(r.windowStart) > loginWindow {
		r.failures = 0
		r.windowStart = now
	}
	r.failures++
	if r.failures >= loginMaxFailures {
		// Exponential back-off: base * 2^level, capped.
		d := loginBaseLockout << r.lockoutLvl
		if d > loginMaxLockout || d <= 0 {
			d = loginMaxLockout
		}
		r.lockedUntil = now.Add(d)
		r.lockoutLvl++
		r.failures = 0
		r.windowStart = now
		return true
	}
	return false
}

// recordFailures records a failure for several keys (IP + account) at once and
// returns true if any of them became locked.
func (l *loginLimiter) recordFailures(keys ...string) bool {
	var locked bool
	for _, k := range keys {
		if l.recordFailure(k) {
			locked = true
		}
	}
	return locked
}

// reset clears the failure/lockout state for keys (called on a fully successful
// login).
func (l *loginLimiter) reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.rec, normKey(k))
	}
}

func normKey(k string) string { return strings.ToLower(strings.TrimSpace(k)) }
