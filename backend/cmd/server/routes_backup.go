package main

// Admin HTTP endpoints for DB snapshot backup + restore.
//
//	POST /api/admin/backup            — on-demand snapshot → S3 (admin-gated)
//	POST /api/admin/restore           — restore latest snapshot from S3 → live DB
//	                                    (admin-gated AND requires an explicit
//	                                     confirm guard — restore is destructive)
//	GET  /api/admin/backup/status     — latest snapshot metadata (admin-gated)
//
// These wrap the services/sync Compactor/Restorer using the same encrypted
// cluster S3 client + lease config the server already constructs. Restore is
// destructive, so beyond the admin gate it requires {"confirm":"RESTORE"} in
// the body — the same explicit-confirm pattern as the `vulos restore --confirm`
// CLI guard.
//
// The Compactor/Restorer are obtained through factory functions on backupDeps
// so production wires them to the real cluster client (BuildCompactor /
// BuildRestorer) while tests inject ones backed by an in-memory S3 mock.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"vulos/backend/services/auth"
	"vulos/backend/services/cluster"
	"vulos/backend/services/lease"
	"vulos/backend/services/sync"
)

// restoreConfirmToken is the exact string a caller must send in the request
// body to authorise a destructive restore.
const restoreConfirmToken = "RESTORE"

// backupDeps holds the dependencies the backup/restore handlers need.
//
// newCompactor / newRestorer are factories; either may be nil when backup is
// unavailable (e.g. cluster S3 not configured), in which case the endpoints
// return 503.
type backupDeps struct {
	authStore *auth.Store
	// newCompactor builds a Compactor for an on-demand backup. nil → unavailable.
	newCompactor func() (*sync.Compactor, error)
	// newRestorer builds a Restorer for restore + status. nil → unavailable.
	newRestorer func() (*sync.Restorer, error)
	// dbPath is reported in responses/logs.
	dbPath string
}

// clusterBackupDeps wires backupDeps to the production cluster client + lease
// config. s3 may be nil when the cluster is disabled.
func clusterBackupDeps(authStore *auth.Store, s3 *cluster.Client, leaseCfg lease.S3Config, nodeID, dbPath string) backupDeps {
	deps := backupDeps{authStore: authStore, dbPath: dbPath}
	if s3 != nil {
		deps.newCompactor = func() (*sync.Compactor, error) {
			return sync.BuildCompactor(sync.BackupConfig{NodeID: nodeID, DBPath: dbPath}, s3, leaseCfg)
		}
		deps.newRestorer = func() (*sync.Restorer, error) {
			return sync.BuildRestorer(s3, dbPath)
		}
	}
	return deps
}

// registerBackupRoutes wires the admin backup/restore endpoints into mux.
func registerBackupRoutes(mux *http.ServeMux, deps backupDeps) {
	// POST /api/admin/backup — trigger an on-demand snapshot upload.
	mux.HandleFunc("POST /api/admin/backup", func(w http.ResponseWriter, r *http.Request) {
		if !backupIsAdmin(w, r, deps.authStore) {
			return
		}
		if deps.newCompactor == nil {
			writeErr(w, 503, "cluster S3 not configured — backup unavailable")
			return
		}
		compactor, err := deps.newCompactor()
		if err != nil {
			writeErr(w, 500, "build compactor: "+err.Error())
			return
		}
		actor := r.Header.Get("X-User-ID")
		execAuditLog(r, "POST /api/admin/backup", "db="+deps.dbPath)
		if err := compactor.Run(r.Context()); err != nil {
			log.Printf("[backup] on-demand backup failed (actor=%s): %v", actor, err)
			writeErr(w, 500, "backup: "+err.Error())
			return
		}
		log.Printf("[backup] on-demand backup complete (actor=%s db=%s)", actor, deps.dbPath)
		writeJSON(w, map[string]string{"status": "ok", "db": deps.dbPath})
	})

	// GET /api/admin/backup/status — report the latest snapshot in the bucket.
	mux.HandleFunc("GET /api/admin/backup/status", func(w http.ResponseWriter, r *http.Request) {
		if !backupIsAdmin(w, r, deps.authStore) {
			return
		}
		if deps.newRestorer == nil {
			writeErr(w, 503, "cluster S3 not configured — backup unavailable")
			return
		}
		restorer, err := deps.newRestorer()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		doc, err := restorer.LatestSnapshot(r.Context())
		if err != nil {
			if errors.Is(err, sync.ErrNoSnapshot) {
				writeJSON(w, map[string]any{"has_snapshot": false})
				return
			}
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{
			"has_snapshot": true,
			"version":      doc.Version,
			"key":          doc.Key,
			"created_at":   doc.CreatedAt,
		})
	})

	// POST /api/admin/restore — DESTRUCTIVE restore. Admin-gated AND confirm-gated.
	mux.HandleFunc("POST /api/admin/restore", func(w http.ResponseWriter, r *http.Request) {
		if !backupIsAdmin(w, r, deps.authStore) {
			return
		}
		if deps.newRestorer == nil {
			writeErr(w, 503, "cluster S3 not configured — restore unavailable")
			return
		}

		var req struct {
			Confirm string `json:"confirm"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request body")
			return
		}
		// Destructive-action guard: require the explicit confirm token.
		if req.Confirm != restoreConfirmToken {
			writeErr(w, 400, `restore is destructive: send {"confirm":"RESTORE"} to proceed`)
			return
		}

		restorer, err := deps.newRestorer()
		if err != nil {
			writeErr(w, 500, "build restorer: "+err.Error())
			return
		}
		actor := r.Header.Get("X-User-ID")
		execAuditLog(r, "POST /api/admin/restore", "db="+deps.dbPath)
		res, err := restorer.Restore(r.Context())
		if err != nil {
			if errors.Is(err, sync.ErrNoSnapshot) {
				writeErr(w, 404, "no snapshot to restore")
				return
			}
			log.Printf("[restore] restore failed (actor=%s): %v", actor, err)
			writeErr(w, 500, "restore: "+err.Error())
			return
		}
		log.Printf("[restore] restore complete (actor=%s version=%d db=%s)", actor, res.Version, deps.dbPath)
		writeJSON(w, map[string]any{
			"status":  "restored",
			"version": res.Version,
			"bytes":   res.Bytes,
			"db":      deps.dbPath,
		})
	})
}

// backupIsAdmin enforces the same admin gate used by the other admin/device
// routes. Writes 401/403 and returns false when the request is not an admin.
func backupIsAdmin(w http.ResponseWriter, r *http.Request, authStore *auth.Store) bool {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "unauthorized")
		return false
	}
	p, ok := authStore.GetProfile(userID)
	if !ok || p.Role != auth.RoleAdmin {
		writeErr(w, 403, "admin only")
		return false
	}
	return true
}
