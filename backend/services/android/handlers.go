package android

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// requireOwner gates a redroid handler to the box OWNER, mirroring
// services/telephony.Service.requireOwner: redroid is a single, box-owned
// Android container (not a per-user sandbox), so another authenticated
// account on a multi-user box must not start/stop it or feed it a location.
// Fail-closed: denied when the owner is unresolved (isOwner returns false
// for "").
func (s *Service) requireOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isOwner(r.Header.Get("X-User-ID")) {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// RegisterHandlers wires the /api/android/* routes onto mux. All routes are
// owner-gated. Safe to call even when docker/binder are absent — the
// handlers return an honest {"available":false,"missing_prerequisites":[...]}
// status rather than erroring, and Start/Stop/FeedLocation return a clean
// "unavailable" error the settings/app-store UI can render as "needs Docker
// (+ kernel binder support)".
func RegisterHandlers(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("GET /api/android/status", s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.Status())
	}))

	mux.HandleFunc("POST /api/android/start", s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Start(); err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("POST /api/android/stop", s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Stop(); err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("POST /api/android/location", s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		if err := s.FeedLocation(body.Lat, body.Lng); err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))
}
