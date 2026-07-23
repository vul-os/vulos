package orgadmin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// callerGateStub authorises a fixed caller account. Empty ⇒ 401.
type callerGateStub struct{ acct string }

func (g callerGateStub) TenantFromSession(w http.ResponseWriter, _ *http.Request) (string, bool) {
	if g.acct == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return "", false
	}
	return g.acct, true
}

func (g callerGateStub) CallerFromSession(w http.ResponseWriter, _ *http.Request) (string, string, bool) {
	if g.acct == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return "", "", false
	}
	return g.acct, g.acct, true
}

func newOrgRouteSvc() *OrgService {
	svc := NewOrgService(NewMemOrgStore(), &fakeMailer{}, "example.com")
	svc.runBg = func(f func()) { f() }
	svc.sleep = func(time.Duration) {}
	svc.Backoff = func(int) time.Duration { return 0 }
	return svc
}

func TestOrgRoutes_CreateListSwitchContract(t *testing.T) {
	svc := newOrgRouteSvc()
	mux := http.NewServeMux()
	RegisterOrgRoutes(mux, svc, callerGateStub{acct: "acct-1"})

	// POST /api/org/create → {id,slug,role}
	body, _ := json.Marshal(map[string]string{"name": "Acme Inc"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/org/create", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d", rec.Code)
	}
	var created CreateOrgResult
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create decode: %v (%s)", err, rec.Body.String())
	}
	if created.ID == "" || created.Slug != "acme-inc" || created.Role != "owner" {
		t.Fatalf("create result = %+v, want slug=acme-inc role=owner", created)
	}
	// Exact key set check.
	var rawCreate map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &rawCreate)
	for _, k := range []string{"id", "slug", "role"} {
		if _, ok := rawCreate[k]; !ok {
			t.Fatalf("create response missing key %q: %v", k, rawCreate)
		}
	}

	// GET /api/org → [{id,slug,name,role,current}]
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/org", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("list decode: %v (%s)", err, rec.Body.String())
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	for _, k := range []string{"id", "slug", "name", "role", "current"} {
		if _, ok := list[0][k]; !ok {
			t.Fatalf("list item missing key %q: %v", k, list[0])
		}
	}
	if list[0]["current"] != true {
		t.Fatalf("freshly created org should be current: %v", list[0])
	}

	// POST /api/org/switch {id} → {id,current:true}
	sw, _ := json.Marshal(map[string]string{"id": created.ID})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/org/switch", bytes.NewReader(sw)))
	if rec.Code != http.StatusOK {
		t.Fatalf("switch status = %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"current":true`) {
		t.Fatalf("switch body = %s", rec.Body.String())
	}

	// Switch to a non-member org → 404.
	sw, _ = json.Marshal(map[string]string{"id": "nope"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/org/switch", bytes.NewReader(sw)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("switch-nonmember status = %d, want 404", rec.Code)
	}
}

func TestOrgRoutes_Unauthenticated(t *testing.T) {
	svc := newOrgRouteSvc()
	mux := http.NewServeMux()
	RegisterOrgRoutes(mux, svc, callerGateStub{acct: ""})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/org", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
