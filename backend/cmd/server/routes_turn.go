package main

import (
	"encoding/json"
	"net/http"

	"vulos/backend/services/auth"
	"vulos/backend/services/network"
)

// registerTURNRoutes wires the NET-10 TURN/coturn settings endpoints into mux.
//
// GET  /api/turn/config  — returns host, realm, configured-bool; secret is NEVER returned.
// POST /api/turn/config  — ADMIN ONLY — accepts host, port, realm, secret; persists to ~/.vulos/db/turn.json.
// POST /api/turn/test    — ADMIN ONLY — TCP-dials host:port and reports reachability + latency.
func registerTURNRoutes(mux *http.ServeMux, turnStore *network.TURNStore, authStore *auth.Store) {
	// GET /api/turn/config — public view (no secret) — any authenticated user may read
	mux.HandleFunc("GET /api/turn/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, turnStore.Get())
	})

	// POST /api/turn/config — persist full config including optional secret
	// H2 fix: requires admin role — TURN config contains credentials.
	mux.HandleFunc("POST /api/turn/config", func(w http.ResponseWriter, r *http.Request) {
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Host   string `json:"host"`
			Port   int    `json:"port"`
			Realm  string `json:"realm"`
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request body")
			return
		}
		if err := turnStore.Set(req.Host, req.Port, req.Realm, req.Secret); err != nil {
			writeErr(w, 500, "failed to persist TURN config: "+err.Error())
			return
		}
		// Return the safe public view (secret excluded)
		writeJSON(w, turnStore.Get())
	})

	// POST /api/turn/test — TCP reachability probe — admin only
	// H2 fix: probing arbitrary host:port combinations is an SSRF-adjacent
	// capability; restrict to admins.
	mux.HandleFunc("POST /api/turn/test", func(w http.ResponseWriter, r *http.Request) {
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		result := turnStore.TestReachability()
		if !result.Success {
			w.WriteHeader(200) // still 200 — let the body convey success/fail
		}
		writeJSON(w, result)
	})
}
