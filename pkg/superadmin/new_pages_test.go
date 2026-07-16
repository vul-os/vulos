// new_pages_test.go — tests for the superadmin portal pages:
// analytics (usage only), orgs management, migrations status.
//
// This is the OSS OPERATIONAL console. Commercial surfaces (billing
// reconciliation, pricing, regions cost model) were removed — a commercial
// distributor owns that admin in its own module (COORDINATOR: billing admin
// belongs in cloud, task #66).
//
// Test coverage requirements:
//   - All handlers are authz-gated (RequireSuperAdmin returns 403/401 without creds).
//   - CSRF protection: state-changing POSTs return 403 without token.
//   - Page renders: all pages render HTTP 200 with expected content.
package superadmin_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/superadmin"
)

// ─────────────────────────────────────────────────────────────────────────────
// Analytics page
// ─────────────────────────────────────────────────────────────────────────────

// TestAnalyticsPage_Renders verifies the analytics page renders HTTP 200 with
// the basic tile labels.
func TestAnalyticsPage_Renders(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)

	// Create a couple of users so we get non-zero totals.
	createUser(t, authStore, "a1@analytics.test", "test-password-12")
	createUser(t, authStore, "a2@analytics.test", "test-password-12")

	req := httptest.NewRequest("GET", "/superadmin/analytics", nil)
	rr := httptest.NewRecorder()
	pages.Analytics(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body, _ := io.ReadAll(rr.Body)
	s := string(body)
	for _, want := range []string{"Analytics", "Total users", "DAU", "MAU"} {
		if !strings.Contains(s, want) {
			t.Errorf("analytics page missing %q", want)
		}
	}
}

// TestAnalyticsPage_WithUsageProvider verifies the per-product usage section
// renders when an AnalyticsUsageProvider is wired.
func TestAnalyticsPage_WithUsageProvider(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	pages.SetAnalyticsUsageProvider(func(_ context.Context) []superadmin.ProductUsageTrend {
		return []superadmin.ProductUsageTrend{
			{
				Product: "mail",
				Unit:    "sends",
				Total7d: 42,
				PeakDay: 10,
				Days: []superadmin.DayCount{
					{Day: "2026-06-21", Count: 5},
					{Day: "2026-06-22", Count: 10},
				},
			},
		}
	})

	req := httptest.NewRequest("GET", "/superadmin/analytics", nil)
	rr := httptest.NewRecorder()
	pages.Analytics(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	s := string(body)
	if !strings.Contains(s, "mail") {
		t.Error("expected 'mail' product in analytics page")
	}
	if !strings.Contains(s, "42") {
		t.Error("expected Total7d '42' in analytics page")
	}
}

// TestAnalyticsPage_AuthzGated verifies that RequireSuperAdmin blocks
// unauthenticated requests.
func TestAnalyticsPage_AuthzGated(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	os.Setenv("VULOS_ADMIN_IP_ALLOWLIST", "10.0.0.0/8")
	defer os.Unsetenv("VULOS_ADMIN_IP_ALLOWLIST")

	pages := superadmin.NewPages(saStore, al, authStore)
	mw := superadmin.RequireSuperAdmin(saStore, authStore, al)
	handler := mw(http.HandlerFunc(pages.Analytics))

	req := httptest.NewRequest("GET", "/superadmin/analytics", nil)
	req.RemoteAddr = "192.168.1.1:12345" // not in allowlist → 403
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-allowed IP, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Orgs management pages
// ─────────────────────────────────────────────────────────────────────────────

// TestOrgsList_Renders_NoProvider verifies the orgs list renders gracefully
// when no OrgListProvider is wired.
func TestOrgsList_Renders_NoProvider(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)

	req := httptest.NewRequest("GET", "/superadmin/orgs", nil)
	rr := httptest.NewRecorder()
	pages.OrgsList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "Orgs") {
		t.Error("expected 'Orgs' heading in page")
	}
}

// TestOrgsList_WithProvider verifies org rows are rendered when the provider
// is wired.
func TestOrgsList_WithProvider(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	pages.SetOrgProviders(
		func(_ context.Context, q string, limit, offset int) superadmin.OrgListResult {
			return superadmin.OrgListResult{
				Orgs: []superadmin.OrgAdminRow{
					{ID: "org1", Slug: "acme-corp", Name: "Acme Corp", OwnerEmail: "owner@acme.test", MemberCount: 5, Tier: "pro"},
					{ID: "org2", Slug: "test-co", Name: "Test Co", OwnerEmail: "owner@test.co", MemberCount: 1, Tier: "free"},
				},
			}
		},
		nil, nil, nil,
	)

	req := httptest.NewRequest("GET", "/superadmin/orgs", nil)
	rr := httptest.NewRecorder()
	pages.OrgsList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	s := string(body)
	for _, want := range []string{"acme-corp", "Acme Corp", "owner@acme.test", "pro", "test-co"} {
		if !strings.Contains(s, want) {
			t.Errorf("orgs list missing %q", want)
		}
	}
}

// TestOrgDetail_NotFound returns 404 when detail provider returns nil.
func TestOrgDetail_NotFound(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	pages.SetOrgProviders(
		nil,
		func(_ context.Context, orgID string) *superadmin.OrgAdminDetail { return nil },
		nil, nil,
	)

	mux := http.NewServeMux()
	mux.Handle("GET /superadmin/orgs/{id}", http.HandlerFunc(pages.OrgDetail))
	req := httptest.NewRequest("GET", "/superadmin/orgs/notexist", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing org, got %d", rr.Code)
	}
}

// TestOrgActionExecute_CSRFGated verifies that the org action POST returns 403
// when the CSRF token is missing.
func TestOrgActionExecute_CSRFGated(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	csrf := superadmin.CSRFProtect(al)

	mux := http.NewServeMux()
	// Wrap with CSRF protection as wire_superadmin does.
	mux.Handle("POST /superadmin/orgs/{id}/action",
		csrf(http.HandlerFunc(pages.OrgActionExecute)))

	form := url.Values{}
	form.Set("action", "suspend")
	// Deliberately omit csrf_token.
	req := httptest.NewRequest("POST", "/superadmin/orgs/org1/action",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when CSRF token missing, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Migrations status page
// ─────────────────────────────────────────────────────────────────────────────

// TestMigrationsPage_Renders_NoManifest verifies the page renders gracefully
// with no manifest configured.
func TestMigrationsPage_Renders_NoManifest(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	// No manifest set — page should show notice.

	req := httptest.NewRequest("GET", "/superadmin/migrations", nil)
	rr := httptest.NewRecorder()
	pages.MigrationsStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "Migrations Status") {
		t.Error("expected 'Migrations Status' heading")
	}
}

// TestMigrationsPage_Renders_WithManifest verifies the migrations page polls
// configured endpoints and renders their status.
func TestMigrationsPage_Renders_WithManifest(t *testing.T) {
	// Spin up a local HTTP server to act as a product migration status endpoint.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","applied":12,"pending":0,"latest":"0012_add_index"}`))
	}))
	defer ts.Close()

	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	pages.SetMigrationManifest([]superadmin.MigrationEntry{
		{Product: "cp", StatusURL: ts.URL},
	})

	req := httptest.NewRequest("GET", "/superadmin/migrations", nil)
	rr := httptest.NewRecorder()
	pages.MigrationsStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	s := string(body)
	for _, want := range []string{"cp", "ok", "12", "0012_add_index"} {
		if !strings.Contains(s, want) {
			t.Errorf("migrations page missing %q", want)
		}
	}
}

// TestMigrationsPage_DegradedOnUnreachable verifies the page marks a product
// as "unreachable" when its endpoint is down, rather than failing the whole page.
func TestMigrationsPage_DegradedOnUnreachable(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	pages.SetMigrationManifest([]superadmin.MigrationEntry{
		{Product: "mail", StatusURL: "http://127.0.0.1:1"}, // unreachable port
	})

	req := httptest.NewRequest("GET", "/superadmin/migrations", nil)
	rr := httptest.NewRecorder()
	pages.MigrationsStatus(rr, req)

	// Page must still render 200 (degrade gracefully).
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 even on unreachable endpoint, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	s := string(body)
	// Should show "unreachable" status for the mail product.
	if !strings.Contains(s, "unreachable") {
		t.Error("expected 'unreachable' status for unreachable endpoint")
	}
	if !strings.Contains(s, "mail") {
		t.Error("expected 'mail' product name in table")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// LoadMigrationManifestFromEnv
// ─────────────────────────────────────────────────────────────────────────────

// TestLoadMigrationManifestFromEnv_ValidJSON verifies a well-formed JSON array
// is parsed correctly.
func TestLoadMigrationManifestFromEnv_ValidJSON(t *testing.T) {
	os.Setenv("VULOS_MIGRATE_MANIFEST_JSON",
		`[{"product":"cp","status_url":"http://cp/migrate/status"},{"product":"mail","status_url":"http://mail/status"}]`)
	defer os.Unsetenv("VULOS_MIGRATE_MANIFEST_JSON")

	entries := superadmin.LoadMigrationManifestFromEnv()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Product != "cp" || entries[0].StatusURL != "http://cp/migrate/status" {
		t.Errorf("entry[0] mismatch: %+v", entries[0])
	}
	if entries[1].Product != "mail" {
		t.Errorf("entry[1].Product mismatch: %q", entries[1].Product)
	}
}

// TestLoadMigrationManifestFromEnv_Empty returns nil for empty env var.
func TestLoadMigrationManifestFromEnv_Empty(t *testing.T) {
	os.Unsetenv("VULOS_MIGRATE_MANIFEST_JSON")
	entries := superadmin.LoadMigrationManifestFromEnv()
	if entries != nil {
		t.Errorf("expected nil for empty env var, got %v", entries)
	}
}

// TestLoadMigrationManifestFromEnv_Malformed returns nil for malformed JSON.
func TestLoadMigrationManifestFromEnv_Malformed(t *testing.T) {
	os.Setenv("VULOS_MIGRATE_MANIFEST_JSON", `not-json`)
	defer os.Unsetenv("VULOS_MIGRATE_MANIFEST_JSON")
	entries := superadmin.LoadMigrationManifestFromEnv()
	if entries != nil {
		t.Errorf("expected nil for malformed JSON, got %v", entries)
	}
}
