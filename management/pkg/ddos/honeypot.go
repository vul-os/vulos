package ddos

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"
)

// honeypotPaths are routes that no legitimate client should ever request.
var honeypotPaths = []string{
	"/wp-admin.php",
	"/.env",
	"/phpmyadmin",
	"/wp-login.php",
	"/.git/config",
	"/admin.php",
	"/xmlrpc.php",
	"/.aws/credentials",
}

// HoneypotAuditLogger is the interface the honeypot uses to log hits.
// Implemented by *auditlog.Logger — passed as interface to avoid circular imports.
type HoneypotAuditLogger interface {
	Record(ctx context.Context, actor, action, target string, metadata map[string]string) error
}

// SuspiciousTracker tracks IPs that are in "suspicious" state (rate-limited but
// not yet blocked) for use by the tarpit middleware.
type SuspiciousTracker struct {
	mu         sync.Mutex
	suspicious map[string]time.Time // ip → expiry
}

var globalSuspicious = &SuspiciousTracker{
	suspicious: make(map[string]time.Time),
}

// MarkSuspicious marks ip as suspicious for the given duration.
func MarkSuspicious(ip string, d time.Duration) {
	globalSuspicious.mu.Lock()
	globalSuspicious.suspicious[ip] = time.Now().Add(d)
	globalSuspicious.mu.Unlock()
}

// IsSuspicious returns true if ip is currently marked suspicious.
func IsSuspicious(ip string) bool {
	globalSuspicious.mu.Lock()
	defer globalSuspicious.mu.Unlock()
	exp, ok := globalSuspicious.suspicious[ip]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(globalSuspicious.suspicious, ip)
		return false
	}
	return true
}

// RegisterHoneypotRoutes registers handlers for all honeypot paths on mux.
// Hits are auto-blocked for 24h, logged, and tarpitted for 30s.
func RegisterHoneypotRoutes(mux *http.ServeMux, bl *BlocklistStore, al HoneypotAuditLogger) {
	for _, p := range honeypotPaths {
		path := p // capture
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			ip := RealClientIP(r)
			log.Printf("[ddos/honeypot] HIT ip=%s path=%s ua=%q",
				ip, path, r.Header.Get("User-Agent"))

			// Auto-block for 24h — but NEVER block a private/shared/unroutable
			// upstream. When the edge trust header is missing, RealClientIP falls
			// back to the shared proxy IP; blocking it would 403 the whole plane
			// (a self-inflicted outage). An empty IP is unresolvable, so skip too.
			if ip == "" || IsUnblockableIP(ip) {
				log.Printf("[ddos/honeypot] NOT blocking ip=%q path=%s: private/shared/unresolvable upstream", ip, path)
			} else {
				ttl := 24 * time.Hour
				if err := bl.Add(r.Context(), ip, "honeypot:"+path, "system", &ttl); err != nil {
					log.Printf("[ddos/honeypot] blocklist add: %v", err)
				}
			}

			// Audit log.
			if al != nil {
				_ = al.Record(r.Context(), ip, "ddos.honeypot.hit", path,
					map[string]string{"ua": r.Header.Get("User-Agent"), "method": r.Method})
			}

			// Tarpit: hold the connection for 30s to waste attacker resources.
			select {
			case <-time.After(30 * time.Second):
			case <-r.Context().Done():
			}

			http.NotFound(w, r)
		})
	}
	log.Printf("[ddos/honeypot] %d honeypot routes registered", len(honeypotPaths))
}

// TarpitMiddleware slows down requests from "suspicious" IPs by 5–10 seconds.
// This is applied after rate-limit detection to drain attacker throughput
// without burning bandwidth on fast responses.
func TarpitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := RealClientIP(r)
		if IsSuspicious(ip) {
			delay := 5*time.Second + time.Duration(time.Now().UnixNano()%int64(5*time.Second))
			log.Printf("[ddos/tarpit] slowing ip=%s path=%s delay=%s", ip, r.URL.Path, delay)
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// HoneypotPaths returns the list of registered honeypot paths (for dashboard display).
func HoneypotPaths() []string { return honeypotPaths }
