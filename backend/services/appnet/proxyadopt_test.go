package appnet

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func newAdoptEnv(t *testing.T) (*http.ServeMux, *Manager, *ExternalUpstreamStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewExternalUpstreamStoreAt(filepath.Join(dir, "ext.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	mgr := NewManager()
	mux := http.NewServeMux()
	RegisterProxyAdoptHandlers(mux, ProxyAdoptDeps{Mgr: mgr, Store: store})
	return mux, mgr, store
}

func adoptReq(t *testing.T, mux *http.ServeMux, method, path, userID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// TestAdopt_RequiresAuth verifies the adopt endpoints reject unauthenticated
// callers (no X-User-ID stamped by the middleware).
func TestAdopt_RequiresAuth(t *testing.T) {
	mux, _, _ := newAdoptEnv(t)
	rr := adoptReq(t, mux, http.MethodPost, "/api/apps/proxy", "", map[string]any{"name": "grafana", "port": 3000})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated adopt: got %d, want 401", rr.Code)
	}
}

// TestAdopt_LoopbackReservedPortsRejected verifies loopback-only / reserved-port
// enforcement at the API layer.
func TestAdopt_LoopbackReservedPortsRejected(t *testing.T) {
	mux, _, _ := newAdoptEnv(t)
	bad := []int{0, 22, 80, 443, 1023, 7070, 7999, 8080, 70000}
	for _, p := range bad {
		rr := adoptReq(t, mux, http.MethodPost, "/api/apps/proxy", "user1", map[string]any{"name": "svc", "port": p})
		if rr.Code != http.StatusBadRequest {
			t.Errorf("adopt reserved/invalid port %d: got %d, want 400 (%s)", p, rr.Code, rr.Body.String())
		}
	}
}

// TestAdopt_RegisterResolveRevoke covers the full lifecycle: adopt → resolvable
// by GetForProfile (owner-scoped) → revoke → no longer resolvable.
func TestAdopt_RegisterResolveRevoke(t *testing.T) {
	mux, mgr, store := newAdoptEnv(t)

	rr := adoptReq(t, mux, http.MethodPost, "/api/apps/proxy", "user1",
		map[string]any{"name": "My Grafana", "port": 33000, "requires_product": "obs-pro"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("adopt: got %d, want 201 (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		ID, URL, RequiresProduct string
		Port                     int
	}
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.ID != "my-grafana" {
		t.Fatalf("slugified id = %q, want my-grafana", resp.ID)
	}
	if resp.URL != "/app/my-grafana/" {
		t.Fatalf("url = %q", resp.URL)
	}

	// Resolvable through GetForProfile for the OWNER only.
	if ns, ok := mgr.GetForProfile("my-grafana", "user1", "default"); !ok {
		t.Fatal("adopted upstream not resolvable for owner")
	} else if ns.NSIP != "127.0.0.1" || ns.AppPort != 33000 {
		t.Fatalf("synthetic ns wrong: %s:%d", ns.NSIP, ns.AppPort)
	}
	// NOT resolvable for a different user (owner-scoped).
	if _, ok := mgr.GetForProfile("my-grafana", "user2", "default"); ok {
		t.Fatal("adopted upstream leaked to a non-owner")
	}
	// Persisted.
	if store.Get("user1", "my-grafana", "default") == nil {
		t.Fatal("adopted upstream not persisted")
	}

	// A different user cannot revoke it.
	rr = adoptReq(t, mux, http.MethodDelete, "/api/apps/proxy/my-grafana", "user2", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("non-owner revoke: got %d, want 404", rr.Code)
	}

	// Owner revokes.
	rr = adoptReq(t, mux, http.MethodDelete, "/api/apps/proxy/my-grafana", "user1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("owner revoke: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if _, ok := mgr.GetForProfile("my-grafana", "user1", "default"); ok {
		t.Fatal("adopted upstream still resolvable after revoke")
	}
	if store.Get("user1", "my-grafana", "default") != nil {
		t.Fatal("adopted upstream still persisted after revoke")
	}
}

// TestAdopt_SystemAppNameRejected verifies an adopted port cannot shadow a
// system app id.
func TestAdopt_SystemAppNameRejected(t *testing.T) {
	mux, _, _ := newAdoptEnv(t)
	rr := adoptReq(t, mux, http.MethodPost, "/api/apps/proxy", "user1", map[string]any{"name": "settings", "port": 33001})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("adopt system-app name: got %d, want 403", rr.Code)
	}
}

// TestValidateAdoptablePort covers the pure validator.
func TestValidateAdoptablePort(t *testing.T) {
	ok := []int{1024, 3000, 33000, 65535}
	for _, p := range ok {
		if err := ValidateAdoptablePort(p); err != nil {
			t.Errorf("ValidateAdoptablePort(%d) = %v, want nil", p, err)
		}
	}
	bad := []int{-1, 0, 22, 80, 1023, 7070, 7500, 7999, 8080, 65536}
	for _, p := range bad {
		if err := ValidateAdoptablePort(p); err == nil {
			t.Errorf("ValidateAdoptablePort(%d) = nil, want error", p)
		}
	}
}

// TestExternalUpstreamStore_RoundTrip verifies persistence survives reopen.
func TestExternalUpstreamStore_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ext.json")
	s1, err := NewExternalUpstreamStoreAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Set(ExternalUpstream{AppID: "a", OwnerID: "u", Profile: "default", Port: 33000}); err != nil {
		t.Fatal(err)
	}
	s2, err := NewExternalUpstreamStoreAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Get("u", "a", "default"); got == nil || got.Port != 33000 {
		t.Fatalf("round-trip failed: %+v", got)
	}
	all := s2.All()
	if len(all) != 1 {
		t.Fatalf("All() = %d, want 1", len(all))
	}
}
