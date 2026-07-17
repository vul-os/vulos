package superadmin_test

// role_separation_test.go — proves the two roles are cleanly separated and the
// admin role is extra-hardened:
//
//   1. management ADMINS / operators — reach the super-admin console
//      (/superadmin/*, /api/superadmin/*) only through the full four-layer
//      RequireSuperAdmin gate (IP allowlist → main session → super-admin status →
//      WebAuthn-backed admin session).
//   2. portal USERS — ordinary signed-in accounts. A portal user has a valid main
//      session but is NOT a super-admin, and MUST be rejected from every admin
//      route.
//
// The gate wraps a sentinel handler that writes 200 "REACHED" only if the request
// gets through, so each case asserts, per route, whether the gate opened. This is
// the faithful production wrapper (wireSuperAdminConsole wraps every admin route
// with exactly this middleware), exercised over the whole route surface at once.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/superadmin"
)

// protectedAdminRoutes is the full set of super-admin routes that are mounted
// behind RequireSuperAdmin in wireSuperAdminConsole + RegisterSecurity. A portal
// user must be rejected from EVERY one. Path params are substituted with concrete
// values so the request routes to the registered pattern.
var protectedAdminRoutes = []struct{ method, pattern, path string }{
	// ── Operator HTML pages (admin / adminCSRF) ──────────────────────────────
	{"GET", "/superadmin/", "/superadmin/"},
	{"GET", "/superadmin/accounts", "/superadmin/accounts"},
	{"GET", "/superadmin/accounts/{id}", "/superadmin/accounts/acc123"},
	{"GET", "/superadmin/accounts/{id}/confirm", "/superadmin/accounts/acc123/confirm"},
	{"POST", "/superadmin/accounts/{id}/action", "/superadmin/accounts/acc123/action"},
	{"GET", "/superadmin/reserved-handles", "/superadmin/reserved-handles"},
	{"POST", "/superadmin/reserved-handles", "/superadmin/reserved-handles"},
	{"GET", "/superadmin/reserved-handles/confirm-delete", "/superadmin/reserved-handles/confirm-delete"},
	{"POST", "/superadmin/reserved-handles/delete", "/superadmin/reserved-handles/delete"},
	{"GET", "/superadmin/auditlog", "/superadmin/auditlog"},
	{"GET", "/superadmin/maintenance", "/superadmin/maintenance"},
	{"POST", "/superadmin/maintenance/verify-auditlog", "/superadmin/maintenance/verify-auditlog"},
	{"POST", "/superadmin/maintenance/rotation-check", "/superadmin/maintenance/rotation-check"},
	{"POST", "/superadmin/maintenance/subprocessor-changelog", "/superadmin/maintenance/subprocessor-changelog"},
	{"GET", "/superadmin/analytics", "/superadmin/analytics"},
	{"GET", "/superadmin/orgs", "/superadmin/orgs"},
	{"GET", "/superadmin/migrations", "/superadmin/migrations"},
	{"GET", "/superadmin/relay", "/superadmin/relay"},
	{"GET", "/superadmin/incidents", "/superadmin/incidents"},
	// ── Security telemetry dashboard (RegisterSecurity) ──────────────────────
	{"GET", "/superadmin/security", "/superadmin/security"},
	{"POST", "/superadmin/security/ato/dismiss", "/superadmin/security/ato/dismiss"},
	// ── JSON admin API consumed by the React /console/admin section (apiAdmin) ─
	{"GET", "/api/superadmin/accounts", "/api/superadmin/accounts"},
	{"GET", "/api/superadmin/accounts/{id}", "/api/superadmin/accounts/acc123"},
	{"POST", "/api/superadmin/accounts/{id}/suspend", "/api/superadmin/accounts/acc123/suspend"},
	{"POST", "/api/superadmin/accounts/{id}/unsuspend", "/api/superadmin/accounts/acc123/unsuspend"},
	{"POST", "/api/superadmin/accounts/{id}/2fa-reset", "/api/superadmin/accounts/acc123/2fa-reset"},
	{"GET", "/api/superadmin/reserved-handles", "/api/superadmin/reserved-handles"},
	{"POST", "/api/superadmin/reserved-handles", "/api/superadmin/reserved-handles"},
	{"DELETE", "/api/superadmin/reserved-handles/{handle}", "/api/superadmin/reserved-handles/foo"},
	{"GET", "/api/superadmin/admins", "/api/superadmin/admins"},
	{"POST", "/api/superadmin/admins", "/api/superadmin/admins"},
	{"DELETE", "/api/superadmin/admins/{id}", "/api/superadmin/admins/acc123"},
	{"GET", "/api/superadmin/dashboard", "/api/superadmin/dashboard"},
	{"GET", "/api/superadmin/audit", "/api/superadmin/audit"},
	{"GET", "/api/superadmin/security", "/api/superadmin/security"},
	{"GET", "/api/superadmin/whoami", "/api/superadmin/whoami"},
}

// adminGateMux builds a ServeMux where every protected admin route is a sentinel
// handler wrapped in the REAL RequireSuperAdmin gate — the same wrapping
// wireSuperAdminConsole uses in production. Reaching the sentinel (200 "REACHED")
// means the gate opened.
func adminGateMux(t *testing.T) (*http.ServeMux, *auth.Store, *superadmin.Store) {
	t.Helper()
	authStore, saStore, al := openTestStores(t)
	os.Unsetenv("VULOS_ADMIN_IP_ALLOWLIST") // allow all IPs so the gate turns on the auth layers
	gate := superadmin.RequireSuperAdmin(saStore, authStore, al)
	sentinel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("REACHED"))
	})
	mux := http.NewServeMux()
	for _, r := range protectedAdminRoutes {
		mux.Handle(r.method+" "+r.pattern, gate(sentinel))
	}
	return mux, authStore, saStore
}

func doReq(mux *http.ServeMux, method, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "203.0.113.7:5555"
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// Case A: no session at all → every admin route denied (never 200/REACHED).
func TestRoleSeparation_Anonymous_DeniedEverywhere(t *testing.T) {
	mux, _, _ := adminGateMux(t)
	for _, r := range protectedAdminRoutes {
		rr := doReq(mux, r.method, r.path)
		if rr.Code == http.StatusOK || strings.Contains(rr.Body.String(), "REACHED") {
			t.Errorf("ANON reached admin route %s %s (code %d) — gate failed open", r.method, r.pattern, rr.Code)
		}
		if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusForbidden {
			t.Errorf("ANON %s %s = %d, want 401/403", r.method, r.pattern, rr.Code)
		}
	}
}

// Case B: a NORMAL portal user (valid main session, NOT a super-admin) is
// rejected from every admin route with 403 (not_superadmin) — the core
// role-separation guarantee.
func TestRoleSeparation_PortalUser_DeniedEverywhere(t *testing.T) {
	mux, authStore, _ := adminGateMux(t)
	createUser(t, authStore, "portal-user@test.example", "portal-password-12345")
	res, err := authStore.Login(t.Context(), "portal-user@test.example", "portal-password-12345", "203.0.113.7", "test")
	if err != nil || res == nil || res.Token == "" {
		t.Fatalf("portal user login: %v", err)
	}
	userCookie := &http.Cookie{Name: auth.SessionCookieName, Value: res.Token}

	for _, r := range protectedAdminRoutes {
		rr := doReq(mux, r.method, r.path, userCookie)
		if rr.Code == http.StatusOK || strings.Contains(rr.Body.String(), "REACHED") {
			t.Errorf("PORTAL USER reached admin route %s %s (code %d) — role separation broken", r.method, r.pattern, rr.Code)
		}
		// A signed-in non-admin fails the super-admin status layer → 403.
		if rr.Code != http.StatusForbidden {
			t.Errorf("PORTAL USER %s %s = %d, want 403 (not a super-admin)", r.method, r.pattern, rr.Code)
		}
	}
}

// Case C: a real super-admin WITH a main session but WITHOUT the WebAuthn-backed
// admin-session cookie is still rejected (401) — proving the admin surface
// requires the full gate / second factor, not merely super-admin status.
func TestRoleSeparation_AdminWithoutStepUp_Denied(t *testing.T) {
	mux, authStore, saStore := adminGateMux(t)
	id := createUser(t, authStore, "operator@test.example", "operator-password-98765")
	promoteSuperAdmin(t, authStore, id)
	_ = saStore
	res, err := authStore.Login(t.Context(), "operator@test.example", "operator-password-98765", "203.0.113.7", "test")
	if err != nil || res == nil {
		t.Fatalf("operator login: %v", err)
	}
	mainCookie := &http.Cookie{Name: auth.SessionCookieName, Value: res.Token}

	for _, r := range protectedAdminRoutes {
		rr := doReq(mux, r.method, r.path, mainCookie)
		if rr.Code == http.StatusOK || strings.Contains(rr.Body.String(), "REACHED") {
			t.Errorf("ADMIN-without-stepup reached %s %s (code %d) — WebAuthn admin session not enforced", r.method, r.pattern, rr.Code)
		}
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("ADMIN-without-stepup %s %s = %d, want 401 (admin session required)", r.method, r.pattern, rr.Code)
		}
	}
}

// Case D: a fully-authenticated admin (main session + super-admin status +
// WebAuthn admin session) passes the gate on every route — proving the gate is
// not merely over-blocking and that the FULL gate is exactly what admits an
// operator.
func TestRoleSeparation_FullyAuthedAdmin_Admitted(t *testing.T) {
	mux, authStore, saStore := adminGateMux(t)
	id := createUser(t, authStore, "fulladmin@test.example", "fulladmin-password-54321")
	promoteSuperAdmin(t, authStore, id)
	res, err := authStore.Login(t.Context(), "fulladmin@test.example", "fulladmin-password-54321", "203.0.113.7", "test")
	if err != nil || res == nil {
		t.Fatalf("full admin login: %v", err)
	}
	adminToken, err := saStore.CreateAdminSession(t.Context(), id, "203.0.113.7", "test")
	if err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	mainCookie := &http.Cookie{Name: auth.SessionCookieName, Value: res.Token}
	adminCookie := &http.Cookie{Name: superadmin.AdminSessionCookieName, Value: adminToken}

	for _, r := range protectedAdminRoutes {
		rr := doReq(mux, r.method, r.path, mainCookie, adminCookie)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "REACHED") {
			t.Errorf("FULLY-AUTHED ADMIN blocked from %s %s (code %d) — full gate should admit an operator", r.method, r.pattern, rr.Code)
		}
	}
}
