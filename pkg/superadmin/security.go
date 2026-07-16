// security.go — static asset serving + HTTP security headers for the
// super-admin portal.
//
// The admin UI is server-rendered html/template with a STRICT Content-Security-
// Policy that forbids inline styles and inline scripts. To satisfy that policy
// the CSS and the small amount of JS (the WebAuthn login ceremony and the
// read-only billing fetch) are served as external files from /superadmin/*
// under script-src 'self' / style-src 'self'. They are embedded at compile time
// with go:embed.
package superadmin

import (
	"net/http"
	"time"

	"github.com/vul-os/vulos-management/pkg/auditlog"

	_ "embed"
)

//go:embed assets/admin.css
var assetAdminCSS []byte

//go:embed assets/login.js
var assetLoginJS []byte

//go:embed assets/account_detail.js
var assetAccountDetailJS []byte

// adminContentSecurityPolicy is the strict CSP applied to every admin response.
// default-src 'none' denies everything not explicitly allowed; styles and
// scripts come only from 'self' (the embedded external files); images may be
// self-hosted or data: URIs (the inline cost-bar SVGs are rendered as markup,
// not images, so 'self' suffices but data: is kept for any small icons);
// forms may only post back to the same origin; the page may not be framed.
const adminContentSecurityPolicy = "default-src 'none'; " +
	"style-src 'self'; " +
	"script-src 'self'; " +
	"img-src 'self' data:; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'"

// SecurityHeaders wraps every super-admin handler (HTML and JSON) with the
// strict CSP and hardening headers. Authenticated HTML pages are additionally
// marked Cache-Control: no-store; the static asset handlers override that with
// a long-lived cache value of their own.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", adminContentSecurityPolicy)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// Default to no-store; asset handlers set their own Cache-Control.
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// serveAsset returns a handler that serves a single embedded asset with the
// given Content-Type and a long immutable cache (the bytes only change on
// redeploy, and the CSP forbids anything but 'self' anyway).
func serveAsset(contentType string, body []byte) http.HandlerFunc {
	modTime := time.Now()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
		// http.ServeContent handles conditional requests / ranges; pass a
		// bytes.Reader-like via http.ServeContent is overkill, just write.
		_ = modTime
		_, _ = w.Write(body)
	}
}

// AdminCSS serves the admin stylesheet.
func (p *Pages) AdminCSS() http.HandlerFunc {
	return serveAsset("text/css; charset=utf-8", assetAdminCSS)
}

// LoginJS serves the WebAuthn login ceremony script.
func (p *Pages) LoginJS() http.HandlerFunc {
	return serveAsset("application/javascript; charset=utf-8", assetLoginJS)
}

// AccountDetailJS serves the read-only per-account billing fetch script.
func (p *Pages) AccountDetailJS() http.HandlerFunc {
	return serveAsset("application/javascript; charset=utf-8", assetAccountDetailJS)
}

// auditFromRequest is a small helper to audit-log an event using the actor +
// client IP derived from the request context/headers.
func auditFromRequest(r *http.Request, al *auditlog.Logger, actor, action, target string, extra map[string]string) {
	if al == nil {
		return
	}
	meta := map[string]string{"ip": remoteIP(r)}
	for k, v := range extra {
		meta[k] = v
	}
	auditAction(r.Context(), al, actor, action, target, meta)
}
