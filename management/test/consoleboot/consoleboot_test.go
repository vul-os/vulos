package consoleboot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cproutes"
	"github.com/vul-os/vulos-management/pkg/cpserver"
)

// TestMain forces the self-host (local) profile and enables the OPERATOR console
// so this whole binary boots the console-enabled control plane exactly once.
func TestMain(m *testing.M) {
	_ = os.Setenv("VULOS_ENV", "local")
	_ = os.Setenv("VULOS_DEV", "true")
	_ = os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	_ = os.Unsetenv("VULOS_ADMIN_IP_ALLOWLIST") // dev → allow-all IP layer
	// Opt the operator console IN (this is what flips the admin gate from
	// mounted-but-deny-all to a live, store-backed gate).
	_ = os.Setenv("VULOS_ENABLE_SUPERADMIN", "1")
	_ = os.Setenv("VULOS_DB_DIR", mustTempDir())
	os.Exit(m.Run())
}

func mustTempDir() string {
	d, err := os.MkdirTemp("", "consoleboot-*")
	if err != nil {
		panic(err)
	}
	return d
}

// bootConsoleServer assembles the self-host control plane with the SPA fallback
// registrar — the same wiring cmd/server uses — and the console enabled via env.
func bootConsoleServer(t *testing.T) *cpserver.Server {
	t.Helper()
	srv, err := cpserver.New(cpserver.Config{
		Environment: "local",
		Version:     "consoleboot-test",
		DBDir:       os.Getenv("VULOS_DB_DIR"),
	}, cpserver.Deps{
		Routes: []cpserver.RouteRegistrar{
			func(mux *http.ServeMux, _ *cpserver.Runtime) error {
				cproutes.RegisterSPAFallback(mux)
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("cpserver.New (console-enabled self-host): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func req(h http.Handler, method, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "192.0.2.10:4444"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}

// TestL1_SecurityDashboardUsesRealGate proves the L1 fix: with the console
// enabled, the security telemetry dashboard is gated by the REAL admin
// middleware (built from the registered super-admin store), NOT the dead deny-all
// fallback. A request with no session therefore hits the real gate's session
// layer → 401, and the response is NOT the "superadmin not configured" 403 the
// dead gate emits. Before the ordering fix, WireSecurityRequireAdmin snapshotted
// a nil singleton and every request was dead-denied.
func TestL1_SecurityDashboardUsesRealGate(t *testing.T) {
	h := bootConsoleServer(t).Handler()

	rr := req(h, http.MethodGet, "/superadmin/security")
	if strings.Contains(rr.Body.String(), "superadmin not configured") {
		t.Fatalf("security dashboard is DEAD-GATED (deny-all) — L1 ordering regression; body=%s", rr.Body.String())
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET /superadmin/security (no session) = %d, want 401 from the real gate (L1); body=%s", rr.Code, rr.Body.String())
	}
}

// TestConsoleBoot_HealthReportsRails confirms the console-enabled self-host still
// boots clean and reports the free rails (M2 observability).
func TestConsoleBoot_HealthReportsRails(t *testing.T) {
	h := bootConsoleServer(t).Handler()
	rr := req(h, http.MethodGet, "/healthz")
	if rr.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", rr.Code)
	}
	for _, want := range []string{`"billing_rail":"noop"`, `"entitlements_rail":"noop"`} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Errorf("/healthz missing %s; body=%s", want, rr.Body.String())
		}
	}
}

// portalUserCookie signs up a normal account through the runtime auth store and
// returns its live main-session cookie — a genuine portal USER (not an admin).
func portalUserCookie(t *testing.T, srv *cpserver.Server) *http.Cookie {
	t.Helper()
	rt := srv.Runtime()
	ctx := context.Background()
	email := fmt.Sprintf("portal-%d@example.test", time.Now().UnixNano())
	const pw = "portal-user-password-123456"
	if _, _, err := rt.AuthStore.Signup(ctx, email, pw, "192.0.2.10", "test"); err != nil {
		t.Fatalf("signup portal user: %v", err)
	}
	res, err := rt.AuthStore.Login(ctx, email, pw, "192.0.2.10", "test")
	if err != nil || res == nil || res.Token == "" {
		t.Fatalf("login portal user: %v", err)
	}
	return &http.Cookie{Name: auth.SessionCookieName, Value: res.Token}
}

// TestRoleSeparation_PortalUserDeniedOverHTTP proves, over the real wired HTTP
// surface, that a signed-in PORTAL USER is rejected from every representative
// admin route (/superadmin/* and /api/superadmin/*) — never a 2xx — while the
// public operator login page stays reachable (separation is enforced at the
// gate, not by hiding the door).
func TestRoleSeparation_PortalUserDeniedOverHTTP(t *testing.T) {
	srv := bootConsoleServer(t)
	h := srv.Handler()
	user := portalUserCookie(t, srv)

	protected := []struct{ method, path string }{
		{http.MethodGet, "/superadmin/"},
		{http.MethodGet, "/superadmin/accounts"},
		{http.MethodGet, "/superadmin/auditlog"},
		{http.MethodGet, "/superadmin/security"},
		{http.MethodGet, "/superadmin/ddos"},
		{http.MethodGet, "/api/superadmin/accounts"},
		{http.MethodGet, "/api/superadmin/admins"},
		{http.MethodGet, "/api/superadmin/dashboard"},
		{http.MethodGet, "/api/superadmin/relay-scale"},
		{http.MethodGet, "/api/superadmin/whoami"},
		{http.MethodPost, "/api/superadmin/admins"},
		{http.MethodDelete, "/api/superadmin/admins/some-id"},
	}
	for _, r := range protected {
		rr := req(h, r.method, r.path, user)
		if rr.Code >= 200 && rr.Code < 300 {
			t.Errorf("PORTAL USER got %d (2xx) on %s %s — role separation broken; body=%s", rr.Code, r.method, r.path, rr.Body.String())
		}
		if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusForbidden {
			t.Errorf("PORTAL USER %s %s = %d, want 401/403", r.method, r.path, rr.Code)
		}
	}

	// The PUBLIC operator login page is reachable (a portal user can see the door;
	// it just cannot open it). Confirms the denials above are the gate, not a blanket block.
	if rr := req(h, http.MethodGet, "/superadmin/login", user); rr.Code != http.StatusOK {
		t.Errorf("GET /superadmin/login = %d, want 200 (public operator login page)", rr.Code)
	}
}
