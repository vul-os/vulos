// HTTP handlers for the STORE-BYO-01 per-account storage-backend selector.
//
// Endpoints:
//
//	GET  /api/settings/storage  — returns the current Config as JSON
//	PUT  /api/settings/storage  — accepts a Config JSON body and persists it
//
// Register with RegisterStorageHandlers(mux, store).
// The store should be opened once at startup (Open / DefaultDBPath).
package storagemode

import (
	"encoding/json"
	"net/http"
)

// RegisterStorageHandlers registers the storage-backend settings endpoints on mux.
func RegisterStorageHandlers(mux *http.ServeMux, s *Store) {
	mux.HandleFunc("/api/settings/storage", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleGetStorage(w, r, s)
		case http.MethodPut:
			handlePutStorage(w, r, s)
		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	})
}

// handleGetStorage handles GET /api/settings/storage.
// Returns the current storage Config as JSON.
func handleGetStorage(w http.ResponseWriter, r *http.Request, s *Store) {
	cfg, err := s.Get()
	if err != nil {
		writeJSONError(w, "could not load storage config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, cfg, http.StatusOK)
}

// handlePutStorage handles PUT /api/settings/storage.
// Accepts a JSON body matching Config and persists it.
func handlePutStorage(w http.ResponseWriter, r *http.Request, s *Store) {
	var cfg Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Set(cfg); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Return the newly-persisted config for immediate client confirmation.
	updated, err := s.Get()
	if err != nil {
		writeJSONError(w, "saved but could not re-read config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, updated, http.StatusOK)
}

// writeJSON serialises v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, v any, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError writes {"error": msg} with the given status code.
func writeJSONError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
