package meeting

import (
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// IPRateLimiter is a per-IP sliding-window rate limiter for join validation
// (brute-force protection for /meet/{room_id} and /api/meeting/schedule).
// Window: 60 requests per minute per IP.

const (
	RateLimitWindow  = time.Minute
	RateLimitMaxReqs = 60
)

// trustedProxyCIDRs is loaded from VULOS_TRUSTED_PROXIES (comma-separated CIDRs).
// Only RemoteAddr values within these CIDRs may supply X-Forwarded-For.
// Default: empty (trust nothing). In production, set to the load balancer's CIDR.
var trustedProxyCIDRs []*net.IPNet

func init() {
	loadTrustedProxies()
}

func loadTrustedProxies() {
	raw := os.Getenv("VULOS_TRUSTED_PROXIES")
	if raw == "" {
		trustedProxyCIDRs = nil
		return
	}
	var nets []*net.IPNet
	for _, cidr := range strings.Split(raw, ",") {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			nets = append(nets, ipNet)
		}
	}
	trustedProxyCIDRs = nets
}

// isTrustedProxy returns true if the given host address is within one of the
// configured trusted-proxy CIDRs.
func isTrustedProxy(addrHost string) bool {
	if len(trustedProxyCIDRs) == 0 {
		return false
	}
	ip := net.ParseIP(addrHost)
	if ip == nil {
		return false
	}
	for _, cidr := range trustedProxyCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// realIP extracts the effective client IP from the request.
// X-Forwarded-For is only trusted if r.RemoteAddr is within VULOS_TRUSTED_PROXIES.
func realIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && isTrustedProxy(host) {
		// Use the leftmost (original client) IP.
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	return host
}

type ipState struct {
	ts []time.Time
}

// IPRateLimiter tracks request timestamps per IP.
type IPRateLimiter struct {
	mu      sync.Mutex
	clients map[string]*ipState
}

var globalLimiter = &IPRateLimiter{
	clients: make(map[string]*ipState),
}

// GlobalLimiter returns the process-wide IPRateLimiter.
func GlobalLimiter() *IPRateLimiter { return globalLimiter }

// Allow returns true if the IP is within rate limits.
func (rl *IPRateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	state, ok := rl.clients[ip]
	if !ok {
		state = &ipState{}
		rl.clients[ip] = state
	}

	// Evict timestamps outside the window
	cutoff := now.Add(-RateLimitWindow)
	valid := state.ts[:0]
	for _, t := range state.ts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	state.ts = valid

	if len(state.ts) >= RateLimitMaxReqs {
		return false
	}
	state.ts = append(state.ts, now)
	return true
}

// GinMiddleware returns a gin-compatible http.HandlerFunc wrapper.
// X-Forwarded-For is only trusted when r.RemoteAddr is within VULOS_TRUSTED_PROXIES.
// Usage: r.Use(meeting.GlobalLimiter().GinMiddleware())
func (rl *IPRateLimiter) GinMiddleware() func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	return func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
		ip := realIP(r)
		if !rl.Allow(ip) {
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// ReactionRateLimiter is a per-peer sliding-window rate limiter for reaction
// signals (10 reactions per 10 seconds per peer). Called by the SFU/signaling
// layer before relaying a reaction event. In the current architecture reactions
// travel peer-to-peer over WebRTC DataChannel and never transit the server, so
// this limiter is available for future SFU integration.
//
// P2P mesh limitation: when reactions are sent over direct DataChannel (no
// server intermediation) a malicious peer can bypass this counter. Mitigation:
// the receiving peer-side should mute/ignore that peer's reactions after
// detecting excessive frequency.
type ReactionRateLimiter struct {
	mu      sync.Mutex
	clients map[string]*ipState
}

var globalReactionLimiter = &ReactionRateLimiter{
	clients: make(map[string]*ipState),
}

// GlobalReactionLimiter returns the process-wide ReactionRateLimiter.
func GlobalReactionLimiter() *ReactionRateLimiter { return globalReactionLimiter }

const (
	ReactionWindow  = 10 * time.Second
	ReactionMaxReqs = 10
)

// Allow returns true if the peer is within the reaction rate limit (10/10s).
func (rl *ReactionRateLimiter) Allow(peerID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	state, ok := rl.clients[peerID]
	if !ok {
		state = &ipState{}
		rl.clients[peerID] = state
	}

	cutoff := now.Add(-ReactionWindow)
	valid := state.ts[:0]
	for _, t := range state.ts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	state.ts = valid

	if len(state.ts) >= ReactionMaxReqs {
		return false
	}
	state.ts = append(state.ts, now)
	return true
}
