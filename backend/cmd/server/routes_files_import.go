package main

// routes_files_import.go — FILES IMPORT engine HTTP surface.
//
// IMPORT copies files/folders from a connected provider (Google Drive/Docs,
// Microsoft OneDrive/Office) into the user's OWNED Drive. This is DISTINCT from
// the external-mount endpoints (routes_files_external.go): a mount is a live
// read-through view; an import is a copy the user keeps even after the
// integration is disconnected or the upstream file is deleted.
//
// All endpoints are session-authed (identity from X-User-ID, like the rest of
// /api/files). Tokens are minted per-call from the CP integration broker and
// never persisted. Degrade: when the seam is not wired, /import/status reports
// available=false and start returns 503 (ErrImportUnavailable).
//
// Endpoints (under /api/files/import):
//   GET    /api/files/import/status                seam availability + sources
//   POST   /api/files/import       {provider,source,mode}  start a job (runs in bg)
//   GET    /api/files/import/jobs                   list the caller's jobs + status
//   POST   /api/files/import/jobs/{id}/sync         re-pull a job (additive-only)
//   DELETE /api/files/import/jobs/{id}              remove a job (keeps the copies)

import (
	"context"
	"net/http"
	"time"

	"vulos/backend/services/files"
)

// importRunTimeout bounds a single background import run. Generous: a large Drive
// can take a while; the per-call provider tokens are minted fresh as needed.
const importRunTimeout = 2 * time.Hour

// registerFilesImportRoutes wires the import endpoints. svc may be nil (Files
// disabled) — every endpoint then returns 503.
func registerFilesImportRoutes(mux *http.ServeMux, svc *files.Service) {
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

	mux.HandleFunc("GET /api/files/import/status", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		writeJSON(w, map[string]any{
			"available": svc.ImportEnabled(),
			"sources":   svc.AvailableImportSources(),
		})
	}))

	// Start an import. Returns the created job immediately (status pending) and
	// runs the copy in the background; the UI polls /import/jobs for progress.
	mux.HandleFunc("POST /api/files/import", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			Provider string `json:"provider"`
			Source   string `json:"source"`
			Mode     string `json:"mode"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		job, err := svc.StartImport(r.Context(), uid, req.Provider, req.Source, req.Mode)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		runImportInBackground(svc, uid, job.ID)
		writeJSON(w, job)
	}))

	mux.HandleFunc("GET /api/files/import/jobs", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		jobs, err := svc.ListImportJobs(uid)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		if jobs == nil {
			jobs = []files.ImportJob{}
		}
		writeJSON(w, map[string]any{"jobs": jobs})
	}))

	// Re-pull a sync job: an idempotent, ADDITIVE-ONLY refresh (provider→Vulos
	// only; never deletes the Vulos copy). Ownership is enforced by re-checking
	// the job belongs to the caller before kicking off the run.
	mux.HandleFunc("POST /api/files/import/jobs/{id}/sync", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		id := r.PathValue("id")
		jobs, err := svc.ListImportJobs(uid)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		var job *files.ImportJob
		for i := range jobs {
			if jobs[i].ID == id {
				job = &jobs[i]
				break
			}
		}
		if job == nil {
			writeErr(w, 404, "not found")
			return
		}
		runImportInBackground(svc, uid, job.ID)
		writeJSON(w, map[string]string{"status": "syncing", "id": job.ID})
	}))

	mux.HandleFunc("DELETE /api/files/import/jobs/{id}", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		if err := svc.DeleteImportJob(uid, r.PathValue("id")); err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]string{"status": "deleted"})
	}))
}

// runImportInBackground runs a job off the request goroutine with its own bounded
// context so the copy continues after the HTTP response returns.
//
// It goes through the OWNER-SCOPED RunImportJobForOwner so the "this job is
// mine" invariant is re-checked at the service boundary, not only by the
// caller's own pre-flight lookup.
func runImportInBackground(svc *files.Service, ownerID, jobID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), importRunTimeout)
		defer cancel()
		_ = svc.RunImportJobForOwner(ctx, ownerID, jobID)
	}()
}
