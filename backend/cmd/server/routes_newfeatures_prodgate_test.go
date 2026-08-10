package main

// routes_newfeatures_prodgate_test.go — the subdomain-provisioning prod gate
// arms on `--env=prod`, not just on VULOS_ENV=prod.
//
// registerNewFeatureRoutes refuses to mount the subdomain / edge-cache routes
// when VULOS_DNS_API, VULOS_CADDY_DIR or VULOS_NGINX_DIR are unset in
// production, because in dev they silently default to "noop" — i.e. the box
// would tell a customer their domain is provisioned while doing nothing.
//
// That gate used to read os.Getenv("VULOS_ENV") == "prod". The documented (and
// cmd/init-implemented) way to start the box is `vulos-server --env=prod`,
// which sets NO environment variable — so on every real production box the
// gate was false and the noop defaults were installed. These tests start the
// wiring the way main() does and keep VULOS_ENV unset throughout.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	vulenv "vulos/backend/services/env"
)

// routeIsMounted reports whether mux has a handler registered for method+path.
// It uses mux.Handler (pattern lookup) rather than serving the request so a
// handler with nil stores cannot panic the test.
func routeIsMounted(mux *http.ServeMux, method, path string) bool {
	_, pattern := mux.Handler(httptest.NewRequest(method, path, nil))
	return pattern != ""
}

// prodGateEnv clears every variable the gate consults and points the
// deployment store at a temp file so nothing touches the real data dir.
func prodGateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("VULOS_DNS_API", "")
	t.Setenv("VULOS_CADDY_DIR", "")
	t.Setenv("VULOS_NGINX_DIR", "")
	t.Setenv("VULOS_DEPLOY_DB", filepath.Join(t.TempDir(), "deployments.json"))
	t.Setenv("LLMUX_URL", "")
	t.Setenv("VULOS_LLMUX_URL", "")
	t.Setenv("VULOS_AI_MODE", "")
	os.Unsetenv("VULOS_ENV")
}

// TestSubdomainProvisioning_GateArmsOnEnvFlag: `--env=prod` with VULOS_ENV
// unset must leave the subdomain and edge-cache routes UNMOUNTED.
func TestSubdomainProvisioning_GateArmsOnEnvFlag(t *testing.T) {
	prodGateEnv(t)

	resolved, err := vulenv.Parse("prod") // exactly `--env=prod`
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	vulenv.SetActive(resolved)
	t.Cleanup(func() { vulenv.SetActive("") })

	if v := os.Getenv("VULOS_ENV"); v != "" {
		t.Fatalf("test precondition broken: VULOS_ENV=%q — this test only proves anything while it is UNSET", v)
	}

	mux := http.NewServeMux()
	_, lmStore, _ := registerNewFeatureRoutes(mux, newFeatureDeps{
		dbDir:     t.TempDir(),
		activeEnv: resolved,
	})
	if lmStore != nil {
		t.Cleanup(func() { _ = lmStore.Close() })
	}

	if routeIsMounted(mux, "GET", "/api/apps/demo/deployment") {
		t.Error("GET /api/apps/{id}/deployment is mounted with --env=prod and VULOS_DNS_API unset — provisioning would noop while reporting success")
	}
	if routeIsMounted(mux, "POST", "/api/apps/demo/deprovision") {
		t.Error("POST /api/apps/{id}/deprovision is mounted with --env=prod and VULOS_DNS_API unset")
	}
	if routeIsMounted(mux, "POST", "/api/apps/demo/cache/purge") {
		t.Error("POST /api/apps/{id}/cache/purge is mounted with --env=prod and VULOS_NGINX_DIR unset")
	}

	// The gate must not have installed the dev noop defaults either.
	if got := os.Getenv("VULOS_DNS_API"); got != "" {
		t.Errorf("VULOS_DNS_API = %q after prod wiring, want unset — the dev noop default leaked into production", got)
	}
	if got := os.Getenv("VULOS_CADDY_DIR"); got != "" {
		t.Errorf("VULOS_CADDY_DIR = %q after prod wiring, want unset", got)
	}
}

// TestSubdomainProvisioning_GateOpenOutsideProd is the counterweight: with
// `--env=local` the same unset variables must still yield a working dev box,
// so a gate that simply never mounted the routes would fail here.
func TestSubdomainProvisioning_GateOpenOutsideProd(t *testing.T) {
	prodGateEnv(t)

	vulenv.SetActive(vulenv.EnvLocal)
	t.Cleanup(func() { vulenv.SetActive("") })

	mux := http.NewServeMux()
	_, lmStore, _ := registerNewFeatureRoutes(mux, newFeatureDeps{
		dbDir:     t.TempDir(),
		activeEnv: vulenv.EnvLocal,
	})
	if lmStore != nil {
		t.Cleanup(func() { _ = lmStore.Close() })
	}

	if !routeIsMounted(mux, "GET", "/api/apps/demo/deployment") {
		t.Error("GET /api/apps/{id}/deployment is NOT mounted with --env=local — a dev checkout would not boot the subdomain routes")
	}
	if got := os.Getenv("VULOS_DNS_API"); got != "noop" {
		t.Errorf("VULOS_DNS_API = %q with --env=local, want the dev noop default", got)
	}
}

// TestSubdomainProvisioning_GateFallsBackToProcessEnv covers the call-site that
// forgets to inject activeEnv: it must fall back to the environment main()
// resolved rather than silently meaning "dev".
func TestSubdomainProvisioning_GateFallsBackToProcessEnv(t *testing.T) {
	prodGateEnv(t)

	vulenv.SetActive(vulenv.EnvProd)
	t.Cleanup(func() { vulenv.SetActive("") })

	mux := http.NewServeMux()
	_, lmStore, _ := registerNewFeatureRoutes(mux, newFeatureDeps{dbDir: t.TempDir()}) // activeEnv omitted
	if lmStore != nil {
		t.Cleanup(func() { _ = lmStore.Close() })
	}

	if routeIsMounted(mux, "GET", "/api/apps/demo/deployment") {
		t.Error("subdomain routes mounted in a prod process when the call-site omitted activeEnv — the fallback failed open")
	}
}
