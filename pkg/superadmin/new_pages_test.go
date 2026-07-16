// new_pages_test.go — tests for the new superadmin portal pages:
// analytics, orgs management, billing reconciliation, migrations status.
//
// Test coverage requirements (from task):
//   - All handlers are authz-gated (RequireSuperAdmin returns 403/401 without creds).
//   - CSRF protection: state-changing POSTs return 403 without token.
//   - Cost reconciliation math: ReconcileTenant, TierMarginStatus, drift flags.
//   - Page renders: all new pages render HTTP 200 with expected content.
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
// Billing reconciliation math
// ─────────────────────────────────────────────────────────────────────────────

// TestReconcileTenant_CleanMargin verifies correct margin computation when
// revenue exceeds COGS.
func TestReconcileTenant_CleanMargin(t *testing.T) {
	row := superadmin.ReconcileTenant(
		"acct1", "user@example.com", "pro", "active",
		/* mailSends=       */ 500,
		/* machineMinutes=  */ 1000,
		/* meetMinutes=     */ 100,
		/* storageGBMo=     */ 2.0,
		/* egressGB=        */ 0.5,
		/* llmCostUSD=      */ 0.50,
		/* revenueUSD=      */ 9.00,
		/* priceUSD=        */ 9.00,
	)
	if row.Tier != "pro" {
		t.Errorf("tier mismatch: %q", row.Tier)
	}
	if row.COGSEstUSD <= 0 {
		t.Errorf("expected positive COGS, got %.4f", row.COGSEstUSD)
	}
	if row.MarginPct <= 0 || row.MarginPct > 100 {
		t.Errorf("expected margin in (0,100], got %.1f", row.MarginPct)
	}
	// At pro price $9 with ~$1–2 COGS, no drift expected.
	if len(row.DriftFlags) > 0 {
		t.Errorf("unexpected drift flags: %v", row.DriftFlags)
	}
}

// TestReconcileTenant_COGSExceedsPrice verifies the COGS-exceeds-price drift
// flag is emitted when metered COGS > list price.
func TestReconcileTenant_COGSExceedsPrice(t *testing.T) {
	// Use enormous compute minutes so COGS clearly exceeds the $9 pro price.
	row := superadmin.ReconcileTenant(
		"acct2", "heavy@example.com", "pro", "active",
		0, 100_000 /* minutes * $0.0018 = $180 COGS */, 0,
		0, 0, 0,
		/* revenueUSD= */ 9.00,
		/* priceUSD=   */ 9.00,
	)
	foundCOGS := false
	for _, f := range row.DriftFlags {
		if strings.Contains(f, "COGS") {
			foundCOGS = true
		}
	}
	if !foundCOGS {
		t.Errorf("expected COGS drift flag, got: %v", row.DriftFlags)
	}
	if row.MarginPct >= 0 {
		t.Errorf("expected negative margin when COGS > revenue, got %.1f%%", row.MarginPct)
	}
}

// TestReconcileTenant_FreeTierOverBudget verifies the free-tier over-budget
// drift flag.
func TestReconcileTenant_FreeTierOverBudget(t *testing.T) {
	// $0.32 * 2 = $0.64 budget; drive COGS above that.
	row := superadmin.ReconcileTenant(
		"acct3", "free@example.com", "free", "active",
		/* mailSends=      */ 10_000, // 10k * $0.10/1000 = $1.00 SES alone
		0, 0, 0, 0, 0,
		/* revenueUSD= */ 0.0,
		/* priceUSD=   */ 0.0,
	)
	foundFree := false
	for _, f := range row.DriftFlags {
		if strings.Contains(f, "free over budget") {
			foundFree = true
		}
	}
	if !foundFree {
		t.Errorf("expected 'free over budget' drift flag, got: %v", row.DriftFlags)
	}
}

// TestReconcileTenant_PastDueUncollected verifies the past_due drift flag.
func TestReconcileTenant_PastDueUncollected(t *testing.T) {
	row := superadmin.ReconcileTenant(
		"acct4", "pastdue@example.com", "pro", "past_due",
		0, 0, 0, 0, 0, 0,
		/* revenueUSD= */ 0.0, // no revenue collected
		/* priceUSD=   */ 9.0,
	)
	foundPastDue := false
	for _, f := range row.DriftFlags {
		if strings.Contains(f, "past_due") {
			foundPastDue = true
		}
	}
	if !foundPastDue {
		t.Errorf("expected 'past_due' drift flag, got: %v", row.DriftFlags)
	}
}

// TestTierMarginStatus_GREEN verifies small delta → GREEN.
func TestTierMarginStatus_GREEN(t *testing.T) {
	s := superadmin.TierMarginStatus(60.0, 62.4)
	if s != "GREEN" {
		t.Errorf("expected GREEN for delta 2.4pp, got %s", s)
	}
}

// TestTierMarginStatus_YELLOW verifies 5–10pp delta → YELLOW.
func TestTierMarginStatus_YELLOW(t *testing.T) {
	s := superadmin.TierMarginStatus(55.0, 62.4)
	if s != "YELLOW" {
		t.Errorf("expected YELLOW for delta 7.4pp, got %s", s)
	}
}

// TestTierMarginStatus_RED verifies >10pp delta → RED.
func TestTierMarginStatus_RED(t *testing.T) {
	s := superadmin.TierMarginStatus(40.0, 62.4)
	if s != "RED" {
		t.Errorf("expected RED for delta 22.4pp, got %s", s)
	}
}

// TestBillingReconPage_Renders_NoProvider verifies the billing-recon page
// renders gracefully when no provider is wired.
func TestBillingReconPage_Renders_NoProvider(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)

	req := httptest.NewRequest("GET", "/superadmin/billing-recon", nil)
	rr := httptest.NewRecorder()
	pages.BillingReconciliation(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "Billing Reconciliation") {
		t.Error("expected 'Billing Reconciliation' in page")
	}
}

// TestBillingReconPage_WithProvider verifies the billing-recon page renders
// tier summary and drift flags when the provider returns data.
func TestBillingReconPage_WithProvider(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	pages.SetBillingReconProvider(func(_ context.Context) superadmin.BillingReconResult {
		return superadmin.BillingReconResult{
			TotalRevenueUSD:    1000.0,
			TotalCOGSUSD:       400.0,
			BlendedMarginPct:   60.0,
			ModelMarginPct:     62.4,
			BlendedDeltaPP:     -2.4,
			BlendedStatus:      "GREEN",
			UncollectedPastDue: 2,
			ByTier: []superadmin.TierSummary{
				{Tier: "pro", AccountCount: 10, TotalRevenueUSD: 90, TotalCOGSUSD: 38.3,
					MarginPct: 57.4, ModelMarginPct: 57.4, DeltaPP: 0, Status: "GREEN"},
			},
			Drifted: []superadmin.TenantReconRow{
				{AccountID: "acct1", Email: "heavy@test.com", Tier: "pro", State: "active",
					RevenueUSD: 9, COGSEstUSD: 15, MarginPct: -66.7,
					DriftFlags: []string{"COGS $15.00 > price $9.00"}},
			},
		}
	})

	req := httptest.NewRequest("GET", "/superadmin/billing-recon", nil)
	rr := httptest.NewRecorder()
	pages.BillingReconciliation(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	s := string(body)
	for _, want := range []string{
		"Billing Reconciliation",
		"1000.00",                      // TotalRevenueUSD
		"GREEN",                        // BlendedStatus
		"pro",                          // tier row
		"heavy@test.com",               // drifted tenant
		"COGS $15.00 &gt; price $9.00", // html-escaped drift flag
	} {
		if !strings.Contains(s, want) {
			t.Errorf("billing-recon page missing %q", want)
		}
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
// Cost model constants sanity check
// ─────────────────────────────────────────────────────────────────────────────

// TestCostModelConstants verifies the cost model constants match the
// billingmodel/model.py reference values.
func TestCostModelConstants(t *testing.T) {
	// Fly: $0.0018/machine-min from model.py
	if superadmin.FlyMachineMinUSD != 0.0018 {
		t.Errorf("FlyMachineMinUSD mismatch: got %v", superadmin.FlyMachineMinUSD)
	}
	// Tigris: $0.021/GB-month
	if superadmin.TigrisStorageGBMonthUSD != 0.021 {
		t.Errorf("TigrisStorageGBMonthUSD mismatch: got %v", superadmin.TigrisStorageGBMonthUSD)
	}
	// SES: $0.10/1k emails
	if superadmin.SESPer1000USD != 0.10 {
		t.Errorf("SESPer1000USD mismatch: got %v", superadmin.SESPer1000USD)
	}
	// Meet: $0.01/participant-min
	if superadmin.MeetParticipantMinUSD != 0.01 {
		t.Errorf("MeetParticipantMinUSD mismatch: got %v", superadmin.MeetParticipantMinUSD)
	}
	// Pro price: $10/user/mo (TIERS.md FINAL 2026-06-28; was $9 — updated)
	if superadmin.TierListPrice("pro") != 10.00 {
		t.Errorf("TierListPrice(pro) mismatch: got %v, want 10.00", superadmin.TierListPrice("pro"))
	}
	// Personal price: $6/mo (new tier, single user)
	if superadmin.TierListPrice("personal") != 6.00 {
		t.Errorf("TierListPrice(personal) mismatch: got %v, want 6.00", superadmin.TierListPrice("personal"))
	}
	// Free: $0
	if superadmin.TierListPrice("free") != 0.00 {
		t.Errorf("TierListPrice(free) mismatch: got %v", superadmin.TierListPrice("free"))
	}
}

// TestTierModelMargin verifies tier expected margins.
func TestTierModelMargin(t *testing.T) {
	tests := []struct {
		tier     string
		expected float64
	}{
		{"free", -100.0},
		{"pro", 57.4},
		{"team", 68.3},
		{"enterprise", 56.9},
		{"unknown", 62.4}, // blended fallback
	}
	for _, tc := range tests {
		got := superadmin.TierModelMargin(tc.tier)
		if got != tc.expected {
			t.Errorf("TierModelMargin(%q) = %.1f, want %.1f", tc.tier, got, tc.expected)
		}
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
