package main

// routes_files_external.go — FILES PHASE 4 HTTP surface: external stores mounted
// as virtual drives (Google Drive in Wave 1).
//
// All endpoints are session-authed (identity from X-User-ID, like the rest of
// /api/files). The Files service mints a SHORT-LIVED provider token from the
// cloud integration broker per call and uses it for exactly one provider API
// request — tokens are never persisted. Open-core/degrade: when the seam is not
// wired, /external/status reports available=false and the connect/list/content
// endpoints return 503 (mapped from ErrExternalUnavailable) so the rest of Files
// works unchanged standalone.
//
// Endpoints (under /api/files/external/):
//   GET  status                         seam availability + connectable providers
//   GET  mounts                         list the caller's connected external drives
//   POST connect   {provider,name}      record a mount (verifies CP connectivity)
//   POST disconnect {id}                remove a mount
//   GET  list?mount=<id>&folder=<id>    list folders/files in a mounted store
//   GET  content?mount=<id>&file=<id>   download a file's bytes (OS-mediated)

import (
	"io"
	"net/http"

	"vulos/backend/services/files"
)

// registerFilesExternalRoutes wires the external-mount endpoints. svc may be nil
// (Files disabled) — every endpoint then returns 503.
func registerFilesExternalRoutes(mux *http.ServeMux, svc *files.Service) {
	guard := func(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if svc == nil {
				writeErr(w, 503, "files service unavailable")
				return
			}
			userID := r.Header.Get("X-User-ID")
			if userID == "" {
				writeErr(w, 401, "unauthorized")
				return
			}
			h(w, r, userID)
		}
	}

	mux.HandleFunc("GET /api/files/external/status", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		writeJSON(w, map[string]any{
			"available": svc.ExternalEnabled(),
			"providers": svc.AvailableProviders(),
		})
	}))

	mux.HandleFunc("GET /api/files/external/mounts", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		mounts, err := svc.ListExternalMounts(uid)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		if mounts == nil {
			mounts = []files.ExternalMount{}
		}
		writeJSON(w, map[string]any{"mounts": mounts})
	}))

	mux.HandleFunc("POST /api/files/external/connect", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			Provider string `json:"provider"`
			Name     string `json:"name"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		m, err := svc.ConnectExternal(r.Context(), uid, req.Provider, req.Name)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, m)
	}))

	mux.HandleFunc("POST /api/files/external/disconnect", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			ID string `json:"id"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if err := svc.DisconnectExternal(uid, req.ID); err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]string{"status": "disconnected"})
	}))

	mux.HandleFunc("GET /api/files/external/list", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		nodes, err := svc.ListExternal(r.Context(), uid, r.URL.Query().Get("mount"), r.URL.Query().Get("folder"))
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"nodes": nodes})
	}))

	mux.HandleFunc("GET /api/files/external/content", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		mount := r.URL.Query().Get("mount")
		fileID := r.URL.Query().Get("file")
		name := r.URL.Query().Get("name")
		rc, ct, _, err := svc.DownloadExternal(r.Context(), uid, mount, fileID)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		defer rc.Close()
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		if name == "" {
			name = "download"
		}
		w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(name)+"\"")
		io.Copy(w, rc)
	}))
}
