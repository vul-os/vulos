package main

import (
	"net/http"

	"vulos/backend/services/android"
	"vulos/backend/services/auth"
)

// registerAndroidRoutes wires the box-side redroid (Android-in-a-container)
// management service onto mux. Mirrors registerTelephonyRoutes: the whole
// /api/android surface is gated to the box OWNER, since redroid is a single
// box-owned container (not a per-user sandbox).
//
// Always safe to call: the service is hardware/kernel-gated. With no Docker or
// no binder/ashmem kernel support, GET /api/android/status returns an honest
// {"available":false,"missing_prerequisites":[...]} and the mutating routes
// return a clean "unavailable" error. This only mounts the HTTP surface — the
// app-store registry entry stays inert until the founder signs it, so nothing
// here provisions or auto-starts a container on its own.
func registerAndroidRoutes(mux *http.ServeMux, authStore *auth.Store) *android.Service {
	ownerID := func() string {
		if authStore == nil {
			return ""
		}
		return authStore.AdminUserID()
	}
	svc := android.New(nil, ownerID)
	android.RegisterHandlers(mux, svc)
	return svc
}
