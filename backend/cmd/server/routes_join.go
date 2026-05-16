package main

// routes_join.go — INIT-08: join an existing Vulos cluster from a NEW device.
//
//	POST /api/setup/join         — validate S3 + passphrase, persist creds
//	                               (no passphrase), begin background sync.
//	GET  /api/setup/join/status  — poll sync-state.json progress.
//
// Both endpoints are setup-time and therefore UNAUTHENTICATED (added to
// auth.publicPaths). The POST endpoint is rate-limited per IP because it is
// public and performs network + crypto work.
//
// Per ROUTES.md: one wiring function, exactly one line added to main.go, no
// globals captured — `home` is passed in.

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"vulos/backend/services/joinsync"
)

// registerJoinRoutes wires the cluster-join endpoints onto mux.
func registerJoinRoutes(mux *http.ServeMux, home string) {
	rl := newJoinRateLimiter()

	mux.HandleFunc("POST /api/setup/join", func(w http.ResponseWriter, r *http.Request) {
		ip := joinExtractIP(r)
		if rl.limited(ip) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "too many requests")
			return
		}

		var req joinsync.JoinRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			rl.record(ip)
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}

		result, err := joinsync.Join(req, home)
		if err != nil {
			rl.record(ip)
			switch {
			case errors.Is(err, joinsync.ErrBadRequest):
				writeErr(w, http.StatusBadRequest, err.Error())
			case errors.Is(err, joinsync.ErrBadPassphrase):
				// 401: caller supplied the wrong cluster passphrase.
				writeErr(w, http.StatusUnauthorized, "incorrect passphrase for this cluster")
			case errors.Is(err, joinsync.ErrUnreachable):
				// 502: S3 bucket/endpoint not reachable with these creds.
				writeErr(w, http.StatusBadGateway, "S3 bucket unreachable — check bucket, region, and credentials")
			default:
				writeErr(w, http.StatusInternalServerError, err.Error())
			}
			return
		}

		// Success — reset the limiter for this IP and return 200.
		rl.reset(ip)
		writeJSON(w, result)
	})

	mux.HandleFunc("GET /api/setup/join/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, joinsync.Progress(home))
	})
}

// --- minimal helpers local to this file (J08_ prefix avoids collisions) ---

func joinExtractIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// joinRateLimiter is a lightweight per-IP token bucket for the unauthenticated
// join endpoint: 5 attempts / minute, reset on success. Mirrors the
// jcRateLimiter pattern used by routes_joincode.go.
type joinRateLimiter struct {
	mu      sync.Mutex
	counts  map[string]*joinRLRecord
	maxReqs int
	window  time.Duration
}

type joinRLRecord struct {
	count int
	since time.Time
}

func newJoinRateLimiter() *joinRateLimiter {
	return &joinRateLimiter{
		counts:  make(map[string]*joinRLRecord),
		maxReqs: 5,
		window:  time.Minute,
	}
}

func (rl *joinRateLimiter) limited(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rec, ok := rl.counts[ip]
	if !ok || time.Since(rec.since) > rl.window {
		return false
	}
	return rec.count >= rl.maxReqs
}

func (rl *joinRateLimiter) record(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rec, ok := rl.counts[ip]
	if !ok || time.Since(rec.since) > rl.window {
		rl.counts[ip] = &joinRLRecord{count: 1, since: time.Now()}
		return
	}
	rec.count++
}

func (rl *joinRateLimiter) reset(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.counts, ip)
}
