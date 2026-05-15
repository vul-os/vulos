// Package appfs provides a sandboxed filesystem persistence API for default
// Vula OS apps. Each app gets an isolated sub-directory under ~/.vulos/<app>/
// and can read, write, delete, and list files within that sandbox.
//
// Routes:
//
//	GET    /api/appdata/{app}         — list files in the app's sandbox
//	GET    /api/appdata/{app}/{path}  — read a file
//	PUT    /api/appdata/{app}/{path}  — write (create or overwrite) a file
//	DELETE /api/appdata/{app}/{path}  — delete a file
//
// Path-traversal protection: any path that contains "..", is absolute, or
// resolves outside the sandbox root is rejected with 400.
package appfs

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// appIDPattern matches safe app identifiers (lowercase letters, digits, hyphens).
var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]{0,62}$`)

// Service is the appfs handler. It exposes HTTP handler methods that can be
// registered directly with an http.ServeMux.
type Service struct {
	// baseDir is the root under which per-app sandboxes live (~/.vulos).
	baseDir string
}

// New creates a new Service rooted at baseDir (typically ~/.vulos).
// The directory is created if it does not already exist.
func New(baseDir string) *Service {
	os.MkdirAll(baseDir, 0755)
	return &Service{baseDir: baseDir}
}

// sandboxDir returns the absolute, cleaned sandbox root for the given appID and
// ensures the directory exists. Returns an error if appID is invalid.
func (s *Service) sandboxDir(appID string) (string, error) {
	if !appIDPattern.MatchString(appID) {
		return "", fmt.Errorf("invalid app id")
	}
	dir := filepath.Join(s.baseDir, appID)
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
	appID := r.PathValue("app")
	sandbox, err := s.sandboxDir(appID)
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
	appID := r.PathValue("app")
	relPath := r.PathValue("path")

	sandbox, err := s.sandboxDir(appID)
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
	appID := r.PathValue("app")
	relPath := r.PathValue("path")

	sandbox, err := s.sandboxDir(appID)
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
	appID := r.PathValue("app")
	relPath := r.PathValue("path")

	sandbox, err := s.sandboxDir(appID)
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
