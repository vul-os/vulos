package main

// routes_publicapi.go — public REST API v1 (PUBLICAPI-01).
//
// Ported from vulos-management's pkg/publicapi (the vulos.cloud CP's public
// REST API) + its pkg/apikeys pairing, adapted to a single self-hosted box.
// See backend/services/publicapi's package doc for the full port rationale
// (why billing/webhooks were dropped) and backend/internal/apikey/store.go
// for why locally issued keys use a "vkl_" prefix instead of "vk_".
//
// Routes registered:
//
//	GET    /api/v1/account/me        bearer vkl_ auth, 60 req/min  box account profile
//	GET    /api/v1/account/usage     bearer vkl_ auth, 60 req/min  box storage + device usage
//	GET    /api/v1/devices           bearer vkl_ auth, 60 req/min  paired devices (empty until wired)
//
//	GET    /api/developer/keys       session auth   list this user's public-API keys (metadata only)
//	POST   /api/developer/keys       session auth + step-up   issue a new key (raw secret shown once)
//	DELETE /api/developer/keys/{id}  session auth   revoke a key
//
// IMPORTANT — REQUIRED companion edit (NOT applied by this file; see the
// fold's integration manifest): /api/v1/ must be added to services/auth/
// handlers.go's publicPrefixes so the OS session-gate defers to this
// package's OWN bearer-key auth, exactly like the existing /metrics and
// /api/peering/inbound/ entries ("does its own auth, not actually public").
// Without that edit, a caller presenting `Authorization: Bearer vkl_…` is
// 401'd by the global session gate before ever reaching these handlers,
// because /api/v1/ carries no OS session and (being a "vkl_" key, not
// "vk_") the CP-delegated vk_ path never recognises it either.
//
// /api/developer/keys is intentionally NOT added to publicPrefixes — it is a
// normal session-gated (cookie/AT10) settings-panel endpoint, unchanged.
import (
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"vulos/backend/internal/apikey"
	"vulos/backend/services/auth"
	"vulos/backend/services/publicapi"
	"vulos/backend/services/stepup"
)

// registerPublicAPIRoutes opens the local API-key store (its own dedicated
// SQLite file under dbDir, never shared with another writer) and mounts the
// /api/v1/* public REST surface plus the /api/developer/keys management
// routes the Settings → Developer panel uses.
//
// authStore may be nil in degraded boot (no auth store); routes then fail
// closed (503/401) rather than serve without an identity source.
func registerPublicAPIRoutes(mux *http.ServeMux, authStore *auth.Store, dbDir string) func() {
	store, err := apikey.OpenLocalStore(filepath.Join(dbDir, "publicapi_keys.db"))
	if err != nil {
		log.Printf("[publicapi] WARNING: could not open local key store (%v) — /api/v1/* and /api/developer/keys not registered", err)
		return func() {}
	}

	h := publicapi.NewHandler(authStore, store, nil /* devices: see package doc */)

	mux.HandleFunc("GET /api/v1/account/me", h.GetAccountMe)
	mux.HandleFunc("GET /api/v1/account/usage", h.GetAccountUsage)
	mux.HandleFunc("GET /api/v1/devices", h.ListDevices)

	registerDeveloperKeyRoutes(mux, store, authStore)

	log.Printf("[publicapi] registered /api/v1/{account/me,account/usage,devices} (bearer vkl_ auth, 60 req/min) + /api/developer/keys (session auth)")
	return func() {
		if cerr := store.Close(); cerr != nil {
			log.Printf("[publicapi] key store close: %v", cerr)
		}
	}
}

// registerDeveloperKeyRoutes wires the session-authenticated key-management
// endpoints the DeveloperPanel settings UI calls. Separated out for clarity
// and testability.
func registerDeveloperKeyRoutes(mux *http.ServeMux, store *apikey.LocalStore, authStore *auth.Store) {
	mux.HandleFunc("GET /api/developer/keys", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			publicAPIErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		keys, err := store.ListKeys(r.Context(), userID)
		if err != nil {
			log.Printf("[publicapi] list keys user=%s: %v", userID, err)
			publicAPIErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		publicAPIJSON(w, http.StatusOK, keys)
	})

	mux.HandleFunc("POST /api/developer/keys", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			publicAPIErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// Sensitive: minting a durable bearer credential for this account's
		// public API. Require a freshly elevated (step-up) session so a
		// hijacked/left-open browser tab cannot silently mint one.
		if err := stepup.Require(r); err != nil {
			publicAPIErr(w, http.StatusForbidden, "extra verification required — confirm your password to issue a key")
			return
		}

		var body struct {
			Name string `json:"name"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		if err := dec.Decode(&body); err != nil {
			publicAPIErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			name = "API key"
		}

		rawKey, meta, err := store.IssueKey(r.Context(), userID, name)
		if err != nil {
			log.Printf("[publicapi] issue key user=%s: %v", userID, err)
			publicAPIErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		publicAPIJSON(w, http.StatusCreated, map[string]any{
			"id":         meta.ID,
			"name":       meta.Name,
			"created_at": meta.CreatedAt,
			"key":        rawKey, // shown once; caller must copy it now
		})
	})

	mux.HandleFunc("DELETE /api/developer/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			publicAPIErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		keyID := r.PathValue("id")
		if keyID == "" {
			publicAPIErr(w, http.StatusBadRequest, "key id required")
			return
		}
		if err := store.RevokeKey(r.Context(), userID, keyID); err != nil {
			if err == apikey.ErrLocalKeyNotFound {
				publicAPIErr(w, http.StatusNotFound, "key not found")
				return
			}
			log.Printf("[publicapi] revoke key %s user=%s: %v", keyID, userID, err)
			publicAPIErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func publicAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func publicAPIErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
