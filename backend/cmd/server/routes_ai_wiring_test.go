package main

// routes_ai_wiring_test.go — the server actually mounts the AI backend it was
// handed.
//
// internal/llmuxclient proves the embedded gateway serves /api/ai/*. This file
// proves the OS reaches it: that registerNewFeatureRoutes mounts the backend
// from newFeatureDeps rather than rebuilding one from the environment (in
// embedded mode a second build would mean two provider registries, two key
// stores and two sets of metering on one box), and that the model-management
// page reports an unconfigured gateway rather than failing.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vulos/backend/internal/llmuxclient"
	"vulos/backend/services/models"
)

// TestRegisterNewFeatureRoutes_UsesInjectedAIBackend drives the real wiring
// function and asserts GET /api/ai/status came from the backend it was given.
func TestRegisterNewFeatureRoutes_UsesInjectedAIBackend(t *testing.T) {
	t.Setenv("LLMUX_URL", "")
	t.Setenv("VULOS_LLMUX_URL", "")
	t.Setenv("VULOS_AI_MODE", "")

	backend := llmuxclient.RemoteBackend(llmuxclient.New(llmuxclient.Config{BaseURL: "http://injected.invalid"}))

	mux := http.NewServeMux()
	_, lmStore, _ := registerNewFeatureRoutes(mux, newFeatureDeps{
		dbDir:     t.TempDir(),
		aiBackend: backend,
	})
	if lmStore != nil {
		t.Cleanup(func() { _ = lmStore.Close() })
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/ai/status", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Mode    string `json:"mode"`
		Gateway string `json:"gateway"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Mode != string(llmuxclient.ModeRemote) || body.Gateway != "http://injected.invalid" {
		t.Fatalf("status reports mode=%q gateway=%q — the injected backend was not the one mounted",
			body.Mode, body.Gateway)
	}
}

// TestRegisterNewFeatureRoutes_NilAIBackendStillMountsRoutes checks the
// degraded path: with no backend and nothing in the environment, /api/ai/* is
// still mounted and answers 503 rather than 404.
func TestRegisterNewFeatureRoutes_NilAIBackendStillMountsRoutes(t *testing.T) {
	t.Setenv("LLMUX_URL", "")
	t.Setenv("VULOS_LLMUX_URL", "")
	t.Setenv("VULOS_AI_MODE", "")
	t.Setenv("VULOS_LLMUX_CONFIG", "")
	t.Setenv("LLMUX_CONFIG", "")

	mux := http.NewServeMux()
	_, lmStore, _ := registerNewFeatureRoutes(mux, newFeatureDeps{dbDir: t.TempDir()})
	if lmStore != nil {
		t.Cleanup(func() { _ = lmStore.Close() })
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/ai/status", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "unconfigured" {
		t.Fatalf("status = %q, want unconfigured", body.Status)
	}
}

// TestModelRoutes_UnconfiguredBackendStillRenders keeps the model-management
// page honest about a box with no gateway, now that it takes a Backend rather
// than a *Client.
func TestModelRoutes_UnconfiguredBackendStillRenders(t *testing.T) {
	mux := http.NewServeMux()
	registerModelRoutes(mux, models.New(t.TempDir()), llmuxclient.Unconfigured(), ownerOK)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/models", nil)
	req.Header.Set("X-User-ID", "owner")
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		ChatModels      json.RawMessage `json:"chat_models"`
		ChatModelsError string          `json:"chat_models_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ChatModelsError == "" {
		t.Fatalf("expected chat_models_error for an unconfigured gateway")
	}
}
