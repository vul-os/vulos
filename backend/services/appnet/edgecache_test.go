package appnet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// sanitizeNginxIdent
// ---------------------------------------------------------------------------

func TestSanitizeNginxIdent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"notes", "notes"},
		{"my-app", "my_app"},
		{"my--app", "my__app"},
		{"abc123", "abc123"},
		{"a.b/c", "a_b_c"},
	}
	for _, c := range cases {
		got := sanitizeNginxIdent(c.in)
		if got != c.want {
			t.Errorf("sanitizeNginxIdent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// buildEdgeCacheConfig
// ---------------------------------------------------------------------------

func TestBuildEdgeCacheConfig(t *testing.T) {
	d := &Deployment{
		AppID:   "notes",
		Profile: "default",
		FQDN:    "notes--default.local.vulos.org",
	}
	cfg := buildEdgeCacheConfig(d, "127.0.0.1:9000", "/var/cache/nginx/vulos")

	if cfg.AppID != "notes" {
		t.Errorf("AppID = %q, want %q", cfg.AppID, "notes")
	}
	if cfg.Profile != "default" {
		t.Errorf("Profile = %q, want %q", cfg.Profile, "default")
	}
	if cfg.FQDN != "notes--default.local.vulos.org" {
		t.Errorf("FQDN = %q", cfg.FQDN)
	}
	if cfg.Upstream != "127.0.0.1:9000" {
		t.Errorf("Upstream = %q", cfg.Upstream)
	}
	if !strings.HasPrefix(cfg.CacheZone, "vulos_") {
		t.Errorf("CacheZone = %q, want vulos_* prefix", cfg.CacheZone)
	}
	if !strings.Contains(cfg.CachePath, "notes_default") {
		t.Errorf("CachePath = %q, want notes_default in path", cfg.CachePath)
	}
}

func TestBuildEdgeCacheConfig_EmptyProfile(t *testing.T) {
	d := &Deployment{AppID: "calc", Profile: ""}
	cfg := buildEdgeCacheConfig(d, "127.0.0.1:9001", "/cache")
	if cfg.Profile != "default" {
		t.Errorf("Profile = %q, want %q", cfg.Profile, "default")
	}
	if !strings.Contains(cfg.CacheZone, "default") {
		t.Errorf("CacheZone = %q should contain 'default'", cfg.CacheZone)
	}
}

// ---------------------------------------------------------------------------
// EdgeCacheManager.WriteConfig
// ---------------------------------------------------------------------------

func newTestEdgeCacheManager(t *testing.T) *EdgeCacheManager {
	t.Helper()
	dir := t.TempDir()
	return newEdgeCacheManagerForTest(dir, filepath.Join(dir, "cache"))
}

func TestEdgeCacheManager_WriteConfig(t *testing.T) {
	ecm := newTestEdgeCacheManager(t)

	d := &Deployment{
		AppID:       "myapp",
		Profile:     "prod",
		FQDN:        "myapp--prod.testulid.vulos.org",
		Provisioned: time.Now().UTC(),
	}

	if err := ecm.WriteConfig(d, "127.0.0.1:9090"); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	confPath := ecm.confPath("myapp", "prod")
	data, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read conf: %v", err)
	}
	content := string(data)

	checks := []struct {
		needle string
		desc   string
	}{
		{"myapp--prod.testulid.vulos.org", "FQDN"},
		{"127.0.0.1:9090", "upstream"},
		{"proxy_cache", "proxy_cache directive"},
		{"proxy_cache_valid 200 5m", "5-minute cache TTL"},
		{"X-Cache $upstream_cache_status", "X-Cache header"},
		{"/api/", "API pass-through location"},
		{"proxy_no_cache $http_authorization", "auth bypass"},
		{"stub_status", "stub_status"},
	}
	for _, c := range checks {
		if !strings.Contains(content, c.needle) {
			t.Errorf("conf missing %s (%q): content:\n%s", c.desc, c.needle, content)
		}
	}
}

func TestEdgeCacheManager_WriteConfig_Idempotent(t *testing.T) {
	ecm := newTestEdgeCacheManager(t)
	d := &Deployment{AppID: "app", Profile: "default", FQDN: "app--default.x.vulos.org"}

	if err := ecm.WriteConfig(d, "127.0.0.1:9000"); err != nil {
		t.Fatalf("first WriteConfig: %v", err)
	}
	if err := ecm.WriteConfig(d, "127.0.0.1:9000"); err != nil {
		t.Fatalf("second WriteConfig: %v", err)
	}

	confPath := ecm.confPath("app", "default")
	if _, err := os.Stat(confPath); err != nil {
		t.Fatalf("conf file missing after idempotent write: %v", err)
	}
}

func TestEdgeCacheManager_WriteConfig_Noop(t *testing.T) {
	ecm := &EdgeCacheManager{nginxDir: "noop", nginxReloader: func() error { return nil }}
	d := &Deployment{AppID: "a", Profile: "default", FQDN: "a--default.x.vulos.org"}
	if err := ecm.WriteConfig(d, "127.0.0.1:0"); err != nil {
		t.Errorf("WriteConfig(noop): unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// EdgeCacheManager.RemoveConfig
// ---------------------------------------------------------------------------

func TestEdgeCacheManager_RemoveConfig(t *testing.T) {
	ecm := newTestEdgeCacheManager(t)
	d := &Deployment{AppID: "rm", Profile: "default", FQDN: "rm--default.x.vulos.org"}

	if err := ecm.WriteConfig(d, "127.0.0.1:9000"); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if err := ecm.RemoveConfig("rm", "default"); err != nil {
		t.Fatalf("RemoveConfig: %v", err)
	}
	if _, err := os.Stat(ecm.confPath("rm", "default")); !os.IsNotExist(err) {
		t.Errorf("conf file still present after RemoveConfig")
	}
}

func TestEdgeCacheManager_RemoveConfig_MissingIsOK(t *testing.T) {
	ecm := newTestEdgeCacheManager(t)
	if err := ecm.RemoveConfig("nonexistent", "default"); err != nil {
		t.Errorf("RemoveConfig on missing file should be a no-op, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// parseNginxStubStatus
// ---------------------------------------------------------------------------

func TestParseNginxStubStatus(t *testing.T) {
	body := `Active connections: 3
server accepts handled requests
 100 100 500
Reading: 0 Writing: 1 Waiting: 2`

	s := parseNginxStubStatus(body)
	if s.ActiveConnections != 3 {
		t.Errorf("ActiveConnections = %d, want 3", s.ActiveConnections)
	}
	if s.Accepts != 100 {
		t.Errorf("Accepts = %d, want 100", s.Accepts)
	}
	if s.Handled != 100 {
		t.Errorf("Handled = %d, want 100", s.Handled)
	}
	if s.Requests != 500 {
		t.Errorf("Requests = %d, want 500", s.Requests)
	}
}

func TestParseNginxStubStatus_Empty(t *testing.T) {
	s := parseNginxStubStatus("")
	if s.ActiveConnections != 0 || s.Requests != 0 {
		t.Errorf("empty body should yield zero values, got %+v", s)
	}
}

// ---------------------------------------------------------------------------
// FetchStubStatus — stub HTTP server
// ---------------------------------------------------------------------------

func TestFetchStubStatus_Live(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Active connections: 7\nserver accepts handled requests\n 200 200 1000\nReading: 0 Writing: 3 Waiting: 4"))
	}))
	defer srv.Close()

	ecm := newEdgeCacheManagerForTest(t.TempDir(), t.TempDir())
	ecm.statusURL = srv.URL
	ecm.httpClient = &http.Client{}

	stats := ecm.FetchStubStatus(context.Background())
	if stats.ActiveConnections != 7 {
		t.Errorf("ActiveConnections = %d, want 7", stats.ActiveConnections)
	}
	if stats.Requests != 1000 {
		t.Errorf("Requests = %d, want 1000", stats.Requests)
	}
}

func TestFetchStubStatus_Unavailable(t *testing.T) {
	ecm := newEdgeCacheManagerForTest(t.TempDir(), t.TempDir())
	ecm.statusURL = "http://127.0.0.1:1" // nothing listens there
	ecm.httpClient = &http.Client{}

	stats := ecm.FetchStubStatus(context.Background())
	if stats.Note == "" {
		t.Error("expected a non-empty Note when stub_status is unreachable")
	}
}

func TestFetchStubStatus_NoURL(t *testing.T) {
	ecm := newEdgeCacheManagerForTest(t.TempDir(), t.TempDir())
	ecm.statusURL = ""
	stats := ecm.FetchStubStatus(context.Background())
	if stats.Note == "" {
		t.Error("expected Note when statusURL is empty")
	}
}

// ---------------------------------------------------------------------------
// HTTP API handlers
// ---------------------------------------------------------------------------

func newCacheTestEnv(t *testing.T) (*http.ServeMux, *Provisioner, *EdgeCacheManager) {
	t.Helper()
	t.Setenv("VULOS_DNS_API", "noop")
	t.Setenv("VULOS_INSTANCE_ID", "testulid")
	t.Setenv("VULOS_BASE_DOMAIN", "vulos.org")
	t.Setenv("VULOS_CADDY_DIR", "noop")

	dir := t.TempDir()
	ds, _ := NewDeploymentStoreAt(filepath.Join(dir, "dep.json"))
	p := NewProvisioner(ds, nil)

	ecm := newEdgeCacheManagerForTest(filepath.Join(dir, "nginx"), filepath.Join(dir, "cache"))

	mux := http.NewServeMux()
	RegisterEdgeCacheHandlers(mux, ecm, p)
	return mux, p, ecm
}

func TestCacheAPI_Purge_NotFound(t *testing.T) {
	mux, _, _ := newCacheTestEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/api/apps/ghost/cache/purge", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("purge on unknown app: status %d, want 404", rr.Code)
	}
}

func TestCacheAPI_Purge_OK(t *testing.T) {
	mux, p, _ := newCacheTestEnv(t)

	// Pre-provision a deployment.
	_, err := p.ProvisionSubdomain(context.Background(), "notes", "default", "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("ProvisionSubdomain: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/apps/notes/cache/purge", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("purge: status %d, body: %s", rr.Code, rr.Body)
	}

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode purge response: %v", err)
	}
	if resp["status"] != "purged" {
		t.Errorf("status = %q, want %q", resp["status"], "purged")
	}
	if resp["app_id"] != "notes" {
		t.Errorf("app_id = %q, want %q", resp["app_id"], "notes")
	}
	if resp["fqdn"] != "notes--default.testulid.vulos.org" {
		t.Errorf("fqdn = %q", resp["fqdn"])
	}
}

func TestCacheAPI_Stats_NotFound(t *testing.T) {
	mux, _, _ := newCacheTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/apps/ghost/cache/stats", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("stats on unknown app: status %d, want 404", rr.Code)
	}
}

func TestCacheAPI_Stats_OK(t *testing.T) {
	mux, p, ecm := newCacheTestEnv(t)

	// Serve a stub stub_status endpoint.
	statsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Active connections: 2\nserver accepts handled requests\n 10 10 50\nReading: 0 Writing: 1 Waiting: 1"))
	}))
	defer statsSrv.Close()
	ecm.statusURL = statsSrv.URL

	_, _ = p.ProvisionSubdomain(context.Background(), "calc", "default", "127.0.0.1:9001")

	req := httptest.NewRequest(http.MethodGet, "/api/apps/calc/cache/stats", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("stats: status %d, body: %s", rr.Code, rr.Body)
	}

	var stats NginxStubStatus
	if err := json.NewDecoder(rr.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats response: %v", err)
	}
	if stats.ActiveConnections != 2 {
		t.Errorf("active_connections = %d, want 2", stats.ActiveConnections)
	}
	if stats.Requests != 50 {
		t.Errorf("requests = %d, want 50", stats.Requests)
	}
}

func TestCacheAPI_PurgeWritesNginxConf(t *testing.T) {
	mux, p, ecm := newCacheTestEnv(t)

	_, _ = p.ProvisionSubdomain(context.Background(), "blog", "default", "127.0.0.1:9002")

	req := httptest.NewRequest(http.MethodPost, "/api/apps/blog/cache/purge", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("purge: status %d", rr.Code)
	}

	// After purge, the Nginx conf for the app should have been written.
	confPath := ecm.confPath("blog", "default")
	data, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("expected nginx conf at %s after purge, got: %v", confPath, err)
	}
	if !strings.Contains(string(data), "blog--default.testulid.vulos.org") {
		t.Errorf("nginx conf does not contain expected FQDN: %s", data)
	}
}
