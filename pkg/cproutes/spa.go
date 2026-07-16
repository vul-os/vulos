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
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

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
		// styles, fonts and images. Set before serving so it wins over the default.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self' data:; "+
				"connect-src 'self' https://api.github.com; "+
				"manifest-src 'self'; "+
				"worker-src 'self' blob:; "+
				"base-uri 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'")

		// Serve a real static asset (hashed JS/CSS, favicon, manifest, images)
		// directly when it exists under dir; otherwise fall through to index.html
		// so client-side routing owns the path (deep links like /products/office).
		if upath != "/" {
			full := filepath.Join(dir, filepath.FromSlash(upath))
			if rel, err := filepath.Rel(dir, full); err == nil && !strings.HasPrefix(rel, "..") {
				if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
					fileServer.ServeHTTP(w, r)
					return
				}
			}
		}
		http.ServeFile(w, r, indexPath)
	})
	log.Printf("[spa] serving landing/console SPA from %s (apex same-host: /api=API, else→index.html)", dir)
}
