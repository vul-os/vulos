package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// minFreeDiskBytes is the threshold below which disk is considered degraded.
const minFreeDiskBytes = 500 * 1024 * 1024 // 500 MiB

// clusterHealthResponse is the JSON shape returned by GET /api/health.
type clusterHealthResponse struct {
	Status    string            `json:"status"`
	Checks    map[string]string `json:"checks"`
	Timestamp string            `json:"timestamp"`
}

// handleClusterHealth implements GET /api/health (public, no auth).
// Returns 200 when healthy, 503 when any check is degraded.
func handleClusterHealth(dataDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checks := make(map[string]string)
		degraded := false

		// --- check 1: data-dir writable ---
		probe := filepath.Join(dataDir, fmt.Sprintf(".health-probe-%d", time.Now().UnixNano()))
		if err := os.WriteFile(probe, []byte("ok"), 0600); err != nil {
			checks["data_dir_writable"] = "degraded: " + err.Error()
			degraded = true
		} else {
			os.Remove(probe)
			checks["data_dir_writable"] = "ok"
		}

		// --- check 2: free disk space ---
		var stat syscall.Statfs_t
		if err := syscall.Statfs(dataDir, &stat); err != nil {
			checks["disk_space"] = "degraded: " + err.Error()
			degraded = true
		} else {
			free := stat.Bavail * uint64(stat.Bsize)
			if free < minFreeDiskBytes {
				checks["disk_space"] = fmt.Sprintf("degraded: only %d MiB free", free/1024/1024)
				degraded = true
			} else {
				checks["disk_space"] = fmt.Sprintf("ok: %d MiB free", free/1024/1024)
			}
		}

		// --- check 3: sync-lag placeholder ---
		// Will be wired to real cluster sync once that layer is implemented.
		checks["sync_lag"] = "ok"

		status := "ok"
		if degraded {
			status = "degraded"
		}

		resp := clusterHealthResponse{
			Status:    status,
			Checks:    checks,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}

		if degraded {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			writeJSON(w, resp)
			return
		}
		writeJSON(w, resp)
	}
}
