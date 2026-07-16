package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

const (
	// SessionCookieName is the cookie name for the session token.
	SessionCookieName = "vc_session"
	// SessionDuration is how long a session lives: 30 days.
	SessionDuration = 30 * 24 * time.Hour
	// SessionCookieMaxAge matches SessionDuration in seconds.
	SessionCookieMaxAge = 2592000 // 30 * 24 * 60 * 60
)

// newSessionToken generates a cryptographically random 32-byte token,
// encoded as base64url (no padding) for use as a session ID.
func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// secureCookie controls whether the Secure flag is set on session cookies.
// Default true (production). Call SetSecureCookie(false) in local/HTTP-only
// environments so the browser accepts the cookie over plain HTTP.
var secureCookie = true

// cookieDomain records the configured VULOS_COOKIE_DOMAIN (the deployment's
// registrable "family" domain, e.g. "vulos.org"). It is used by return-to /
// CORS origin checks (see cmd/server/routes_auth.go, internal/middleware) to
// recognise our own hosts — it is NO LONGER written onto the session cookie.
//
// ROUTING MODEL (PART A.4): the console session cookie is HOST-SCOPED to the
// apex (`vulos.org`) — it carries NO Domain attribute — so the browser attaches
// it ONLY to the exact apex host and NEVER to `os.vulos.org`, `<org>.os.…`, or
// any product plane. The OS/app plane learns identity exclusively via
// audience-bound router / app tokens (see internal/osrouter, internal/apptoken),
// never by receiving the apex session cookie. This closes the `.vulos.org`-wide
// cookie bleed that made the session reachable from every subdomain.
var cookieDomain = ""

// SetSecureCookies configures whether session cookies are issued with Secure=true.
// Must be called once at process startup (before any requests are handled).
// In local dev (HTTP only) pass false; in staging and production pass true.
func SetSecureCookies(secure bool) { secureCookie = secure }

// InitCookieDomain reads VULOS_COOKIE_DOMAIN (the deployment family domain) and
// caches it for return-to / CORS origin recognition. It is OPTIONAL in every
// environment: the session cookie is host-scoped regardless, so no domain is
// required for auth to be secure. Must be called once at startup.
func InitCookieDomain() {
	cookieDomain = os.Getenv("VULOS_COOKIE_DOMAIN")
	log.Printf("[auth] session cookie: HOST-SCOPED to the apex (no Domain attribute); family domain=%q", cookieDomain)
}

// CookieDomain returns the configured family domain string (may be empty). It is
// NOT the session-cookie Domain (that is always empty/host-scoped).
func CookieDomain() string { return cookieDomain }

// SetSessionCookie writes the session cookie to the response. The cookie is
// HttpOnly, SameSite=Lax, Path=/, Max-Age=30 days, and HOST-SCOPED (no Domain
// attribute) so it never bleeds to os.<domain> or any product subdomain.
// The Secure flag follows the package-level secureCookie setting.
func SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:  SessionCookieName,
		Value: token,
		Path:  "/",
		// Domain deliberately omitted → host-scoped to the apex (PART A.4).
		MaxAge:   SessionCookieMaxAge,
		HttpOnly: true,
		Secure:   secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie expires the session cookie immediately. Host-scoped to match
// SetSessionCookie (a Domain mismatch would leave the cookie uncleared).
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

// SessionFromRequest extracts the session token from the request cookie.
// Returns "" if no valid cookie is present.
func SessionFromRequest(r *http.Request) string {
	c, err := r.Cookie(SessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// now is a hook so tests can override the clock.
var now = func() time.Time { return time.Now().UTC() }

// NowFunc returns the current clock function (used by external tests to capture
// the original func before overriding it).
func NowFunc() func() time.Time { return now }

// SetNowFunc replaces the clock function. Callers are responsible for restoring
// the original value (use t.Cleanup or defer in tests).
func SetNowFunc(f func() time.Time) { now = f }
