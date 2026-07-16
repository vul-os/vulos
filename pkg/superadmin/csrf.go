// csrf.go — CSRF protection for the super-admin portal.
//
// Strategy: a double-submit, session-independent token. On every rendered page
// the server issues a random token in a cookie (vulos_admin_csrf, HttpOnly,
// SameSite=Strict) AND embeds the same value in a hidden <input name="csrf_token">
// in every state-changing form. On POST/DELETE the submitted form value must
// equal the cookie value (constant-time compare). This works for the public
// login POST (no admin session yet) and for the authenticated pages alike, and
// is defence-in-depth on top of the already-SameSite=Strict session cookies.
//
// The JSON /api/superadmin/* endpoints are intentionally NOT routed through this
// CSRF check: they are no longer triggered by browser forms (every HTML form now
// posts to a same-origin /superadmin/* handler), and they require the
// SameSite=Strict admin session cookie which a cross-site attacker cannot cause
// the browser to send. See wire_superadmin.go for the route wiring.
package superadmin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"

	"github.com/vul-os/vulos-management/pkg/auditlog"
)

// CSRFCookieName is the cookie carrying the double-submit CSRF token.
const CSRFCookieName = "vulos_admin_csrf"

// CSRFFieldName is the hidden form field carrying the CSRF token.
const CSRFFieldName = "csrf_token"

// issueCSRFToken returns the CSRF token for this request, reusing the value from
// an existing cookie when present (so multiple tabs/forms share one token) or
// minting and setting a fresh one otherwise. The token is always (re)written to
// the cookie to keep it alive for the session.
func issueCSRFToken(w http.ResponseWriter, r *http.Request, secure bool) string {
	var token string
	if c, err := r.Cookie(CSRFCookieName); err == nil && len(c.Value) >= 16 {
		token = c.Value
	} else {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			// Extremely unlikely; fall back to an empty token (verify will fail
			// closed, denying the POST).
			return ""
		}
		token = base64.RawURLEncoding.EncodeToString(raw)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/superadmin",
		MaxAge:   int(AdminSessionAbsoluteTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	return token
}

// verifyCSRF returns true when the request carries a CSRF cookie whose value
// matches the submitted csrf_token form field (constant-time). It fails closed
// on any missing/short value.
func verifyCSRF(r *http.Request) bool {
	c, err := r.Cookie(CSRFCookieName)
	if err != nil || len(c.Value) < 16 {
		return false
	}
	// PostFormValue parses the body (form-urlencoded or multipart) and caches it,
	// so downstream handlers can still read their fields.
	submitted := r.PostFormValue(CSRFFieldName)
	if submitted == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(submitted)) == 1
}

// CSRFProtect wraps a state-changing handler, rejecting requests whose CSRF
// token is missing or wrong with 403 and an audit entry. Safe methods
// (GET/HEAD/OPTIONS) pass through untouched.
func CSRFProtect(al *auditlog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if !verifyCSRF(r) {
				actor := AdminAccountIDFromCtx(r.Context())
				if actor == "" {
					actor = "unknown"
				}
				auditFromRequest(r, al, actor, "admin.csrf.denied",
					r.Method+" "+r.URL.Path, nil)
				http.Error(w, "forbidden: invalid CSRF token", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
