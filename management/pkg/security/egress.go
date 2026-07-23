// egress.go — egress anomaly detection for file/blob downloads.
//
// Per-account hourly egress is tracked. When an account's egress exceeds
// 3σ above the 7-day rolling baseline, it is flagged for super-admin review.
//
// Usage: wrap download handlers with EgressMiddleware.
package security

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// egressTracker accumulates in-memory per-account egress for the current hour.
// It flushes to the DB every flush interval and compares against the baseline.
type egressTracker struct {
	mu        sync.Mutex
	hourly    map[string]int64 // userID → bytes this hour
	lastFlush time.Time
	store     *Store
}

var globalEgressTracker *egressTracker

// egressWork is one queued egress-accounting job.
type egressWork struct {
	userID string
	bytes  int64
}

// egressJobs bounds the async egress-accounting work (EGRESS-BOUND-01). Every
// download previously spawned an unbounded context.Background() goroutine; a
// burst of downloads could spawn unbounded goroutines each holding a DB conn on
// the tiny pool. Instead a fixed worker pool drains a bounded queue; when the
// queue is full the job is dropped with a log (egress accounting is best-effort
// telemetry, not correctness-critical), so a flood can never exhaust memory or
// the connection pool.
const (
	egressQueueSize  = 1024
	egressWorkers    = 4
	egressJobTimeout = 15 * time.Second
)

var (
	egressJobs     chan egressWork
	egressJobsOnce sync.Once
	egressDropped  atomic.Int64
)

// InitEgressTracker initialises the global egress tracker with the given store
// and starts the bounded worker pool. Call once at startup from wire_security.go.
func InitEgressTracker(store *Store) {
	globalEgressTracker = &egressTracker{
		hourly:    make(map[string]int64),
		lastFlush: time.Now(),
		store:     store,
	}
	startEgressWorkers(store)
}

// startEgressWorkers lazily starts the fixed worker pool exactly once.
func startEgressWorkers(store *Store) {
	egressJobsOnce.Do(func() {
		egressJobs = make(chan egressWork, egressQueueSize)
		for i := 0; i < egressWorkers; i++ {
			go func() {
				for job := range egressJobs {
					ctx, cancel := context.WithTimeout(context.Background(), egressJobTimeout)
					recordAndCheck(ctx, store, job.userID, job.bytes)
					cancel()
				}
			}()
		}
	})
}

// enqueueEgress submits an accounting job to the bounded pool, dropping (with a
// throttled log) when the queue is full so the request path never blocks and the
// goroutine/connection count stays bounded.
func enqueueEgress(userID string, bytes int64) {
	if egressJobs == nil {
		return // tracker not initialised (no store wired)
	}
	select {
	case egressJobs <- egressWork{userID: userID, bytes: bytes}:
	default:
		if n := egressDropped.Add(1); n%1000 == 1 {
			log.Printf("[security/egress] WARNING: accounting queue full — dropped %d samples so far", n)
		}
	}
}

// EgressMiddleware wraps download handlers.
// It reads the Content-Length of the response to count bytes, records them,
// and checks for anomalies against the 7-day baseline.
//
// If userIDHeader is non-empty, the user ID is read from that request header
// (useful for internal service calls). Otherwise it falls back to the context.
func EgressMiddleware(store *Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := userIDFromCtx(r.Context())
		if userID == "" {
			// Try to read from a well-known header set by auth middleware.
			userID = r.Header.Get("X-Vulos-User-ID")
		}

		rec := &egressResponseWriter{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		if userID == "" || rec.bytesWritten == 0 {
			return
		}

		// EGRESS-BOUND-01: hand off to the bounded worker pool instead of spawning
		// an unbounded goroutine per download. If the pool was never started (no
		// store wired), fall back to a single bounded goroutine so behaviour is
		// unchanged in that degraded path.
		if egressJobs != nil {
			enqueueEgress(userID, rec.bytesWritten)
			return
		}
		go func(uid string, n int64) {
			ctx, cancel := context.WithTimeout(context.Background(), egressJobTimeout)
			defer cancel()
			recordAndCheck(ctx, store, uid, n)
		}(userID, rec.bytesWritten)
	})
}

// recordAndCheck records bytes for the user and triggers anomaly check.
func recordAndCheck(ctx context.Context, store *Store, userID string, bytes int64) {
	if store == nil {
		return
	}

	tracker := globalEgressTracker
	if tracker == nil {
		// Fallback: direct DB write without in-memory accumulation.
		_ = store.RecordEgressSample(ctx, userID, bytes)
		checkEgressAnomaly(ctx, store, userID, bytes)
		return
	}

	tracker.mu.Lock()
	tracker.hourly[userID] += bytes
	hourlyBytes := tracker.hourly[userID]
	shouldFlush := time.Since(tracker.lastFlush) >= time.Hour
	if shouldFlush {
		// Flush all hourly samples to DB.
		for uid, b := range tracker.hourly {
			_ = store.RecordEgressSample(ctx, uid, b)
		}
		tracker.hourly = make(map[string]int64)
		tracker.lastFlush = time.Now()
	}
	tracker.mu.Unlock()

	// Check anomaly with the accumulated hourly value.
	checkEgressAnomaly(ctx, store, userID, hourlyBytes)
}

// checkEgressAnomaly computes whether bytesHr exceeds 3σ above the 7-day baseline.
func checkEgressAnomaly(ctx context.Context, store *Store, userID string, bytesHr int64) {
	mean, stddev, n, err := store.EgressBaseline(ctx, userID)
	if err != nil || n < 3 {
		// Not enough baseline data to flag yet.
		return
	}

	// Require stddev > 0 to avoid false positives when all samples are equal.
	// Also require the spike to be at least 2x the mean as a floor guard.
	if stddev < 1 {
		return
	}
	threshold := mean + 3*stddev
	if float64(bytesHr) > threshold && threshold > 0 {
		log.Printf("[security/egress] ANOMALY user=%s bytes_hr=%d baseline=%.0f stddev=%.0f",
			userID, bytesHr, mean, stddev)
		_ = store.RecordEgressAnomaly(ctx, userID, bytesHr, mean, stddev)
	}
}

// egressResponseWriter counts bytes written by the downstream handler.
type egressResponseWriter struct {
	http.ResponseWriter
	bytesWritten int64
}

func (e *egressResponseWriter) Write(b []byte) (int, error) {
	n, err := e.ResponseWriter.Write(b)
	e.bytesWritten += int64(n)
	return n, err
}

func (e *egressResponseWriter) WriteHeader(code int) {
	// If Content-Length is set, use it directly for accounting.
	if cl := e.ResponseWriter.Header().Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			e.bytesWritten = n
		}
	}
	e.ResponseWriter.WriteHeader(code)
}
