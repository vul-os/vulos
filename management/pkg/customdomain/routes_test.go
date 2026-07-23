package customdomain

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

// fixedGate is a SessionGate that always authorises a fixed tenant. An empty
// tenant means "unauthenticated" → it writes 401 and returns false.
type fixedGate struct{ tenant string }

func (g fixedGate) TenantFromSession(w http.ResponseWriter, _ *http.Request) (string, bool) {
	if g.tenant == "" {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not authenticated"})
		return "", false
	}
	return g.tenant, true
}

// errStore wraps MemStore but forces Upsert to return an unexpected (non-
// sentinel) error so the default error-scrub branch in mapError is exercised.
// Get is left intact so the ownership pre-check passes and the service-layer
// call is reached.
type errStore struct {
	*MemStore
	failUpsert bool
}

func (e *errStore) Upsert(ctx context.Context, d CustomDomain) error {
	if e.failUpsert {
		return errors.New("boom: secret internal detail leaked here")
	}
	return e.MemStore.Upsert(ctx, d)
}

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

func testService(dns *fakeDNS) *Service {
	node := fakeNodeResolver{ref: BundleNodeRef{Provider: "fly", App: "vulos-node-1", ID: "svc-1"}}
	return NewService(NewMemStore(), NewStaticAttacher(), node, dns)
}

func TestRoutes_RequestVerification(t *testing.T) {
	svc := testService(&fakeDNS{txt: map[string][]string{}})
	h := newRouter(svc, "tenant-a")

	rec := do(t, h, http.MethodPost, "/api/org/domains", map[string]string{"domain": "mail.acme.example"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var instr VerificationInstruction
	if err := json.Unmarshal(rec.Body.Bytes(), &instr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if instr.Token == "" || instr.RecordType != "TXT" {
		t.Fatalf("bad instruction: %+v", instr)
	}
}

func TestRoutes_RequestVerification_BadInput(t *testing.T) {
	svc := testService(&fakeDNS{})
	h := newRouter(svc, "tenant-a")

	// Empty domain → 400.
	if rec := do(t, h, http.MethodPost, "/api/org/domains", map[string]string{"domain": ""}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty domain status = %d, want 400", rec.Code)
	}
	// Reserved suffix → 400 (ErrInvalidDomain).
	if rec := do(t, h, http.MethodPost, "/api/org/domains", map[string]string{"domain": "x.vulos.org"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("reserved suffix status = %d, want 400", rec.Code)
	}
}

func TestRoutes_Unauthenticated(t *testing.T) {
	svc := testService(&fakeDNS{})
	h := newRouter(svc, "") // empty tenant → gate writes 401

	rec := do(t, h, http.MethodGet, "/api/org/domains", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRoutes_List(t *testing.T) {
	dns := &fakeDNS{txt: map[string][]string{}}
	svc := testService(dns)
	h := newRouter(svc, "tenant-a")
	// Seed two domains for tenant-a, one for tenant-b.
	attachDomain(t, svc, "tenant-a", "one.acme.example", dns)
	attachDomain(t, svc, "tenant-b", "two.beta.example", dns)

	rec := do(t, h, http.MethodGet, "/api/org/domains", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out struct {
		Domains []CustomDomain `json:"domains"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Domains) != 1 {
		t.Fatalf("tenant-a list = %d, want 1 (no cross-tenant leak)", len(out.Domains))
	}
}

func TestRoutes_VerifyFlow(t *testing.T) {
	dns := &fakeDNS{txt: map[string][]string{}}
	svc := testService(dns)
	h := newRouter(svc, "tenant-a")
	ctx := context.Background()

	instr, err := svc.RequestVerification(ctx, "tenant-a", "mail.acme.example")
	if err != nil {
		t.Fatalf("seed request: %v", err)
	}

	// Verify before publishing TXT → 422 (verification failed), state pending.
	rec := do(t, h, http.MethodPost, "/api/org/domains/mail.acme.example/verify", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("pre-publish verify status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}

	// Publish TXT then verify → 200, attached.
	dns.txt[VerificationRecordName("mail.acme.example")] = []string{instr.Token}
	rec = do(t, h, http.MethodPost, "/api/org/domains/mail.acme.example/verify", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("post-publish verify status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var row CustomDomain
	if err := json.Unmarshal(rec.Body.Bytes(), &row); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if row.VerifyState != StateAttached {
		t.Fatalf("state = %q, want attached", row.VerifyState)
	}
}

func TestRoutes_CrossTenant404(t *testing.T) {
	dns := &fakeDNS{txt: map[string][]string{}}
	svc := testService(dns)
	// The router authorises tenant-b, but the domain belongs to tenant-a.
	h := newRouter(svc, "tenant-b")
	attachDomain(t, svc, "tenant-a", "secret.acme.example", dns)

	// Verify another tenant's domain → 404 (no existence disclosure).
	if rec := do(t, h, http.MethodPost, "/api/org/domains/secret.acme.example/verify", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant verify status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	// Detach another tenant's domain → 404.
	if rec := do(t, h, http.MethodDelete, "/api/org/domains/secret.acme.example", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant detach status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	// Unknown domain → 404 too.
	if rec := do(t, h, http.MethodDelete, "/api/org/domains/ghost.example", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown detach status = %d, want 404", rec.Code)
	}
}

func TestRoutes_Detach(t *testing.T) {
	dns := &fakeDNS{txt: map[string][]string{}}
	svc := testService(dns)
	h := newRouter(svc, "tenant-a")
	attachDomain(t, svc, "tenant-a", "mail.acme.example", dns)

	rec := do(t, h, http.MethodDelete, "/api/org/domains/mail.acme.example", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("detach status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	row, _ := svc.Store.Get(context.Background(), "mail.acme.example")
	if row.VerifyState != StateDetached {
		t.Fatalf("state = %q, want detached", row.VerifyState)
	}
}

// TestRoutes_ErrorScrub confirms an unexpected internal error never leaks raw
// detail to the client (the default branch returns the generic message).
func TestRoutes_ErrorScrub(t *testing.T) {
	es := &errStore{MemStore: NewMemStore()}
	// Seed a domain owned by tenant-a via the real MemStore (failGet off).
	if err := es.MemStore.Upsert(context.Background(), CustomDomain{
		Domain: "mail.acme.example", TenantID: "tenant-a",
		VerifyToken: "tok", VerifyState: StateVerified,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	node := fakeNodeResolver{ref: BundleNodeRef{App: "n"}}
	svc := NewService(es, NewStaticAttacher(), node, &fakeDNS{txt: map[string][]string{}})
	h := newRouter(svc, "tenant-a")

	// owns() (Get) succeeds → ownership confirmed. Detach then calls Upsert,
	// which we force to fail with a raw error. The default branch must scrub it.
	es.failUpsert = true
	rec := do(t, h, http.MethodDelete, "/api/org/domains/mail.acme.example", nil)
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
