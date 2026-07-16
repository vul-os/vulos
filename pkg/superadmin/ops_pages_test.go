// ops_pages_test.go — render + gating tests for the operator-console pages that
// draw their data from injected seams: RELAY & USAGE (item 5) and INCIDENTS
// (item 6). (The managed-box FLEET page was removed with box-provisioning.)
package superadmin_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/superadmin"
)

// ── Relay ─────────────────────────────────────────────────────────────────────

func TestRelayPage_NoProvider(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	req := httptest.NewRequest("GET", "/superadmin/relay", nil)
	rr := httptest.NewRecorder()
	pages.Relay(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Relay usage not wired") {
		t.Error("expected 'not wired' notice when no relay provider")
	}
}

func TestRelayPage_WithProvider(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	pages.SetRelayProvider(func(_ context.Context) superadmin.RelayOverview {
		return superadmin.RelayOverview{
			Period:       "2026-07",
			TotalGBMonth: 42.5,
			PoPs: []superadmin.RelayPoPRow{
				{Region: "eu", Healthy: 2, Total: 2, Status: "up"},
				{Region: "jnb", Healthy: 1, Total: 2, Status: "degraded"},
			},
			Accounts: []superadmin.RelayAccountRow{
				{AccountID: "acct1", Email: "whale@vulos.org", GB: 30.0, Sessions: 5, OverageGB: 5.0},
			},
		}
	})
	req := httptest.NewRequest("GET", "/superadmin/relay", nil)
	rr := httptest.NewRecorder()
	pages.Relay(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"2026-07", "whale@vulos.org", "jnb", "degraded", "up"} {
		if !strings.Contains(body, want) {
			t.Errorf("relay page missing %q", want)
		}
	}
}

// ── Incidents ─────────────────────────────────────────────────────────────────

// fakeIncidents is an in-memory IncidentAdmin for the console tests.
type fakeIncidents struct {
	incidents   []superadmin.IncidentView
	maintenance []superadmin.MaintenanceView
	created     int
	updated     int
	resolved    int
	scheduled   int
	failCreate  bool
}

func (f *fakeIncidents) List(_ context.Context) ([]superadmin.IncidentView, []superadmin.MaintenanceView, error) {
	return f.incidents, f.maintenance, nil
}
func (f *fakeIncidents) Create(_ context.Context, title, body, severity string, _ []string) error {
	if f.failCreate {
		return errors.New("boom")
	}
	f.created++
	f.incidents = append(f.incidents, superadmin.IncidentView{ID: "inc-new", Title: title, Severity: severity})
	return nil
}
func (f *fakeIncidents) Update(_ context.Context, _, _, _ string) error { f.updated++; return nil }
func (f *fakeIncidents) Resolve(_ context.Context, _ string) error      { f.resolved++; return nil }
func (f *fakeIncidents) ScheduleMaintenance(_ context.Context, _, _ string, _ []string, _, _ string) error {
	f.scheduled++
	return nil
}

func TestIncidentsPage_NoAdmin(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	req := httptest.NewRequest("GET", "/superadmin/incidents", nil)
	rr := httptest.NewRecorder()
	pages.Incidents(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Status store not wired") {
		t.Error("expected 'not wired' notice when no incident admin")
	}
}

func TestIncidentsPage_RendersAndCreates(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	fake := &fakeIncidents{
		incidents:   []superadmin.IncidentView{{ID: "inc1", Title: "Relay flap", Severity: "major", StartedAt: "2026-07-15T00:00:00Z"}},
		maintenance: []superadmin.MaintenanceView{{ID: "m1", Title: "DB upgrade", StartsAt: "2026-07-20T02:00:00Z", EndsAt: "2026-07-20T04:00:00Z"}},
	}
	pages.SetIncidentAdmin(fake)

	// Render lists existing incidents + maintenance.
	req := httptest.NewRequest("GET", "/superadmin/incidents", nil)
	rr := httptest.NewRecorder()
	pages.Incidents(rr, req)
	body := rr.Body.String()
	for _, want := range []string{"Relay flap", "major", "DB upgrade"} {
		if !strings.Contains(body, want) {
			t.Errorf("incidents page missing %q", want)
		}
	}

	// Create posts through to the admin seam (CSRF verified upstream; here we call
	// the handler directly to exercise the create path).
	form := url.Values{}
	form.Set("title", "New outage")
	form.Set("severity", "critical")
	form.Set("components", "relay-eu, control-plane")
	creq := httptest.NewRequest("POST", "/superadmin/incidents/create", strings.NewReader(form.Encode()))
	creq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	crr := httptest.NewRecorder()
	pages.IncidentCreate(crr, creq)
	if crr.Code != http.StatusSeeOther {
		t.Fatalf("create: want 303, got %d", crr.Code)
	}
	if fake.created != 1 {
		t.Fatalf("create not called: %d", fake.created)
	}
}

func TestIncidentsCreate_MissingTitle(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	pages.SetIncidentAdmin(&fakeIncidents{})

	form := url.Values{} // no title
	req := httptest.NewRequest("POST", "/superadmin/incidents/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	pages.IncidentCreate(rr, req)
	// Redirects back with ?err=; never a 5xx.
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("want 303 redirect, got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Errorf("expected ?err= redirect, got %q", loc)
	}
}

// Every incident mutation must be behind CSRF (tested via the wire wrapper).
func TestIncidentsCreate_CSRFGated(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	pages.SetIncidentAdmin(&fakeIncidents{})
	csrf := superadmin.CSRFProtect(al)
	mux := http.NewServeMux()
	mux.Handle("POST /superadmin/incidents/create", csrf(http.HandlerFunc(pages.IncidentCreate)))

	form := url.Values{}
	form.Set("title", "x")
	req := httptest.NewRequest("POST", "/superadmin/incidents/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without CSRF token, got %d", rr.Code)
	}
}
