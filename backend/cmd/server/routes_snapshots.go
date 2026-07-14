package main

// Admin HTTP endpoints for OS SNAPSHOTS — point-in-time restore points for the
// box's bucket (services/snapshot).
//
//	POST /api/admin/snapshots               — snapshot now (admin)
//	GET  /api/admin/snapshots               — list snapshots + sizes (admin)
//	GET  /api/admin/snapshots/usage         — snapshot storage usage (admin)
//	POST /api/admin/snapshots/prune         — apply retention now (admin)
//	POST /api/admin/snapshots/{id}/restore  — DESTRUCTIVE restore (admin + confirm)
//
// Restore + prune mutate/destroy state, so beyond the admin gate restore
// additionally requires {"confirm":"RESTORE"} in the body — the same explicit-
// confirm discipline as the DB restore route (routes_backup.go). Every action is
// written to the exec audit trail.
//
// The snapshotter is obtained through a factory on snapshotDeps so production
// wires it to the box's real object store (osStore resolution) while tests can
// inject an in-memory one. When no object store is configured the factory is nil
// and the endpoints report 503, exactly like the DB backup routes.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"vulos/backend/services/auth"
	"vulos/backend/services/snapshot"
)

// snapshotDeps holds the dependencies the snapshot handlers need.
type snapshotDeps struct {
	authStore *auth.Store
	// newSnapshotter builds a Snapshotter bound to the box's object store.
	// nil → snapshots unavailable (no object store) → endpoints return 503.
	newSnapshotter func() (*snapshot.Snapshotter, error)
	// policy is the retention policy applied by the prune endpoint + scheduler.
	policy snapshot.Policy
}

// registerSnapshotRoutes wires the admin snapshot endpoints into mux.
func registerSnapshotRoutes(mux *http.ServeMux, deps snapshotDeps) {
	// POST /api/admin/snapshots — snapshot now.
	mux.HandleFunc("POST /api/admin/snapshots", func(w http.ResponseWriter, r *http.Request) {
		if !backupIsAdmin(w, r, deps.authStore) {
			return
		}
		s, ok := snapshotOr503(w, deps)
		if !ok {
			return
		}
		execAuditLog(r, "POST /api/admin/snapshots", "snapshot now")
		idx, err := s.Create(r.Context(), snapshot.KindManual, "")
		if err != nil {
			log.Printf("[snapshot] manual snapshot failed (actor=%s): %v", r.Header.Get("X-User-ID"), err)
			writeErr(w, 500, "snapshot: "+err.Error())
			return
		}
		log.Printf("[snapshot] manual snapshot %s (actor=%s objects=%d added=%dB)",
			idx.ID, r.Header.Get("X-User-ID"), idx.ObjectCount, idx.BlobBytesAdded)
		writeJSON(w, idx)
	})

	// GET /api/admin/snapshots — list.
	mux.HandleFunc("GET /api/admin/snapshots", func(w http.ResponseWriter, r *http.Request) {
		if !backupIsAdmin(w, r, deps.authStore) {
			return
		}
		s, ok := snapshotOr503(w, deps)
		if !ok {
			return
		}
		list, err := s.List(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if list == nil {
			list = []snapshot.Index{}
		}
		writeJSON(w, map[string]any{"snapshots": list})
	})

	// GET /api/admin/snapshots/usage — metered storage figure.
	mux.HandleFunc("GET /api/admin/snapshots/usage", func(w http.ResponseWriter, r *http.Request) {
		if !backupIsAdmin(w, r, deps.authStore) {
			return
		}
		s, ok := snapshotOr503(w, deps)
		if !ok {
			return
		}
		u, err := s.StorageUsage(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, u)
	})

	// POST /api/admin/snapshots/prune — apply retention now.
	mux.HandleFunc("POST /api/admin/snapshots/prune", func(w http.ResponseWriter, r *http.Request) {
		if !backupIsAdmin(w, r, deps.authStore) {
			return
		}
		s, ok := snapshotOr503(w, deps)
		if !ok {
			return
		}
		execAuditLog(r, "POST /api/admin/snapshots/prune", "apply retention")
		res, err := s.Prune(r.Context(), deps.policy)
		if err != nil {
			log.Printf("[snapshot] prune failed (actor=%s): %v", r.Header.Get("X-User-ID"), err)
			writeErr(w, 500, "prune: "+err.Error())
			return
		}
		writeJSON(w, res)
	})

	// POST /api/admin/snapshots/{id}/restore — DESTRUCTIVE. Admin + confirm.
	mux.HandleFunc("POST /api/admin/snapshots/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		if !backupIsAdmin(w, r, deps.authStore) {
			return
		}
		s, ok := snapshotOr503(w, deps)
		if !ok {
			return
		}
		id := r.PathValue("id")

		var req struct {
			Confirm string `json:"confirm"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request body")
			return
		}
		if req.Confirm != restoreConfirmToken {
			writeErr(w, 400, `restore is destructive: send {"confirm":"RESTORE"} to proceed`)
			return
		}

		actor := r.Header.Get("X-User-ID")
		execAuditLog(r, "POST /api/admin/snapshots/restore", "id="+id)
		res, err := s.Restore(r.Context(), id, snapshot.RestoreOptions{})
		if err != nil {
			// An integrity failure is a fail-closed abort — the box was NOT
			// mutated. Surface it distinctly from a generic error.
			if errors.Is(err, snapshot.ErrIntegrity) {
				log.Printf("[snapshot] restore ABORTED, integrity (actor=%s id=%s): %v", actor, id, err)
				writeErr(w, 422, "restore aborted: snapshot failed integrity verification (box untouched)")
				return
			}
			log.Printf("[snapshot] restore failed (actor=%s id=%s): %v", actor, id, err)
			writeErr(w, 500, "restore: "+err.Error())
			return
		}
		log.Printf("[snapshot] restore complete (actor=%s id=%s wrote=%d deleted=%d safety=%s)",
			actor, id, res.ObjectsWritten, res.ObjectsDeleted, res.SafetySnapshotID)
		writeJSON(w, res)
	})
}

// snapshotOr503 builds a Snapshotter or writes a 503 and returns ok=false when
// snapshots are unavailable (no object store).
func snapshotOr503(w http.ResponseWriter, deps snapshotDeps) (*snapshot.Snapshotter, bool) {
	if deps.newSnapshotter == nil {
		writeErr(w, 503, "object store not configured — snapshots unavailable")
		return nil, false
	}
	s, err := deps.newSnapshotter()
	if err != nil {
		writeErr(w, 503, "snapshots unavailable: "+err.Error())
		return nil, false
	}
	return s, true
}
