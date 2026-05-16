package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"vulos/backend/services/stream"
)

// registerStreamRoutes wires stream-toolbar endpoints into mux.
// All routes are additive and do not conflict with the existing
// streamPool.RegisterHandlers routes (sessions/launch/resize/stop/vnc/ws).
func registerStreamRoutes(mux *http.ServeMux, streamPool *stream.Pool) {
	// POST /api/stream/fps — change the capture framerate for a session.
	// The video pipeline restarts with the new FPS; audio is unaffected.
	// Body: {"id": "<session-id>", "fps": 60}
	// Valid fps values: 1–240 (30/60/90/120 are the UI-exposed options).
	mux.HandleFunc("POST /api/stream/fps", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID  string `json:"id"`
			FPS int    `json:"fps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		if req.ID == "" {
			http.Error(w, `{"error":"id required"}`, http.StatusBadRequest)
			return
		}
		if req.FPS < 1 || req.FPS > 240 {
			http.Error(w, `{"error":"fps must be 1–240"}`, http.StatusBadRequest)
			return
		}
		sess := streamPool.Get(req.ID)
		if sess == nil {
			http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
			return
		}
		sess.SetFPS(req.FPS)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{"status":"ok","fps":%d}`, req.FPS)))
	})

	// POST /api/stream/mangohud — toggle the MangoHud overlay for a session.
	// The video pipeline restarts with MANGOHUD=1 (on) or without it (off).
	// Body: {"id": "<session-id>", "enabled": true}
	mux.HandleFunc("POST /api/stream/mangohud", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		if req.ID == "" {
			http.Error(w, `{"error":"id required"}`, http.StatusBadRequest)
			return
		}
		sess := streamPool.Get(req.ID)
		if sess == nil {
			http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
			return
		}
		sess.SetMangoHud(req.Enabled)
		w.Header().Set("Content-Type", "application/json")
		enabled := "false"
		if req.Enabled {
			enabled = "true"
		}
		w.Write([]byte(fmt.Sprintf(`{"status":"ok","mangohud":%s}`, enabled)))
	})
}
