package main

import (
	"net/http"

	"vulos/backend/services/location"
)

// registerLocationRoutes wires the box-side per-user LOCATION service
// (LOCATION-01) onto mux. The box has no GPS of its own — the client's browser
// reports its position via POST /api/location, cached per user (X-User-ID) and
// exposed to other box apps via GET /api/location so each app doesn't need its
// own browser geolocation prompt.
//
// A modem-GPS fallback is attached (NewModemSource): it is used only when the
// client fix is stale/absent AND a GNSS-capable USB LTE modem happens to be
// present. It degrades cleanly to "unavailable" on boxes with no such modem —
// the overwhelmingly common case — so this is always safe to wire in.
func registerLocationRoutes(mux *http.ServeMux) *location.Service {
	svc := location.New(location.NewModemSource())
	location.RegisterHandlers(mux, svc)
	return svc
}
