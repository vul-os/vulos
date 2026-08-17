// Package bootmode reports THIS INSTANCE's own identity-and-replication state,
// and serves it at GET /api/setup/mode.
//
// # What this package cannot tell you
//
// It cannot tell you whether a human has completed first-run setup on this box.
// GET /api/setup/status is the sole authority for that: it stats the
// setup-complete marker written by POST /api/setup/complete
// (backend/cmd/server/routes_setup.go). Nothing here reads that marker, and
// nothing here should be used as a stand-in for it.
//
// That sentence is here because the opposite assumption shipped. The modes used
// to be named "setup", "sync" and "normal", and the wizard read "normal" as
// "already set up":
//
//	if (data.mode === 'normal') {
//	  // Already set up — shouldn't be here, but complete gracefully
//	  onComplete()
//	}
//
// "normal" only ever meant "db/instance.json exists and no sync is running".
// registerIdentityRoutes calls identity.Load at STARTUP, and identity.Load
// creates instance.json when it is missing — so the file exists before the
// server accepts its first connection. The old "normal" was therefore true on
// every first boot, before any human had done anything, and the wizard
// dismissed itself into the login screen on a box with no accounts. Worse, the
// old "setup" mode was unreachable over HTTP for the same reason: no client
// could ever observe it. TestModeInstanceAbsentIsUnreachableOverHTTP
// (backend/cmd/server/routes_bootmode_reachability_test.go) pins that.
//
// # The modes
//
// The names now say what is on disk, so there is no word left to misread as a
// statement about the owner:
//
//	instance_absent — db dir or db/instance.json is missing: this process has
//	                  not yet written its own instance identity. Observable only
//	                  in-process (a server that is serving has already written
//	                  it), and NOT a synonym for "needs setup".
//	syncing         — db/sync-state.json says status "syncing": this box is
//	                  replicating from a cluster it is joining.
//	instance_ready  — instance identity present, no replication in progress.
//	                  Says NOTHING about accounts, ownership, or setup.
//
// The only question a caller can honestly answer from this endpoint is "is this
// box mid-join?" — i.e. mode == syncing. frontend/src/lib/bootmode.ts mirrors
// these three strings and is checked against this file by TestModeStringsMatchFrontend.
package bootmode

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
)

// The three mode strings this package can emit. Callers must branch on these
// constants rather than on string literals, and must not infer setup state from
// any of them — see the package doc.
const (
	// ModeInstanceAbsent: no db dir, or no db/instance.json. The process has
	// not written its instance identity yet.
	ModeInstanceAbsent = "instance_absent"
	// ModeSyncing: db/sync-state.json reports status "syncing".
	ModeSyncing = "syncing"
	// ModeInstanceReady: instance identity on disk, no replication running.
	ModeInstanceReady = "instance_ready"
)

// Modes returns every value Detect can put in Result.Mode, in the order they
// are documented. Tests use it to enumerate the wire contract; nothing should
// hard-code a subset of it.
func Modes() []string {
	return []string{ModeInstanceAbsent, ModeSyncing, ModeInstanceReady}
}

// Result holds the detected instance state and optional sync state.
type Result struct {
	Mode      string `json:"mode"`
	SyncState string `json:"sync_state,omitempty"`
}

// syncStateFile is the filename within the db dir that tracks cluster sync progress.
const syncStateFile = "sync-state.json"

// instanceFile is the filename that records a persisted instance identity (NET-06).
const instanceFile = "instance.json"

// Detect inspects home to determine this instance's state.
// home is the box data root; the db dir is home/db.
func Detect(home string) (Result, error) {
	dbDir := filepath.Join(home, "db")

	// Rule 1: db dir absent → no instance identity yet.
	if _, err := os.Stat(dbDir); os.IsNotExist(err) {
		return Result{Mode: ModeInstanceAbsent}, nil
	} else if err != nil {
		return Result{}, err
	}

	// Rule 2: instance.json absent → no instance identity yet.
	instancePath := filepath.Join(dbDir, instanceFile)
	if _, err := os.Stat(instancePath); os.IsNotExist(err) {
		return Result{Mode: ModeInstanceAbsent}, nil
	} else if err != nil {
		return Result{}, err
	}

	// Rule 3: sync-state.json present with status "syncing" → replicating.
	syncPath := filepath.Join(dbDir, syncStateFile)
	if data, err := os.ReadFile(syncPath); err == nil {
		var ss struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(data, &ss) == nil && ss.Status == "syncing" {
			return Result{Mode: ModeSyncing, SyncState: ss.Status}, nil
		}
	}

	// Rule 4: identity present, nothing replicating.
	return Result{Mode: ModeInstanceReady}, nil
}

// HasInstanceIdentity reports whether this box has written its own instance
// identity to disk.
//
// This is a statement about ONE FILE, db/instance.json. It is true on a
// pristine, unconfigured, account-less box the moment the server starts, so it
// must never be used to gate anything on "the owner has set this box up" —
// that question belongs to the setup-complete marker.
func HasInstanceIdentity(home string) bool {
	result, err := Detect(home)
	if err != nil {
		return false
	}
	return result.Mode == ModeInstanceReady || result.Mode == ModeSyncing
}

// RegisterHandlers registers the instance-state HTTP endpoint on mux.
// GET /api/setup/mode returns JSON
// {"mode":"instance_absent"|"syncing"|"instance_ready","sync_state":"..."}.
func RegisterHandlers(mux *http.ServeMux, home string) {
	mux.HandleFunc("GET /api/setup/mode", func(w http.ResponseWriter, r *http.Request) {
		result, err := Detect(home)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
}
