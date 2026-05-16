package main

// cl06_routes_storage.go — CLUSTER-06: storage status endpoint
// Reads ~/.vulos/db/storage.json (written by INIT-04 / POST /api/setup/storage)
// and returns a sanitised status object (no credentials).

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
)

// cl06StorageFile is the JSON format written by storageprov (INIT-04).
type cl06StorageFile struct {
	Endpoint  string  `json:"endpoint"`
	Bucket    string  `json:"bucket"`
	Region    string  `json:"region"`
	AccessKey string  `json:"access_key"` // never forwarded to the client
	SecretKey string  `json:"secret_key"` // never forwarded to the client
	SizeGB    float64 `json:"size_gb"`
	UsedGB    float64 `json:"used_gb"`
}

// cl06StorageStatus is the shape returned by GET /api/storage/status.
type cl06StorageStatus struct {
	Configured bool    `json:"configured"`
	Bucket     string  `json:"bucket"`
	Region     string  `json:"region"`
	SizeGB     float64 `json:"size_gb"`
	UsedGB     float64 `json:"used_gb"`
}

// registerStorageRoutes wires the storage status endpoint into mux.
// The enable toggle is provided by the existing POST /api/setup/storage — NOT duplicated here.
func registerStorageRoutes(mux *http.ServeMux, home string) {
	storagePath := filepath.Join(home, ".vulos", "db", "storage.json")

	// GET /api/storage/status — public-safe: no credentials in response.
	mux.HandleFunc("GET /api/storage/status", func(w http.ResponseWriter, r *http.Request) {
		status := cl06StorageStatus{}

		data, err := os.ReadFile(storagePath)
		if err != nil {
			// File absent = not configured; return zero-value struct with configured=false.
			writeJSON(w, status)
			return
		}

		var sf cl06StorageFile
		if err := json.Unmarshal(data, &sf); err != nil {
			writeErr(w, 500, "storage config corrupt")
			return
		}

		status.Configured = sf.AccessKey != "" && sf.Bucket != ""
		status.Bucket = sf.Bucket
		status.Region = sf.Region
		// size/used are best-effort; storageprov may leave them 0 if not knowable.
		status.SizeGB = sf.SizeGB
		status.UsedGB = sf.UsedGB

		writeJSON(w, status)
	})
}
