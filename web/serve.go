package web

import (
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
)

// MountPrefix is where the console SPA is served. The Vite build sets
// `base: '/console/'` so hashed asset URLs (/console/assets/*) resolve here —
// keep the two in lockstep. A self-hoster running the management binary reaches
// the React console at <origin>/console/.
const MountPrefix = "/console/"

// Register mounts the embedded console SPA on mux at MountPrefix with SPA
// fallback (unknown sub-paths serve index.html so client-side routing owns deep
// links). It is a no-op that logs when no built SPA is embedded, so a
// backend-only build is unaffected. Mount it as an ordinary registrar — the
// prefix is more specific than the apex "/" fallback, so concrete API/health
// routes and this console both win over the apex SPA catch-all.
//
// Go's ServeMux auto-redirects the bare /console → /console/, so no separate
// exact-path handler is needed.
func Register(mux *http.ServeMux) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		log.Printf("[console] embedded SPA unavailable: %v", err)
		return
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		log.Printf("[console] no built console SPA embedded (web/dist/index.html missing) — %s disabled", MountPrefix)
		return
	}
	mux.Handle(MountPrefix, http.StripPrefix(strings.TrimSuffix(MountPrefix, "/"), spaHandler(sub)))
	log.Printf("[console] serving embedded management SPA at %s", MountPrefix)
}

// spaHandler serves static assets from sub when they exist, else falls back to
// index.html. r.URL.Path arrives already stripped of the /console prefix.
func spaHandler(sub fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(sub))
	index, _ := fs.ReadFile(sub, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		// Self-scoped CSP: the API-wide default (default-src 'none') would block
		// the bundle's own scripts/styles/fonts. Mirrors pkg/cproutes/spa.go.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self' data:; "+
				"connect-src 'self'; "+
				"manifest-src 'self'; "+
				"worker-src 'self' blob:; "+
				"base-uri 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'")

		upath := path.Clean("/" + r.URL.Path)
		if upath != "/" {
			name := strings.TrimPrefix(upath, "/")
			if f, err := sub.Open(name); err == nil {
				st, statErr := f.Stat()
				_ = f.Close()
				if statErr == nil && !st.IsDir() {
					fileServer.ServeHTTP(w, r)
					return
				}
			}
		}

		// SPA fallback → index.html (client-side routing owns deep links).
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
