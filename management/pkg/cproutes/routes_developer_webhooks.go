// routes_developer_webhooks.go — Developer console: outbound webhook registrations.
//
// TODO(DEVCON-WEBHOOKS-01): This is a stub. Full webhook CRUD (register a
// destination URL + event types + signing secret; list/test/delete registrations)
// is deferred to a later slice. The endpoints exist now so the Developer.jsx
// panel can render correctly without a 404.
//
// Planned endpoints:
//
//	GET    /api/developer/webhooks           — list registered webhooks
//	POST   /api/developer/webhooks           — register a webhook (url, events, secret)
//	DELETE /api/developer/webhooks/{id}      — remove a registration
//	POST   /api/developer/webhooks/{id}/test — send a test event
//
// Until the store + delivery engine are built these return empty lists / not-implemented.
package cproutes

import (
	"net/http"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/httpx"
)

// RegisterDeveloperWebhookRoutes wires the (stub) webhook management endpoints.
func RegisterDeveloperWebhookRoutes(mux *http.ServeMux, authStore *auth.Store) {
	h := &devWebhookHandlers{auth: authStore}
	mux.HandleFunc("GET /api/developer/webhooks", h.list)
	mux.HandleFunc("POST /api/developer/webhooks", h.create)
	mux.HandleFunc("DELETE /api/developer/webhooks/{id}", h.delete)
	mux.HandleFunc("POST /api/developer/webhooks/{id}/test", h.test)
}

type devWebhookHandlers struct {
	auth *auth.Store
}

// list returns the (currently empty) list of registered webhooks.
func (h *devWebhookHandlers) list(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	// TODO(DEVCON-WEBHOOKS-01): query the developer_webhooks table once it exists.
	httpx.JSON(w, []any{})
}

// create is a stub that returns 501 until the delivery engine is built.
func (h *devWebhookHandlers) create(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	// TODO(DEVCON-WEBHOOKS-01): implement webhook registration.
	httpx.Err(w, http.StatusNotImplemented, "webhook registration is not yet available")
}

// delete is a stub that returns 501 until the delivery engine is built.
func (h *devWebhookHandlers) delete(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	// TODO(DEVCON-WEBHOOKS-01): implement webhook removal.
	httpx.Err(w, http.StatusNotImplemented, "webhook management is not yet available")
}

// test is a stub that returns 501 until the delivery engine is built.
func (h *devWebhookHandlers) test(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	// TODO(DEVCON-WEBHOOKS-01): implement webhook test delivery.
	httpx.Err(w, http.StatusNotImplemented, "webhook test delivery is not yet available")
}
