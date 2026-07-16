// waf.go — WAF middleware using coraza (pure-Go OWASP CRS-compatible engine).
//
// Blocks SQLi, XSS, path traversal, command injection, and scanner probes.
// Trusted routes (/api/superadmin/*, /api/webhooks/*) bypass the WAF entirely.
// All violations are written to the security Store's WAF events table.
package security

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/corazawaf/coraza/v3"
	"github.com/corazawaf/coraza/v3/types"
)

// wafAuditLogger is an internal audit-log sink for coraza.
type wafAuditLogger struct{}

func (wafAuditLogger) Write(p []byte) (int, error) { return len(p), nil } // swallowed; we log per-interrupt below

// trustedWAFRoutes are prefixes that bypass WAF inspection entirely.
// These are endpoints that receive signed/structured bodies (webhooks, JMAP).
var trustedWAFRoutes = []string{
	"/api/superadmin/",
	"/api/webhooks/",
}

// isTrustedWAFRoute returns true if the request path should skip WAF.
func isTrustedWAFRoute(path string) bool {
	for _, prefix := range trustedWAFRoutes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// WAF holds a coraza WAF instance and the security store.
type WAF struct {
	waf   coraza.WAF
	store *Store
}

// NewWAF creates a new WAF instance loaded with inline CRS-equivalent rules.
// We use coraza's directive-based configuration to inline the minimum CRS
// rules that cover the four attack classes. A full CRS bundle can be swapped
// in by extending the directives slice.
func NewWAF(store *Store) (*WAF, error) {
	cfg := coraza.NewWAFConfig().
		WithErrorCallback(func(rule types.MatchedRule) {
			// per-violation callback handled in Middleware
		}).
		WithDirectives(crsDirectives())

	wafInstance, err := coraza.NewWAF(cfg)
	if err != nil {
		return nil, fmt.Errorf("security/waf: init coraza: %w", err)
	}
	return &WAF{waf: wafInstance, store: store}, nil
}

// crsDirectives returns Coraza/SecLang directives that implement a minimal
// OWASP CRS-compatible ruleset covering the four primary attack classes.
// Production deployments should replace this with the full coraza-crs bundle.
// Note: coraza requires "deny,status:403" for disruptive blocking; "block" alone
// does not set an interruption in the current coraza v3 build.
func crsDirectives() string {
	return `SecRuleEngine On
SecRequestBodyAccess On
SecResponseBodyAccess Off
SecRequestBodyLimit 10485760
SecRule ARGS "@detectSQLi" "id:942100,phase:2,deny,status:403,msg:'SQL Injection Attack',tag:'attack-sqli'"
SecRule ARGS "@rx (?i)(union.{0,20}select|insert.{0,5}into|delete.{0,5}from|drop.{0,5}table|exec.{0,5}[(]|xp_cmdshell)" "id:942110,phase:2,deny,status:403,msg:'SQL Injection Keyword',tag:'attack-sqli'"
SecRule ARGS "@detectXSS" "id:941100,phase:2,deny,status:403,msg:'XSS Attack',tag:'attack-xss'"
SecRule ARGS "@rx (?i)(<script[^>]*>|javascript:|on[a-z]+\s*=|<iframe|<object|eval[(])" "id:941110,phase:2,deny,status:403,msg:'XSS Pattern',tag:'attack-xss'"
SecRule REQUEST_URI "@rx (?i)(\.\./|\.\.\\|%2e%2e%2f|%2e%2e/|\.\.%2f)" "id:930100,phase:1,deny,status:403,msg:'Path Traversal Attack',tag:'attack-lfi'"
SecRule ARGS "@rx (?i)(;|\|{1,2}|&&|\$[(])" "id:932100,phase:2,deny,status:403,msg:'OS Command Injection',tag:'attack-rce'"
SecRule REQUEST_HEADERS:User-Agent "@rx (?i)(nikto|nmap|masscan|sqlmap|acunetix|nessus|openvas|w3af|burpsuite|dirbuster|gobuster|ffuf|nuclei)" "id:913100,phase:1,deny,status:403,msg:'Security Scanner Detected',tag:'attack-reputation-scanner'"
`
}

// wafResponseWriter wraps http.ResponseWriter to capture the status code
// and allow the WAF interrupt handler to prevent writes after blocking.
type wafResponseWriter struct {
	http.ResponseWriter
	statusCode  int
	interrupted bool
	headersSent bool
}

func (w *wafResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	if !w.interrupted {
		w.headersSent = true
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *wafResponseWriter) Write(b []byte) (int, error) {
	if w.interrupted {
		return len(b), nil // discard
	}
	return w.ResponseWriter.Write(b)
}

// Middleware returns an http.Handler middleware that applies WAF rules.
// Trusted routes bypass the WAF. Violations are logged and the request is
// blocked with 403.
func (wf *WAF) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isTrustedWAFRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		tx := wf.waf.NewTransaction()
		defer tx.Close()

		// Process connection (required before URI).
		clientIP, clientPort := splitAddr(r.RemoteAddr)
		tx.ProcessConnection(clientIP, clientPort, "", 0)

		// Process request URI (does not add GET args — see AddGetRequestArgument below).
		tx.ProcessURI(r.URL.RequestURI(), r.Method, r.Proto)

		// Add GET query arguments so ARGS_GET is populated for rule matching.
		for key, vals := range r.URL.Query() {
			for _, val := range vals {
				tx.AddGetRequestArgument(key, val)
			}
		}

		// Process request headers.
		for name, vals := range r.Header {
			for _, val := range vals {
				tx.AddRequestHeader(name, val)
			}
		}
		if it := tx.ProcessRequestHeaders(); it != nil {
			wf.handleInterrupt(w, r, it, tx)
			return
		}

		// Process request body (best-effort; skip on error).
		if r.Body != nil && r.ContentLength > 0 {
			if it, _, err := tx.ReadRequestBodyFrom(r.Body); err == nil && it != nil {
				wf.handleInterrupt(w, r, it, tx)
				return
			}
		}

		if it, err := tx.ProcessRequestBody(); err == nil && it != nil {
			wf.handleInterrupt(w, r, it, tx)
			return
		}

		wrapped := &wafResponseWriter{ResponseWriter: w, statusCode: 200}
		next.ServeHTTP(wrapped, r)
	})
}

// splitAddr splits "host:port" into (host, portInt).
func splitAddr(addr string) (string, int) {
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		host := addr[:idx]
		port := 0
		for _, c := range addr[idx+1:] {
			if c >= '0' && c <= '9' {
				port = port*10 + int(c-'0')
			}
		}
		return host, port
	}
	return addr, 0
}

func (wf *WAF) handleInterrupt(w http.ResponseWriter, r *http.Request, it *types.Interruption, tx types.Transaction) {
	clientIP := r.RemoteAddr
	if idx := strings.LastIndex(clientIP, ":"); idx != -1 {
		clientIP = clientIP[:idx]
	}
	ruleID := fmt.Sprintf("%d", it.RuleID)
	pattern := it.Data
	path := r.URL.Path

	log.Printf("[security/waf] BLOCK rule=%s pattern=%q ip=%s path=%s",
		ruleID, pattern, clientIP, path)

	// Persist to audit DB (best-effort).
	if wf.store != nil {
		_ = wf.store.RecordWAFEvent(context.Background(), ruleID, pattern, clientIP, path)
	}

	http.Error(w, `{"error":"blocked by security policy"}`, http.StatusForbidden)
}
