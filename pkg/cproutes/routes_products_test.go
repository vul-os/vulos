// routes_products_test.go — tests for GET /api/products (seam B).
package cproutes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/orgadmin"
)

// productsResponse mirrors the wire shape consumed by the OS box shell.
type productsResponse []struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Blurb  string `json:"blurb"`
	URL    string `json:"url"`
	Icon   string `json:"icon"`
	Status string `json:"status"`
	Embed  bool   `json:"embed"`
}

func fetchProducts(t *testing.T) productsResponse {
	t.Helper()
	mux := http.NewServeMux()
	// nil svc/gate/authStore → GET serves catalogue defaults (all "available"),
	// exactly the legacy behaviour these tests assert.
	registerProductsRoutes(mux, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/products", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/products = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got productsResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode products: %v", err)
	}
	return got
}

// TestProductsDefaultDomain verifies the registry serves the suite products with
// URLs on the prod default domain (vulos.org) when VULOS_DOMAIN is unset.
func TestProductsDefaultDomain(t *testing.T) {
	t.Setenv("VULOS_DOMAIN", "")
	got := fetchProducts(t)

	want := map[string]string{
		"os":     "https://os.vulos.org",
		"office": "https://office.vulos.org",
		"files":  "https://files.vulos.org",
		"relay":  "https://relay.vulos.org",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d products, want %d", len(got), len(want))
	}
	for _, p := range got {
		wantURL, ok := want[p.ID]
		if !ok {
			t.Errorf("unexpected product id %q", p.ID)
			continue
		}
		if p.URL != wantURL {
			t.Errorf("product %q URL = %q, want %q", p.ID, p.URL, wantURL)
		}
		if p.Status != "available" {
			t.Errorf("product %q status = %q, want available", p.ID, p.Status)
		}
		if p.Name == "" || p.Icon == "" {
			t.Errorf("product %q missing name/icon: %+v", p.ID, p)
		}
	}
}

// TestProductsFollowsDomain verifies every product URL derives from VULOS_DOMAIN
// so a self-host / non-prod cloud advertises its own zone.
func TestProductsFollowsDomain(t *testing.T) {
	t.Setenv("VULOS_DOMAIN", "example.test")
	got := fetchProducts(t)

	byID := map[string]string{}
	for _, p := range got {
		byID[p.ID] = p.URL
	}
	if byID["office"] != "https://office.example.test" {
		t.Errorf("office URL = %q, want https://office.example.test", byID["office"])
	}
	if byID["files"] != "https://files.example.test" {
		t.Errorf("files URL = %q, want https://files.example.test", byID["files"])
	}
	if byID["relay"] != "https://relay.example.test" {
		t.Errorf("relay URL = %q, want https://relay.example.test", byID["relay"])
	}
	for _, p := range got {
		if got := byID[p.ID]; got == "" {
			t.Errorf("product %q has empty URL", p.ID)
		}
	}
}

// ── PATCH /api/products/:id — authz + contract (PRODUCTS-ADMIN-01) ───────────

// fakeCallerGate is an orgadmin.CallerGate double fixing (tenant, caller). An
// empty tenant means "unauthenticated": it writes 401 and returns ok=false,
// mirroring the real gate's auth-failure contract.
type fakeCallerGate struct{ tenant, caller string }

func (g fakeCallerGate) TenantFromSession(w http.ResponseWriter, _ *http.Request) (string, bool) {
	if g.tenant == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return "", false
	}
	return g.tenant, true
}

func (g fakeCallerGate) CallerFromSession(w http.ResponseWriter, _ *http.Request) (string, string, bool) {
	if g.tenant == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return "", "", false
	}
	return g.tenant, g.caller, true
}

// newProductsRouter builds a mux with GET+PATCH /api/products wired to a real
// orgadmin.Service whose caller resolves to `role` for `tenant`, backed by an
// in-memory product-status store. authStore is nil (GET serves catalogue
// defaults; the override read path is covered by the orgadmin service tests).
func newProductsRouter(t *testing.T, tenant, role string) (*http.ServeMux, *orgadmin.Service) {
	t.Helper()
	svc := orgadmin.NewService(nil, nil, nil, nil, nil, orgadmin.NewMemStore(), nil)
	svc.Roles = fixedRoleForTest{role: role}
	svc.Products = orgadmin.NewMemStore()
	mux := http.NewServeMux()
	registerProductsRoutes(mux, svc, fakeCallerGate{tenant: tenant, caller: "caller-1"}, nil)
	return mux, svc
}

// fixedRoleForTest is an orgadmin.RoleResolver double returning a canned role.
type fixedRoleForTest struct{ role string }

func (f fixedRoleForTest) RoleInOrg(_ context.Context, _, _ string) (string, error) {
	return f.role, nil
}

func patchProduct(t *testing.T, mux *http.ServeMux, id, status string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/products/"+id, strings.NewReader(`{"status":"`+status+`"}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// TestPatchProduct_AdminOK: an owner/admin PATCH persists and echoes the updated
// product with the new status.
func TestPatchProduct_AdminOK(t *testing.T) {
	for _, role := range []string{"owner", "admin"} {
		mux, svc := newProductsRouter(t, "org-1", role)
		rr := patchProduct(t, mux, "office", "off")
		if rr.Code != http.StatusOK {
			t.Fatalf("role %s: PATCH = %d, want 200; body=%s", role, rr.Code, rr.Body.String())
		}
		var p struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
			t.Fatalf("decode echo: %v", err)
		}
		if p.ID != "office" || p.Status != "off" {
			t.Errorf("role %s: echo = %+v, want id=office status=off", role, p)
		}
		// Persisted + tenant-scoped.
		got, _ := svc.ProductStatuses(context.Background(), "org-1")
		if got["office"] != "off" {
			t.Errorf("role %s: not persisted, got %v", role, got)
		}
	}
}

// TestPatchProduct_NonAdmin403: a member/billing-admin PATCH is refused 403 and
// nothing is persisted.
func TestPatchProduct_NonAdmin403(t *testing.T) {
	for _, role := range []string{"member", "billing-admin"} {
		mux, svc := newProductsRouter(t, "org-1", role)
		rr := patchProduct(t, mux, "office", "off")
		if rr.Code != http.StatusForbidden {
			t.Fatalf("role %s: PATCH = %d, want 403; body=%s", role, rr.Code, rr.Body.String())
		}
		got, _ := svc.ProductStatuses(context.Background(), "org-1")
		if len(got) != 0 {
			t.Errorf("role %s: forbidden write leaked: %v", role, got)
		}
	}
}

// TestPatchProduct_Unauthenticated401: no session → 401 (gate writes it).
func TestPatchProduct_Unauthenticated401(t *testing.T) {
	mux, _ := newProductsRouter(t, "", "owner") // empty tenant ⇒ unauthenticated
	rr := patchProduct(t, mux, "office", "off")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("PATCH = %d, want 401", rr.Code)
	}
}

// TestPatchProduct_UnknownProduct404: an id not in the catalogue is rejected
// (before any store write) with 404.
func TestPatchProduct_UnknownProduct404(t *testing.T) {
	mux, svc := newProductsRouter(t, "org-1", "owner")
	rr := patchProduct(t, mux, "ghost", "off")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("PATCH unknown = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
	got, _ := svc.ProductStatuses(context.Background(), "org-1")
	if len(got) != 0 {
		t.Errorf("unknown-product write leaked: %v", got)
	}
}

// TestPatchProduct_InvalidStatus400: a status outside configured|available|off
// is rejected 400.
func TestPatchProduct_InvalidStatus400(t *testing.T) {
	mux, _ := newProductsRouter(t, "org-1", "owner")
	for _, bad := range []string{"enabled", "ON", "deleted", ""} {
		rr := patchProduct(t, mux, "office", bad)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status %q: PATCH = %d, want 400", bad, rr.Code)
		}
	}
}

// TestPatchProduct_ReflectsInBuild: after a PATCH the tenant's overrides overlay
// GET's catalogue build — the office entry now shows the new status while other
// products keep the "available" default.
func TestPatchProduct_ReflectsInBuild(t *testing.T) {
	mux, svc := newProductsRouter(t, "org-1", "owner")
	if rr := patchProduct(t, mux, "office", "configured"); rr.Code != http.StatusOK {
		t.Fatalf("PATCH = %d", rr.Code)
	}
	overrides, _ := svc.ProductStatuses(context.Background(), "org-1")
	reg := buildProductRegistry(overrides)
	byID := map[string]string{}
	for _, p := range reg {
		byID[p.ID] = p.Status
	}
	if byID["office"] != "configured" {
		t.Errorf("office status = %q, want configured", byID["office"])
	}
	if byID["files"] != "available" {
		t.Errorf("files status = %q, want available (default preserved)", byID["files"])
	}
}

// TestPatchProduct_Idempotent: re-PATCHing the same status yields the same 200 +
// echo (no error on the second write).
func TestPatchProduct_Idempotent(t *testing.T) {
	mux, _ := newProductsRouter(t, "org-1", "owner")
	for i := 0; i < 2; i++ {
		rr := patchProduct(t, mux, "office", "off")
		if rr.Code != http.StatusOK {
			t.Fatalf("PATCH #%d = %d, want 200", i, rr.Code)
		}
	}
}

// TestPatchProduct_CrossTenantIsolation is the load-bearing cross-tenant test at
// the HTTP layer. Two distinct sessions (org-A owner, org-B owner) share ONE
// product-status store. org-A's PATCH must land ONLY under org-A: org-B's
// overrides stay empty regardless of what org-A writes. The tenant is derived
// strictly from the session gate, never from the request, so there is no request
// knob (header/body/path) with which org-A could target org-B — this test would
// break if a future refactor introduced one.
func TestPatchProduct_CrossTenantIsolation(t *testing.T) {
	shared := orgadmin.NewMemStore() // one store, two tenants.

	newFor := func(tenant string) (*http.ServeMux, *orgadmin.Service) {
		svc := orgadmin.NewService(nil, nil, nil, nil, nil, orgadmin.NewMemStore(), nil)
		svc.Roles = fixedRoleForTest{role: "owner"}
		svc.Products = shared
		mux := http.NewServeMux()
		registerProductsRoutes(mux, svc, fakeCallerGate{tenant: tenant, caller: tenant + "-caller"}, nil)
		return mux, svc
	}

	muxA, svcA := newFor("org-A")
	_, svcB := newFor("org-B")

	// org-A turns office off.
	if rr := patchProduct(t, muxA, "office", "off"); rr.Code != http.StatusOK {
		t.Fatalf("org-A PATCH = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// org-A sees its own override.
	gotA, _ := svcA.ProductStatuses(context.Background(), "org-A")
	if gotA["office"] != "off" {
		t.Errorf("org-A office = %q, want off", gotA["office"])
	}
	// org-B must see NOTHING — org-A's write never crosses the tenant boundary.
	gotB, _ := svcB.ProductStatuses(context.Background(), "org-B")
	if len(gotB) != 0 {
		t.Errorf("cross-tenant leak: org-B overrides = %v, want empty", gotB)
	}
}

// TestPatchProduct_TenantIgnoresRequestBodyTenant proves the tenant is NOT taken
// from the request body: even if a caller stuffs a foreign tenant/org id into the
// PATCH body, the write still lands under the SESSION's tenant only. (The body
// contract is {status}; any extra fields are ignored — this pins that.)
func TestPatchProduct_TenantIgnoresRequestBodyTenant(t *testing.T) {
	mux, svc := newProductsRouter(t, "org-1", "owner")
	// Attacker attempts to redirect the write to "victim-org" via body fields.
	req := httptest.NewRequest(http.MethodPatch, "/api/products/office",
		strings.NewReader(`{"status":"off","tenant_id":"victim-org","org":"victim-org","tenant":"victim-org"}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	// The write landed under the session tenant (org-1), not the injected one.
	if got, _ := svc.ProductStatuses(context.Background(), "org-1"); got["office"] != "off" {
		t.Errorf("session-tenant write missing: %v", got)
	}
	if got, _ := svc.ProductStatuses(context.Background(), "victim-org"); len(got) != 0 {
		t.Errorf("body-injected tenant leaked a write: %v", got)
	}
}

// TestProductsAnonGET_DoesNotLeakOrgOff is the Concern-4 regression: when an org
// sets a product to "off", an ANONYMOUS GET /api/products (no session) must serve
// the catalogue default ("available") — never that org's stored "off". This pins
// that the anon fallback (nil overrides) can't over- OR under-expose another
// org's per-product intent. Here authStore is nil ⇒ productOverridesFor short-
// circuits to nil (anonymous), so no override — regardless of what any org stored.
func TestProductsAnonGET_DoesNotLeakOrgOff(t *testing.T) {
	// An org has set office=off in the shared store...
	store := orgadmin.NewMemStore()
	if err := store.SetProductStatus(context.Background(), "org-1", "office", "off"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := orgadmin.NewService(nil, nil, nil, nil, nil, orgadmin.NewMemStore(), nil)
	svc.Products = store

	// ...but an anonymous GET (authStore nil ⇒ no session resolution) must not see
	// it: every product renders the "available" default.
	mux := http.NewServeMux()
	registerProductsRoutes(mux, svc, fakeCallerGate{tenant: "org-1", caller: "c"}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/products", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", rr.Code)
	}
	var got productsResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, p := range got {
		if p.Status != "available" {
			t.Errorf("anon GET product %q status = %q, want available (org's off must not leak to anon)", p.ID, p.Status)
		}
	}
}
