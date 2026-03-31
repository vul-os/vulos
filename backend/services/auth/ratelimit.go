package auth

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateLimiter implements fail2ban-style brute force protection.
// Tracks failed attempts per IP. After MaxAttempts failures within Window,
// the IP is banned for BanDuration.
type RateLimiter struct {
	mu          sync.Mutex
	attempts    map[string]*attemptRecord
	bans        map[string]time.Time
	maxAttempts int
	window      time.Duration
	banDuration time.Duration
}

type attemptRecord struct {
	count    int
	firstAt  time.Time
	lastAt   time.Time
}

func NewRateLimiter(maxAttempts int, window, banDuration time.Duration) *RateLimiter {
	rl := &RateLimiter{
		attempts:    make(map[string]*attemptRecord),
		bans:        make(map[string]time.Time),
		maxAttempts: maxAttempts,
		window:      window,
		banDuration: banDuration,
	}
	// Cleanup goroutine
	go rl.cleanup()
	return rl
}

// DefaultRateLimiter: 5 failures in 10 minutes → banned for 30 minutes.
func DefaultRateLimiter() *RateLimiter {
	return NewRateLimiter(5, 10*time.Minute, 30*time.Minute)
}

// RecordFailure logs a failed auth attempt from an IP.
func (rl *RateLimiter) RecordFailure(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	rec, ok := rl.attempts[ip]
	if !ok || now.Sub(rec.firstAt) > rl.window {
		rl.attempts[ip] = &attemptRecord{count: 1, firstAt: now, lastAt: now}
		return
	}

	rec.count++
	rec.lastAt = now

	if rec.count >= rl.maxAttempts {
		rl.bans[ip] = now.Add(rl.banDuration)
		delete(rl.attempts, ip)
		log.Printf("[auth/fail2ban] IP %s banned for %s after %d failures", ip, rl.banDuration, rec.count)
	}
}

// RecordSuccess clears failure history for an IP.
func (rl *RateLimiter) RecordSuccess(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.attempts, ip)
}

// IsBanned checks if an IP is currently banned.
func (rl *RateLimiter) IsBanned(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	banUntil, ok := rl.bans[ip]
	if !ok {
		return false
	}
	if time.Now().After(banUntil) {
		delete(rl.bans, ip)
		return false
	}
	return true
}

// Unban manually removes a ban.
func (rl *RateLimiter) Unban(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.bans, ip)
	delete(rl.attempts, ip)
}

// BannedIPs returns all currently banned IPs with expiry.
func (rl *RateLimiter) BannedIPs() map[string]time.Time {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	result := make(map[string]time.Time)
	for ip, until := range rl.bans {
		if until.After(now) {
			result[ip] = until
		}
	}
	return result
}

// Stats returns current rate limiter stats.
func (rl *RateLimiter) Stats() map[string]any {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return map[string]any{
		"tracked_ips":  len(rl.attempts),
		"banned_ips":   len(rl.bans),
		"max_attempts": rl.maxAttempts,
		"window":       rl.window.String(),
		"ban_duration": rl.banDuration.String(),
	}
}

// Middleware rejects requests from banned IPs.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)
		if rl.IsBanned(ip) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1800")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"error":"too many failed attempts, try again later"}`)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractIP(r *http.Request) string {
	// Only trust proxy headers from loopback (reverse proxy on same host)
	remoteHost, _, _ := net.SplitHostPort(r.RemoteAddr)
	fromTrustedProxy := remoteHost == "127.0.0.1" || remoteHost == "::1"

	if fromTrustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			for i, c := range xff {
				if c == ',' {
					return strings.TrimSpace(xff[:i])
				}
				_ = i
			}
			return strings.TrimSpace(xff)
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return xri
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// cleanup runs periodically to remove expired entries.
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for ip, until := range rl.bans {
			if now.After(until) {
				delete(rl.bans, ip)
			}
		}
		for ip, rec := range rl.attempts {
			if now.Sub(rec.lastAt) > rl.window {
				delete(rl.attempts, ip)
			}
		}
		rl.mu.Unlock()
	}
}
