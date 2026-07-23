package superadmin_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/superadmin"

	_ "modernc.org/sqlite"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// setupHardening builds the auth + superadmin stores plus an audit logger AND a
// second raw *sql.DB handle onto the SAME in-memory audit DSN so tests can read
// the audit entries written by the handlers.
func setupHardening(t *testing.T) (*auth.Store, *superadmin.Store, *auditlog.Logger, *sql.DB) {
	t.Helper()
	os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	os.Setenv("VULOS_DEV", "true")

	authCPDB, err := cpdb.OpenSQLiteDSN(uniqueDSN("auth"))
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	authStore, err := auth.OpenAuthStore(authCPDB, []byte("test-secret"))
	if err != nil {
		_ = authCPDB.Close()
		t.Fatalf("open auth store: %v", err)
	}
	t.Cleanup(func() { authStore.Close() })

	saStore, err := superadmin.New(authStore.CPDB())
	if err != nil {
		t.Fatalf("open superadmin store: %v", err)
	}

	auditDSN := uniqueDSN("alh")
	aldb, err := cpdb.OpenSQLiteDSN(auditDSN)
	if err != nil {
		t.Fatalf("open cpdb: %v", err)
	}
	al, err := auditlog.Open(aldb)
	if err != nil {
		t.Fatalf("open auditlog: %v", err)
	}
	t.Cleanup(func() { al.Close() })

	raw, err := sql.Open("sqlite", auditDSN)
	if err != nil {
		t.Fatalf("open raw audit db: %v", err)
	}
	t.Cleanup(func() { raw.Close() })

	return authStore, saStore, al, raw
}

func auditCount(t *testing.T, db *sql.DB, action string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM auditlog_entries WHERE action = ?`, action).Scan(&n); err != nil {
		t.Fatalf("audit count %q: %v", action, err)
	}
	return n
}

func cookieValue(rr *httptest.ResponseRecorder, name string) string {
	for _, c := range rr.Result().Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func adminCtx(r *http.Request, accountID string) *http.Request {
	ctx := context.WithValue(r.Context(), superadmin.ExportedCtxAdminAccountID, accountID)
	return r.WithContext(ctx)
}

// ─────────────────────────────────────────────────────────────────────────────
// Security headers / CSP
// ─────────────────────────────────────────────────────────────────────────────

func TestSecurityHeaders_Present(t *testing.T) {
	h := superadmin.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/superadmin/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	want := map[string]string{
		"Content-Security-Policy":    "default-src 'none'",
		"X-Frame-Options":            "DENY",
		"X-Content-Type-Options":     "nosniff",
		"Referrer-Policy":            "no-referrer",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Cache-Control":              "no-store",
	}
	for k, substr := range want {
		got := rr.Header().Get(k)
		if !strings.Contains(got, substr) {
			t.Errorf("header %s = %q, want to contain %q", k, got, substr)
		}
	}
	csp := rr.Header().Get("Content-Security-Policy")
	for _, frag := range []string{"script-src 'self'", "style-src 'self'", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, frag) {
			t.Errorf("CSP missing %q (got %q)", frag, csp)
		}
	}
	// The CSP must NOT permit inline scripts/styles.
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP allows unsafe-inline: %q", csp)
	}
}

// TestRenderedPages_NoInlineStyleOrScript verifies the templates reference the
// external stylesheet and contain no inline <style> blocks or inline event
// handlers (which would violate the strict CSP).
func TestRenderedPages_NoInlineStyleOrScript(t *testing.T) {
	authStore, saStore, al, _ := setupHardening(t)
	pages := superadmin.NewPages(saStore, al, authStore)

	req := httptest.NewRequest("GET", "/superadmin/", nil)
	rr := httptest.NewRecorder()
	pages.Dashboard(rr, req)
	body := rr.Body.String()

	if !strings.Contains(body, `<link rel="stylesheet" href="/superadmin/admin.css">`) {
		t.Error("dashboard missing external stylesheet link")
	}
	if strings.Contains(body, "<style") {
		t.Error("dashboard contains an inline <style> block")
	}
	if strings.Contains(body, "onclick=") || strings.Contains(body, "onsubmit=") {
		t.Error("dashboard contains inline event handler attributes")
	}
}

func TestAdminCSS_Served(t *testing.T) {
	authStore, saStore, al, _ := setupHardening(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	rr := httptest.NewRecorder()
	pages.AdminCSS().ServeHTTP(rr, httptest.NewRequest("GET", "/superadmin/admin.css", nil))
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Errorf("admin.css Content-Type = %q", ct)
	}
	if !strings.Contains(rr.Body.String(), "--bg:") {
		t.Error("admin.css body missing CSS custom properties")
	}
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("admin.css Cache-Control = %q, want long cache", cc)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CSRF
// ─────────────────────────────────────────────────────────────────────────────

func TestCSRF_RejectsMissingAndWrong_AcceptsValid(t *testing.T) {
	_, _, al, _ := setupHardening(t)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := superadmin.CSRFProtect(al)(ok)
	const token = "this-is-a-valid-csrf-token-0001"

	// 1. Missing token + cookie → 403.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/superadmin/x", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("missing token: got %d, want 403", rr.Code)
	}

	// 2. Cookie present but form token wrong → 403.
	form := url.Values{superadmin.CSRFFieldName: {"wrong-token-value-xxxxxxxxxx"}}
	req := httptest.NewRequest("POST", "/superadmin/x", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: superadmin.CSRFCookieName, Value: token})
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("wrong token: got %d, want 403", rr.Code)
	}

	// 3. Matching cookie + form token → 200.
	form = url.Values{superadmin.CSRFFieldName: {token}}
	req = httptest.NewRequest("POST", "/superadmin/x", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: superadmin.CSRFCookieName, Value: token})
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid token: got %d, want 200", rr.Code)
	}

	// A GET is never blocked.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/superadmin/x", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET: got %d, want 200", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Login rate-limit / lockout
// ─────────────────────────────────────────────────────────────────────────────

func TestLogin_LockoutAfterFailures(t *testing.T) {
	authStore, saStore, al, raw := setupHardening(t)
	pages := superadmin.NewPages(saStore, al, authStore)

	post := func() *httptest.ResponseRecorder {
		form := url.Values{
			"email":    {"nobody@test.example"},
			"password": {"wrong-password-123"},
			"totp":     {"000000"},
		}
		req := httptest.NewRequest("POST", "/superadmin/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "203.0.113.7:5555"
		rr := httptest.NewRecorder()
		pages.LoginPost(rr, req)
		return rr
	}

	// First 5 failures: rendered generic error (200), not yet locked.
	for i := 0; i < 5; i++ {
		rr := post()
		if rr.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d: locked too early", i+1)
		}
	}
	// 6th attempt: locked out → 429.
	rr := post()
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("after threshold: got %d, want 429", rr.Code)
	}
	if n := auditCount(t, raw, "admin.login.locked"); n < 1 {
		t.Errorf("expected at least one admin.login.locked audit entry, got %d", n)
	}
}

func TestLogin_GenericError_NoEnumeration(t *testing.T) {
	authStore, saStore, al, _ := setupHardening(t)
	pages := superadmin.NewPages(saStore, al, authStore)

	form := url.Values{"email": {"ghost@test.example"}, "password": {"x"}, "totp": {"1"}}
	req := httptest.NewRequest("POST", "/superadmin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	pages.LoginPost(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "Invalid credentials") {
		t.Errorf("expected generic error, got body: %s", body)
	}
	for _, leak := range []string{"super-admin", "Not a super", "TOTP not configured", "no admin WebAuthn"} {
		if strings.Contains(body, leak) {
			t.Errorf("login error leaks %q", leak)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Destructive action confirm + step-up flow
// ─────────────────────────────────────────────────────────────────────────────

func TestDestructiveConfirm_SuspendFlow(t *testing.T) {
	authStore, saStore, al, raw := setupHardening(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	pages.SetInboxSender(&mockInboxSender{})

	operator := createUser(t, authStore, "operator@test.example", "password-secure-xx1")
	promoteSuperAdmin(t, authStore, operator)
	target := createUser(t, authStore, "victim@test.example", "password-secure-xx2")

	inject := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, adminCtx(r, operator))
		})
	}
	csrf := superadmin.CSRFProtect(al)
	mux := http.NewServeMux()
	mux.Handle("GET /superadmin/accounts/{id}/confirm", inject(http.HandlerFunc(pages.AccountActionConfirm)))
	mux.Handle("POST /superadmin/accounts/{id}/action", inject(csrf(http.HandlerFunc(pages.AccountActionExecute))))

	// 1. GET the confirm page: describes the action, sets a CSRF cookie, audits.
	creq := httptest.NewRequest("GET", "/superadmin/accounts/"+target+"/confirm?action=suspend", nil)
	crr := httptest.NewRecorder()
	mux.ServeHTTP(crr, creq)
	if crr.Code != http.StatusOK {
		t.Fatalf("confirm GET: got %d", crr.Code)
	}
	if !strings.Contains(crr.Body.String(), "Suspend account") {
		t.Errorf("confirm page missing description; body=%s", crr.Body.String())
	}
	token := cookieValue(crr, superadmin.CSRFCookieName)
	if token == "" {
		t.Fatal("confirm GET did not set a CSRF cookie")
	}
	if n := auditCount(t, raw, "admin.action.confirm_view"); n < 1 {
		t.Errorf("expected confirm_view audit entry, got %d", n)
	}

	// 2a. Execute WITHOUT CSRF token → 403, account stays active.
	bad := httptest.NewRequest("POST", "/superadmin/accounts/"+target+"/action",
		strings.NewReader(url.Values{"action": {"suspend"}}.Encode()))
	bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	brr := httptest.NewRecorder()
	mux.ServeHTTP(brr, bad)
	if brr.Code != http.StatusForbidden {
		t.Fatalf("execute without CSRF: got %d, want 403", brr.Code)
	}
	if d, _ := saStore.GetAccountDetail(context.Background(), target); d.Suspended {
		t.Fatal("account suspended despite missing CSRF token")
	}

	// 2b. Execute WITH valid CSRF token → redirect + account suspended.
	form := url.Values{"action": {"suspend"}, superadmin.CSRFFieldName: {token}}
	ereq := httptest.NewRequest("POST", "/superadmin/accounts/"+target+"/action", strings.NewReader(form.Encode()))
	ereq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ereq.AddCookie(&http.Cookie{Name: superadmin.CSRFCookieName, Value: token})
	err := httptest.NewRecorder()
	mux.ServeHTTP(err, ereq)
	if err.Code != http.StatusSeeOther {
		t.Fatalf("execute with CSRF: got %d, want 303", err.Code)
	}
	d, _ := saStore.GetAccountDetail(context.Background(), target)
	if !d.Suspended {
		t.Fatal("account not suspended after confirmed execute")
	}
	if n := auditCount(t, raw, "admin.account.suspend"); n < 1 {
		t.Errorf("expected admin.account.suspend audit entry, got %d", n)
	}
}

// TestStepUpTOTP_RequiredWhenEnabled verifies that with VULOS_ADMIN_REAUTH_TOTP=1
// a destructive execute without a valid TOTP code is rejected (redirected with an
// error) and the action does not take effect.
func TestStepUpTOTP_RequiredWhenEnabled(t *testing.T) {
	authStore, saStore, al, raw := setupHardening(t)
	os.Setenv("VULOS_ADMIN_REAUTH_TOTP", "1")
	t.Cleanup(func() { os.Unsetenv("VULOS_ADMIN_REAUTH_TOTP") })

	pages := superadmin.NewPages(saStore, al, authStore)
	operator := createUser(t, authStore, "op2@test.example", "password-secure-xx3")
	promoteSuperAdmin(t, authStore, operator)
	target := createUser(t, authStore, "victim2@test.example", "password-secure-xx4")

	const token = "csrf-token-for-stepup-test-001"
	form := url.Values{"action": {"suspend"}, superadmin.CSRFFieldName: {token}} // no totp
	req := httptest.NewRequest("POST", "/x", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: superadmin.CSRFCookieName, Value: token})
	req.SetPathValue("id", target)
	req = adminCtx(req, operator)

	rr := httptest.NewRecorder()
	superadmin.CSRFProtect(al)(http.HandlerFunc(pages.AccountActionExecute)).ServeHTTP(rr, req)

	if d, _ := saStore.GetAccountDetail(context.Background(), target); d.Suspended {
		t.Fatal("account suspended despite missing step-up TOTP")
	}
	if n := auditCount(t, raw, "admin.action.stepup_failed"); n < 1 {
		t.Errorf("expected stepup_failed audit entry, got %d", n)
	}
}

// TestAdminSessionAbsoluteTTL confirms the absolute-TTL constant is tight.
func TestAdminSessionAbsoluteTTL(t *testing.T) {
	if superadmin.AdminSessionAbsoluteTTL > 8*60*60*1e9 {
		t.Errorf("admin session absolute TTL too long: %v", superadmin.AdminSessionAbsoluteTTL)
	}
	if superadmin.AdminSessionIdleTimeout > 30*60*1e9 {
		t.Errorf("admin session idle timeout too long: %v", superadmin.AdminSessionIdleTimeout)
	}
}
