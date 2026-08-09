package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	vulenv "vulos/backend/services/env"
)

func TestCPConfigured_Selection(t *testing.T) {
	t.Run("both unset → self-host", func(t *testing.T) {
		t.Setenv("VULOS_CP_URL", "")
		t.Setenv("VULOS_CLOUD_URL", "")
		if cpConfigured() {
			t.Fatalf("expected self-host (cpConfigured=false) when both unset")
		}
	})
	t.Run("VULOS_CP_URL set → cloud", func(t *testing.T) {
		t.Setenv("VULOS_CP_URL", "https://gateway.example.com")
		t.Setenv("VULOS_CLOUD_URL", "")
		if !cpConfigured() {
			t.Fatalf("expected cloud (cpConfigured=true) when VULOS_CP_URL set")
		}
	})
	t.Run("VULOS_CLOUD_URL set → cloud", func(t *testing.T) {
		t.Setenv("VULOS_CP_URL", "")
		t.Setenv("VULOS_CLOUD_URL", "https://gateway.example.com")
		if !cpConfigured() {
			t.Fatalf("expected cloud (cpConfigured=true) when VULOS_CLOUD_URL set")
		}
	})
}

// TestSelfHostIntegrations_RegisteredWhenNoCP proves the self-host routes are
// wired (and answer coherently) when no CP is configured, off-prod (dev KEK
// fallback) so no INTEGRATIONS_KEK is required.
func TestSelfHostIntegrations_RegisteredWhenNoCP(t *testing.T) {
	t.Setenv("VULOS_CP_URL", "")
	t.Setenv("VULOS_CLOUD_URL", "")
	t.Setenv("INTEGRATIONS_KEK", "") // dev fallback (env=local)

	mux := http.NewServeMux()
	registerSelfHostIntegrations(mux, "", vulenv.EnvLocal)

	// /token exists and is session-gated (401 without a user, not 404).
	req := httptest.NewRequest(http.MethodPost, "/api/integrations/google/token", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("self-host token route: code=%d want 401 (route missing?)", rec.Code)
	}
}

// TestSelfHostIntegrations_DisabledFailClosedInProdWithoutKEK proves the
// self-host path registers NOTHING (fail-closed) when the KEK is unset in prod.
func TestSelfHostIntegrations_DisabledFailClosedInProdWithoutKEK(t *testing.T) {
	t.Setenv("INTEGRATIONS_KEK", "")
	mux := http.NewServeMux()
	registerSelfHostIntegrations(mux, "", vulenv.EnvProd)

	// No route registered → 404 (the mux has no handler at all).
	req := httptest.NewRequest(http.MethodPost, "/api/integrations/google/token", nil)
	req.Header.Set("X-User-ID", "u1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("prod-no-KEK should register nothing: code=%d want 404", rec.Code)
	}
}
