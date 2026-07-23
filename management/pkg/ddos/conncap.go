package ddos

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	defaultUnauthCap = 10
	defaultAuthCap   = 50
)

// ConnCapConfig holds limits for the concurrent-connection cap middleware.
type ConnCapConfig struct {
	// UnauthCap is the max concurrent connections per IP for unauthenticated routes.
	UnauthCap int
	// AuthCap is the max concurrent connections per IP for authenticated routes.
	AuthCap int
	// AuthPathPrefix: requests with this prefix are treated as authenticated.
	// Default: "/api/" (everything except /api/auth/* which has its own rule).
	AuthPathPrefix string
}

// DefaultConnCapConfig is the default connection cap configuration.
var DefaultConnCapConfig = ConnCapConfig{
	UnauthCap:      defaultUnauthCap,
	AuthCap:        defaultAuthCap,
	AuthPathPrefix: "/api/",
}

// connCounter tracks per-IP active connection counts.
type connCounter struct {
	mu     sync.Mutex
	counts map[string]int
	lastGC time.Time
}

func newConnCounter() *connCounter {
	return &connCounter{
		counts: make(map[string]int),
		lastGC: time.Now(),
	}
}

// inc increments the counter for ip and returns the new count.
func (c *connCounter) inc(ip string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[ip]++
	return c.counts[ip]
}

// dec decrements the counter for ip, removing the key when it reaches zero.
func (c *connCounter) dec(ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[ip]--
	if c.counts[ip] <= 0 {
		delete(c.counts, ip)
	}
}

// ConnCap returns a middleware that caps concurrent connections per IP.
// Unauthenticated routes get UnauthCap; all other routes get AuthCap.
// Excess connections receive 503 with Retry-After: 5.
func ConnCap(cfg ConnCapConfig) func(http.Handler) http.Handler {
	if cfg.UnauthCap == 0 {
		cfg.UnauthCap = defaultUnauthCap
	}
	if cfg.AuthCap == 0 {
		cfg.AuthCap = defaultAuthCap
	}
	if cfg.AuthPathPrefix == "" {
		cfg.AuthPathPrefix = "/api/"
	}

	unauthRoutes := map[string]bool{
		"/api/auth/login":            true,
		"/api/auth/signup":           true,
		"/api/auth/handle-available": true,
		"/api/auth/forgot-password":  true,
		"/api/captcha/challenge":     true,
	}

	counter := newConnCounter()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := RealClientIP(r)

			cap := cfg.AuthCap
			if unauthRoutes[r.URL.Path] {
				cap = cfg.UnauthCap
			}

			count := counter.inc(ip)
			defer counter.dec(ip)

			if count > cap {
				log.Printf("[ddos/conncap] 503 ip=%s path=%s connections=%d cap=%d",
					ip, r.URL.Path, count, cap)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "5")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "too many concurrent connections"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
