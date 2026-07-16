// routes_products.go — the live product registry endpoint (seam B).
//
//	GET /api/products — the suite product list consumed by the OS box shell.
//
// The control plane tells the box shell which Vulos products exist and how to
// reach them. The shell fetches this list and falls back to its own bundled
// defaults when the API is unreachable (offline / self-host static). The JSON
// shape here MUST match that shell's normalizeProduct() contract:
//
//	{ id, name, blurb, url, icon, status, embed }
//
// Every product URL is DERIVED from env.Domain() (via env.SubdomainURL) so a
// self-host / non-prod cloud advertises its own zone instead of a hardcoded
// vulos.org. The catalogue itself is in-code: registry.json (repo root) carries
// the OSS product map + App Hub catalogue but has neither the shell shape
// (icons/blurb/embed) nor per-deployment URLs, so it is not the right source for
// this seam.
package cproutes

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/env"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/orgadmin"
)

// productEntry is the JSON shape returned by GET /api/products. Field tags are
// load-bearing: they must match the Workspace shell's product contract.
type productEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Blurb  string `json:"blurb"`
	URL    string `json:"url"`
	Icon   string `json:"icon"`
	Status string `json:"status"`
	Embed  bool   `json:"embed"`
}

// shellProduct is a domain-independent catalogue entry. The reachable URL is
// derived at request time from env.SubdomainURL(sub)+path, so the same catalogue
// renders correct URLs on any deployment domain.
type shellProduct struct {
	id    string
	name  string
	blurb string
	sub   string // subdomain label, e.g. "mail" → https://mail.<domain>
	path  string // optional path suffix appended to the product URL
	icon  string // lucide icon name used by the shell
	embed bool   // true → opened in-shell via <ProductFrame/>; false → external
}

// shellProductCatalog is the suite product list. It mirrors the box shell's
// bundled default set so the live registry is the same set, just domain-correct.
// Order is the shell's display order.
//
// BOX-FEDERATED PIVOT (2026-07-15): mail (box-level PIM via lilmail, not a CP
// product), Talk and Meet (third-party comms) are no longer first-party products
// here. The suite is OS + Office + Board + Files + Relay; the box runs lilmail
// directly for PIM.
var shellProductCatalog = []shellProduct{
	{id: "os", name: "Vulos OS", blurb: "The web-native desktop", sub: "os", icon: "Monitor", embed: true},
	{id: "office", name: "Office", blurb: "Collaborative documents", sub: "office", icon: "FileText", embed: true},
	{id: "board", name: "Board", blurb: "Collaborative whiteboard", sub: "board", icon: "PenTool", embed: true},
	{id: "files", name: "Files", blurb: "Your unified bucket", sub: "files", icon: "FolderOpen", embed: true},
	{id: "relay", name: "Relay", blurb: "Sovereign peer fabric", sub: "relay", icon: "Radio", embed: false},
}

// productCatalogHas reports whether id names a catalogue product (the PATCH
// validation gate: an unknown id is rejected before any store write).
func productCatalogHas(id string) bool {
	for _, p := range shellProductCatalog {
		if p.id == id {
			return true
		}
	}
	return false
}

// productEntryFor materialises one catalogue product into the wire shape,
// deriving its URL from the current deployment domain and applying a per-org
// status override when one is present (else the "available" default).
func productEntryFor(p shellProduct, overrides map[string]string) productEntry {
	status := "available"
	if ov, ok := overrides[p.id]; ok && orgadmin.ValidProductStatus(ov) {
		// Only honour a stored value that is still a valid status (fail safe: a
		// malformed row never wedges the read path).
		status = ov
	}
	return productEntry{
		ID:     p.id,
		Name:   p.name,
		Blurb:  p.blurb,
		URL:    env.SubdomainURL(p.sub) + p.path,
		Icon:   p.icon,
		Status: status,
		Embed:  p.embed,
	}
}

// buildProductRegistry materialises the catalogue into the wire shape, deriving
// every URL from the current deployment domain (env.Domain()) and overlaying the
// caller-org's per-product status overrides (empty map ⇒ all "available").
func buildProductRegistry(overrides map[string]string) []productEntry {
	out := make([]productEntry, 0, len(shellProductCatalog))
	for _, p := range shellProductCatalog {
		out = append(out, productEntryFor(p, overrides))
	}
	return out
}

// productStatusReq is the PATCH /api/products/:id body: { "status": "..." }.
type productStatusReq struct {
	Status string `json:"status"`
}

// registerProductsRoutes wires the product-registry seam into mux.
//
//	GET   /api/products       — public catalogue read, overlaid with the caller's
//	                            per-org status overrides when a session is present.
//	                            Unauthenticated callers get catalogue defaults so
//	                            the Workspace shell still renders offline/self-host.
//	PATCH /api/products/:id    — session-authed + owner/admin-gated write of one
//	                            product's per-org status. Echoes the updated entry.
//
// svc + gate may be nil (dev / SQL-store-unavailable paths that don't wire the
// orgadmin plane): GET then serves catalogue defaults and PATCH reports 404
// (fail closed — no unauthenticated writes, no silent accepts).
func registerProductsRoutes(mux *http.ServeMux, svc *orgadmin.Service, gate orgadmin.CallerGate, authStore *auth.Store) {
	mux.HandleFunc("GET /api/products", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, buildProductRegistry(productOverridesFor(r, svc, authStore)))
	})

	mux.HandleFunc("PATCH /api/products/{id}", func(w http.ResponseWriter, r *http.Request) {
		// Session + caller identity via the SAME gate the org-admin member-management
		// routes use (CallerFromSession writes the 401 on auth failure).
		if svc == nil || gate == nil {
			http.Error(w, `{"error":"not available on this instance"}`, http.StatusNotFound)
			return
		}
		tenant, caller, ok := gate.CallerFromSession(w, r)
		if !ok {
			return // gate already wrote the auth error.
		}

		id := r.PathValue("id")
		if !productCatalogHas(id) {
			// Unknown product id: reject BEFORE any authz/store touch so the caller
			// gets a clean 404 (never a leak of whether the org has that product).
			http.Error(w, `{"error":"unknown product"}`, http.StatusNotFound)
			return
		}

		var body productStatusReq
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		if !orgadmin.ValidProductStatus(body.Status) {
			http.Error(w, `{"error":"invalid status"}`, http.StatusBadRequest)
			return
		}

		// Authorize (owner/admin only) + validate + persist, all tenant-scoped to the
		// SESSION's tenant (never a client-supplied one → cross-org is impossible).
		if err := svc.SetProductStatus(r.Context(), tenant, caller, id, body.Status); err != nil {
			switch err {
			case orgadmin.ErrForbidden:
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			case orgadmin.ErrInvalidInput:
				http.Error(w, `{"error":"invalid status"}`, http.StatusBadRequest)
			case orgadmin.ErrNotFound:
				http.Error(w, `{"error":"not available on this instance"}`, http.StatusNotFound)
			default:
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			}
			return
		}

		// Audit: an org-admin product-status change (ORGADMIN-AUDIT-01). Recorded
		// only after SetProductStatus succeeded, org-scoped so it surfaces in the
		// caller-org's GET /api/org/audit and nowhere else.
		auditRecordOrg(r.Context(), tenant, caller, "org.product.status", "product:"+id,
			map[string]string{"product": id, "status": body.Status})

		// Echo the updated product (the Workspace productsAdmin.js contract:
		// PATCH → 200 { ...product }). Re-read the tenant's overrides so the echoed
		// entry reflects the just-persisted status exactly.
		overrides, _ := svc.ProductStatuses(r.Context(), tenant)
		if overrides == nil {
			overrides = map[string]string{}
		}
		// Belt-and-braces: ensure the echoed status is the one we just wrote even if
		// the read races (idempotent, tenant-scoped).
		overrides[id] = body.Status
		for _, p := range shellProductCatalog {
			if p.id == id {
				httpx.JSON(w, productEntryFor(p, overrides))
				return
			}
		}
		// Unreachable (id validated above), but fail closed if the catalogue changed.
		http.Error(w, `{"error":"unknown product"}`, http.StatusNotFound)
	})
}

// productOverridesFor resolves the caller-org's per-product status overrides for
// the GET read path. It is BEST-EFFORT: an unauthenticated request (no session)
// or any lookup failure yields an empty map (catalogue defaults) rather than an
// error — the public registry must always render. When a valid session is
// present, the overrides are scoped to THAT session's tenant.
func productOverridesFor(r *http.Request, svc *orgadmin.Service, authStore *auth.Store) map[string]string {
	if svc == nil || authStore == nil {
		return nil
	}
	token := auth.SessionFromRequest(r)
	if token == "" {
		return nil // anonymous: catalogue defaults.
	}
	u, err := authStore.LookupSession(r.Context(), token)
	if err != nil || u == nil {
		return nil // expired/partial/invalid session: catalogue defaults.
	}
	// tenant == account id under the 1:1 account==org convention (same as the
	// orgadmin plane). Overrides are read scoped to this tenant only.
	ov, err := svc.ProductStatuses(r.Context(), u.ID)
	if err != nil {
		return nil
	}
	return ov
}
