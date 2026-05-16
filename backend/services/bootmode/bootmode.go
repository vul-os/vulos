// Package bootmode detects the current server boot mode and exposes it via HTTP.
//
// Rules:
//   - db dir absent OR no instance.json  → "setup"
//   - sync-state.json present with state "syncing" → "sync"
//   - db dir + instance.json present, no active sync   → "normal"
package bootmode

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
)

// Result holds the detected boot mode and optional sync state.
type Result struct {
	Mode      string `json:"mode"`
	SyncState string `json:"sync_state,omitempty"`
}

// syncStateFile is the filename within the db dir that tracks cluster sync progress.
const syncStateFile = "sync-state.json"

// instanceFile is the filename that records a persisted instance identity (NET-06).
const instanceFile = "instance.json"

// Detect inspects home to determine the current boot mode.
// home is the user home directory; the db dir is home/.vulos/db.
func Detect(home string) (Result, error) {
	dbDir := filepath.Join(home, ".vulos", "db")

	// Rule 1: db dir absent → setup
	if _, err := os.Stat(dbDir); os.IsNotExist(err) {
		return Result{Mode: "setup"}, nil
	} else if err != nil {
		return Result{}, err
	}

	// Rule 2: instance.json absent → setup
	instancePath := filepath.Join(dbDir, instanceFile)
	if _, err := os.Stat(instancePath); os.IsNotExist(err) {
		return Result{Mode: "setup"}, nil
	} else if err != nil {
		return Result{}, err
	}

	// Rule 3: sync-state.json present with status "syncing" → sync
	syncPath := filepath.Join(dbDir, syncStateFile)
	if data, err := os.ReadFile(syncPath); err == nil {
		var ss struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(data, &ss) == nil && ss.Status == "syncing" {
			return Result{Mode: "sync", SyncState: ss.Status}, nil
		}
	}

	// Rule 4: db dir + instance.json present, no active sync → normal
	return Result{Mode: "normal"}, nil
}

// RegisterHandlers registers the boot-mode HTTP endpoint on mux.
// GET /api/setup/mode returns JSON {"mode":"setup"|"sync"|"normal","sync_state":"..."}.
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
