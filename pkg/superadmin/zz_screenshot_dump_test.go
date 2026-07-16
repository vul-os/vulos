// zz_screenshot_dump_test.go — offline HTML dumper for the screenshotter.
//
// This is NOT a behavioural test. It is a tooling harness used by
// scripts/screenshots to render the real super-admin console templates — the
// exact html/template + admin.css this repo ships — populated with fabricated
// demo data, into standalone .html files that headless Chromium then captures.
//
// Why render offline instead of driving the live server? The console is gated
// by RequireSuperAdmin: main session + IP allowlist + a WebAuthn hardware-key
// step-up. That cannot be satisfied headlessly without fabricating hardware-key
// credentials. So we drive the SAME page handlers the server mounts (via the
// same NewPages constructor and the same seam providers cmd/server wires),
// bypassing only the auth middleware — the pixels are identical to production.
//
// It never touches real data: every store is an in-memory SQLite DB seeded with
// invented accounts/orgs/usage. It runs only when SCREENSHOT_DUMP=1 and writes
// to SCREENSHOT_OUT; a normal `go test` skips it.
package superadmin_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/superadmin"
)

// rewriteAssets makes the server-absolute asset URLs relative so the dumped HTML
// renders when served from the flat output directory (admin.css, login.js and
// account_detail.js all sit next to the .html files).
func rewriteAssets(html string) string {
	r := strings.NewReplacer(
		`/superadmin/admin.css`, `admin.css`,
		`/superadmin/login.js`, `login.js`,
		`/superadmin/account_detail.js`, `account_detail.js`,
	)
	return r.Replace(html)
}

func writePage(t *testing.T, outDir, name string, h http.HandlerFunc, req *http.Request) {
	t.Helper()
	rr := httptest.NewRecorder()
	h(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("render %s: status %d\n%s", name, rr.Code, rr.Body.String())
	}
	dst := filepath.Join(outDir, "admin-"+name+".html")
	if err := os.WriteFile(dst, []byte(rewriteAssets(rr.Body.String())), 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func writeAsset(t *testing.T, outDir, name string, h http.Handler) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/superadmin/"+name, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("asset %s: status %d", name, rr.Code)
	}
	if err := os.WriteFile(filepath.Join(outDir, name), rr.Body.Bytes(), 0o644); err != nil {
		t.Fatalf("write asset %s: %v", name, err)
	}
}

// TestScreenshotDumpAdminConsole renders every major super-admin console page to
// SCREENSHOT_OUT for the Playwright screenshotter. Skipped unless SCREENSHOT_DUMP=1.
func TestScreenshotDumpAdminConsole(t *testing.T) {
	if os.Getenv("SCREENSHOT_DUMP") != "1" {
		t.Skip("set SCREENSHOT_DUMP=1 to dump console HTML (used by scripts/screenshots)")
	}
	outDir := os.Getenv("SCREENSHOT_OUT")
	if outDir == "" {
		t.Fatal("SCREENSHOT_OUT must be set")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	authStore, saStore, _ := openTestStores(t)
	ctx := context.Background()

	// Audit logger on the SAME db the console reads (authStore CPDB), so the
	// dashboard + audit-log pages show a populated, verifiable chain.
	al, err := auditlog.Open(authStore.CPDB())
	if err != nil {
		t.Fatalf("open audit logger: %v", err)
	}

	// ── Seed accounts ────────────────────────────────────────────────────────
	demoAccounts := []struct{ email, pw string }{
		{"ada@vulos.org", "correct-horse-battery-staple-1"},
		{"grace@vulos.org", "correct-horse-battery-staple-2"},
		{"linus@acme.test", "correct-horse-battery-staple-3"},
		{"margaret@acme.test", "correct-horse-battery-staple-4"},
		{"dennis@example.test", "correct-horse-battery-staple-5"},
		{"katherine@example.test", "correct-horse-battery-staple-6"},
	}
	var firstID string
	for i, a := range demoAccounts {
		id := createUser(t, authStore, a.email, a.pw)
		if i == 0 {
			firstID = id
			promoteSuperAdmin(t, authStore, id)
		}
	}

	// ── Seed audit chain ─────────────────────────────────────────────────────
	seedAudit := []struct{ actor, action, target string }{
		{"ada@vulos.org", "admin.login.success", "ada@vulos.org"},
		{"ada@vulos.org", "account.suspend", "spam-bot-4471"},
		{"grace@vulos.org", "pricing.update", "sku:pro_seat"},
		{"ada@vulos.org", "region.upsert", "region:jnb"},
		{"grace@vulos.org", "incident.create", "inc-2026-07-14"},
		{"ada@vulos.org", "reserved_handle.add", "postmaster"},
		{"ada@vulos.org", "account.refund", "txn_9f2a1c"},
		{"grace@vulos.org", "org.suspend", "org:acme-corp"},
	}
	for _, e := range seedAudit {
		_ = al.Record(ctx, e.actor, e.action, e.target, map[string]string{"ip": "10.0.0.7"})
		time.Sleep(time.Millisecond)
	}

	// ── Reserved handles ─────────────────────────────────────────────────────
	for _, h := range []struct{ handle, reason string }{
		{"postmaster", "RFC 2142 mandatory mailbox"},
		{"abuse", "RFC 2142 mandatory mailbox"},
		{"admin", "reserved — platform"},
		{"security", "reserved — platform"},
		{"vulos", "reserved — brand"},
	} {
		_ = saStore.AddReservedHandle(h.handle, h.reason, firstID, al)
	}

	// ── Migration manifest: a local stub so the page shows populated rows ─────
	migSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "pending"):
			_, _ = w.Write([]byte(`{"status":"pending","pending":2,"applied":38,"latest":"0038_add_dunning_state"}`))
		default:
			_, _ = w.Write([]byte(`{"status":"ok","pending":0,"applied":41,"latest":"0041_org_seats"}`))
		}
	}))
	defer migSrv.Close()

	pages := superadmin.NewPages(saStore, al, authStore)

	// ── Wire the same seam providers cmd/server injects, with demo data ──────
	pages.SetFleetBillingProvider(func(context.Context) superadmin.FleetBilling {
		return superadmin.FleetBilling{
			MRRZARCents:    41_820_00,
			AccountCount:   1284,
			SuspendedCount: 7,
			Products: []superadmin.FleetProductRow{
				{Product: "mail", Usage: "2.1M sends / 30d", EstCostUSDCts: 21_040},
				{Product: "meet", Usage: "18.4k mtg-min / 30d", EstCostUSDCts: 3_310},
				{Product: "relay", Usage: "9.7 TB / 30d", EstCostUSDCts: 9_700},
				{Product: "storage", Usage: "4.3 TB unified buckets", EstCostUSDCts: 4_730},
			},
		}
	})

	pages.SetAnalyticsUsageProvider(func(context.Context) []superadmin.ProductUsageTrend {
		mk := func(product, unit string, base int64) superadmin.ProductUsageTrend {
			days := make([]superadmin.DayCount, 0, 7)
			var total, peak int64
			for i := 0; i < 7; i++ {
				c := base + int64((i*i*37)%23)*base/40
				days = append(days, superadmin.DayCount{Day: time.Now().AddDate(0, 0, i-6).Format("2006-01-02"), Count: c})
				total += c
				if c > peak {
					peak = c
				}
			}
			return superadmin.ProductUsageTrend{Product: product, Unit: unit, Total7d: total, PeakDay: peak, Days: days}
		}
		return []superadmin.ProductUsageTrend{
			mk("mail", "sends", 41_000),
			mk("meet", "min", 2_600),
			mk("relay", "GiB", 1_380),
			mk("office", "edits", 8_900),
		}
	})

	pages.SetOrgProviders(
		func(context.Context, string, int, int) superadmin.OrgListResult {
			return superadmin.OrgListResult{Orgs: []superadmin.OrgAdminRow{
				{ID: "org1", Slug: "acme-corp", Name: "Acme Corp", OwnerEmail: "linus@acme.test", MemberCount: 42, SeatCount: 40, Tier: "pro", CreatedAt: "2026-02-11"},
				{ID: "org2", Slug: "globex", Name: "Globex", OwnerEmail: "margaret@acme.test", MemberCount: 12, SeatCount: 12, Tier: "pro", CreatedAt: "2026-03-02"},
				{ID: "org3", Slug: "hooli", Name: "Hooli", OwnerEmail: "dennis@example.test", MemberCount: 5, SeatCount: 0, Tier: "free", CreatedAt: "2026-05-19"},
				{ID: "org4", Slug: "initech", Name: "Initech", OwnerEmail: "katherine@example.test", MemberCount: 3, SeatCount: 0, Tier: "free", Suspended: true, CreatedAt: "2026-06-08"},
			}}
		},
		func(_ context.Context, orgID string) *superadmin.OrgAdminDetail {
			return &superadmin.OrgAdminDetail{
				OrgAdminRow:  superadmin.OrgAdminRow{ID: orgID, Slug: "acme-corp", Name: "Acme Corp", OwnerEmail: "linus@acme.test", MemberCount: 42, SeatCount: 40, Tier: "pro", CreatedAt: "2026-02-11"},
				UsageSummary: "2.1M mail sends, 640 GiB storage, 18.4k meet-min in the current period.",
				Members: []superadmin.OrgMemberRow{
					{AccountID: "acct-l", Email: "linus@acme.test", Role: "owner", JoinedAt: "2026-02-11"},
					{AccountID: "acct-m", Email: "margaret@acme.test", Role: "admin", JoinedAt: "2026-02-12"},
					{AccountID: "acct-d", Email: "dennis@example.test", Role: "member", JoinedAt: "2026-03-01"},
				},
			}
		},
		nil, nil,
	)

	pages.SetRelayProvider(func(context.Context) superadmin.RelayOverview {
		return superadmin.RelayOverview{
			Period:       time.Now().Format("2006-01"),
			TotalGBMonth: 9_734.2,
			PoPs: []superadmin.RelayPoPRow{
				{Region: "eu", Healthy: 3, Total: 3, Status: "up"},
				{Region: "jnb", Healthy: 1, Total: 2, Status: "degraded"},
				{Region: "us", Healthy: 2, Total: 2, Status: "up"},
			},
			Accounts: []superadmin.RelayAccountRow{
				{AccountID: "org1", Email: "linus@acme.test", GB: 4_120.5, Sessions: 210, OverageGB: 120.5},
				{AccountID: "org2", Email: "margaret@acme.test", GB: 1_980.0, Sessions: 88, OverageGB: 0},
				{AccountID: "acct-d", Email: "dennis@example.test", GB: 640.2, Sessions: 31, OverageGB: 0},
			},
		}
	})

	pages.SetBillingReconProvider(func(context.Context) superadmin.BillingReconResult {
		return superadmin.BillingReconResult{
			TotalRevenueUSD:    12_480.0,
			TotalCOGSUSD:       4_690.0,
			BlendedMarginPct:   62.4,
			ModelMarginPct:     64.0,
			BlendedDeltaPP:     -1.6,
			BlendedStatus:      "GREEN",
			UncollectedPastDue: 3,
			ByTier: []superadmin.TierSummary{
				{Tier: "free", AccountCount: 940, TotalRevenueUSD: 0, TotalCOGSUSD: 310, MarginPct: 0, ModelMarginPct: 0, DeltaPP: 0, Status: "GREEN"},
				{Tier: "pro", AccountCount: 322, TotalRevenueUSD: 9_660, TotalCOGSUSD: 3_640, MarginPct: 62.3, ModelMarginPct: 64.0, DeltaPP: -1.7, Status: "GREEN"},
				{Tier: "team", AccountCount: 22, TotalRevenueUSD: 2_820, TotalCOGSUSD: 740, MarginPct: 73.8, ModelMarginPct: 70.0, DeltaPP: 3.8, Status: "YELLOW"},
			},
			Drifted: []superadmin.TenantReconRow{
				{AccountID: "acct-h", Email: "heavy@acme.test", Tier: "pro", State: "active", RevenueUSD: 9, PriceUSD: 9, COGSEstUSD: 15.4, MarginPct: -71.1, DriftFlags: []string{"COGS $15.40 > price $9.00"}},
				{AccountID: "acct-x", Email: "past@example.test", Tier: "pro", State: "past_due", RevenueUSD: 0, PriceUSD: 9, COGSEstUSD: 2.1, MarginPct: -100, DriftFlags: []string{"past_due — uncollected"}},
			},
		}
	})

	pages.SetPricingProviders(func(context.Context) []superadmin.PriceRowView {
		return []superadmin.PriceRowView{
			{SKU: "pro_seat", Name: "Pro seat", Unit: "seat/mo", USDCents: 900, USD: "9.00", ZAR: "165.30", Active: true, UpdatedAt: "2026-07-01", UpdatedBy: "grace@vulos.org"},
			{SKU: "team_seat", Name: "Team seat", Unit: "seat/mo", USDCents: 1200, USD: "12.00", ZAR: "220.40", Custom: true, Active: true, UpdatedAt: "2026-07-01", UpdatedBy: "grace@vulos.org"},
			{SKU: "mail_overage", Name: "Mail overage", Unit: "1k sends", USDCents: 10, USD: "0.10", ZAR: "1.84", Active: true, UpdatedAt: "2026-06-20", UpdatedBy: "ada@vulos.org"},
			{SKU: "relay_overage", Name: "Relay overage", Unit: "GiB", USDCents: 1, USD: "0.01", ZAR: "0.18", Active: true, UpdatedAt: "2026-06-20", UpdatedBy: "ada@vulos.org"},
		}
	}, nil)

	pages.SetRegionProviders(func(context.Context) []superadmin.RegionRowView {
		return []superadmin.RegionRowView{
			{ID: "eu", Name: "EU (Frankfurt)", EgressCostCentsGB: 1, RelayPriceCentsGB: 1, FreeRelayGB: 100, ComputeMultBps: 10000, ComputeMult: "1.0x", MarkupX: "1.0x", Active: true, Accounts: 812, Boxes: 34, SpendUSD: "1240.18", HasSpendData: true, RelayGBMonth: 6120.4, EstRelayCostUSD: "61.20", UpdatedAt: "2026-07-01", UpdatedBy: "ada@vulos.org"},
			{ID: "jnb", Name: "Johannesburg", EgressCostCentsGB: 9, RelayPriceCentsGB: 12, FreeRelayGB: 50, ComputeMultBps: 18000, ComputeMult: "1.8x", MarkupX: "1.3x", Active: true, Accounts: 214, Boxes: 9, SpendUSD: "410.55", HasSpendData: true, RelayGBMonth: 2410.0, EstRelayCostUSD: "216.90", UpdatedAt: "2026-07-01", UpdatedBy: "ada@vulos.org"},
			{ID: "us", Name: "US East", EgressCostCentsGB: 2, RelayPriceCentsGB: 2, FreeRelayGB: 100, ComputeMultBps: 11000, ComputeMult: "1.1x", MarkupX: "1.0x", Active: false, UpdatedAt: "2026-05-14", UpdatedBy: "grace@vulos.org"},
		}
	}, nil)

	pages.SetIncidentAdmin(&screenshotIncidents{
		incidents: []superadmin.IncidentView{
			{ID: "inc-2026-07-14", Title: "Relay JHB PoP degraded", Severity: "major", StartedAt: "2026-07-14T09:12:00Z"},
			{ID: "inc-2026-07-02", Title: "Delayed mail delivery (SES throttle)", Severity: "minor", StartedAt: "2026-07-02T14:40:00Z"},
		},
		maintenance: []superadmin.MaintenanceView{
			{ID: "m-2026-07-20", Title: "Postgres minor-version upgrade", StartsAt: "2026-07-20T02:00:00Z", EndsAt: "2026-07-20T03:00:00Z"},
		},
	})

	pages.SetMigrationManifest([]superadmin.MigrationEntry{
		{Product: "control-plane", StatusURL: migSrv.URL + "/cp/migrate/status"},
		{Product: "mail", StatusURL: migSrv.URL + "/mail/migrate/status"},
		{Product: "meet", StatusURL: migSrv.URL + "/meet/migrate/status/pending"},
		{Product: "relay", StatusURL: migSrv.URL + "/relay/migrate/status"},
	})

	// ── Render every page ────────────────────────────────────────────────────
	req := func(path string) *http.Request { return httptest.NewRequest("GET", path, nil) }

	writePage(t, outDir, "login", pages.LoginGet, req("/superadmin/login"))
	writePage(t, outDir, "dashboard", pages.Dashboard, req("/superadmin/"))
	writePage(t, outDir, "accounts", pages.AccountsList, req("/superadmin/accounts"))
	writePage(t, outDir, "analytics", pages.Analytics, req("/superadmin/analytics"))
	writePage(t, outDir, "orgs", pages.OrgsList, req("/superadmin/orgs"))
	writePage(t, outDir, "relay", pages.Relay, req("/superadmin/relay"))
	writePage(t, outDir, "billing-recon", pages.BillingReconciliation, req("/superadmin/billing-recon"))
	writePage(t, outDir, "pricing", pages.Pricing, req("/superadmin/pricing"))
	writePage(t, outDir, "regions", pages.Regions, req("/superadmin/regions"))
	writePage(t, outDir, "incidents", pages.Incidents, req("/superadmin/incidents"))
	writePage(t, outDir, "migrations", pages.MigrationsStatus, req("/superadmin/migrations"))
	writePage(t, outDir, "auditlog", pages.AuditLog, req("/superadmin/auditlog"))
	writePage(t, outDir, "reserved-handles", pages.ReservedHandles, req("/superadmin/reserved-handles"))
	writePage(t, outDir, "maintenance", pages.Maintenance, req("/superadmin/maintenance"))

	// Path-param pages need a mux to populate r.PathValue.
	amux := http.NewServeMux()
	amux.HandleFunc("GET /superadmin/accounts/{id}", pages.AccountDetail)
	amux.HandleFunc("GET /superadmin/orgs/{id}", pages.OrgDetail)
	writePage(t, outDir, "account-detail", amux.ServeHTTP, req("/superadmin/accounts/"+firstID))
	writePage(t, outDir, "org-detail", amux.ServeHTTP, req("/superadmin/orgs/org1"))

	// ── Assets ───────────────────────────────────────────────────────────────
	writeAsset(t, outDir, "admin.css", pages.AdminCSS())
	writeAsset(t, outDir, "login.js", pages.LoginJS())
	writeAsset(t, outDir, "account_detail.js", pages.AccountDetailJS())

	t.Logf("dumped super-admin console HTML to %s", outDir)
}

// screenshotIncidents is a static IncidentAdmin for the dump (renders the list;
// never mutates).
type screenshotIncidents struct {
	incidents   []superadmin.IncidentView
	maintenance []superadmin.MaintenanceView
}

func (f *screenshotIncidents) List(context.Context) ([]superadmin.IncidentView, []superadmin.MaintenanceView, error) {
	return f.incidents, f.maintenance, nil
}
func (f *screenshotIncidents) Create(context.Context, string, string, string, []string) error {
	return nil
}
func (f *screenshotIncidents) Update(context.Context, string, string, string) error { return nil }
func (f *screenshotIncidents) Resolve(context.Context, string) error                { return nil }
func (f *screenshotIncidents) ScheduleMaintenance(context.Context, string, string, []string, string, string) error {
	return nil
}
