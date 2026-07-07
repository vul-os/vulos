package main

// routes_aiapps.go — AI-06: Edit-with-AI route for saved AI apps.
//
// Exports:
//   registerAIAppsRoutes(mux, aiAppsDir, authStore)
//
// Security model (mirrors SEC-B pattern used across this server):
//   1. Kill-switch: DISABLE_AI_APP_EDIT env var blocks the route entirely.
//   2. Admin gate: caller must have role == "admin".
//   3. Audit log: every mutating call is logged via log.Printf with [aiapps-audit] prefix.
//   4. Path-traversal (SEC-I): id is validated against [a-z0-9_-] charset, then realpath
//      containment is confirmed before any write.
//   5. Atomic write: temp file + os.Rename so readers never see a partial file.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"vulos/backend/services/auth"
)

// aiEdit_idRe allows only safe, unambiguous id characters (no dots, slashes, …).
var aiEdit_idRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

// registerAIAppsRoutes wires the POST /api/ai-apps/{id}/update route into mux.
// Existing GET and DELETE handlers for ai-apps remain in main.go (this PR is additive only).
func registerAIAppsRoutes(mux *http.ServeMux, aiAppsDir string, authStore *auth.Store) {
	// POST /api/ai-apps/{id}/update — admin-gated, audit-logged, path-safe, atomic write.
	mux.HandleFunc("POST /api/ai-apps/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		// 1. Kill-switch (shared with save/delete/snapshot/rollback)
		if aiAppsEditDisabled() {
			writeErr(w, 503, "ai app editing is disabled")
			return
		}

		// 2. Admin gate
		userID := r.Header.Get("X-User-ID")
		p, ok := authStore.GetProfile(userID)
		if !ok || p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}

		// 3. Validate id charset (SEC-I — prevent path injection via id)
		id := r.PathValue("id")
		if !aiEdit_idRe.MatchString(id) {
			writeErr(w, 400, "invalid app id")
			return
		}

		// 4. Realpath containment check (SEC-I — belt-and-suspenders)
		appDir := filepath.Join(aiAppsDir, id)
		resolvedAppDir, err := filepath.EvalSymlinks(appDir)
		if err != nil {
			// Directory must exist (app must have been saved first)
			writeErr(w, 404, "app not found")
			return
		}
		resolvedBase, err := filepath.EvalSymlinks(aiAppsDir)
		if err != nil {
			writeErr(w, 500, "internal error")
			return
		}
		if !isAI6_containedUnder(resolvedAppDir, resolvedBase) {
			writeErr(w, 400, "invalid app id")
			return
		}

		// 5. Parse request body — limit to 10 MiB (HTML/Python should never be larger)
		var req struct {
			HTML   string `json:"html"`
			Python string `json:"python"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20)).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request body")
			return
		}
		if req.HTML == "" && req.Python == "" {
			writeErr(w, 400, "at least one of html or python must be provided")
			return
		}

		// 6. Audit log (before write — mirrors SEC-B pattern)
		log.Printf("[aiapps-audit] user=%s action=update app=%s html=%v python=%v ip=%s",
			userID, id, req.HTML != "", req.Python != "", r.RemoteAddr)

		// 6b. Auto-snapshot the CURRENT live version before overwriting it, so the
		// rollback UI has real history to restore from. Best-effort: a snapshot
		// failure must not block a legitimate update (it is logged), but on the
		// happy path every update is preceded by a bounded (a07MaxVersions) snapshot.
		if err := SnapshotVersion(aiAppsDir, id); err != nil {
			log.Printf("[aiapps-audit] user=%s action=update app=%s WARN pre-update snapshot failed: %v", userID, id, err)
		}

		// 7. Atomic write via temp+rename
		if req.HTML != "" {
			if err := aiEdit_atomicWrite(filepath.Join(appDir, "index.html"), []byte(req.HTML)); err != nil {
				log.Printf("[aiapps-audit] user=%s action=update app=%s ERROR writing html: %v", userID, id, err)
				writeErr(w, 500, fmt.Sprintf("failed to write html: %v", err))
				return
			}
		}
		if req.Python != "" {
			if err := aiEdit_atomicWrite(filepath.Join(appDir, "server.py"), []byte(req.Python)); err != nil {
				log.Printf("[aiapps-audit] user=%s action=update app=%s ERROR writing python: %v", userID, id, err)
				writeErr(w, 500, fmt.Sprintf("failed to write python: %v", err))
				return
			}
		}

		log.Printf("[aiapps-audit] user=%s action=update app=%s OK", userID, id)
		writeJSON(w, map[string]string{"id": id, "status": "updated"})
	})
}

// isAI6_containedUnder reports whether child is equal to or nested inside parent.
// Both paths must already be resolved (filepath.EvalSymlinks).
func isAI6_containedUnder(child, parent string) bool {
	// Ensure parent ends with separator so "prefix-attack" (parentXYZ) can't match.
	if len(parent) > 0 && parent[len(parent)-1] != filepath.Separator {
		parent += string(filepath.Separator)
	}
	return child == parent[:len(parent)-1] || // exact match (child == parent dir itself)
		len(child) > len(parent) && child[:len(parent)] == parent
}

// aiEdit_atomicWrite writes data to path atomically using a temp file + rename.
func aiEdit_atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".aiEdit_tmp_*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
