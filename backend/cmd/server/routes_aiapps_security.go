package main

// routes_aiapps_security.go — SEC-I: hardened AI-apps handlers
// Replaces the inline anonymous handlers in main.go with:
//   - ^[a-z0-9][a-z0-9-]{0,63}$ charset validation on {id}
//   - realpath-containment under aiAppsDir before any FS operation
//   - admin-gate + audit on POST /api/ai-apps/save and DELETE /api/ai-apps/{id}
//   - GET /api/ai-apps/{id}/html and /python are read-only, no exec path

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"vulos/backend/services/auth"
)

// secI_idRe accepts app ids of the form ai-<digits> (and any future lowercase slug).
// Pattern: starts with [a-z0-9], followed by 0-63 chars of [a-z0-9-].
var secI_idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// secI_resolvedBase returns the resolved (realpath) base directory and caches it.
// On systems where the aiAppsDir does not yet exist the raw cleaned path is used.
func secI_resolvedBase(aiAppsDir string) string {
	if r, err := filepath.EvalSymlinks(aiAppsDir); err == nil {
		return r
	}
	return filepath.Clean(aiAppsDir)
}

// secI_safeAppDir validates id and returns the resolved path to the app subdirectory,
// guaranteed to be contained within aiAppsDir.
// Returns ("", false) if id fails charset validation or the resolved path escapes the base.
func secI_safeAppDir(aiAppsDir, id string) (string, bool) {
	if !secI_idRe.MatchString(id) {
		return "", false
	}
	base := secI_resolvedBase(aiAppsDir)
	candidate := filepath.Join(base, id)
	// Resolve any symlinks in the candidate (best-effort; dir may not exist yet for save).
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = resolved
	}
	// Must be strictly under base + separator.
	if len(candidate) <= len(base)+1 || candidate[:len(base)+1] != base+string(os.PathSeparator) {
		return "", false
	}
	return candidate, true
}

// secI_isAdmin returns true when the request carries an X-User-ID header that maps
// to an admin profile in authStore.
func secI_isAdmin(r *http.Request, authStore *auth.Store) bool {
	p, _ := authStore.GetProfile(r.Header.Get("X-User-ID"))
	return p != nil && p.Role == auth.RoleAdmin
}

// secI_auditLog emits a structured audit line to the process log.
func secI_auditLog(action, id, userID string, ok bool) {
	result := "OK"
	if !ok {
		result = "DENIED"
	}
	log.Printf("[SEC-I audit] action=%s id=%q user=%q result=%s ts=%s",
		action, id, userID, result, time.Now().UTC().Format(time.RFC3339))
}

// registerAIAppsSecurityWrappers registers the hardened AI-apps HTTP handlers on mux.
// Call this once from main() in place of the inline anonymous handlers.
func registerAIAppsSecurityWrappers(mux *http.ServeMux, aiAppsDir string, authStore *auth.Store) {
	// Ensure base directory exists.
	os.MkdirAll(aiAppsDir, 0755)

	// POST /api/ai-apps/save — admin-gated, audited, no exec.
	mux.HandleFunc("POST /api/ai-apps/save", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")

		// Kill-switch → admin gate.
		if !secI_isAdmin(r, authStore) {
			secI_auditLog("save", "", userID, false)
			writeErr(w, 403, "admin only")
			return
		}

		var req struct {
			Title  string `json:"title"`
			HTML   string `json:"html"`
			Python string `json:"python"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}

		// Generate a deterministic, charset-safe id.
		id := fmt.Sprintf("ai-%d", time.Now().UnixMilli())

		appDir, ok := secI_safeAppDir(aiAppsDir, id)
		if !ok {
			// Should never happen for our generated ids, but be safe.
			secI_auditLog("save", id, userID, false)
			writeErr(w, 400, "invalid app id")
			return
		}

		if err := os.MkdirAll(appDir, 0755); err != nil {
			writeErr(w, 500, "could not create app directory")
			return
		}

		meta := map[string]string{"title": req.Title, "id": id, "created": time.Now().Format(time.RFC3339)}
		metaData, _ := json.MarshalIndent(meta, "", "  ")
		os.WriteFile(filepath.Join(appDir, "meta.json"), metaData, 0644)

		if req.HTML != "" {
			os.WriteFile(filepath.Join(appDir, "index.html"), []byte(req.HTML), 0644)
		}
		if req.Python != "" {
			// Written at rest for later sandboxed execution (not executed here).
			os.WriteFile(filepath.Join(appDir, "server.py"), []byte(req.Python), 0644)
		}

		secI_auditLog("save", id, userID, true)
		writeJSON(w, map[string]string{"id": id, "status": "saved"})
	})

	// GET /api/ai-apps — list all saved apps (read-only, no admin gate).
	mux.HandleFunc("GET /api/ai-apps", func(w http.ResponseWriter, r *http.Request) {
		base := secI_resolvedBase(aiAppsDir)
		entries, _ := os.ReadDir(base)
		var apps []map[string]string
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// Charset-check the directory name as an id.
			if !secI_idRe.MatchString(e.Name()) {
				continue
			}
			appDir, ok := secI_safeAppDir(base, e.Name())
			if !ok {
				continue
			}
			metaPath := filepath.Join(appDir, "meta.json")
			data, err := os.ReadFile(metaPath)
			if err != nil {
				continue
			}
			var meta map[string]string
			json.Unmarshal(data, &meta)
			if _, err := os.Stat(filepath.Join(appDir, "server.py")); err == nil {
				meta["has_python"] = "true"
			}
			if _, err := os.Stat(filepath.Join(appDir, "index.html")); err == nil {
				meta["has_html"] = "true"
			}
			apps = append(apps, meta)
		}
		writeJSON(w, apps)
	})

	// GET /api/ai-apps/{id}/html — realpath-contained read, no exec.
	mux.HandleFunc("GET /api/ai-apps/{id}/html", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		appDir, ok := secI_safeAppDir(aiAppsDir, id)
		if !ok {
			writeErr(w, 400, "invalid id")
			return
		}
		data, err := os.ReadFile(filepath.Join(appDir, "index.html"))
		if err != nil {
			writeErr(w, 404, "not found")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write(data)
	})

	// GET /api/ai-apps/{id}/python — realpath-contained read, no exec.
	mux.HandleFunc("GET /api/ai-apps/{id}/python", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		appDir, ok := secI_safeAppDir(aiAppsDir, id)
		if !ok {
			writeErr(w, 400, "invalid id")
			return
		}
		data, err := os.ReadFile(filepath.Join(appDir, "server.py"))
		if err != nil {
			writeErr(w, 404, "not found")
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write(data)
	})

	// DELETE /api/ai-apps/{id} — admin-gated, audited, realpath-contained.
	mux.HandleFunc("DELETE /api/ai-apps/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		userID := r.Header.Get("X-User-ID")

		if !secI_isAdmin(r, authStore) {
			secI_auditLog("delete", id, userID, false)
			writeErr(w, 403, "admin only")
			return
		}

		appDir, ok := secI_safeAppDir(aiAppsDir, id)
		if !ok {
			secI_auditLog("delete", id, userID, false)
			writeErr(w, 400, "invalid id")
			return
		}

		os.RemoveAll(appDir)
		secI_auditLog("delete", id, userID, true)
		writeJSON(w, map[string]string{"status": "deleted"})
	})
}
