// honeyaccount.go — honeypot account detection and IP auto-block.
//
// Any login attempt against a seeded honeypot account triggers:
//  1. A hit record in security_honeypot_hits.
//  2. A 24-hour IP block via security_ip_blocks.
//  3. An audit log entry.
//
// Honeypot accounts are seeded via Store.SeedHoneypotAccounts at bootstrap.
// The number of accounts defaults to VULOS_HONEYPOT_ACCOUNT_COUNT (default 5).
package security

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
)

// DefaultHoneypotCount is the default number of honeypot accounts to seed.
const DefaultHoneypotCount = 5

// HoneypotAccountCount returns the number of honeypot accounts to seed.
func HoneypotAccountCount() int {
	if v := os.Getenv("VULOS_HONEYPOT_ACCOUNT_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultHoneypotCount
}

// honeypotLoginBody is the minimal shape read from POST /api/auth/login.
type honeypotLoginBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// HoneypotLoginMiddleware wraps the login handler and intercepts requests
// targeting honeypot accounts. It fires the alert + block BEFORE passing
// to the real handler (so the real handler never even processes the attempt).
func HoneypotLoginMiddleware(store *Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Body != nil && store != nil {
			// Read and reconstruct the body so the downstream handler still works.
			var bodyBuf []byte
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			bodyBuf = buf[:n]
			// Restore body for downstream.
			r.Body = newBytesReadCloser(bodyBuf)
			r.ContentLength = int64(len(bodyBuf))

			var b honeypotLoginBody
			if err := json.Unmarshal(bodyBuf, &b); err == nil && b.Email != "" {
				if store.IsHoneypotAccount(r.Context(), b.Email) {
					clientIP := extractIP(r)
					log.Printf("[security/honeypot] HIT email=%s ip=%s", b.Email, clientIP)
					_ = store.RecordHoneypotHit(r.Context(), b.Email, clientIP)
					_ = store.BlockIP(r.Context(), clientIP, "honeypot_hit")
					// Return same 401 as a failed real login — don't reveal honey.
					w.Header().Set("Content-Type", "application/json")
					http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// HoneypotCheckLogin is a functional API for the wire layer.
// It checks whether an email is a honeypot account and, if so, records
// the hit and blocks the IP. Returns true if the login should be blocked.
func HoneypotCheckLogin(ctx context.Context, store *Store, email, clientIP string) bool {
	if store == nil || email == "" {
		return false
	}
	if !store.IsHoneypotAccount(ctx, email) {
		return false
	}
	log.Printf("[security/honeypot] HIT email=%s ip=%s", email, clientIP)
	_ = store.RecordHoneypotHit(ctx, email, clientIP)
	_ = store.BlockIP(ctx, clientIP, "honeypot_hit")
	return true
}
