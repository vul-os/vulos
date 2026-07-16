// routes_developer_mcp.go — Developer console: MCP server registrations.
//
// TODO(DEVCON-MCP-01): This is a stub. Full MCP server CRUD (register an MCP
// server URL, secret, scopes; manage per-user/per-org server listing) is
// deferred to a later slice. The endpoints exist now so the Developer.jsx panel
// can render correctly without a 404.
//
// Planned endpoints:
//
//	GET    /api/developer/mcp-servers         — list registered MCP servers
//	POST   /api/developer/mcp-servers         — register an MCP server (name, url, secret)
//	DELETE /api/developer/mcp-servers/{id}    — remove a registration
//
// Until the store + service are built these return empty lists / not-implemented.
package cproutes

import (
	"net/http"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/httpx"
)

// RegisterDeveloperMCPRoutes wires the (stub) MCP server management endpoints.
func RegisterDeveloperMCPRoutes(mux *http.ServeMux, authStore *auth.Store) {
	h := &devMCPHandlers{auth: authStore}
	mux.HandleFunc("GET /api/developer/mcp-servers", h.list)
	mux.HandleFunc("POST /api/developer/mcp-servers", h.create)
	mux.HandleFunc("DELETE /api/developer/mcp-servers/{id}", h.delete)
}

type devMCPHandlers struct {
	auth *auth.Store
}

// list returns the (currently empty) list of registered MCP servers.
func (h *devMCPHandlers) list(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	// TODO(DEVCON-MCP-01): query the mcp_servers table once it exists.
	httpx.JSON(w, []any{})
}

// create is a stub that returns 501 until the store is built.
func (h *devMCPHandlers) create(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	// TODO(DEVCON-MCP-01): implement MCP server registration.
	httpx.Err(w, http.StatusNotImplemented, "MCP server registration is not yet available")
}

// delete is a stub that returns 501 until the store is built.
func (h *devMCPHandlers) delete(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	// TODO(DEVCON-MCP-01): implement MCP server removal.
	httpx.Err(w, http.StatusNotImplemented, "MCP server management is not yet available")
}
