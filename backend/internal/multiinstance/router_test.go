package multiinstance_test

// MINST-03 tests: per-instance app routing table + FQDN derivation.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vulos/backend/internal/multiinstance"
)

// ---------------------------------------------------------------------------
// FQDN helper
// ---------------------------------------------------------------------------

func TestFQDN(t *testing.T) {
	cases := []struct {
		app, profile, ulid, base, want string
	}{
		{
			app:     "browser",
			profile: "work",
			ulid:    "01HWZTEST0000000000000001",
			base:    "vulos.net",
			want:    "browser--work.01HWZTEST0000000000000001.vulos.net",
		},
		{
			app:     "notes",
			profile: "personal",
			ulid:    "01HWZTEST0000000000000002",
			base:    "vulos.net",
			want:    "notes--personal.01HWZTEST0000000000000002.vulos.net",
		},
		{
			// Upper-case app/profile are normalised to lower-case.
			app:     "BROWSER",
			profile: "Work",
			ulid:    "01HWZTEST0000000000000003",
			base:    "vulos.net",
			want:    "browser--work.01HWZTEST0000000000000003.vulos.net",
		},
		{
			// Empty base falls back to the package constant "vulos.net".
			app:     "terminal",
			profile: "default",
			ulid:    "01HWZTEST0000000000000004",
			base:    "",
			want:    "terminal--default.01HWZTEST0000000000000004.vulos.net",
		},
	}

	for _, tc := range cases {
		got := multiinstance.FQDN(tc.app, tc.profile, tc.ulid, tc.base)
		if got != tc.want {
			t.Errorf("FQDN(%q,%q,%q,%q) = %q; want %q",
				tc.app, tc.profile, tc.ulid, tc.base, got, tc.want)
		}
	}
}

// TestFQDNSanitisesDoubleDash verifies that "--" embedded in app/profile slugs
// is collapsed to prevent ambiguity with the scheme separator.
func TestFQDNSanitisesDoubleDash(t *testing.T) {
	got := multiinstance.FQDN("my--app", "my--profile", "ULID1", "vulos.net")
	// "--" inside the slug components should become "-"
	if got != "my-app--my-profile.ULID1.vulos.net" {
		t.Errorf("unexpected FQDN with double-dash slug: %q", got)
	}
}

// ---------------------------------------------------------------------------
// BuildTable
// ---------------------------------------------------------------------------

func TestBuildTablePinnedInstance(t *testing.T) {
	r := openTempRegistry(t)

	inst := multiinstance.Instance{
		ULID:        "01HWZTEST0000000000000010",
		DisplayName: "My Laptop",
		Kind:        multiinstance.KindDevice,
		EndpointURL: "https://laptop.example.com",
		Status:      multiinstance.StatusOnline,
	}
	if err := r.Upsert(inst); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ar := multiinstance.NewAppRouter(r, "vulos.net")
	apps := []multiinstance.PublishedApp{
		{App: "browser", Profile: "work", InstanceULID: inst.ULID},
	}

	table, err := ar.BuildTable(apps)
	if err != nil {
		t.Fatalf("BuildTable: %v", err)
	}
	if len(table) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(table))
	}

	e := table[0]
	if e.App != "browser" {
		t.Errorf("App: got %q want %q", e.App, "browser")
	}
	if e.Profile != "work" {
		t.Errorf("Profile: got %q want %q", e.Profile, "work")
	}
	if e.InstanceULID != inst.ULID {
		t.Errorf("InstanceULID: got %q want %q", e.InstanceULID, inst.ULID)
	}
	if e.InstanceDisplayName != inst.DisplayName {
		t.Errorf("InstanceDisplayName: got %q want %q", e.InstanceDisplayName, inst.DisplayName)
	}
	if e.InstanceEndpoint != inst.EndpointURL {
		t.Errorf("InstanceEndpoint: got %q want %q", e.InstanceEndpoint, inst.EndpointURL)
	}
	wantFQDN := multiinstance.FQDN("browser", "work", inst.ULID, "vulos.net")
	if e.FQDN != wantFQDN {
		t.Errorf("FQDN: got %q want %q", e.FQDN, wantFQDN)
	}
}

func TestBuildTableExpandsAcrossAllInstances(t *testing.T) {
	r := openTempRegistry(t)

	ulids := []string{
		"01HWZTEST0000000000000020",
		"01HWZTEST0000000000000021",
		"01HWZTEST0000000000000022",
	}
	for _, ulid := range ulids {
		if err := r.Upsert(multiinstance.Instance{
			ULID:   ulid,
			Kind:   multiinstance.KindDevice,
			Status: multiinstance.StatusOnline,
		}); err != nil {
			t.Fatalf("Upsert %s: %v", ulid, err)
		}
	}

	ar := multiinstance.NewAppRouter(r, "vulos.net")
	// InstanceULID empty → expand across all instances.
	apps := []multiinstance.PublishedApp{
		{App: "notes", Profile: "default"},
	}

	table, err := ar.BuildTable(apps)
	if err != nil {
		t.Fatalf("BuildTable: %v", err)
	}
	if len(table) != len(ulids) {
		t.Fatalf("expected %d entries (one per instance), got %d", len(ulids), len(table))
	}

	// Verify each entry has the correct FQDN for its instance.
	seen := make(map[string]bool)
	for _, e := range table {
		want := multiinstance.FQDN("notes", "default", e.InstanceULID, "vulos.net")
		if e.FQDN != want {
			t.Errorf("FQDN for instance %s: got %q want %q", e.InstanceULID, e.FQDN, want)
		}
		seen[e.InstanceULID] = true
	}
	for _, ulid := range ulids {
		if !seen[ulid] {
			t.Errorf("ULID %s missing from routing table", ulid)
		}
	}
}

func TestBuildTableUnknownInstanceSkipped(t *testing.T) {
	r := openTempRegistry(t)
	// Registry is empty — pinned ULID will not be found.
	ar := multiinstance.NewAppRouter(r, "vulos.net")
	apps := []multiinstance.PublishedApp{
		{App: "browser", Profile: "work", InstanceULID: "01HWZTEST9999999999999999"},
	}

	table, err := ar.BuildTable(apps)
	if err != nil {
		t.Fatalf("BuildTable: %v", err)
	}
	if len(table) != 0 {
		t.Errorf("expected empty table for unknown instance, got %d entries", len(table))
	}
}

func TestBuildTableEmptyAppsEmptyTable(t *testing.T) {
	r := openTempRegistry(t)
	ar := multiinstance.NewAppRouter(r, "vulos.net")

	table, err := ar.BuildTable(nil)
	if err != nil {
		t.Fatalf("BuildTable(nil): %v", err)
	}
	if len(table) != 0 {
		t.Errorf("expected empty table, got %d entries", len(table))
	}
}

// ---------------------------------------------------------------------------
// HTTP handler — GET /api/routing/apps
// ---------------------------------------------------------------------------

func TestRegisterHandlersRoutingTable(t *testing.T) {
	r := openTempRegistry(t)

	inst := multiinstance.Instance{
		ULID:        "01HWZTEST0000000000000030",
		DisplayName: "Cloud Instance",
		Kind:        multiinstance.KindCloud,
		EndpointURL: "https://cloud.example.com",
		Status:      multiinstance.StatusOnline,
	}
	if err := r.Upsert(inst); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ar := multiinstance.NewAppRouter(r, "vulos.net").
		WithAppProvider(func() []multiinstance.PublishedApp {
			return []multiinstance.PublishedApp{
				{App: "browser", Profile: "work", InstanceULID: inst.ULID},
				{App: "notes", Profile: "personal", InstanceULID: inst.ULID},
			}
		})

	mux := http.NewServeMux()
	multiinstance.RegisterHandlers(mux, ar)

	req := httptest.NewRequest(http.MethodGet, "/api/routing/apps", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q want %q", ct, "application/json")
	}

	var table []multiinstance.AppEntry
	if err := json.NewDecoder(w.Body).Decode(&table); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(table) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(table))
	}

	// Verify FQDNs resolve to the correct instance endpoint domain.
	for _, e := range table {
		if e.InstanceULID != inst.ULID {
			t.Errorf("InstanceULID: got %q want %q", e.InstanceULID, inst.ULID)
		}
		if e.InstanceEndpoint != inst.EndpointURL {
			t.Errorf("InstanceEndpoint: got %q want %q", e.InstanceEndpoint, inst.EndpointURL)
		}
	}
}

func TestRegisterHandlersEmptyProviderReturnsEmptyArray(t *testing.T) {
	r := openTempRegistry(t)
	ar := multiinstance.NewAppRouter(r, "vulos.net")

	mux := http.NewServeMux()
	multiinstance.RegisterHandlers(mux, ar)

	req := httptest.NewRequest(http.MethodGet, "/api/routing/apps", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}

	var table []multiinstance.AppEntry
	if err := json.NewDecoder(w.Body).Decode(&table); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if table == nil || len(table) != 0 {
		t.Errorf("expected empty array [], got %v", table)
	}
}

// TestStubDNSResolvesToCorrectEndpoint verifies the "stub DNS" AC: the FQDN
// built for a pinned instance embeds that instance's ULID, and the table row
// also carries the instance's endpoint URL.  A real DNS resolver would be
// configured to point {ulid}.vulos.net → that endpoint; here we assert the
// routing-table row carries all the data needed to populate such a mapping.
func TestStubDNSResolvesToCorrectEndpoint(t *testing.T) {
	r := openTempRegistry(t)

	instances := []multiinstance.Instance{
		{
			ULID:        "01HWZTEST0000000000000040",
			DisplayName: "Device A",
			EndpointURL: "https://device-a.vulos.net",
			Kind:        multiinstance.KindDevice,
			Status:      multiinstance.StatusOnline,
		},
		{
			ULID:        "01HWZTEST0000000000000041",
			DisplayName: "Device B",
			EndpointURL: "https://device-b.vulos.net",
			Kind:        multiinstance.KindDevice,
			Status:      multiinstance.StatusOnline,
		},
	}
	for _, inst := range instances {
		if err := r.Upsert(inst); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	ar := multiinstance.NewAppRouter(r, "vulos.net")
	apps := []multiinstance.PublishedApp{
		{App: "browser", Profile: "work", InstanceULID: instances[0].ULID},
		{App: "browser", Profile: "work", InstanceULID: instances[1].ULID},
	}

	table, err := ar.BuildTable(apps)
	if err != nil {
		t.Fatalf("BuildTable: %v", err)
	}
	if len(table) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(table))
	}

	// Verify: FQDN for entry[i] embeds instances[i].ULID, and the row carries
	// instances[i].EndpointURL — the data needed by stub or real DNS.
	for i, e := range table {
		wantFQDN := multiinstance.FQDN("browser", "work", instances[i].ULID, "vulos.net")
		if e.FQDN != wantFQDN {
			t.Errorf("[%d] FQDN: got %q want %q", i, e.FQDN, wantFQDN)
		}
		if e.InstanceEndpoint != instances[i].EndpointURL {
			t.Errorf("[%d] InstanceEndpoint: got %q want %q", i, e.InstanceEndpoint, instances[i].EndpointURL)
		}
	}
}
