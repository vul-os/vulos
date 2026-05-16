package main

// routes_netmode.go — NET-09 connection-mode switching API.
//
//   GET  /api/network/mode  — read current mode + valid list + status (no auth).
//   POST /api/network/mode  — admin-gated, audit-logged switch between
//                             fabric / direct / own / local.
//
// The handler mirrors the SEC-B gate ordering used by /api/exec et al.:
//   1. kill-switch (VULOS_DISABLE_EXEC)
//   2. admin-role check
//   3. execAuditLog
//   4. mutation
// See ROUTES.md for the per-area routes file contract.

import (
	"encoding/json"
	"net/http"
	"os"

	"vulos/backend/services/auth"
	"vulos/backend/services/network"
)

// netModeResponse is the unified body returned by both GET and POST handlers.
type netModeResponse struct {
	Mode                    network.ConnectionMode   `json:"mode"`
	ExternalListenerBlocked bool                     `json:"external_listener_blocked"`
	ValidModes              []network.ConnectionMode `json:"valid_modes"`
	Status                  network.Status           `json:"status"`
}

// registerNetModeRoutes wires GET/POST /api/network/mode onto mux.
// All dependencies are passed explicitly per the ROUTES.md contract.
//
// Side-effect: invokes netSvc.LoadMode(os.UserHomeDir()) at registration time
// to hydrate the persisted ConnectionMode (defaults to ModeFabric on first
// boot or any I/O failure). This keeps the single-wiring-line contract in
// main.go intact and is safe to call multiple times.
func registerNetModeRoutes(mux *http.ServeMux, netSvc *network.Service, authStore *auth.Store) {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		netSvc.LoadMode(home)
	}

	buildResponse := func() netModeResponse {
		return netModeResponse{
			Mode:                    netSvc.GetMode(),
			ExternalListenerBlocked: netSvc.ExternalListenerBlocked(),
			ValidModes:              network.ValidConnectionModes(),
			Status:                  netSvc.Status(),
		}
	}

	// GET /api/network/mode — read-only status. Public read is acceptable here:
	// the response only exposes the current mode + the status struct already
	// served by /api/network/status. No secrets.
	mux.HandleFunc("GET /api/network/mode", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, buildResponse())
	})

	// POST /api/network/mode — mutate. Body: {"mode":"fabric"|"direct"|"own"|"local"}.
	mux.HandleFunc("POST /api/network/mode", func(w http.ResponseWriter, r *http.Request) {
		// 1. Kill-switch — disable the endpoint entirely.
		if os.Getenv("VULOS_DISABLE_EXEC") != "" {
			writeErr(w, 503, "network mode endpoint disabled by configuration")
			return
		}

		// 2. Admin-role gate.
		userID := r.Header.Get("X-User-ID")
		if authStore == nil {
			writeErr(w, 503, "auth store unavailable")
			return
		}
		if p, _ := authStore.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}

		// Parse body.
		var req struct {
			Mode network.ConnectionMode `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Mode == "" {
			writeErr(w, 400, "mode required")
			return
		}
		if !network.IsValidConnectionMode(req.Mode) {
			writeErr(w, 400, "invalid mode (valid: fabric, direct, own, local)")
			return
		}

		// 3. Audit BEFORE mutation.
		execAuditLog(r, "POST /api/network/mode", "mode="+string(req.Mode))

		// 4. Apply.
		if err := netSvc.SetMode(req.Mode); err != nil {
			writeErr(w, 500, err.Error())
			return
		}

		writeJSON(w, buildResponse())
	})
}
