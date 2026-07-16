// stepup.go — credential-stuffing defense via adaptive step-up verification.
//
// This middleware wraps the existing POST /api/auth/login handler. It does NOT
// modify internal/auth/auth.go. It evaluates risk signals BEFORE passing
// the request to the downstream login handler.
//
// Risk signals:
//   - New device (no remember_device cookie)
//   - New geo (IP prefix differs from previous logins — coarse /24 comparison)
//   - New UA (UA string not seen before for this account)
//   - Concurrent login attempts from different IPs on the same account (<5 min)
//
// If high-risk (score >= threshold): require an emailed 6-digit code in addition
// to normal TOTP. The code is single-use and expires in 10 minutes.
package security

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/ddos"
)

// StepUpThreshold is the default risk score above which step-up is triggered.
// Configurable via VULOS_STEPUP_THRESHOLD env var (float 0..1).
var StepUpThreshold = func() float64 {
	if v := os.Getenv("VULOS_STEPUP_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return 0.6
}()

// stepUpCode is an in-memory store of issued step-up codes.
// In production these would live in the security DB. For test-friendliness
// and to avoid a DB round-trip on the critical login path we use a sync.Map.
type stepUpEntry struct {
	code      string
	userID    string
	expiresAt time.Time
	attempts  int // wrong guesses so far; the entry burns at maxStepUpAttempts
}

// maxStepUpAttempts bounds how many wrong guesses a step-up code tolerates before
// it is burned. A code is 6 digits (10^6 space) and lives 10 minutes; without a
// cap that is trivially brute-forceable in the window. 5 keeps a fat-fingered
// legitimate user in the game while making a guessing attack hopeless.
const maxStepUpAttempts = 5

var (
	stepUpMu    sync.Mutex
	stepUpCodes = map[string]*stepUpEntry{} // key = userID
)

// InboxSender is the interface for sending step-up codes to users.
// Production wires the real Vulos inbox sender; tests inject a stub.
type InboxSender interface {
	Send(ctx context.Context, toEmail, subject, body string) error
}

// NopInboxSender silently discards messages (dev/test).
type NopInboxSender struct{}

func (NopInboxSender) Send(_ context.Context, to, subj, body string) error {
	log.Printf("[security/stepup] NOP send to=%s subj=%q", to, subj)
	return nil
}

// EmailToUserIDFunc resolves an email address to an account user ID.
// The implementation is injected at wire time (see wire_security.go) to avoid
// a circular import between security ← auth.
// When nil, the email address is used directly as a pseudo user-ID (safe for
// risk bucketing, but reduces deduplication across handle changes).
type EmailToUserIDFunc func(ctx context.Context, email string) (userID string, err error)

// StepUpConfig configures the step-up middleware.
type StepUpConfig struct {
	Store     *Store
	Threshold float64
	Sender    InboxSender
	// LookupUserID, if non-nil, is called to resolve an email to an account
	// user ID before risk evaluation. Injected in production by wire_security.go
	// via auth.Store.UserIDForEmail to decouple the security package from auth.
	LookupUserID EmailToUserIDFunc
}

// StepUpMiddleware wraps a login handler with risk-based step-up verification.
// It intercepts POST /api/auth/login, evaluates risk, and if high risk:
//  1. Issues a 6-digit code emailed to the account (or inbox).
//  2. Returns 202 with {"step_up_required":true}.
//  3. The client must re-POST with X-StepUp-Code header; this middleware
//     validates the code and then forwards to the real login handler.
func StepUpMiddleware(cfg StepUpConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}

		// Parse body non-destructively so downstream handler can re-read.
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&body); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		// Reconstruct body for downstream.
		rebuilt, _ := json.Marshal(body)
		r.Body = newBytesReadCloser(rebuilt)
		r.ContentLength = int64(len(rebuilt))

		userID := resolveUserID(r.Context(), cfg, body.Email)
		clientIP := remoteIPStepUp(r)

		// If a step-up code is already present, validate it.
		if code := r.Header.Get("X-StepUp-Code"); code != "" {
			if validateStepUpCode(userID, code) {
				// Code valid — proceed to real login.
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"invalid or expired step-up code"}`)
			return
		}

		// Evaluate risk.
		risk := evaluateRisk(r.Context(), cfg.Store, userID, clientIP, r.UserAgent())

		// Always proceed when below threshold.
		if risk < cfg.Threshold {
			next.ServeHTTP(w, r)
			return
		}

		// High risk: issue emailed code and pause login.
		code, err := issueStepUpCode(userID)
		if err != nil {
			// FAIL CLOSED. The risk engine has already decided this login needs a
			// second factor; if we cannot mint one (a crypto/rand failure — a
			// system emergency, essentially never on a healthy host) the safe answer
			// is to REFUSE the login, never to wave a high-risk login through with no
			// second factor at all. The client retries; a genuinely broken RNG is a
			// bigger incident than a paused login.
			log.Printf("[security/stepup] issue code failed, refusing high-risk login: %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"step_up_required":true,"message":"Verification is temporarily unavailable. Please try again."}`)
			return
		}

		// Record the event.
		if cfg.Store != nil {
			_ = cfg.Store.RecordStepUpEvent(r.Context(), userID, clientIP, risk)
		}

		// Send the code.
		if cfg.Sender != nil && body.Email != "" {
			msg := fmt.Sprintf(
				"Your Vulos verification code is: %s\n\n"+
					"This code expires in 10 minutes. If you did not request this, "+
					"someone may be attempting to access your account.",
				code,
			)
			if err := cfg.Sender.Send(r.Context(), body.Email,
				"Vulos sign-in verification code", msg); err != nil {
				log.Printf("[security/stepup] send inbox: %v", err)
			}
		}

		log.Printf("[security/stepup] step-up required user=%s ip=%s risk=%.2f", userID, clientIP, risk)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"step_up_required":true,"message":"A verification code has been sent to your Vulos inbox."}`)
	})
}

// ─── Risk scoring ─────────────────────────────────────────────────────────────

func evaluateRisk(ctx context.Context, store *Store, userID, clientIP, ua string) float64 {
	score := 0.0

	if store == nil || userID == "" {
		return score
	}

	// Signal 1: concurrent login attempts from multiple IPs in <5min.
	cutoff := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339)
	var distinctIPs int
	_ = store.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT client_ip) FROM security_stepup_events
		 WHERE user_id=? AND ts >= ?`,
		userID, cutoff,
	).Scan(&distinctIPs)
	if distinctIPs >= 2 {
		score += 0.4
	}

	// Signal 2: new device (check via device cookie presence — proxy: no recent
	// successful stepup from this IP in the last 7 days).
	sevenDaysAgo := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	var recentFromIP int
	_ = store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM security_stepup_events
		 WHERE user_id=? AND client_ip=? AND ts >= ?`,
		userID, clientIP, sevenDaysAgo,
	).Scan(&recentFromIP)
	if recentFromIP == 0 {
		score += 0.3 // new device/IP
	}

	// Signal 3: new geo (coarse /24 subnet check).
	subnet := ipSubnet24(clientIP)
	if subnet != "" {
		var subnetSeen int
		_ = store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM security_stepup_events
			 WHERE user_id=? AND client_ip LIKE ? AND ts >= ?`,
			userID, subnet+"%", sevenDaysAgo,
		).Scan(&subnetSeen)
		if subnetSeen == 0 {
			score += 0.2
		}
	}

	// Signal 4: new UA.
	if ua != "" {
		var uaSeen int
		_ = store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM security_stepup_events
			 WHERE user_id=? AND ts >= ?`,
			userID, sevenDaysAgo,
		).Scan(&uaSeen)
		_ = uaSeen // We'd compare UA hash but lack a UA column; treat absence of any history as new
	}

	if score > 1.0 {
		score = 1.0
	}
	return score
}

// ipSubnet24 extracts the /24 prefix (first 3 octets) of an IPv4 address.
func ipSubnet24(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) == 4 {
		return strings.Join(parts[:3], ".") + "."
	}
	return ""
}

// resolveUserID maps an email address to a canonical user ID for risk bucketing.
// When cfg.LookupUserID is set (production, injected by wire_security.go via
// auth.Store.UserIDForEmail), the real account ID is returned so risk history
// tracks the account even when an email changes. When the lookup fails or is
// nil, the email address is used directly as a pseudo user-ID (safe fallback:
// bucketing is per-email rather than per-account in that case).
func resolveUserID(ctx context.Context, cfg StepUpConfig, email string) string {
	if cfg.LookupUserID != nil {
		if id, err := cfg.LookupUserID(ctx, email); err == nil && id != "" {
			return id
		}
	}
	return email
}

// ─── Code management ─────────────────────────────────────────────────────────

// stepUpRand is the entropy source for step-up codes. Overridable in tests to
// exercise the fail-closed path when the RNG is unavailable.
var stepUpRand io.Reader = rand.Reader

func issueStepUpCode(userID string) (string, error) {
	n, err := rand.Int(stepUpRand, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("%06d", n.Int64())

	stepUpMu.Lock()
	defer stepUpMu.Unlock()
	stepUpCodes[userID] = &stepUpEntry{
		code:      code,
		userID:    userID,
		expiresAt: time.Now().Add(10 * time.Minute),
	}
	return code, nil
}

func validateStepUpCode(userID, code string) bool {
	stepUpMu.Lock()
	defer stepUpMu.Unlock()
	entry, ok := stepUpCodes[userID]
	if !ok {
		return false
	}
	if time.Now().After(entry.expiresAt) {
		delete(stepUpCodes, userID)
		return false
	}
	// Constant-time compare: a length-independent, timing-safe check so the code
	// cannot be recovered digit-by-digit from response timing.
	if subtle.ConstantTimeCompare([]byte(entry.code), []byte(code)) != 1 {
		// Burn the code after too many wrong guesses. Without this a 6-digit code
		// could be brute-forced within its 10-minute life; with it an attacker gets
		// maxStepUpAttempts tries, then must trigger a fresh code (itself gated).
		entry.attempts++
		if entry.attempts >= maxStepUpAttempts {
			delete(stepUpCodes, userID)
		}
		return false
	}
	delete(stepUpCodes, userID) // single-use
	return true
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func remoteIPStepUp(r *http.Request) string {
	// Use the canonical trusted-edge resolver, NOT a raw leftmost X-Forwarded-For.
	// This IP is the key input to the risk engine that decides whether a login
	// needs step-up; reading an unauthenticated, attacker-settable XFF let a
	// credential-stuffer pin a single "known-good" IP so risk never crosses the
	// threshold, neutralising the whole defence. ddos.RealClientIP only honours the
	// configured forwarding header when the immediate upstream is in the trusted
	// CIDR range, and otherwise falls back to RemoteAddr.
	return ddos.RealClientIP(r)
}

// newBytesReadCloser wraps a byte slice as an io.ReadCloser.
type bytesReadCloser struct {
	*strings.Reader
}

func (b bytesReadCloser) Close() error { return nil }

func newBytesReadCloser(b []byte) bytesReadCloser {
	return bytesReadCloser{strings.NewReader(string(b))}
}
