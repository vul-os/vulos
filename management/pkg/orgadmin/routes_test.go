package orgadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixedGate authorises a fixed tenant. Empty tenant ⇒ "unauthenticated":
// writes 401 and returns false.
type fixedGate struct{ tenant string }

func (g fixedGate) TenantFromSession(w http.ResponseWriter, _ *http.Request) (string, bool) {
	if g.tenant == "" {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not authenticated"})
		return "", false
	}
	return g.tenant, true
}

// tenantScopedMembers is a MemberStore that only knows about ONE tenant's
// members. Any call for a different tenant returns ErrNotFound — exercising the
// cross-tenant-404 contract end to end.
type tenantScopedMembers struct {
	owner   string
	members []Member
}

func (m *tenantScopedMembers) ListMembers(_ context.Context, tenant string) ([]Member, error) {
	if tenant != m.owner {
		return []Member{}, nil // other tenants legitimately have zero members
	}
	return m.members, nil
}
func (m *tenantScopedMembers) Invite(_ context.Context, _, email, role, _ string) (InviteResponse, error) {
	return InviteResponse{ID: "inv-1", Email: email, Role: role, State: "pending"}, nil
}
func (m *tenantScopedMembers) RemoveMember(_ context.Context, tenant, _ string) error {
	if tenant != m.owner {
		return ErrNotFound
	}
	return nil
}
func (m *tenantScopedMembers) SetRole(_ context.Context, tenant, _, _ string) error {
	if tenant != m.owner {
		return ErrNotFound
	}
	return nil
}
func (m *tenantScopedMembers) SetMemberName(_ context.Context, tenant, _, _ string) error {
	if tenant != m.owner {
		return ErrNotFound
	}
	return nil
}

// errMembers forces a raw (non-sentinel) error so the error-scrub branch fires.
type errMembers struct{}

func (errMembers) ListMembers(_ context.Context, _ string) ([]Member, error) {
	return nil, errors.New("boom: secret internal detail leaked here")
}
func (errMembers) Invite(_ context.Context, _, _, _, _ string) (InviteResponse, error) {
	return InviteResponse{}, errors.New("boom")
}
func (errMembers) RemoveMember(_ context.Context, _, _ string) error     { return errors.New("boom") }
func (errMembers) SetRole(_ context.Context, _, _, _ string) error       { return errors.New("boom") }
func (errMembers) SetMemberName(_ context.Context, _, _, _ string) error { return errors.New("boom") }

func newRouter(svc *Service, tenant string) http.Handler {
	mux := http.NewServeMux()
	RegisterRoutes(mux, svc, fixedGate{tenant: tenant})
	return mux
}

func do(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func fullService() *Service {
	return &Service{
		Members: &tenantScopedMembers{owner: "tenant-a", members: []Member{
			{ID: "m1", Name: "Ada", Email: "ada@x.io", Role: "owner", LastActive: "2026-05-24T10:00:00Z"},
		}},
		Instances: &mockInstances{resp: InstancesResponse{
			Pooled: []PooledInstance{{ID: "p1", Region: "fra", Health: "healthy"}},
			BYO:    []BYOInstance{{ID: "b1", Label: "HQ", ConnectionState: "online", LANCertStatus: "issued"}},
		}},
		UsageR:  &mockUsage{resp: UsageResponse{ActiveUsers: 3, GPUHours: GPUHours{L40S: 2.5, A100x40: 1, H100: 0}, StorageGB: 12.3}},
		QuotaR:  &mockQuota{resp: QuotasResponse{Tier: "pro", Items: []QuotaItem{{Key: "storage", Label: "Storage", Used: 12, Cap: 50, Unit: "GB"}}}},
		Billing: &mockBilling{resp: BillingSummaryResponse{Subscription: SubscriptionSummary{Tier: "pro", State: "active"}, WalletZAR: 200, WalletUSD: 11.76, Rate: 17}},
		Backup:  NewMemStore(),
	}
}

func TestRoutes_Unauthenticated(t *testing.T) {
	h := newRouter(fullService(), "") // empty tenant ⇒ gate writes 401
	for _, p := range []string{"/api/org/members", "/api/org/instances", "/api/org/usage", "/api/org/quotas", "/api/org/backup-mode", "/api/billing/summary"} {
		if rec := do(t, h, http.MethodGet, p, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401", p, rec.Code)
		}
	}
}

func TestRoutes_ListMembers(t *testing.T) {
	h := newRouter(fullService(), "tenant-a")
	rec := do(t, h, http.MethodGet, "/api/org/members", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out MembersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Members) != 1 || out.Members[0].Email != "ada@x.io" || out.Members[0].LastActive == "" {
		t.Fatalf("unexpected members: %#v", out.Members)
	}
}

func TestRoutes_Invite(t *testing.T) {
	h := newRouter(fullService(), "tenant-a")
	// Valid invite — includes the optional name field (accepted, no error).
	rec := do(t, h, http.MethodPost, "/api/org/invite", map[string]string{"email": "new@x.io", "role": "Member", "name": "New Person"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var inv InviteResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &inv)
	if inv.Role != "member" || inv.State != "pending" {
		t.Fatalf("unexpected invite: %#v", inv)
	}
	// Bad email → 400.
	if rec := do(t, h, http.MethodPost, "/api/org/invite", map[string]string{"email": "nope", "role": "member"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad email status = %d, want 400", rec.Code)
	}
	// Bad role → 400.
	if rec := do(t, h, http.MethodPost, "/api/org/invite", map[string]string{"email": "a@x.io", "role": "boss"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad role status = %d, want 400", rec.Code)
	}
}

func TestRoutes_RemoveAndRole(t *testing.T) {
	h := newRouter(fullService(), "tenant-a")
	if rec := do(t, h, http.MethodDelete, "/api/org/members/m1", nil); rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPatch, "/api/org/members/m1/role", map[string]string{"role": "Admin"}); rec.Code != http.StatusOK {
		t.Fatalf("role status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Bad role on PATCH → 400.
	if rec := do(t, h, http.MethodPatch, "/api/org/members/m1/role", map[string]string{"role": "boss"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad role status = %d, want 400", rec.Code)
	}
}

func TestRoutes_CrossTenant404(t *testing.T) {
	// Members owned by tenant-a, but the router authorises tenant-b.
	h := newRouter(fullService(), "tenant-b")
	if rec := do(t, h, http.MethodDelete, "/api/org/members/m1", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant remove = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPatch, "/api/org/members/m1/role", map[string]string{"role": "admin"}); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant role = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, "/api/org/members/m1/name", map[string]string{"name": "X"}); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant name = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRoutes_SetMemberName(t *testing.T) {
	h := newRouter(fullService(), "tenant-a")
	// Valid set-name → 200, echoes the (trimmed) name.
	rec := do(t, h, http.MethodPut, "/api/org/members/m1/name", map[string]string{"name": "  Ada Lovelace  "})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["name"] != "Ada Lovelace" || out["id"] != "m1" {
		t.Fatalf("unexpected set-name response: %#v", out)
	}
	// Empty name (clear) is allowed → 200.
	if rec := do(t, h, http.MethodPut, "/api/org/members/m1/name", map[string]string{"name": ""}); rec.Code != http.StatusOK {
		t.Fatalf("clear-name status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRoutes_Instances(t *testing.T) {
	h := newRouter(fullService(), "tenant-a")
	rec := do(t, h, http.MethodGet, "/api/org/instances", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out InstancesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Pooled) != 1 || len(out.BYO) != 1 || out.Dedicated == nil {
		t.Fatalf("unexpected instances: %#v", out)
	}
}

func TestRoutes_UsageQuotasBilling(t *testing.T) {
	h := newRouter(fullService(), "tenant-a")

	// Usage — confirm gpu_hours keys match JSX (L40S / A100_40GB / H100).
	rec := do(t, h, http.MethodGet, "/api/org/usage", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage status = %d", rec.Code)
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	gh, ok := raw["gpu_hours"].(map[string]any)
	if !ok {
		t.Fatalf("gpu_hours missing/typed wrong: %v", raw["gpu_hours"])
	}
	for _, k := range []string{"L40S", "A100_40GB", "H100"} {
		if _, present := gh[k]; !present {
			t.Fatalf("gpu_hours missing key %q (JSX-required): %v", k, gh)
		}
	}

	// Quotas.
	rec = do(t, h, http.MethodGet, "/api/org/quotas", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("quotas status = %d", rec.Code)
	}
	var q QuotasResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &q)
	if q.Tier != "pro" || len(q.Items) != 1 {
		t.Fatalf("unexpected quotas: %#v", q)
	}

	// Billing summary — confirm nested subscription + rate.
	rec = do(t, h, http.MethodGet, "/api/billing/summary", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("billing status = %d", rec.Code)
	}
	var bs BillingSummaryResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &bs)
	if bs.Subscription.Tier != "pro" || bs.Rate != 17 {
		t.Fatalf("unexpected billing: %#v", bs)
	}
}

// TestRoutes_DoesNotClaimBillingInvoices guards the route-ownership split:
// GET /api/billing/invoices belongs to registerBillingCheckoutRoutes (the
// subscription_events ledger + receipt subroutes). If orgadmin ever registers
// it again, the second HandleFunc below panics the ServeMux at startup —
// exactly the boot crash this test pins down.
func TestRoutes_DoesNotClaimBillingInvoices(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, fullService(), fixedGate{tenant: "tenant-a"})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("orgadmin must not register GET /api/billing/invoices (owned by billing-checkout): %v", r)
		}
	}()
	mux.HandleFunc("GET /api/billing/invoices", func(w http.ResponseWriter, r *http.Request) {})
}

func TestRoutes_BackupModeRoundtrip(t *testing.T) {
	h := newRouter(fullService(), "tenant-a")

	// GET default.
	rec := do(t, h, http.MethodGet, "/api/org/backup-mode", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
	var got BackupModeResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Mode != BackupModeCentral || got.PerLocation == nil {
		t.Fatalf("default backup mode wrong: %#v", got)
	}

	// PUT local + per-location.
	put := BackupModeRequest{Mode: BackupModeLocal, PerLocation: []PerLocationMode{{ID: "l1", Label: "EU", Mode: BackupModeCentral}}}
	rec = do(t, h, http.MethodPut, "/api/org/backup-mode", put)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var after BackupModeResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &after)
	if after.Mode != BackupModeLocal || len(after.PerLocation) != 1 {
		t.Fatalf("put response wrong: %#v", after)
	}

	// GET reflects the change.
	rec = do(t, h, http.MethodGet, "/api/org/backup-mode", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Mode != BackupModeLocal {
		t.Fatalf("persisted mode = %q, want local", got.Mode)
	}

	// PUT invalid mode → 400.
	if rec := do(t, h, http.MethodPut, "/api/org/backup-mode", map[string]any{"mode": "weird"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid put status = %d, want 400", rec.Code)
	}
}

func TestRoutes_ErrorScrub(t *testing.T) {
	svc := &Service{Members: errMembers{}, Backup: NewMemStore()}
	h := newRouter(svc, "tenant-a")
	rec := do(t, h, http.MethodGet, "/api/org/members", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret internal detail") || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("raw error leaked to client: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), genericMsg) {
		t.Fatalf("body missing generic message: %s", rec.Body.String())
	}
}

func TestRoutes_InvalidJSON(t *testing.T) {
	h := newRouter(fullService(), "tenant-a")
	req := httptest.NewRequest(http.MethodPost, "/api/org/invite", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
