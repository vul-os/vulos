package cpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpserver"
)

// TestNew_SelfHostDefaults proves a zero-config control plane assembles with the
// free no-op seams and serves the always-on operational endpoints, and that the
// version endpoint reports the no-op billing rail (never charges).
func TestNew_SelfHostDefaults(t *testing.T) {
	srv, err := cpserver.New(cpserver.Config{Version: "test", DBDir: t.TempDir()}, cpserver.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	h := srv.Handler()

	// Liveness.
	if rr := do(h, "GET", "/healthz"); rr.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", rr.Code)
	}
	// Readiness (in-memory SQLite answers).
	if rr := do(h, "GET", "/readyz"); rr.Code != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200; body=%s", rr.Code, do(h, "GET", "/readyz").Body.String())
	}
	// Version reports the no-op billing rail.
	rr := do(h, "GET", "/version")
	if rr.Code != http.StatusOK {
		t.Fatalf("/version = %d, want 200", rr.Code)
	}
	var v map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode /version: %v", err)
	}
	if v["billing_rail"] != "noop" {
		t.Fatalf("billing_rail = %q, want noop (self-host never charges)", v["billing_rail"])
	}
	if v["domain"] != "vulos.org" {
		t.Fatalf("domain = %q, want default vulos.org", v["domain"])
	}
}

// TestNew_RouteRegistrarHook proves feature routes compose through the hook with
// access to the shared runtime and injected seams.
func TestNew_RouteRegistrarHook(t *testing.T) {
	reg := func(mux *http.ServeMux, rt *cpserver.Runtime) error {
		mux.HandleFunc("GET /api/tier", func(w http.ResponseWriter, r *http.Request) {
			tier, _ := rt.Entitlements.EffectiveTierFor(r.Context(), "acct-1")
			_, _ = w.Write([]byte(tier))
		})
		return nil
	}
	srv, err := cpserver.New(cpserver.Config{DBDir: t.TempDir()}, cpserver.Deps{Routes: []cpserver.RouteRegistrar{reg}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	rr := do(srv.Handler(), "GET", "/api/tier")
	if rr.Code != http.StatusOK || rr.Body.String() != "selfhost" {
		t.Fatalf("/api/tier = %d %q, want 200 \"selfhost\"", rr.Code, rr.Body.String())
	}
}

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}
