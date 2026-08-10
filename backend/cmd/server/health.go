package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"vulos/backend/services/sync"
)

// minFreeDiskBytes is the threshold below which disk is considered degraded.
const minFreeDiskBytes = 500 * 1024 * 1024 // 500 MiB

// syncLagWarnThreshold is the duration after which a non-zero sync lag is
// reported as degraded in the health check.
const syncLagWarnThreshold = 10 * time.Minute

// clusterHealthResponse is the JSON shape returned by GET /api/health.
//
// Checks is `omitempty` because it is only served to an AUTHENTICATED caller —
// see handleClusterHealth. An unauthenticated caller gets status + timestamp
// and nothing else.
type clusterHealthResponse struct {
	Status    string            `json:"status"`
	Checks    map[string]string `json:"checks,omitempty"`
	Timestamp string            `json:"timestamp"`
}

// handleClusterHealth implements GET /api/health.
// Returns 200 when healthy, 503 when any check is degraded.
// syncer may be nil when S3 is not configured (cluster disabled).
//
// SECURITY — two payloads, one verdict:
//
// The route is in auth.publicPaths so that "curl the health endpoint" works as
// the first diagnostic on a sick box (three docs tell you to do exactly that,
// and roadmap/NETWORK.md wants an external router to poll it). What is public is
// only the VERDICT: {"status":"ok"|"degraded","timestamp":"..."} plus the 200/503
// status code. That is everything a liveness/readiness probe needs.
//
// The per-check DETAIL stays session-gated, because every field in it is useful
// to an unauthenticated attacker and to nobody else:
//
//   - data_dir_writable on failure is `"degraded: " + err.Error()`, which carries
//     the box's ABSOLUTE data-dir path and the raw OS error, e.g.
//     "degraded: open /var/lib/vulos/.health-probe-178…: read-only file system".
//     Internal-path and host-state disclosure, from an endpoint you reach without
//     credentials, on a box that is already misbehaving.
//   - disk_space reports exact free capacity ("ok: 247079 MiB free"). That
//     fingerprints the deployment and tells an attacker precisely how much they
//     must write to push the box into a 503.
//   - sync_lag reveals whether S3 cluster sync is configured at all and how
//     recently it ran — cluster topology, unauthenticated.
//
// The checks still RUN for anonymous callers (the verdict would otherwise be a
// lie — data-dir writability is the most important of the three); only the
// detail is withheld.
//
// The authenticated case is decided by X-User-ID, which the auth middleware sets
// ONLY after stripping any client-supplied copy (C1/SEC-A) and validating a real
// session, and which it populates BEFORE the public-path check — so a session
// cookie on a public path still identifies the caller here. Same trusted-header
// pattern as metricsAuthorized in metrics_auth.go.
func handleClusterHealth(dataDir string, syncer *sync.Syncer) http.HandlerFunc {
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

		// --- check 3: sync lag (real value when syncer is running) ---
		if syncer == nil {
			checks["sync_lag"] = "ok: sync disabled (no S3 configured)"
		} else {
			last := syncer.LastSyncTime()
			if last.IsZero() {
				checks["sync_lag"] = "ok: no uploads yet"
			} else {
				lag := time.Since(last)
				if lag > syncLagWarnThreshold {
					checks["sync_lag"] = fmt.Sprintf("degraded: last sync %s ago", lag.Round(time.Second))
					degraded = true
				} else {
					checks["sync_lag"] = fmt.Sprintf("ok: last sync %s ago", lag.Round(time.Second))
				}
			}
		}

		status := "ok"
		if degraded {
			status = "degraded"
		}

		resp := clusterHealthResponse{
			Status:    status,
			Checks:    checks,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}
		// Anonymous caller: verdict only. See the SECURITY note above.
		if r.Header.Get("X-User-ID") == "" {
			resp.Checks = nil
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
