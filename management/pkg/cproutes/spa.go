// wire_spa.go — serves the built landing/console SPA (vite `dist/`) on the same
// origin as the API (same-host model: /api/* + /healthz are the API, EVERYTHING
// else falls back to the SPA index.html for client-side routing).
//
// SPA_WIRE anchor: RegisterSPAFallback(mux) is called from main.go right before
// the middleware chain is assembled, so the catch-all is the LEAST-specific
// route on the ServeMux — every concrete API/health/metrics pattern wins over
// it (Go 1.22 ServeMux precedence), and only paths that match nothing else land
// on the SPA. The handler is a no-op (disabled, paths 404 as before) when no
// built SPA is present, so a backend-only deploy is unaffected.
package cproutes

import (
	"crypto/rand"
	"encoding/base64"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// SPANonce returns a fresh, cryptographically-random per-request CSP nonce.
func SPANonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Never reuse a static nonce: on entropy failure fall back to a value that
		// still differs per process start rather than a constant an attacker could
		// predict and match with an injected inline script.
		return base64.RawStdEncoding.EncodeToString([]byte("spa-nonce-fallback"))
	}
	return base64.RawStdEncoding.EncodeToString(b)
}

// SPAStrictCSP builds the SPA Content-Security-Policy with a per-request script
// nonce and NO 'unsafe-inline' in script-src (M2). The built SPA's only inline
// script (the pre-paint theme bootstrap) carries the nonce (see WriteSPAIndexHTML);
// its module entry + hashed chunks + modulepreload are all same-origin, so 'self'
// admits them without strict-dynamic. object-src is 'none' so no plugin/embed can
// execute. style-src keeps 'unsafe-inline' — Vite/React inject inline styles and
// the finding targets script-src only.
func SPAStrictCSP(nonce string) string {
	return "default-src 'self'; " +
		"script-src 'self' 'nonce-" + nonce + "'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		"font-src 'self' data:; " +
		"connect-src 'self' https://api.github.com; " +
		"manifest-src 'self'; " +
		"worker-src 'self' blob:; " +
		"object-src 'none'; " +
		"base-uri 'self'; " +
		"form-action 'self'; " +
		"frame-ancestors 'none'"
}

// SelfContainedPageCSP is the policy for first-party static HTML *sub-pages* we
// ship in the build output — e.g. the product mini-site landing pages under
// /products/<slug>/landing.html, which the marketing SPA embeds in a SAME-ORIGIN
// <iframe>. Unlike the SPA shell (SPAStrictCSP), these pages:
//
//   - MUST be framable by their own origin, so frame-ancestors is 'self' (and the
//     handler drops X-Frame-Options: DENY → SAMEORIGIN). With 'none'/DENY the
//     browser refuses to render them in the iframe at all — a blank product page.
//   - carry their own inline <script>/<style> with no per-request nonce (they are
//     prebuilt static files, not server-rendered), so script-src/style-src allow
//     'unsafe-inline'. They are trusted first-party assets from our own build.
//
// Everything else stays locked down (default-src 'self', object-src 'none',
// base-uri 'self').
func SelfContainedPageCSP() string {
	return "default-src 'self'; " +
		"script-src 'self' 'unsafe-inline'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		"font-src 'self' data:; " +
		"connect-src 'self'; " +
		"manifest-src 'self'; " +
		"worker-src 'self' blob:; " +
		"object-src 'none'; " +
		"base-uri 'self'; " +
		"form-action 'self'; " +
		"frame-ancestors 'self'"
}

// isSelfContainedPage reports whether a cleaned request path resolves to a
// first-party static HTML sub-page (any *.html other than the root SPA shell).
// These are self-contained mini-sites (product landings, standalone doc sites)
// that need the framable, inline-friendly SelfContainedPageCSP rather than the
// strict SPA policy.
func isSelfContainedPage(upath string) bool {
	return strings.HasSuffix(upath, ".html") && upath != "/index.html"
}

// StampSPANonce stamps the per-request CSP nonce onto every <script element in
// the SPA HTML so the strict, unsafe-inline-free policy (SPAStrictCSP) admits the
// inline theme-bootstrap script and the module entry. Shared by WriteSPAIndexHTML
// (apex, disk-served) and the embedded /console SPA handler (web/serve.go) so both
// surfaces stamp identically — the /console section hosts the ADMIN console and
// must run under the SAME strict CSP as the apex, not the weaker legacy policy.
//
//	"<script>"                    → `<script nonce="N">`
//	`<script type="module" src=…>`→ `<script nonce="N" type="module" src=…>`
func StampSPANonce(raw []byte, nonce string) string {
	return strings.ReplaceAll(string(raw), "<script", `<script nonce="`+nonce+`"`)
}

// WriteSPAIndexHTML reads the SPA index.html, stamps the per-request nonce onto
// every <script tag (so the strict, unsafe-inline-free CSP admits the inline
// theme-bootstrap script and the module entry), and writes it. Returns an error
// (caller should 404/500) if the file cannot be read.
func WriteSPAIndexHTML(w http.ResponseWriter, indexPath, nonce string) error {
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, err = io.WriteString(w, StampSPANonce(raw, nonce))
	return err
}

// spaStaticDir resolves the directory the built SPA is served from. CP_STATIC_DIR
// wins (the container image sets it); otherwise a few conventional locations are
// probed so `go run ./cmd/server` from backend/cp finds the repo-root dist/ for
// local dev. Returns "" when no built SPA is found → the fallback stays disabled.
func spaStaticDir() string {
	cands := []string{}
	if d := os.Getenv("CP_STATIC_DIR"); d != "" {
		cands = append(cands, d)
	}
	// /app/webdist: image layout. dist / ../../dist: local `go run` from backend/cp.
	cands = append(cands, "/app/webdist", "webdist", filepath.Join("..", "..", "dist"), "dist")
	for _, d := range cands {
		if fi, err := os.Stat(filepath.Join(d, "index.html")); err == nil && !fi.IsDir() {
			return d
		}
	}
	return ""
}

// RegisterSPAFallback installs the SPA catch-all on mux when a built SPA exists.
// Never shadows the API: requests to /api/*, /healthz, /readyz and /metrics are
// 404'd here (they only reach this handler when no concrete route matched, i.e.
// an unknown API path — which must NOT be answered with index.html).
func RegisterSPAFallback(mux *http.ServeMux) {
	dir := spaStaticDir()
	if dir == "" {
		log.Printf("[spa] no built SPA found (set CP_STATIC_DIR to enable) — apex serves API only")
		return
	}
	indexPath := filepath.Join(dir, "index.html")
	fileServer := http.FileServer(http.Dir(dir))

	// Registered methodless (the true catch-all) rather than "GET /": a
	// method-specific root pattern conflicts with existing methodless subtree
	// patterns like "/backend/" on the Go 1.22 ServeMux. The method is checked
	// inside instead — only GET/HEAD navigations get the SPA.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		upath := path.Clean("/" + r.URL.Path)

		// Never let the SPA masquerade as (an unknown) API/health/metrics route.
		if upath == "/api" || strings.HasPrefix(upath, "/api/") ||
			upath == "/healthz" || upath == "/readyz" || upath == "/metrics" {
			http.NotFound(w, r)
			return
		}

		// The SPA needs a self-scoped CSP: the API-wide default (default-src
		// 'none', set in middleware) otherwise blocks the bundle's own scripts,
		// styles, fonts and images. Strict, nonce-based, NO script-src
		// 'unsafe-inline' (M2). Set before serving so it wins over the default.
		nonce := SPANonce()
		w.Header().Set("Content-Security-Policy", SPAStrictCSP(nonce))

		// Serve a real static asset (hashed JS/CSS, favicon, manifest, images)
		// directly when it exists under dir; otherwise fall through to index.html
		// so client-side routing owns the path (deep links like /products/office).
		if upath != "/" {
			full := filepath.Join(dir, filepath.FromSlash(upath))
			if rel, err := filepath.Rel(dir, full); err == nil && !strings.HasPrefix(rel, "..") {
				if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
					// First-party static HTML sub-pages (product mini-site landings
					// under /products/<slug>/landing.html, standalone doc sites) are
					// embedded in a SAME-ORIGIN iframe and carry their own inline
					// scripts. The strict SPA CSP (frame-ancestors 'none') + the
					// middleware's X-Frame-Options: DENY would make the browser
					// refuse to render them → a blank product page. Serve them with
					// the framable, inline-friendly self-contained-page policy.
					if isSelfContainedPage(upath) {
						w.Header().Set("Content-Security-Policy", SelfContainedPageCSP())
						w.Header().Set("X-Frame-Options", "SAMEORIGIN")
					}
					fileServer.ServeHTTP(w, r)
					return
				}
			}
		}
		// index.html gets the nonce stamped onto its inline theme-bootstrap script
		// so the strict CSP admits it without 'unsafe-inline'.
		if err := WriteSPAIndexHTML(w, indexPath, nonce); err != nil {
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
	log.Printf("[spa] serving landing/console SPA from %s (apex same-host: /api=API, else→index.html)", dir)
}
