package main

// routes_wifi_test.go — SEC-WIFI-SCAN-01.
//
// GET /api/wifi/scan reads like a query but is not one: wifi.Service.Scan
// shells out to `iw dev <iface> scan trigger` as root, takes the service mutex
// and sleeps 2s. It drives the radio and serialises every caller. Its sibling
// mutations (connect/disconnect/forget) were all admin-gated and audit-logged;
// scan was neither, and as a GET it was reachable from any page a logged-in
// non-admin visited via <img src=...>, where repeated hits pile up on the mutex
// and can disturb the active association.
//
// These tests drive registerWiFiRoutes — the SAME function main() calls — so a
// regression in the shipped route fails here rather than passing against a copy.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"vulos/backend/services/auth"
	"vulos/backend/services/wifi"
)

// fakeWiFi counts the privileged operations so a test can prove the gate ran
// BEFORE the radio was touched, not merely that the status code was 403.
type fakeWiFi struct {
	scans       atomic.Int32
	connects    atomic.Int32
	disconnects atomic.Int32
	forgets     atomic.Int32
}

func (f *fakeWiFi) Status(context.Context) wifi.Connection { return wifi.Connection{} }
func (f *fakeWiFi) Scan(context.Context) ([]wifi.Network, error) {
	f.scans.Add(1)
	return []wifi.Network{{SSID: "somenet"}}, nil
}
func (f *fakeWiFi) Connect(context.Context, string, string) error {
	f.connects.Add(1)
	return nil
}
func (f *fakeWiFi) Disconnect(context.Context) error { f.disconnects.Add(1); return nil }
func (f *fakeWiFi) SavedNetworks(context.Context) []wifi.SavedNetwork {
	return nil
}
func (f *fakeWiFi) ForgetNetwork(context.Context, string) error { f.forgets.Add(1); return nil }

// newWiFiTestMux returns the real registered routes plus the ids of an admin
// and a non-admin profile on the same box.
func newWiFiTestMux(t *testing.T) (*http.ServeMux, *fakeWiFi, string, string) {
	t.Helper()
	// FindOrCreateUser deliberately never grants admin on its own — that is the
	// privilege-escalation guard in services/auth (a cloud login must not be
	// able to mint an admin). The documented way to provision one is a verified
	// email named in VULOS_BOOTSTRAP_ADMIN_EMAIL, so use that rather than
	// reaching past the store's own rules.
	t.Setenv("VULOS_BOOTSTRAP_ADMIN_EMAIL", "admin@example.com")

	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	admin := store.FindOrCreateUser("test", "wifi-admin", "admin@example.com", "Admin", "", true)
	guest := store.FindOrCreateUser("test", "wifi-guest", "guest@example.com", "Guest", "", true)
	if admin == nil || guest == nil {
		t.Fatal("could not create test profiles")
	}
	if p, _ := store.GetProfile(admin.ID); p == nil || p.Role != auth.RoleAdmin {
		t.Fatalf("first user is not admin — test premise broken (role=%v)", p)
	}
	if p, _ := store.GetProfile(guest.ID); p == nil || p.Role == auth.RoleAdmin {
		t.Fatal("second user is admin — test cannot distinguish the gate")
	}

	fake := &fakeWiFi{}
	mux := http.NewServeMux()
	registerWiFiRoutes(mux, fake, store)
	return mux, fake, admin.ID, guest.ID
}

func wifiReq(mux *http.ServeMux, method, path, userID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestWiFiScan_RequiresAdmin(t *testing.T) {
	mux, fake, adminID, guestID := newWiFiTestMux(t)

	// A second, non-admin profile on the box must not be able to drive the radio.
	if rec := wifiReq(mux, "GET", "/api/wifi/scan", guestID); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin scan: got %d, want 403", rec.Code)
	}
	if n := fake.scans.Load(); n != 0 {
		t.Fatalf("non-admin was rejected but the radio scan still ran (%d time(s))", n)
	}

	// Unauthenticated must not either.
	if rec := wifiReq(mux, "GET", "/api/wifi/scan", ""); rec.Code != http.StatusForbidden {
		t.Errorf("unauthenticated scan: got %d, want 403", rec.Code)
	}
	if n := fake.scans.Load(); n != 0 {
		t.Fatalf("unauthenticated request still triggered a scan (%d time(s))", n)
	}

	// The admin still can — the gate must not break the feature.
	if rec := wifiReq(mux, "GET", "/api/wifi/scan", adminID); rec.Code != http.StatusOK {
		t.Errorf("admin scan: got %d, want 200", rec.Code)
	}
	if n := fake.scans.Load(); n != 1 {
		t.Errorf("admin scan ran %d time(s), want 1", n)
	}
}

// The sibling mutations were already gated; pin them so this extraction cannot
// have quietly loosened one.
func TestWiFiMutations_RequireAdmin(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		ran          func(*fakeWiFi) int32
	}{
		{"POST", "/api/wifi/connect", func(f *fakeWiFi) int32 { return f.connects.Load() }},
		{"POST", "/api/wifi/disconnect", func(f *fakeWiFi) int32 { return f.disconnects.Load() }},
		{"POST", "/api/wifi/forget", func(f *fakeWiFi) int32 { return f.forgets.Load() }},
	} {
		t.Run(tc.path, func(t *testing.T) {
			mux, fake, _, guestID := newWiFiTestMux(t)
			if rec := wifiReq(mux, tc.method, tc.path, guestID); rec.Code != http.StatusForbidden {
				t.Errorf("non-admin %s: got %d, want 403", tc.path, rec.Code)
			}
			if n := tc.ran(fake); n != 0 {
				t.Errorf("non-admin was rejected but the operation still ran (%d time(s))", n)
			}
		})
	}
}

// Reading status and the saved-network list stays available to any profile —
// they are genuine reads and the UI needs them.
func TestWiFiReads_StayOpenToAnyProfile(t *testing.T) {
	mux, _, _, guestID := newWiFiTestMux(t)
	for _, p := range []string{"/api/wifi/status", "/api/wifi/saved"} {
		if rec := wifiReq(mux, "GET", p, guestID); rec.Code != http.StatusOK {
			t.Errorf("%s for a non-admin: got %d, want 200", p, rec.Code)
		}
	}
}
