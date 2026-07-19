package cproutes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/compliance"
)

func complianceTestMux(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	authSt, err := openAuthStoreForTest(":memory:", []byte("testsecret"))
	if err != nil {
		t.Fatalf("OpenAuthStore: %v", err)
	}
	t.Cleanup(func() { authSt.Close() }) //nolint:errcheck

	_, tok, err := authSt.Signup(context.Background(), "dsr@example.com", "supersecretpassword1", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	mux := http.NewServeMux()
	RegisterCompliance(mux, compliance.NewMemStore(), authSt)
	return mux, tok
}

func TestCompliance_RequiresSession(t *testing.T) {
	mux, _ := complianceTestMux(t)
	rr := post(t, mux, "/api/compliance/request", map[string]any{"kind": "export"}, "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request must be 401, got %d", rr.Code)
	}
}

func TestCompliance_RecordExportAndList(t *testing.T) {
	mux, tok := complianceTestMux(t)

	rr := post(t, mux, "/api/compliance/request", map[string]any{"kind": "export"}, tok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("record export: want 201, got %d (%s)", rr.Code, rr.Body.String())
	}
	var rec compliance.Request
	decodeResp(t, rr, &rec)
	if rec.ID == "" || rec.Kind != compliance.KindExport || rec.Status != compliance.StatusReceived {
		t.Fatalf("unexpected recorded request: %+v", rec)
	}

	// Erasure too.
	if rr := post(t, mux, "/api/compliance/request", map[string]any{"kind": "erasure", "note": "delete please"}, tok); rr.Code != http.StatusCreated {
		t.Fatalf("record erasure: want 201, got %d", rr.Code)
	}

	// List should return both.
	req := httptest.NewRequest(http.MethodGet, "/api/compliance/requests", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	lrr := httptest.NewRecorder()
	mux.ServeHTTP(lrr, req)
	if lrr.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", lrr.Code)
	}
	var out struct {
		Requests []compliance.Request `json:"requests"`
	}
	decodeResp(t, lrr, &out)
	if len(out.Requests) != 2 {
		t.Fatalf("want 2 requests, got %d", len(out.Requests))
	}
}

func TestCompliance_RejectsUnknownKind(t *testing.T) {
	mux, tok := complianceTestMux(t)
	rr := post(t, mux, "/api/compliance/request", map[string]any{"kind": "obliterate"}, tok)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind must be 400, got %d", rr.Code)
	}
}
