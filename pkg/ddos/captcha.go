package ddos

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// captchaRoutes are the routes that require PoW verification.
var captchaRoutes = map[string]bool{
	"/api/auth/login":            true,
	"/api/auth/signup":           true,
	"/api/auth/handle-available": true,
	"/api/auth/forgot-password":  true,
}

// CaptchaStore manages issued challenges and system-wide settings.
type CaptchaStore struct {
	mu                sync.Mutex
	challenges        map[string]captchaEntry // nonce → entry
	captchaEverywhere bool                    // super-admin toggle
	lastGC            time.Time

	// rateLimiter reference to check per-IP rate for adaptive difficulty.
	limiter *IPRateLimiter
}

type captchaEntry struct {
	challenge  string
	difficulty int
	issuedAt   time.Time
	// route binds the challenge to the request path it was issued for. Verify
	// rejects a solution presented on a DIFFERENT route, so a cheap challenge
	// minted for one endpoint cannot be replayed against another.
	route string
}

// NewCaptchaStore creates a CaptchaStore backed by the given rate limiter
// for adaptive difficulty scaling.
func NewCaptchaStore(limiter *IPRateLimiter) *CaptchaStore {
	return &CaptchaStore{
		challenges: make(map[string]captchaEntry),
		lastGC:     time.Now(),
		limiter:    limiter,
	}
}

// SetCaptchaEverywhere enables or disables the captcha-everywhere mode.
func (s *CaptchaStore) SetCaptchaEverywhere(on bool) {
	s.mu.Lock()
	s.captchaEverywhere = on
	s.mu.Unlock()
}

// CaptchaEverywhere returns the current captcha-everywhere setting.
func (s *CaptchaStore) CaptchaEverywhere() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captchaEverywhere
}

// difficultyFor returns the number of leading zero bits required for a challenge.
// Base difficulty is 4. Scales up by 2 for each factor-of-3 overage over the
// route's rate limit, and jumps to 8 when captcha-everywhere is enabled.
func (s *CaptchaStore) difficultyFor(r *http.Request) int {
	s.mu.Lock()
	everywhere := s.captchaEverywhere
	s.mu.Unlock()

	// Base PoW cost. 4 bits (~16 hashes) was effectively free — no barrier to bulk
	// abuse. Raised to a still-instant-for-a-real-client value; kept moderate
	// deliberately because the browser solves each attempt with an ASYNC WebCrypto
	// digest, so a very high base becomes a multi-second UX cost. Tunable upward
	// (with the cap below) if abuse pressure warrants it.
	base := 12
	if everywhere {
		base = 16
	}

	// Adaptive: if this IP is already at its limit on this route, raise difficulty.
	// Peek is READ-ONLY — consulting the limiter to score difficulty must not spend
	// a token from the real budget (the old code called Allow, which records the
	// request, so merely issuing a challenge double-charged the limiter).
	if s.limiter != nil {
		if !s.limiter.Peek(r) {
			base += 2
		}
	}
	if base > 20 {
		base = 20
	}
	return base
}

// IssueChallenge creates a new PoW challenge for the given route.
// Returns the challenge JSON body.
func (s *CaptchaStore) IssueChallenge(r *http.Request, route string) ([]byte, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("captcha: rand: %w", err)
	}
	challengeHex := hex.EncodeToString(b)
	diff := s.difficultyFor(r)

	s.mu.Lock()
	s.gc()
	s.challenges[challengeHex] = captchaEntry{
		challenge:  challengeHex,
		difficulty: diff,
		issuedAt:   time.Now(),
		route:      route,
	}
	s.mu.Unlock()

	return json.Marshal(map[string]any{
		"algorithm":  "sha256-hashcash",
		"challenge":  challengeHex,
		"difficulty": diff,
		"route":      route,
	})
}

// Verify checks the X-Vulos-PoW header (format: "challenge:nonce") for a request
// on route. Returns true only if the PoW is valid AND the challenge was issued for
// THIS route; the challenge is consumed either way (replay protection).
func (s *CaptchaStore) Verify(pow, route string) bool {
	parts := strings.SplitN(pow, ":", 2)
	if len(parts) != 2 {
		return false
	}
	challenge, nonce := parts[0], parts[1]

	s.mu.Lock()
	entry, ok := s.challenges[challenge]
	if ok {
		delete(s.challenges, challenge) // consume — prevents replay
	}
	s.mu.Unlock()

	if !ok {
		return false
	}
	// Expired challenges (older than 10 minutes) are invalid.
	if time.Since(entry.issuedAt) > 10*time.Minute {
		return false
	}
	// Route binding: a challenge minted for one endpoint (possibly at a lower
	// difficulty) cannot be spent on another. An unbound entry (route "") accepts
	// any route — IssueChallenge always sets a route, so this only affects
	// directly-constructed entries.
	if entry.route != "" && entry.route != route {
		return false
	}

	return verifyHashcash(challenge, nonce, entry.difficulty)
}

// verifyHashcash checks that SHA256(challenge+nonce) has at least `bits` leading zero bits.
func verifyHashcash(challenge, nonce string, bits int) bool {
	h := sha256.Sum256([]byte(challenge + nonce))
	// Count leading zero bits.
	for i := 0; i < bits; i++ {
		byteIdx := i / 8
		bitIdx := uint(7 - (i % 8))
		if byteIdx >= len(h) {
			return false
		}
		if (h[byteIdx]>>bitIdx)&1 != 0 {
			return false
		}
	}
	return true
}

// gc removes challenges older than 15 minutes. Must be called under mu.
func (s *CaptchaStore) gc() {
	now := time.Now()
	if now.Sub(s.lastGC) < time.Minute {
		return
	}
	for k, e := range s.challenges {
		if now.Sub(e.issuedAt) > 15*time.Minute {
			delete(s.challenges, k)
		}
	}
	s.lastGC = now
}

// HandleCaptchaChallenge is the GET /api/captcha/challenge handler.
func HandleCaptchaChallenge(store *CaptchaStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		route := r.URL.Query().Get("route")
		if route == "" {
			route = "unknown"
		}
		body, err := store.IssueChallenge(r, route)
		if err != nil {
			http.Error(w, "captcha issue error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

// CaptchaGate returns middleware that enforces PoW on captchaRoutes.
// Requests without a valid X-Vulos-PoW header on protected routes receive 403.
func CaptchaGate(store *CaptchaStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}
			if !captchaRoutes[r.URL.Path] && !store.CaptchaEverywhere() {
				next.ServeHTTP(w, r)
				return
			}

			pow := r.Header.Get("X-Vulos-PoW")
			if pow == "" || !store.Verify(pow, r.URL.Path) {
				log.Printf("[ddos/captcha] PoW missing/invalid ip=%s path=%s",
					RealClientIP(r), r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error":          "proof-of-work required",
					"captcha_needed": strconv.FormatBool(true),
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
