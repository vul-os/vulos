package main

// routes_wifi.go — the box's Wi-Fi surface.
//
// Extracted from main() for SEC-WIFI-SCAN-01. These routes were registered
// inline, which made their admin gates untestable in the way that matters: a
// test would have had to build its own mux with a COPY of each handler, so it
// would verify a reimplementation and stay green while the shipped route
// regressed. This codebase has already been bitten by exactly that (see
// routes_notify_readclear.go). Registering them here lets the test drive the
// SAME function main() calls.
//
// wifiController is the slice of *wifi.Service these routes use, as an
// interface so a test can inject a fake and assert the gate WITHOUT a real
// radio. *wifi.Service satisfies it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"vulos/backend/services/auth"
	"vulos/backend/services/wifi"
)

type wifiController interface {
	Status(ctx context.Context) wifi.Connection
	Scan(ctx context.Context) ([]wifi.Network, error)
	Connect(ctx context.Context, ssid, password string) error
	Disconnect(ctx context.Context) error
	SavedNetworks(ctx context.Context) []wifi.SavedNetwork
	ForgetNetwork(ctx context.Context, ssid string) error
}

// registerWiFiRoutes wires the Wi-Fi surface into mux.
func registerWiFiRoutes(mux *http.ServeMux, wifiSvc wifiController, authStore *auth.Store) {
	// WiFi
	mux.HandleFunc("GET /api/wifi/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, wifiSvc.Status(r.Context()))
	})
	mux.HandleFunc("GET /api/wifi/scan", func(w http.ResponseWriter, r *http.Request) {
		// SEC-WIFI-SCAN-01: a scan is NOT a read. wifiSvc.Scan shells out to
		// `iw dev <iface> scan trigger` as root, holds the service mutex, and
		// sleeps 2s — so it drives the radio and serialises callers. Every
		// sibling Wi-Fi mutation below is admin-gated and audit-logged; this one
		// was neither, and being a GET it was reachable from any page a
		// logged-in non-admin visited (<img src=...>), where repeated hits pile
		// up on the mutex and can disturb the active association.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		execAuditLog(r, "GET /api/wifi/scan", "")
		networks, err := wifiSvc.Scan(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, networks)
	})
	mux.HandleFunc("POST /api/wifi/connect", func(w http.ResponseWriter, r *http.Request) {
		// SEC: connecting to a network is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			SSID     string `json:"ssid"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		execAuditLog(r, "POST /api/wifi/connect", fmt.Sprintf("ssid=%q", req.SSID))
		if err := wifiSvc.Connect(r.Context(), req.SSID, req.Password); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "connecting"})
	})
	mux.HandleFunc("POST /api/wifi/disconnect", func(w http.ResponseWriter, r *http.Request) {
		// SEC: disconnecting from a network is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		execAuditLog(r, "POST /api/wifi/disconnect", "wifi disconnect")
		wifiSvc.Disconnect(r.Context())
		writeJSON(w, map[string]string{"status": "disconnected"})
	})
	mux.HandleFunc("GET /api/wifi/saved", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, wifiSvc.SavedNetworks(r.Context()))
	})
	mux.HandleFunc("POST /api/wifi/forget", func(w http.ResponseWriter, r *http.Request) {
		// SEC: forgetting a saved network is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			SSID string `json:"ssid"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		execAuditLog(r, "POST /api/wifi/forget", fmt.Sprintf("ssid=%q", req.SSID))
		wifiSvc.ForgetNetwork(r.Context(), req.SSID)
		writeJSON(w, map[string]string{"status": "forgotten"})
	})
}
