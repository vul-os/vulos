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

// aiAppsEditDisabled is the shared kill-switch for AI-app mutation. When
// DISABLE_AI_APP_EDIT=1 every mutating route (save / update / delete / snapshot
// / rollback) fails closed with 503. Read-only routes (list / html / python /
// versions) stay available so already-saved apps remain viewable. The frontend
// reads GET /api/ai-apps/config to surface the disabled state instead of only
// seeing a failure toast.
func aiAppsEditDisabled() bool {
	return os.Getenv("DISABLE_AI_APP_EDIT") == "1"
}

// aiAppsSecurityHeaders writes defense-in-depth headers onto the served AI-app
// HTML so the AI-generated page is inert even if it is ever loaded top-level
// (bypassing the shell's sandboxed iframe). The CSP `sandbox` directive forces
// the document into a unique/opaque origin at the browser level — it therefore
// cannot script the OS origin, read the OS session cookie, or fetch the OS API,
// regardless of how it was opened. `allow-scripts` keeps the app itself
// functional; `allow-same-origin` is deliberately omitted so the page never
// regains access to the serving (OS) origin. frame-ancestors + X-Frame-Options
// keep the served page from being reframed by a hostile top-level document, and
// nosniff stops content-type confusion. default-src 'none' blocks all network
// egress (connect/img/font/etc.) back to the OS origin or anywhere else; inline
// scripts/styles are allowed so a self-contained AI app still renders.
func aiAppsSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"sandbox allow-scripts allow-forms allow-popups; "+
			"default-src 'none'; "+
			"script-src 'unsafe-inline'; "+
			"style-src 'unsafe-inline'; "+
			"img-src data: blob:; "+
			"font-src data:; "+
			"frame-ancestors 'self'; "+
			"base-uri 'none'; "+
			"form-action 'none'")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// The page is untrusted AI output; never let it be cached under the OS origin.
	w.Header().Set("Cache-Control", "no-store")
}

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

	// GET /api/ai-apps/config — surface the kill-switch state to the frontend so a
	// disabled builder renders a banner rather than only failing on click.
	mux.HandleFunc("GET /api/ai-apps/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"edit_disabled": aiAppsEditDisabled()})
	})

	// POST /api/ai-apps/save — kill-switch → admin-gated, audited, no exec.
	mux.HandleFunc("POST /api/ai-apps/save", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")

		// Kill-switch (fail closed before any auth/FS work).
		if aiAppsEditDisabled() {
			writeErr(w, 503, "ai app editing is disabled")
			return
		}

		// Admin gate.
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
		// Defense-in-depth: the served HTML is AI-generated and untrusted. Ship it
		// with a sandbox CSP so it is inert even if opened top-level (bypassing the
		// shell's sandboxed iframe) — it cannot reach the OS origin's cookies,
		// session, or API. Set headers BEFORE Content-Type/body.
		aiAppsSecurityHeaders(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
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

		// Kill-switch (fail closed).
		if aiAppsEditDisabled() {
			writeErr(w, 503, "ai app editing is disabled")
			return
		}

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
