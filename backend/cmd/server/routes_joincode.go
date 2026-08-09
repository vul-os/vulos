package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"vulos/backend/services/auth"
	"vulos/backend/services/joincode"
)

// registerJoinCodeRoutes mounts the join-code endpoints on mux.
//
//	POST /api/cluster/join-code   — admin-only; issues a new code + QR payload.
//	POST /api/setup/join-code     — no auth; decodes a short code for the setup wizard.
//
// The POST endpoint applies best-effort IP rate-limiting to slow abuse.
func registerJoinCodeRoutes(mux *http.ServeMux, home string, authStore *auth.Store) {
	rl := newJCRateLimiter()

	// POST /api/cluster/join-code — admin only, 1h TTL join code.
	//
	// SEC-JOINCODE-GET-01: this used to be a GET. Issuing a join code is not a
	// read — joincode.Issue MINTS a live cluster credential (the record embeds
	// the cluster S3 access key and secret) and PERSISTS it under ~/.vulos/db.
	// As a GET it was reachable by plain navigation or <img src=...> from any
	// page an admin happened to visit. The attacker could not read the response
	// (no CORS), but every hit minted another valid 1-hour credential and grew
	// the on-disk code DB unattended. A credential-issuing endpoint must not be
	// a GET.
	mux.HandleFunc("POST /api/cluster/join-code", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if p, ok := authStore.GetProfile(userID); !ok || p.Role != auth.RoleAdmin {
			jcWriteErr(w, http.StatusForbidden, "admin only")
			return
		}

		jc, code, err := joincode.Issue(home, time.Hour)
		if err != nil {
			jcWriteErr(w, http.StatusInternalServerError, fmt.Sprintf("issue join code: %v", err))
			return
		}

		jcWriteJSON(w, map[string]any{
			"short_code": code,
			"qr_payload": joincode.QRPayload(jc),
			"expires_at": jc.ExpiresAt.UTC().Format(time.RFC3339),
		})
	})

	// POST /api/setup/join-code — no auth required (setup wizard path).
	mux.HandleFunc("POST /api/setup/join-code", func(w http.ResponseWriter, r *http.Request) {
		ip := jcExtractIP(r)
		if rl.limited(ip) {
			w.Header().Set("Retry-After", "60")
			jcWriteErr(w, http.StatusTooManyRequests, "too many requests")
			return
		}

		var req struct {
			ShortCode string `json:"short_code"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.ShortCode == "" {
			rl.record(ip)
			jcWriteErr(w, http.StatusBadRequest, "short_code required")
			return
		}

		jc, err := joincode.Decode(home, req.ShortCode)
		if err != nil {
			rl.record(ip)
			switch err {
			case joincode.ErrExpired:
				jcWriteErr(w, http.StatusGone, "join code expired")
			case joincode.ErrInvalid:
				jcWriteErr(w, http.StatusNotFound, "join code not found or already used")
			default:
				jcWriteErr(w, http.StatusInternalServerError, fmt.Sprintf("decode join code: %v", err))
			}
			return
		}

		rl.reset(ip)
		jcWriteJSON(w, jc)
	})
}

// --- minimal helpers local to this file ---

func jcWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jcWriteErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

func jcExtractIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// jcRateLimiter is a lightweight token-bucket per IP for the unauthenticated join endpoint.
// Allows 5 attempts per minute; resets on success.
type jcRateLimiter struct {
	mu      sync.Mutex
	counts  map[string]*jcRecord
	maxReqs int
	window  time.Duration
}

type jcRecord struct {
	count int
	since time.Time
}

func newJCRateLimiter() *jcRateLimiter {
	return &jcRateLimiter{
		counts:  make(map[string]*jcRecord),
		maxReqs: 5,
		window:  time.Minute,
	}
}

func (rl *jcRateLimiter) limited(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rec, ok := rl.counts[ip]
	if !ok || time.Since(rec.since) > rl.window {
		return false
	}
	return rec.count >= rl.maxReqs
}

func (rl *jcRateLimiter) record(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rec, ok := rl.counts[ip]
	if !ok || time.Since(rec.since) > rl.window {
		rl.counts[ip] = &jcRecord{count: 1, since: time.Now()}
		return
	}
	rec.count++
}

func (rl *jcRateLimiter) reset(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.counts, ip)
}
