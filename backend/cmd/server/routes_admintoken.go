package main

// AT10 — admin token management routes.
//
// Endpoints:
//   GET  /api/auth/admin-token/info   — returns rotates_at + token prefix (never full token)
//   POST /api/auth/admin-token/rotate — issues a new token, returns it ONCE

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"vulos/backend/services/auth"
)

// registerAdminTokenRoutes wires the admin-token endpoints into mux.
// home is os.UserHomeDir() — used to locate ~/.vulos/db/admin-token.json.
func registerAdminTokenRoutes(mux *http.ServeMux, authStore *auth.Store, home string) {
	// GET /api/auth/admin-token/info
	// Admin-gated. Returns current token metadata (prefix only, never full token).
	mux.HandleFunc("GET /api/auth/admin-token/info", func(w http.ResponseWriter, r *http.Request) {
		if !at10IsAdmin(w, r, authStore) {
			return
		}

		preview, rotatesAt := at10InfoFromDisk(home)
		writeJSON(w, map[string]any{
			"token_prefix": preview,
			"rotates_at":   rotatesAt,
			"has_token":    preview != "",
		})
	})

	// POST /api/auth/admin-token/rotate
	// Admin-gated. Issues a new token, returns it ONCE. Audit-logged.
	mux.HandleFunc("POST /api/auth/admin-token/rotate", func(w http.ResponseWriter, r *http.Request) {
		if !at10IsAdmin(w, r, authStore) {
			return
		}

		actorID := r.Header.Get("X-User-ID")
		tok, err := auth.RotateAdminToken(home)
		if err != nil {
			log.Printf("[AT10] rotate failed (actor=%s): %v", actorID, err)
			writeErr(w, 500, fmt.Sprintf("admin-token rotate: %v", err))
			return
		}

		rotatesAt := time.Now().UTC().Add(auth.AT10RotationDays * 24 * time.Hour)
		log.Printf("[AT10] admin token rotated (actor=%s) rotates_at=%s", actorID, rotatesAt.Format(time.RFC3339))

		writeJSON(w, map[string]any{
			"token":      tok,
			"rotates_at": rotatesAt,
			"note":       "store this token securely — it will not be shown again",
		})
	})
}

// at10IsAdmin checks that the request is authenticated as an admin.
// Writes 401/403 and returns false if not.
func at10IsAdmin(w http.ResponseWriter, r *http.Request, authStore *auth.Store) bool {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		fmt.Fprintf(w, `{"error":"unauthorized"}`)
		return false
	}
	p, ok := authStore.GetProfile(userID)
	if !ok || p.Role != auth.RoleAdmin {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		fmt.Fprintf(w, `{"error":"admin only"}`)
		return false
	}
	return true
}

// at10InfoFromDisk reads the admin-token file and returns a safe preview and rotation time.
// Returns ("", zero) if no token is stored.
func at10InfoFromDisk(home string) (preview string, rotatesAt time.Time) {
	p := filepath.Join(home, ".vulos", "db", "admin-token.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return "", time.Time{}
	}
	var rec struct {
		Token     string    `json:"token"`
		RotatesAt time.Time `json:"rotates_at"`
	}
	if json.Unmarshal(data, &rec) != nil || rec.Token == "" {
		return "", time.Time{}
	}
	return auth.AT10TokenPrefix8(rec.Token), rec.RotatesAt
}
