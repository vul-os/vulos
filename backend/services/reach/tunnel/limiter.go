package tunnel

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"vulos/backend/internal/safedial"
)

// limiter.go — the relay's rate limiting and its one guarded outbound dialler.

// limiter is a token bucket keyed by an arbitrary string (a source IP, a
// tunnel name, or "" for a global bucket).
//
// # Why not a library
//
// This is thirty lines and has exactly one subtlety worth getting right: the
// map must not grow without bound. A per-source-IP limiter whose map is never
// pruned IS a memory-exhaustion vector — an attacker sends one request from
// each of a large number of spoofed or real addresses and the defence becomes
// the vulnerability. The sweep below is therefore not housekeeping; it is the
// security-relevant part.
type limiter struct {
	// rate is refills per second. A NEGATIVE rate disables the limiter
	// entirely (the operator's explicit opt-out); zero is never stored
	// because callers pass defaults through rateOr.
	rate  float64
	burst float64
	nowFn func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	lastGC  time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// bucketIdleTTL is how long an untouched bucket survives a sweep. It must
// exceed the time it takes a full bucket to refill, or a legitimate caller
// could be reset mid-backoff; a minute is comfortably past that for every
// rate used here.
const bucketIdleTTL = time.Minute

// bucketGCInterval bounds how often a sweep runs, so a hot path does not walk
// the map on every call.
const bucketGCInterval = 30 * time.Second

// maxBuckets is a hard ceiling reached only under attack. When exceeded, the
// whole map is dropped rather than grown: losing rate-limit state briefly
// admits a small burst, whereas growing without bound admits an OOM. The
// former is recoverable and the latter is not.
const maxBuckets = 50_000

func newLimiter(rate float64, nowFn func() time.Time) *limiter {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &limiter{
		rate:    rate,
		burst:   rate * 2,
		nowFn:   nowFn,
		buckets: make(map[string]*bucket),
		lastGC:  nowFn(),
	}
}

// allow consumes one token for key, reporting whether it was available.
func (l *limiter) allow(key string) bool {
	if l == nil || l.rate < 0 {
		return true // explicitly disabled
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.nowFn()
	l.maybeGCLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxBuckets {
			l.buckets = make(map[string]*bucket)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	// Refill for elapsed time, clamped to the burst ceiling.
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += elapsed.Seconds() * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// maybeGCLocked drops buckets that have been idle long enough to have
// refilled completely — they carry no state worth keeping. Caller holds mu.
func (l *limiter) maybeGCLocked(now time.Time) {
	if now.Sub(l.lastGC) < bucketGCInterval {
		return
	}
	l.lastGC = now
	for k, b := range l.buckets {
		if now.Sub(b.last) > bucketIdleTTL {
			delete(l.buckets, k)
		}
	}
}

// newProbeClient builds the ONE outbound HTTP client the relay uses, for the
// direct-endpoint ownership probe.
//
// This is the only place a relay makes a request to an address influenced by
// an agent, so it is hardened accordingly:
//
//   - The dialler re-screens the RESOLVED IP at connect time (safedial), so a
//     hostname that resolves to a private, loopback, link-local, or
//     cloud-metadata address is refused even if DNS answered differently a
//     moment ago. That defeats DNS rebinding, which is the whole attack
//     against a naive prober running inside a VPS with a metadata service.
//   - Redirects are refused. Following one would let an agent point the
//     relay at an internal address that the first, screened request did not
//     go to.
//   - The transport is not shared with anything else and keeps no
//     connections alive, so nothing here can be reused for a later request.
func newProbeClient(allowPrivate bool) *http.Client {
	dialer := safedial.New(allowPrivate)
	return &http.Client{
		Timeout: directProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
			DisableKeepAlives:   true,
			TLSHandshakeTimeout: 5 * time.Second,
			MaxIdleConns:        0,
		},
	}
}
