package main

// AI-07: AI app version history + rollback
// Endpoints:
//   POST /api/ai-apps/{id}/snapshot  — create a timestamped snapshot of current live files
//   GET  /api/ai-apps/{id}/versions  — list snapshots
//   POST /api/ai-apps/{id}/rollback  — copy a snapshot's files back to live

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"vulos/backend/services/auth"
)

// a07ValidIDRe allows only alphanumeric, dash, underscore (mirrors SEC-I charset).
var a07ValidIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// a07ValidateID validates the app id and resolves its real path, returning the
// resolved app directory or an error.  Mirrors SEC-I realpath containment.
func a07ValidateID(aiAppsDir, id string) (string, error) {
	if !a07ValidIDRe.MatchString(id) {
		return "", fmt.Errorf("invalid id")
	}
	candidate := filepath.Join(aiAppsDir, id)
	resolved, err := filepath.EvalSymlinks(aiAppsDir)
	if err != nil {
		// aiAppsDir not yet present – use Clean-based containment
		resolved = filepath.Clean(aiAppsDir)
	}
	clean := filepath.Clean(candidate)
	// Ensure clean is strictly inside aiAppsDir
	rel, err := filepath.Rel(resolved, clean)
	if err != nil || rel == ".." || len(rel) >= 2 && rel[:2] == ".." {
		return "", fmt.Errorf("path traversal detected")
	}
	return clean, nil
}

// a07VersionMeta is the per-snapshot metadata written to meta.json.
type a07VersionMeta struct {
	Timestamp string `json:"timestamp"`
	Brief     string `json:"brief"`
}

// a07VersionEntry is the entry written into versions.json.
type a07VersionEntry struct {
	Version   string `json:"version"`
	Timestamp string `json:"timestamp"`
	Brief     string `json:"brief"`
}

const a07MaxVersions = 20

// SnapshotVersion creates a timestamped snapshot of the current live files for the
// given app id.  It is exported so it can be called from save/update handlers in
// a future iteration, and is also invoked by the /snapshot endpoint below.
func SnapshotVersion(aiAppsDir, id string) error {
	appDir, err := a07ValidateID(aiAppsDir, id)
	if err != nil {
		return fmt.Errorf("SnapshotVersion: %w", err)
	}

	ts := time.Now().UTC().Format("20060102T150405Z")
	versionsDir := filepath.Join(appDir, "versions")
	snapDir := filepath.Join(versionsDir, ts)
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		return fmt.Errorf("SnapshotVersion: mkdir: %w", err)
	}

	// Copy live files if they exist
	for _, fname := range []string{"index.html", "server.py"} {
		src := filepath.Join(appDir, fname)
		dst := filepath.Join(snapDir, fname)
		if data, err := os.ReadFile(src); err == nil {
			if err := os.WriteFile(dst, data, 0644); err != nil {
				return fmt.Errorf("SnapshotVersion: write %s: %w", fname, err)
			}
		}
	}

	// Build brief from meta.json title
	brief := ""
	if data, err := os.ReadFile(filepath.Join(appDir, "meta.json")); err == nil {
		var meta map[string]string
		if json.Unmarshal(data, &meta) == nil {
			brief = meta["title"]
		}
	}
	if brief == "" {
		brief = "snapshot"
	}

	// Write per-snapshot meta.json
	snapMeta := a07VersionMeta{Timestamp: ts, Brief: brief}
	if snapMetaData, err := json.MarshalIndent(snapMeta, "", "  "); err == nil {
		os.WriteFile(filepath.Join(snapDir, "meta.json"), snapMetaData, 0644)
	}

	// Update versions.json
	versionsFile := filepath.Join(versionsDir, "versions.json")
	var versions []a07VersionEntry
	if data, err := os.ReadFile(versionsFile); err == nil {
		json.Unmarshal(data, &versions)
	}
	versions = append(versions, a07VersionEntry{Version: ts, Timestamp: ts, Brief: brief})

	// Sort by version (timestamp string, ascending)
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Version < versions[j].Version
	})

	// Cap at a07MaxVersions — prune oldest
	if len(versions) > a07MaxVersions {
		toRemove := versions[:len(versions)-a07MaxVersions]
		for _, old := range toRemove {
			oldDir := filepath.Join(versionsDir, old.Version)
			if err := os.RemoveAll(oldDir); err != nil {
				log.Printf("[ai7] prune old version %s: %v", old.Version, err)
			}
		}
		versions = versions[len(versions)-a07MaxVersions:]
	}

	data, err := json.MarshalIndent(versions, "", "  ")
	if err != nil {
		return fmt.Errorf("SnapshotVersion: marshal versions.json: %w", err)
	}
	if err := os.WriteFile(versionsFile, data, 0644); err != nil {
		return fmt.Errorf("SnapshotVersion: write versions.json: %w", err)
	}

	log.Printf("[ai7] snapshot %s for app %s", ts, id)
	return nil
}

// execAuditLog records an admin action with user, action, and target.
func execAuditLog(userID, action, target string) {
	log.Printf("[audit] user=%s action=%s target=%s", userID, action, target)
}

// a07RequireAdmin returns false and writes 403 if the request user is not an admin.
func a07RequireAdmin(w http.ResponseWriter, r *http.Request, authStore *auth.Store) bool {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "unauthorized")
		return false
	}
	p, _ := authStore.GetProfile(userID)
	if p == nil || p.Role != auth.RoleAdmin {
		writeErr(w, 403, "admin only")
		return false
	}
	return true
}

// registerAIAppsVersionsRoutes wires the three AI-07 version-history endpoints.
func registerAIAppsVersionsRoutes(mux *http.ServeMux, aiAppsDir string, authStore *auth.Store) {
	// POST /api/ai-apps/{id}/snapshot — create a snapshot of current live files
	mux.HandleFunc("POST /api/ai-apps/{id}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if !a07RequireAdmin(w, r, authStore) {
			return
		}
		id := r.PathValue("id")
		appDir, err := a07ValidateID(aiAppsDir, id)
		if err != nil {
			writeErr(w, 400, "invalid app id")
			return
		}
		if _, err := os.Stat(appDir); os.IsNotExist(err) {
			writeErr(w, 404, "app not found")
			return
		}
		userID := r.Header.Get("X-User-ID")
		execAuditLog(userID, "ai7_snapshot", id)
		if err := SnapshotVersion(aiAppsDir, id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "snapshotted", "id": id})
	})

	// GET /api/ai-apps/{id}/versions — list version snapshots
	mux.HandleFunc("GET /api/ai-apps/{id}/versions", func(w http.ResponseWriter, r *http.Request) {
		if !a07RequireAdmin(w, r, authStore) {
			return
		}
		id := r.PathValue("id")
		appDir, err := a07ValidateID(aiAppsDir, id)
		if err != nil {
			writeErr(w, 400, "invalid app id")
			return
		}
		if _, err := os.Stat(appDir); os.IsNotExist(err) {
			writeErr(w, 404, "app not found")
			return
		}
		userID := r.Header.Get("X-User-ID")
		execAuditLog(userID, "ai7_list_versions", id)

		versionsFile := filepath.Join(appDir, "versions", "versions.json")
		var versions []a07VersionEntry
		if data, err := os.ReadFile(versionsFile); err == nil {
			json.Unmarshal(data, &versions)
		}
		if versions == nil {
			versions = []a07VersionEntry{}
		}
		// Return newest first
		sort.Slice(versions, func(i, j int) bool {
			return versions[i].Version > versions[j].Version
		})
		writeJSON(w, versions)
	})

	// POST /api/ai-apps/{id}/rollback {version} — restore a snapshot to live files
	mux.HandleFunc("POST /api/ai-apps/{id}/rollback", func(w http.ResponseWriter, r *http.Request) {
		if !a07RequireAdmin(w, r, authStore) {
			return
		}
		id := r.PathValue("id")
		appDir, err := a07ValidateID(aiAppsDir, id)
		if err != nil {
			writeErr(w, 400, "invalid app id")
			return
		}
		if _, err := os.Stat(appDir); os.IsNotExist(err) {
			writeErr(w, 404, "app not found")
			return
		}

		var req struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Version == "" {
			writeErr(w, 400, "version required")
			return
		}

		// Validate version string (timestamp format — only alphanum + special chars used in ts)
		if !regexp.MustCompile(`^[0-9A-Z]+$`).MatchString(req.Version) {
			writeErr(w, 400, "invalid version")
			return
		}

		snapDir := filepath.Join(appDir, "versions", req.Version)
		// Realpath containment for snapDir
		cleanSnap := filepath.Clean(snapDir)
		cleanVersions := filepath.Clean(filepath.Join(appDir, "versions"))
		rel, err := filepath.Rel(cleanVersions, cleanSnap)
		if err != nil || rel == ".." || len(rel) >= 2 && rel[:2] == ".." {
			writeErr(w, 400, "path traversal detected")
			return
		}

		if _, err := os.Stat(snapDir); os.IsNotExist(err) {
			writeErr(w, 404, fmt.Sprintf("version %s not found", req.Version))
			return
		}

		userID := r.Header.Get("X-User-ID")
		execAuditLog(userID, "ai7_rollback", fmt.Sprintf("%s@%s", id, req.Version))

		// Copy files from snapshot back to live
		restored := []string{}
		for _, fname := range []string{"index.html", "server.py"} {
			src := filepath.Join(snapDir, fname)
			dst := filepath.Join(appDir, fname)
			if data, err := os.ReadFile(src); err == nil {
				if err := os.WriteFile(dst, data, 0644); err != nil {
					writeErr(w, 500, fmt.Sprintf("restore %s: %v", fname, err))
					return
				}
				restored = append(restored, fname)
			}
		}

		log.Printf("[ai7] rollback app %s to version %s by user %s (files: %v)", id, req.Version, userID, restored)
		writeJSON(w, map[string]any{
			"status":   "rolled_back",
			"id":       id,
			"version":  req.Version,
			"restored": restored,
		})
	})
}
