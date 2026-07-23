package webhost

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ServeSite returns an http.Handler that serves the static tree of a single
// (userID, site) with SPA-fallback semantics. It is the second hostile
// surface: the request URL path is attacker-influenced and is guarded here
// independently of Deploy's extraction guards.
//
// Behavior:
//
//   - The handler is rooted at the site's htdocs directory. If the site does
//     not exist yet the handler 404s every request.
//   - A real file under the root is served with a strong path-traversal guard
//     (filepath.Clean + confirm the resolved path stays inside the root) and
//     "Cache-Control: public, max-age=300" so the PUBWEB-03 nginx edge cache
//     picks it up.
//   - Anything that is not a real file — a missing path or a directory —
//     falls back to index.html served with "Cache-Control: no-cache" (the SPA
//     entry point). If index.html itself is missing, 404.
//   - Dotfiles are NEVER served: any path segment beginning with "." is
//     refused with 404 (this also keeps meta.json out of reach even though it
//     lives above the root, and blocks .git / .env leakage from a sloppy
//     bundle).
//   - There is NO directory listing. A directory request resolves to the SPA
//     fallback, never an index of files.
func (s *Service) ServeSite(userID, site string) http.Handler {
	root, err := s.siteRoot(userID, site, false)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.serveFrom(root, w, r)
	})
}

// serveFrom implements the SPA-fallback static serving for a resolved root.
func (s *Service) serveFrom(root string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// If the site root itself is absent, nothing is published.
	if _, err := os.Stat(root); err != nil {
		http.NotFound(w, r)
		return
	}

	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}
	// path.Clean collapses "." and ".." lexically; combined with the dotfile
	// refusal and the post-join prefix check below, traversal is closed off.
	clean := path.Clean(upath)

	// Refuse dotfiles anywhere in the path (".", "..", ".git", ".env", …).
	// A "." request maps to the SPA fallback rather than an error.
	if clean != "/" {
		for _, seg := range strings.Split(strings.Trim(clean, "/"), "/") {
			if strings.HasPrefix(seg, ".") {
				http.NotFound(w, r)
				return
			}
		}
	}

	rel := strings.TrimPrefix(clean, "/")
	if rel == "" {
		s.serveIndex(root, w, r)
		return
	}

	joined := filepath.Join(root, filepath.FromSlash(rel))
	resolved := filepath.Clean(joined)
	// Post-join containment check — belt to path.Clean's suspenders.
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}

	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		// Missing file or a directory (no listing) -> SPA fallback.
		s.serveIndex(root, w, r)
		return
	}
	if !info.Mode().IsRegular() {
		// Never serve non-regular files (sockets, devices, symlinks that
		// slipped in, …).
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(resolved)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeContent(w, r, filepath.Base(resolved), info.ModTime(), f)
}

// serveIndex serves the SPA entry point (index.html) with no-cache, or 404 if
// the site has no index.html.
func (s *Service) serveIndex(root string, w http.ResponseWriter, r *http.Request) {
	index := filepath.Join(root, "index.html")
	info, err := os.Stat(index)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(index)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", info.ModTime(), f)
}
