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
// DeploymentStore
// ---------------------------------------------------------------------------

func newTestDeploymentStore(t *testing.T) *DeploymentStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app_deployments.json")
	ds, err := NewDeploymentStoreAt(path)
	if err != nil {
		t.Fatalf("NewDeploymentStoreAt: %v", err)
	}
	return ds
}

func TestDeploymentStore_SetAndGet(t *testing.T) {
	ds := newTestDeploymentStore(t)

	d := &Deployment{
		AppID:       "notes",
		Profile:     "default",
		FQDN:        "notes--default.local.vulos.net",
		Provisioned: time.Now().UTC(),
		TLSObtained: false,
	}
	if err := ds.Set(d); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got := ds.Get("notes", "default")
	if got == nil {
		t.Fatal("Get returned nil after Set")
	}
	if got.FQDN != d.FQDN {
		t.Errorf("Get.FQDN = %q, want %q", got.FQDN, d.FQDN)
	}
}

func TestDeploymentStore_GetMissingReturnsNil(t *testing.T) {
	ds := newTestDeploymentStore(t)
	if got := ds.Get("nonexistent", "default"); got != nil {
		t.Errorf("Get on missing key returned %+v, want nil", got)
	}
}

func TestDeploymentStore_Delete(t *testing.T) {
	ds := newTestDeploymentStore(t)
	_ = ds.Set(&Deployment{AppID: "app", Profile: "default", FQDN: "app--default.x.vulos.net"})
	if err := ds.Delete("app", "default"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := ds.Get("app", "default"); got != nil {
		t.Errorf("Get after Delete returned %+v, want nil", got)
	}
}

func TestDeploymentStore_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dep.json")
	ds1, _ := NewDeploymentStoreAt(path)
	_ = ds1.Set(&Deployment{AppID: "calc", Profile: "default", FQDN: "calc--default.x.vulos.net", Provisioned: time.Now().UTC()})

	ds2, err := NewDeploymentStoreAt(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	got := ds2.Get("calc", "default")
	if got == nil || got.FQDN != "calc--default.x.vulos.net" {
		t.Errorf("round-trip FQDN mismatch: %+v", got)
	}
}

func TestDeploymentStore_All(t *testing.T) {
	ds := newTestDeploymentStore(t)
	_ = ds.Set(&Deployment{AppID: "a", Profile: "default", FQDN: "a--default.x.vulos.net"})
	_ = ds.Set(&Deployment{AppID: "b", Profile: "work", FQDN: "b--work.x.vulos.net"})
	all := ds.All()
	if len(all) != 2 {
		t.Fatalf("All() len = %d, want 2", len(all))
	}
}

// ---------------------------------------------------------------------------
// Provisioner.BuildFQDN
// ---------------------------------------------------------------------------

func newNoopProvisioner(t *testing.T) *Provisioner {
	t.Helper()
	ds := newTestDeploymentStore(t)
	t.Setenv("VULOS_DNS_API", "noop")
	t.Setenv("VULOS_INSTANCE_ID", "testulid")
	t.Setenv("VULOS_BASE_DOMAIN", "vulos.net")
	t.Setenv("VULOS_CADDY_DIR", "noop")
	return NewProvisioner(ds, nil)
}

func TestProvisioner_BuildFQDN(t *testing.T) {
	p := newNoopProvisioner(t)

	cases := []struct {
		app     string
		profile string
		want    string
	}{
		{"notes", "default", "notes--default.testulid.vulos.net"},
		{"notes", "", "notes--default.testulid.vulos.net"},
		{"calc", "work", "calc--work.testulid.vulos.net"},
	}
	for _, c := range cases {
		got := p.BuildFQDN(c.app, c.profile)
		if got != c.want {
			t.Errorf("BuildFQDN(%q, %q) = %q, want %q", c.app, c.profile, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Provisioner.ProvisionSubdomain (noop DNS)
// ---------------------------------------------------------------------------

func TestProvisioner_ProvisionSubdomain_Noop(t *testing.T) {
	p := newNoopProvisioner(t)

	d, err := p.ProvisionSubdomain(context.Background(), "notes", "default", "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("ProvisionSubdomain: %v", err)
	}
	if d.AppID != "notes" {
		t.Errorf("AppID = %q, want %q", d.AppID, "notes")
	}
	if d.Profile != "default" {
		t.Errorf("Profile = %q, want %q", d.Profile, "default")
	}
	if d.FQDN != "notes--default.testulid.vulos.net" {
		t.Errorf("FQDN = %q, want %q", d.FQDN, "notes--default.testulid.vulos.net")
	}
	if d.Provisioned.IsZero() {
		t.Error("Provisioned timestamp is zero")
	}
}

func TestProvisioner_ProvisionSubdomain_Idempotent(t *testing.T) {
	p := newNoopProvisioner(t)

	d1, err := p.ProvisionSubdomain(context.Background(), "notes", "default", "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("first ProvisionSubdomain: %v", err)
	}
	d2, err := p.ProvisionSubdomain(context.Background(), "notes", "default", "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("second ProvisionSubdomain: %v", err)
	}
	if d1.FQDN != d2.FQDN {
		t.Errorf("idempotent: FQDNs differ: %q vs %q", d1.FQDN, d2.FQDN)
	}
	// Second call must not have changed the Provisioned timestamp.
	if !d1.Provisioned.Equal(d2.Provisioned) {
		t.Errorf("idempotent: Provisioned changed: %v → %v", d1.Provisioned, d2.Provisioned)
	}
}

func TestProvisioner_ProvisionSubdomain_DNSError(t *testing.T) {
	// Spin up a stub DNS server that returns 500.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Setenv("VULOS_DNS_API", srv.URL)
	t.Setenv("VULOS_INSTANCE_ID", "testulid")
	t.Setenv("VULOS_BASE_DOMAIN", "vulos.net")
	t.Setenv("VULOS_CADDY_DIR", "noop")

	ds := newTestDeploymentStore(t)
	p := NewProvisioner(ds, nil)

	_, err := p.ProvisionSubdomain(context.Background(), "app", "default", "127.0.0.1:0")
	if err == nil {
		t.Error("expected error on DNS API 500, got nil")
	}
}

func TestProvisioner_ProvisionSubdomain_DNSOK(t *testing.T) {
	// Stub DNS server that returns 200 with a valid response body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(dnsProvisionResponse{
			FQDN:   "calc--default.testulid.vulos.net",
			Status: "ok",
		})
	}))
	defer srv.Close()

	t.Setenv("VULOS_DNS_API", srv.URL)
	t.Setenv("VULOS_INSTANCE_ID", "testulid")
	t.Setenv("VULOS_BASE_DOMAIN", "vulos.net")
	t.Setenv("VULOS_CADDY_DIR", "noop")

	ds := newTestDeploymentStore(t)
	p := NewProvisioner(ds, nil)

	d, err := p.ProvisionSubdomain(context.Background(), "calc", "default", "127.0.0.1:9001")
	if err != nil {
		t.Fatalf("ProvisionSubdomain: %v", err)
	}
	if d.FQDN != "calc--default.testulid.vulos.net" {
		t.Errorf("FQDN = %q", d.FQDN)
	}
}

func TestProvisioner_DeprovisionSubdomain(t *testing.T) {
	p := newNoopProvisioner(t)

	_, err := p.ProvisionSubdomain(context.Background(), "notes", "default", "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("ProvisionSubdomain: %v", err)
	}
	if err := p.DeprovisionSubdomain("notes", "default"); err != nil {
		t.Fatalf("DeprovisionSubdomain: %v", err)
	}
	if got := p.store.Get("notes", "default"); got != nil {
		t.Errorf("deployment still present after deprovision: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Caddy snippet
// ---------------------------------------------------------------------------

func TestProvisioner_WriteCaddySnippet(t *testing.T) {
	caddyDir := t.TempDir()

	t.Setenv("VULOS_DNS_API", "noop")
	t.Setenv("VULOS_INSTANCE_ID", "testulid")
	t.Setenv("VULOS_BASE_DOMAIN", "vulos.net")
	t.Setenv("VULOS_CADDY_DIR", caddyDir)

	ds := newTestDeploymentStore(t)
	p := NewProvisioner(ds, nil)

	_, err := p.ProvisionSubdomain(context.Background(), "myapp", "prod", "127.0.0.1:9090")
	if err != nil {
		t.Fatalf("ProvisionSubdomain: %v", err)
	}

	snippetPath := filepath.Join(caddyDir, "myapp--prod.caddy")
	data, err := os.ReadFile(snippetPath)
	if err != nil {
		t.Fatalf("read caddy snippet: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "myapp--prod.testulid.vulos.net") {
		t.Errorf("snippet missing FQDN: %s", content)
	}
	if !strings.Contains(content, "reverse_proxy 127.0.0.1:9090") {
		t.Errorf("snippet missing upstream: %s", content)
	}
	if !strings.Contains(content, "tls") {
		t.Errorf("snippet missing TLS directive: %s", content)
	}
}

// TestProvisioner_ProvisionSubdomain_CaddyWriteFailsClosed is the wave-34
// fail-open regression: when the Caddy (proxy-config) snippet write fails, the
// subdomain silently would never route, so ProvisionSubdomain must FAIL — it
// must NOT swallow the error and persist a Deployment record (which would also
// poison the idempotency short-circuit on retry).
func TestProvisioner_ProvisionSubdomain_CaddyWriteFailsClosed(t *testing.T) {
	// Make caddyDir unwritable: point it at a path whose parent is a regular
	// file, so os.MkdirAll inside writeCaddySnippet fails.
	base := t.TempDir()
	notADir := filepath.Join(base, "iamafile")
	if err := os.WriteFile(notADir, []byte("x"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	caddyDir := filepath.Join(notADir, "caddy") // parent is a file → MkdirAll fails

	t.Setenv("VULOS_DNS_API", "noop")
	t.Setenv("VULOS_INSTANCE_ID", "testulid")
	t.Setenv("VULOS_BASE_DOMAIN", "vulos.net")
	t.Setenv("VULOS_CADDY_DIR", caddyDir)

	ds := newTestDeploymentStore(t)
	p := NewProvisioner(ds, nil)

	_, err := p.ProvisionSubdomain(context.Background(), "myapp", "default", "127.0.0.1:9000")
	if err == nil {
		t.Fatal("expected error when proxy-config write fails, got nil (fail-open)")
	}
	if !strings.Contains(err.Error(), "proxy config") {
		t.Errorf("error should mention proxy config, got: %v", err)
	}
	// Deployment must NOT have been persisted (idempotency must not be poisoned).
	if got := ds.Get("myapp", "default"); got != nil {
		t.Errorf("Deployment persisted despite write failure: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

func newProvisionTestEnv(t *testing.T) (*http.ServeMux, *VisibilityStore, *Provisioner) {
	t.Helper()
	t.Setenv("VULOS_DNS_API", "noop")
	t.Setenv("VULOS_INSTANCE_ID", "testulid")
	t.Setenv("VULOS_BASE_DOMAIN", "vulos.net")
	t.Setenv("VULOS_CADDY_DIR", "noop")

	dir := t.TempDir()
	vis, _ := NewVisibilityStoreAt(filepath.Join(dir, "vis.json"))
	ds, _ := NewDeploymentStoreAt(filepath.Join(dir, "dep.json"))
	p := NewProvisioner(ds, nil)

	mux := http.NewServeMux()
	RegisterSubdomainHandlers(mux, vis, p, nil)
	return mux, vis, p
}

func TestSubdomainAPI_GetDeployment_NotFound(t *testing.T) {
	mux, _, _ := newProvisionTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/apps/notes/deployment", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET /deployment on missing app: status %d, want 404", rr.Code)
	}
}

func TestSubdomainAPI_GetDeployment_Found(t *testing.T) {
	mux, _, p := newProvisionTestEnv(t)

	// Pre-provision a deployment.
	_, err := p.ProvisionSubdomain(context.Background(), "notes", "default", "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("ProvisionSubdomain: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/apps/notes/deployment", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /deployment: status %d, body: %s", rr.Code, rr.Body)
	}

	var resp deploymentResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.AppID != "notes" {
		t.Errorf("AppID = %q, want %q", resp.AppID, "notes")
	}
	if resp.FQDN != "notes--default.testulid.vulos.net" {
		t.Errorf("FQDN = %q", resp.FQDN)
	}
}

func TestSubdomainAPI_Deprovision(t *testing.T) {
	mux, vis, p := newProvisionTestEnv(t)

	// Set up a public app with a deployment.
	_ = vis.Set("notes", VisibilityPublic)
	_, _ = p.ProvisionSubdomain(context.Background(), "notes", "default", "127.0.0.1:9000")

	req := httptest.NewRequest(http.MethodPost, "/api/apps/notes/deprovision", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /deprovision: status %d, body: %s", rr.Code, rr.Body)
	}

	// Deployment should be gone.
	if got := p.store.Get("notes", "default"); got != nil {
		t.Errorf("deployment still present after deprovision: %+v", got)
	}
	// Visibility should have been reverted.
	if got := vis.Get("notes"); got != VisibilityPrivate {
		t.Errorf("visibility after deprovision = %q, want %q", got, VisibilityPrivate)
	}
}

// ---------------------------------------------------------------------------
// SetVisibilityWithProvisioning
// ---------------------------------------------------------------------------

func TestSetVisibilityWithProvisioning_PublicProvisions(t *testing.T) {
	t.Setenv("VULOS_DNS_API", "noop")
	t.Setenv("VULOS_INSTANCE_ID", "testulid")
	t.Setenv("VULOS_BASE_DOMAIN", "vulos.net")
	t.Setenv("VULOS_CADDY_DIR", "noop")

	dir := t.TempDir()
	vis, _ := NewVisibilityStoreAt(filepath.Join(dir, "vis.json"))
	ds, _ := NewDeploymentStoreAt(filepath.Join(dir, "dep.json"))
	p := NewProvisioner(ds, nil)

	d, err := SetVisibilityWithProvisioning(context.Background(), "calc", "default", VisibilityPublic, vis, p, nil)
	if err != nil {
		t.Fatalf("SetVisibilityWithProvisioning: %v", err)
	}
	if d == nil {
		t.Fatal("expected non-nil Deployment for public visibility")
	}
	if d.FQDN != "calc--default.testulid.vulos.net" {
		t.Errorf("FQDN = %q", d.FQDN)
	}
	if vis.Get("calc") != VisibilityPublic {
		t.Errorf("visibility not updated to public")
	}
}

func TestSetVisibilityWithProvisioning_PrivateDeProvisions(t *testing.T) {
	t.Setenv("VULOS_DNS_API", "noop")
	t.Setenv("VULOS_INSTANCE_ID", "testulid")
	t.Setenv("VULOS_BASE_DOMAIN", "vulos.net")
	t.Setenv("VULOS_CADDY_DIR", "noop")

	dir := t.TempDir()
	vis, _ := NewVisibilityStoreAt(filepath.Join(dir, "vis.json"))
	ds, _ := NewDeploymentStoreAt(filepath.Join(dir, "dep.json"))
	p := NewProvisioner(ds, nil)

	// First publish.
	_, err := SetVisibilityWithProvisioning(context.Background(), "calc", "default", VisibilityPublic, vis, p, nil)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Now make private.
	_, err = SetVisibilityWithProvisioning(context.Background(), "calc", "default", VisibilityPrivate, vis, p, nil)
	if err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	if got := ds.Get("calc", "default"); got != nil {
		t.Errorf("deployment still present after unpublish: %+v", got)
	}
	if vis.Get("calc") != VisibilityPrivate {
		t.Errorf("visibility not reverted to private")
	}
}

func TestSetVisibilityWithProvisioning_DNSErrorRollsBackVisibility(t *testing.T) {
	// Stub DNS server returns 500.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Setenv("VULOS_DNS_API", srv.URL)
	t.Setenv("VULOS_INSTANCE_ID", "testulid")
	t.Setenv("VULOS_BASE_DOMAIN", "vulos.net")
	t.Setenv("VULOS_CADDY_DIR", "noop")

	dir := t.TempDir()
	vis, _ := NewVisibilityStoreAt(filepath.Join(dir, "vis.json"))
	ds, _ := NewDeploymentStoreAt(filepath.Join(dir, "dep.json"))
	p := NewProvisioner(ds, nil)

	_, err := SetVisibilityWithProvisioning(context.Background(), "broken", "default", VisibilityPublic, vis, p, nil)
	if err == nil {
		t.Fatal("expected error on DNS 500, got nil")
	}
	// Visibility must have been rolled back to private.
	if got := vis.Get("broken"); got != VisibilityPrivate {
		t.Errorf("visibility after DNS failure = %q, want %q", got, VisibilityPrivate)
	}
}
