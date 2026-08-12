// Package appfs provides a sandboxed filesystem persistence API for default
// Vulos OS apps. Each (user, app) pair gets an isolated sub-directory under
// ~/.vulos/<userID>/<appID>/ and can read, write, delete, and list files
// within that sandbox.
//
// Routes:
//
//	GET    /api/appdata/{app}         — list files in the caller's app sandbox
//	GET    /api/appdata/{app}/{path}  — read a file
//	PUT    /api/appdata/{app}/{path}  — write (create or overwrite) a file
//	DELETE /api/appdata/{app}/{path}  — delete a file
//
// APPFS-01 (fixed 2026-07-12): the sandbox used to be rooted at
// ~/.vulos/<appID>/ with NO user scoping at all — appID came straight from the
// URL path, so ANY authenticated session could read/overwrite/delete ANY
// app's files regardless of which user actually owns them. On a multi-user
// self-host box, User B could reach User A's app data (and any app's data,
// not just apps User B is entitled to use). The fix adds userID as a second,
// SERVER-DERIVED path segment: every handler reads it from the X-User-ID
// request header, which is populated by auth.Handler.Middleware from the
// validated session (and any client-supplied copy is stripped first — see
// C1/SEC-A in services/auth/handlers.go) — it is never taken from the URL,
// a query param, or any other client-controlled input. A request with no
// resolved user identity is refused (401) rather than falling back to a
// shared/default namespace.
//
// Path-traversal protection: any path that contains "..", is absolute, or
// resolves outside the sandbox root is rejected with 400.
package appfs

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// appIDPattern matches safe app identifiers (lowercase letters, digits, hyphens).
var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]{0,62}$`)

// userIDPattern matches safe user identifiers. Vulos user IDs are generated
// via base64.RawURLEncoding (services/auth.generateID), whose alphabet is
// exactly [A-Za-z0-9_-], so this is intentionally case-sensitive and wider
// than appIDPattern (which is deliberately lowercase-only for URL-supplied
// app slugs). Bounded length guards against pathological header values.
var userIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Service is the appfs handler. It exposes HTTP handler methods that can be
// registered directly with an http.ServeMux.
type Service struct {
	// baseDir is the root under which per-user, per-app sandboxes live
	// (~/.vulos), i.e. sandboxes resolve to baseDir/<userID>/<appID>.
	baseDir string
}

// New creates a new Service rooted at baseDir (typically ~/.vulos).
// The directory is created if it does not already exist.
func New(baseDir string) *Service {
	os.MkdirAll(baseDir, 0755)
	return &Service{baseDir: baseDir}
}

// callerUserID extracts the SERVER-DERIVED user id for the request. It reads
// ONLY the X-User-ID header — set by auth.Handler.Middleware from the
// validated session, never from client input (any client-supplied copy is
// stripped upstream before the trusted value is stamped in its place). This
// function does no stripping itself: it trusts that the middleware chain in
// front of this Service's routes already did (the same assumption every other
// handler in cmd/server/main.go makes of X-User-ID). Returns ("", false) when
// absent or malformed.
func callerUserID(r *http.Request) (string, bool) {
	userID := r.Header.Get("X-User-ID")
	if !userIDPattern.MatchString(userID) {
		return "", false
	}
	return userID, true
}

// sandboxDir returns the absolute, cleaned sandbox root for the given
// (userID, appID) pair and ensures the directory exists. Returns an error if
// either id is invalid.
func (s *Service) sandboxDir(userID, appID string) (string, error) {
	if !userIDPattern.MatchString(userID) {
		return "", fmt.Errorf("invalid user id")
	}
	if !appIDPattern.MatchString(appID) {
		return "", fmt.Errorf("invalid app id")
	}
	dir := filepath.Join(s.baseDir, userID, appID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("cannot create app dir: %w", err)
	}
	// Resolve symlinks so the prefix check is canonical.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("cannot resolve sandbox dir: %w", err)
	}
	return resolved, nil
}

// safeJoin validates and joins relPath under sandbox, returning the absolute path.
// It rejects paths that:
//   - are empty
//   - start with "/"  (absolute)
//   - contain ".."   (traversal component)
//   - resolve outside the sandbox after cleaning
func safeJoin(sandbox, relPath string) (string, error) {
	if relPath == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	// Reject any ".." component before cleaning to catch encoded variants.
	for _, seg := range strings.Split(relPath, "/") {
		if seg == ".." {
			return "", fmt.Errorf("path traversal sequences are not allowed")
		}
	}
	joined := filepath.Join(sandbox, relPath)
	// filepath.EvalSymlinks will fail for non-existent files; use Clean + prefix
	// check instead which is sufficient because we already rejected ".." segments.
	cleaned := filepath.Clean(joined)
	if !strings.HasPrefix(cleaned, sandbox+string(filepath.Separator)) && cleaned != sandbox {
		return "", fmt.Errorf("path escapes sandbox")
	}
	return cleaned, nil
}

// FileEntry is returned by the list endpoint.
type FileEntry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"` // Unix timestamp (seconds)
	IsDir    bool   `json:"is_dir"`
}

// HandleList handles GET /api/appdata/{app}
// Returns a flat list of files (non-recursive) in the app sandbox.
func (s *Service) HandleList(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerUserID(r)
	if !ok {
		writeErr(w, 401, "unauthorized")
		return
	}
	appID := r.PathValue("app")
	sandbox, err := s.sandboxDir(userID, appID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	entries, err := os.ReadDir(sandbox)
	if err != nil {
		writeErr(w, 500, "failed to read directory")
		return
	}

	files := make([]FileEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, FileEntry{
			Name:     e.Name(),
			Size:     info.Size(),
			Modified: info.ModTime().Unix(),
			IsDir:    e.IsDir(),
		})
	}
	writeJSON(w, files)
}

// HandleGet handles GET /api/appdata/{app}/{path}
// Streams the file content with an appropriate Content-Type.
func (s *Service) HandleGet(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerUserID(r)
	if !ok {
		writeErr(w, 401, "unauthorized")
		return
	}
	appID := r.PathValue("app")
	relPath := r.PathValue("path")

	sandbox, err := s.sandboxDir(userID, appID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	absPath, err := safeJoin(sandbox, relPath)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	info, err := os.Stat(absPath)
	if os.IsNotExist(err) {
		writeErr(w, 404, "not found")
		return
	}
	if err != nil {
		writeErr(w, 500, "stat failed")
		return
	}
	if info.IsDir() {
		writeErr(w, 400, "path is a directory; use GET /api/appdata/{app} to list")
		return
	}

	f, err := os.Open(absPath)
	if err != nil {
		writeErr(w, 500, "cannot open file")
		return
	}
	defer f.Close()

	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

// HandlePut handles PUT /api/appdata/{app}/{path}
// Reads the request body and writes it to the sandboxed path, creating
// intermediate directories as needed.
func (s *Service) HandlePut(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerUserID(r)
	if !ok {
		writeErr(w, 401, "unauthorized")
		return
	}
	appID := r.PathValue("app")
	relPath := r.PathValue("path")

	sandbox, err := s.sandboxDir(userID, appID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	absPath, err := safeJoin(sandbox, relPath)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	// Create parent directories if needed.
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		writeErr(w, 500, "cannot create parent directories")
		return
	}

	// Enforce a 64 MiB upload limit to protect against accidental large writes.
	const maxSize = 64 << 20
	body := io.LimitReader(r.Body, maxSize+1)

	data, err := io.ReadAll(body)
	if err != nil {
		writeErr(w, 500, "failed to read request body")
		return
	}
	if int64(len(data)) > maxSize {
		writeErr(w, 413, "file too large (max 64 MiB)")
		return
	}

	if err := os.WriteFile(absPath, data, 0644); err != nil {
		writeErr(w, 500, "failed to write file")
		return
	}

	info, _ := os.Stat(absPath)
	var size int64
	var mtime int64
	if info != nil {
		size = info.Size()
		mtime = info.ModTime().Unix()
	} else {
		mtime = time.Now().Unix()
	}
	writeJSON(w, map[string]any{
		"status":   "ok",
		"path":     relPath,
		"size":     size,
		"modified": mtime,
	})
}

// HandleDelete handles DELETE /api/appdata/{app}/{path}
// Removes the file (not directories) from the sandbox.
func (s *Service) HandleDelete(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerUserID(r)
	if !ok {
		writeErr(w, 401, "unauthorized")
		return
	}
	appID := r.PathValue("app")
	relPath := r.PathValue("path")

	sandbox, err := s.sandboxDir(userID, appID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	absPath, err := safeJoin(sandbox, relPath)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	info, err := os.Stat(absPath)
	if os.IsNotExist(err) {
		writeErr(w, 404, "not found")
		return
	}
	if err != nil {
		writeErr(w, 500, "stat failed")
		return
	}
	if info.IsDir() {
		writeErr(w, 400, "cannot delete directories via this endpoint")
		return
	}

	if err := os.Remove(absPath); err != nil {
		writeErr(w, 500, "failed to delete file")
		return
	}
	writeJSON(w, map[string]string{"status": "deleted", "path": relPath})
}

// Register adds all appfs routes to mux under /api/appdata/.
// The path patterns use Go 1.22+ wildcard syntax.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/appdata/{app}", s.HandleList)
	mux.HandleFunc("GET /api/appdata/{app}/{path...}", s.HandleGet)
	mux.HandleFunc("PUT /api/appdata/{app}/{path...}", s.HandlePut)
	mux.HandleFunc("DELETE /api/appdata/{app}/{path...}", s.HandleDelete)
}

// MigrateLegacySingleUser moves data written under the OLD, un-scoped layout
// (baseDir/<appID>/...) into the new per-user layout (baseDir/<userID>/<appID>/...)
// — see APPFS-01. It is a best-effort, ONE-TIME, single-user-only migration:
//
//   - soleUserID must be the id of the ONLY user account that exists on this
//     box. main.go computes this (empty when there are zero or MULTIPLE
//     accounts) so this function never has to guess which of several users
//     legacy data belongs to — on a genuinely multi-user box, pre-existing
//     unscoped data is left exactly where it is (untouched, still on disk,
//     recoverable by an operator) rather than risk attributing it to the
//     wrong person.
//   - A marker file (baseDir/.appfs_migrated_v2) makes this idempotent: it
//     runs at most once ever, so a later second user signing up on a
//     previously single-user box can never trigger a second, now-ambiguous
//     migration pass.
//
// Call this once at startup, before Register() is wired to live traffic.
func MigrateLegacySingleUser(baseDir, soleUserID string) error {
	marker := filepath.Join(baseDir, ".appfs_migrated_v2")
	if _, err := os.Stat(marker); err == nil {
		return nil // already handled (or deliberately skipped) — never retry.
	}

	defer func() {
		// Best-effort: write the marker regardless of outcome below so a
		// zero-or-multi-user box (soleUserID == "") also stops being rescanned
		// on every subsequent boot.
		_ = os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0644)
	}()

	if soleUserID == "" || !userIDPattern.MatchString(soleUserID) {
		return nil // zero or multiple users — nothing safe to migrate.
	}

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return err
	}

	destRoot := filepath.Join(baseDir, soleUserID)
	for _, e := range entries {
		if !e.IsDir() || !appIDPattern.MatchString(e.Name()) {
			continue // not a legacy per-app dir (or is itself already a userID dir)
		}
		if e.Name() == soleUserID {
			continue // paranoia: never treat the destination itself as a source
		}
		src := filepath.Join(baseDir, e.Name())
		dst := filepath.Join(destRoot, e.Name())
		if _, err := os.Stat(dst); err == nil {
			log.Printf("[appfs] migration: %s already exists under the new per-user layout, leaving legacy %s in place", dst, src)
			continue
		}
		if err := os.MkdirAll(destRoot, 0755); err != nil {
			log.Printf("[appfs] migration: cannot create %s: %v", destRoot, err)
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			log.Printf("[appfs] migration: cannot move %s -> %s: %v", src, dst, err)
			continue
		}
		log.Printf("[appfs] migrated legacy app data %s -> %s", src, dst)
	}
	return nil
}

// writeJSON encodes v as JSON into w with a 200 status.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// writeErr writes a JSON error response with the given HTTP status code.
func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
