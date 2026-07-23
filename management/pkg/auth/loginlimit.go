// loginlimit.go -- shared, DB-backed login-attempt limiter (IDENTITY-SERVICE
// §2.4/§6). This activates the previously-dormant `login_attempts` table.
//
// WHY. The per-IP in-process token bucket (RateBucketSet / authRateLimiter) is
// per-machine: across an N-machine idp fleet an attacker gets N× the budget, and
// it offers NO per-account throttle, so credential stuffing (one IP-rotating
// attacker spraying one account, or many accounts from a botnet) sails past it.
// The authoritative cap is here: a sliding window over `login_attempts`, keyed by
// BOTH email and IP, enforced against the PRIMARY so replica lag can never grant
// an attacker extra attempts.
//
// SAFE + REVERSIBLE. Off by default (AUTH_SHARED_LOGIN_LIMIT unset). When on, it
// is enforced INSIDE Store.Login (and IssuePostAuthSession for OPAQUE) BEFORE any
// credential material is examined, so a throttled request reveals nothing about
// the account. Failures to record/count are FAIL-OPEN on the limiter ONLY (a DB
// hiccup must not lock every user out of login) — the per-IP bucket and the
// argon2/OPAQUE verification remain as the always-present lines of defence. It is
// a DENIAL cap layered on top of real auth, not the auth itself.
package auth

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

// ErrLoginThrottled is returned by Login when the shared per-email/per-IP window
// cap is exceeded. Callers map it to 429 Too Many Requests with a generic
// message (no oracle about which key tripped).
var ErrLoginThrottled = errors.New("auth: login temporarily throttled")

// Shared-limiter tuning. Deliberately generous so a human who fat-fingers a
// password a few times is never affected; the cap targets automated abuse.
const (
	loginWindow          = 15 * time.Minute // sliding window
	loginMaxFailPerEmail = 20               // failed attempts per email per window
	loginMaxFailPerIP    = 50               // failed attempts per source IP per window
)

// sharedLoginLimitEnabled reports whether the DB-backed limiter is active.
// Reversible: unset AUTH_SHARED_LOGIN_LIMIT → behaviour is exactly as before
// (per-IP bucket + lockout only), and the login_attempts table stays dormant.
func sharedLoginLimitEnabled() bool {
	v := os.Getenv("AUTH_SHARED_LOGIN_LIMIT")
	return v == "1" || v == "true"
}

// checkLoginThrottle returns ErrLoginThrottled when the sliding-window failure
// count for either the email or the IP exceeds its cap. No-op (nil) when the
// feature is off. Reads the PRIMARY (a stale replica must not grant extra tries).
// FAIL-OPEN on DB error: a limiter fault must not become a global login outage.
func (s *Store) checkLoginThrottle(ctx context.Context, email, ip string) error {
	if !sharedLoginLimitEnabled() {
		return nil
	}
	email = strings.ToLower(strings.TrimSpace(email))
	since := now().Add(-loginWindow).Format(time.RFC3339)

	if email != "" {
		if n, err := s.countFailedSince(ctx, "email", email, since); err == nil && n >= loginMaxFailPerEmail {
			return ErrLoginThrottled
		}
	}
	if ip != "" {
		if n, err := s.countFailedSince(ctx, "ip", ip, since); err == nil && n >= loginMaxFailPerIP {
			return ErrLoginThrottled
		}
	}
	return nil
}

// CheckLoginThrottle is the exported form of checkLoginThrottle for alternate
// credential-verify entrypoints (e.g. the OPAQUE login route) that must honour
// the SAME shared cap as password login. Returns ErrLoginThrottled when tripped,
// nil otherwise (and nil when the feature is off).
func (s *Store) CheckLoginThrottle(ctx context.Context, email, ip string) error {
	return s.checkLoginThrottle(ctx, email, ip)
}

// RecordLoginAttempt is the exported form of recordLoginAttempt for alternate
// credential-verify entrypoints. Best-effort; no-op when the feature is off.
func (s *Store) RecordLoginAttempt(ctx context.Context, email, ip string, ok bool) {
	s.recordLoginAttempt(ctx, email, ip, ok)
}

// countFailedSince counts failed (ok=0) attempts for the given column since the
// window start. col MUST be a fixed literal ("email" or "ip") — never
// user-controlled — so there is no injection surface.
func (s *Store) countFailedSince(ctx context.Context, col, val, since string) (int, error) {
	q := `SELECT COUNT(1) FROM login_attempts WHERE ` + col + ` = ? AND ok = 0 AND at >= ?`
	var n int
	// PRIMARY on purpose: throttle enforcement must not tolerate replica lag.
	err := s.db.QueryRowContext(ctx, s.db.Rebind(q), val, since).Scan(&n)
	return n, err
}

// recordLoginAttempt appends a row to login_attempts. Best-effort: a failure to
// record is swallowed (the limiter degrades to the per-IP bucket + lockout, and
// the login itself is unaffected). Also lazily prunes rows older than the window
// so the table stays bounded without a background job.
func (s *Store) recordLoginAttempt(ctx context.Context, email, ip string, ok bool) {
	if !sharedLoginLimitEnabled() {
		return
	}
	email = strings.ToLower(strings.TrimSpace(email))
	okVal := 0
	if ok {
		okVal = 1
	}
	n := now()
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO login_attempts (email, ip, ok, at) VALUES (?, ?, ?, ?)`),
		email, ip, okVal, n.Format(time.RFC3339),
	)
	// Lazy GC: drop attempts well past the window (keep 4× for a little history).
	cutoff := n.Add(-4 * loginWindow).Format(time.RFC3339)
	_, _ = s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM login_attempts WHERE at < ?`), cutoff)
}

// loginThrottleStatus is a tiny helper for tests/introspection: current failed
// counts for an (email, ip) pair within the window. Not on any hot path.
func (s *Store) loginThrottleStatus(ctx context.Context, email, ip string) (emailFails, ipFails int) {
	since := now().Add(-loginWindow).Format(time.RFC3339)
	emailFails, _ = s.countFailedSince(ctx, "email", strings.ToLower(strings.TrimSpace(email)), since)
	ipFails, _ = s.countFailedSince(ctx, "ip", ip, since)
	return
}
