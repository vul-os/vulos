// Package auth — handle availability check for Vulos identity (vulos.org).
//
// Provides ValidateHandle (shared validation) and the HandleChecker type
// used by the HTTP handler layer.
package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/vul-os/vulos-management/pkg/env"
)

// reHandle validates a Vulos handle:
//   - 3–32 chars
//   - lowercase ASCII letters, digits, hyphens, underscores
//   - must not start or end with a hyphen
var reHandle = regexp.MustCompile(`^[a-z0-9_][a-z0-9_-]{1,30}[a-z0-9_]$`)

// reHandleShort validates 3-char handles (only lowercase/digit/underscore at boundary).
var reHandleShort = regexp.MustCompile(`^[a-z0-9_]{3}$`)

// reservedHandles is the hard-coded blocklist of handles that can never be claimed.
// This list covers: subdomain conflicts, system addresses, brand protection (vumail retired name).
var reservedHandles = map[string]bool{
	// Subdomains / infrastructure
	"www": true, "app": true, "api": true, "cloud": true, "os": true,
	"mx": true, "mail": true, "smtp": true, "imap": true, "jmap": true,
	"webmail": true, "dav": true, "caldav": true, "carddav": true,
	"meet": true, "talk": true, "docs": true, "status": true, "blog": true,
	"store": true, "shop": true, "ssl": true, "tls": true, "dns": true,
	"ns1": true, "ns2": true, "smtp1": true, "mx1": true, "mx2": true,
	"news": true, "kb": true, "dashboard": true, "console": true,
	"cdn": true, "assets": true, "static": true, "media": true,
	"files": true, "download": true, "downloads": true,
	"login": true, "signup": true, "register": true, "account": true, "accounts": true,
	"profile": true,
	// System / abuse
	"security": true, "info": true, "hello": true, "postmaster": true,
	"abuse": true, "noreply": true, "no-reply": true, "mailer-daemon": true,
	"system": true, "root": true, "admin": true, "administrator": true,
	"super": true, "superadmin": true,
	// Contact / brand
	"owner": true, "ceo": true, "founder": true, "billing": true,
	"sales": true, "support": true, "help": true, "legal": true,
	"privacy": true, "dmca": true, "contact": true,
	// Brand protection
	"vulos": true, "vumail": true, "vu": true, "vulosmail": true,
	// Extended system
	"hi": true, "hey": true,
}

// HandleReason describes why a handle is or isn't available.
type HandleReason string

const (
	HandleReasonAvailable HandleReason = "available"
	HandleReasonTaken     HandleReason = "taken"
	HandleReasonReserved  HandleReason = "reserved"
	HandleReasonInvalid   HandleReason = "invalid"
)

// HandleCheckResult is the response shape for GET /api/auth/handle-available.
type HandleCheckResult struct {
	Available   bool         `json:"available"`
	Reason      HandleReason `json:"reason"`
	Suggestions []string     `json:"suggestions,omitempty"`
}

// ErrHandleInvalid is returned when the handle does not pass format validation.
var ErrHandleInvalid = errors.New("auth: handle does not meet format requirements")

// ValidateHandle checks format rules only (no DB call).
// Returns nil when the handle is syntactically valid.
func ValidateHandle(handle string) error {
	if len(handle) < 3 || len(handle) > 32 {
		return fmt.Errorf("%w: must be 3–32 characters", ErrHandleInvalid)
	}
	// Reject leading/trailing hyphen explicitly before regex.
	if strings.HasPrefix(handle, "-") || strings.HasSuffix(handle, "-") {
		return fmt.Errorf("%w: cannot start or end with a hyphen", ErrHandleInvalid)
	}
	if len(handle) == 3 {
		if !reHandleShort.MatchString(handle) {
			return fmt.Errorf("%w: use lowercase letters, digits, or underscore", ErrHandleInvalid)
		}
		return nil
	}
	if !reHandle.MatchString(handle) {
		return fmt.Errorf("%w: use lowercase letters, digits, hyphens, or underscores", ErrHandleInvalid)
	}
	return nil
}

// HandleChecker checks handle availability against the auth users table.
// The Store already owns the SQLite connection pool so we add the method there.

// IsReservedHandle reports whether handle is on the reserved blocklist — the
// hard-coded reservedHandles map OR the DB-backed additional_reserved_handles
// table (populated via the super-admin API). It normalises (lower/trim) first.
//
// SECURITY: this MUST be consulted by the signup path, not only by the advisory
// GET /api/auth/handle-available endpoint. The reserved list is otherwise
// unenforced — reserved handles are not pre-seeded into the users table, so a
// direct POST /api/auth/signup with handle=admin/postmaster/abuse/security/etc.
// would claim brand/system/RFC-2142 addresses (phishing, abuse interception).
//
// Fail-closed on the hard-coded list (always present). The DB-backed extension
// is best-effort: a query error (e.g. table absent before migration) is treated
// as "not additionally reserved", matching CheckHandleAvailable — the hard-coded
// map remains the security floor.
func (s *Store) IsReservedHandle(ctx context.Context, handle string) bool {
	handle = strings.ToLower(strings.TrimSpace(handle))
	if reservedHandles[handle] {
		return true
	}
	var dbReservedCount int
	if err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM additional_reserved_handles WHERE handle = ?`), handle,
	).Scan(&dbReservedCount); err == nil && dbReservedCount > 0 {
		return true
	}
	return false
}

// CheckHandleAvailable checks whether handle is available for registration.
// It validates format, checks both the hard-coded reserved list and the
// DB-backed additional_reserved_handles table, then queries the live users DB.
// Suggestions are generated when the handle is taken.
func (s *Store) CheckHandleAvailable(ctx context.Context, handle string) HandleCheckResult {
	handle = strings.ToLower(strings.TrimSpace(handle))

	// 1. Format validation.
	if err := ValidateHandle(handle); err != nil {
		return HandleCheckResult{Available: false, Reason: HandleReasonInvalid}
	}

	// 2. Hard-coded reserved list.
	if reservedHandles[handle] {
		return HandleCheckResult{Available: false, Reason: HandleReasonReserved}
	}

	// 3. DB-backed additional reserved handles (populated via the super-admin API).
	//    The table may not exist in all environments (before migration runs); ignore errors.
	var dbReservedCount int
	if err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM additional_reserved_handles WHERE handle = ?`), handle,
	).Scan(&dbReservedCount); err == nil && dbReservedCount > 0 {
		return HandleCheckResult{Available: false, Reason: HandleReasonReserved}
	}

	// 4. Live DB: email column stores the full identity address. Derive the
	//    domain from env so availability checks match signup on a custom
	//    VULOS_DOMAIN deployment (previously hardcoded @vulos.org → divergence).
	email := handle + "@" + env.Domain()
	var count int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM users WHERE email = ?`), email,
	).Scan(&count)
	if err != nil || count > 0 {
		suggestions := generateSuggestions(handle)
		return HandleCheckResult{Available: false, Reason: HandleReasonTaken, Suggestions: suggestions}
	}

	return HandleCheckResult{Available: true, Reason: HandleReasonAvailable}
}

// generateSuggestions returns 3–5 alternative handles for a taken handle.
// Strategy: append digits 1–9, append dot-words, append year.
func generateSuggestions(handle string) []string {
	words := []string{"try", "online", "hello", "real", "hq"}
	year := "2026"

	candidates := make([]string, 0, 8)

	// Digit suffixes: try 1, 2, 3 first.
	for _, d := range []string{"1", "2", "3", "7", "9"} {
		candidates = append(candidates, handle+d)
	}
	// Dot-word suffixes.
	for _, w := range words {
		candidates = append(candidates, handle+"."+w)
	}
	// Year suffix.
	candidates = append(candidates, handle+year)

	// Filter: only keep syntactically valid ones and stop at 5.
	out := make([]string, 0, 5)
	for _, c := range candidates {
		if len(out) >= 5 {
			break
		}
		if err := ValidateHandle(c); err == nil && !reservedHandles[c] {
			out = append(out, c)
		}
	}
	return out
}
