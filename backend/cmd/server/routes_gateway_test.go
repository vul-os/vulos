package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/gwurl"
)

// newGatewayMux wires the gateway routes with injectable authz predicates.
func newGatewayMux(t *testing.T, isOwner func(string) bool, hasOwner func() bool) *http.ServeMux {
	t.Helper()
	// Isolate persistence + reset the resolver so tests don't bleed into each other.
	if err := gwurl.Init(t.TempDir()); err != nil {
		t.Fatalf("gwurl.Init: %v", err)
	}
	t.Cleanup(func() { _ = gwurl.Clear() })
	mux := http.NewServeMux()
	registerGatewayRoutes(mux, gatewaySetGate{isOwner: isOwner, hasOwner: hasOwner})
	return mux
}

func TestGateway_GetReturnsResolved(t *testing.T) {
	mux := newGatewayMux(t, func(string) bool { return true }, func() bool { return true })
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/gateway", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rr.Code)
	}
	var body struct {
		URL     string `json:"url"`
		Source  string `json:"source"`
		Default string `json:"default"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Default != gwurl.Default {
		t.Errorf("default = %q, want %q", body.Default, gwurl.Default)
	}
	// With no override/env in the test env, source should be default.
	if body.Source == "" || body.URL == "" {
		t.Errorf("missing url/source in response: %+v", body)
	}
}

// setReq builds a POST /api/gateway with an owner header when uid != "".
func setReq(uid, url string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/gateway", strings.NewReader(`{"url":"`+url+`"}`))
	r.Header.Set("Content-Type", "application/json")
	if uid != "" {
		r.Header.Set("X-User-ID", uid)
	}
	return r
}

func TestGateway_Set_NonOwnerForbidden(t *testing.T) {
	// An owner exists; the caller is NOT the owner → 403 before any probe.
	mux := newGatewayMux(t,
		func(uid string) bool { return uid == "owner" },
		func() bool { return true },
	)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, setReq("intruder", "https://evil.example.org"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-owner SET status = %d, want 403", rr.Code)
	}
	if gwurl.Configured() != "" {
		t.Fatal("a forbidden SET must not persist an override")
	}
}

func TestGateway_Set_NoSessionForbiddenAfterSetup(t *testing.T) {
	// An owner exists and the request carries no session → 403.
	mux := newGatewayMux(t,
		func(uid string) bool { return uid == "owner" },
		func() bool { return true },
	)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, setReq("", "https://evil.example.org"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("no-session SET status = %d, want 403", rr.Code)
	}
}

func TestGateway_Set_OwnerPassesGate(t *testing.T) {
	// The owner IS allowed past the gate; the probe then fails on an unreachable
	// host → 400 (NOT 403). Proves authz allowed and control reached gwurl.Set.
	mux := newGatewayMux(t,
		func(uid string) bool { return uid == "owner" },
		func() bool { return true },
	)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, setReq("owner", "https://gateway.invalid.example.test"))
	if rr.Code == http.StatusForbidden {
		t.Fatalf("owner SET was forbidden (403); expected to pass the gate")
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("owner SET (unreachable) status = %d, want 400", rr.Code)
	}
}

func TestGateway_Set_FirstBootAllowedWithoutOwner(t *testing.T) {
	// First-boot: no owner exists yet → the gate opens even without a session.
	// The probe still runs (unreachable → 400), proving the gate did NOT 403.
	mux := newGatewayMux(t,
		func(string) bool { return false },
		func() bool { return false }, // hasOwner=false
	)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, setReq("", "https://gateway.invalid.example.test"))
	if rr.Code == http.StatusForbidden {
		t.Fatal("first-boot SET was 403; expected the setup window to be open")
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("first-boot SET (unreachable) status = %d, want 400", rr.Code)
	}
}

func TestGateway_Check_InvalidURL(t *testing.T) {
	mux := newGatewayMux(t, func(string) bool { return true }, func() bool { return true })
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/gateway/check", strings.NewReader(`{"url":"http://insecure.example.org"}`))
	r.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("check status = %d, want 200 (envelope carries the error)", rr.Code)
	}
	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.OK || body.Error == "" {
		t.Fatalf("plaintext URL should fail the check: %+v", body)
	}
}

func TestGateway_Delete_NonOwnerForbidden(t *testing.T) {
	mux := newGatewayMux(t,
		func(uid string) bool { return uid == "owner" },
		func() bool { return true },
	)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/gateway", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-owner DELETE status = %d, want 403", rr.Code)
	}
}
