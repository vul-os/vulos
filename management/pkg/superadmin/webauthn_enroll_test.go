package superadmin_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/superadmin"
)

// enrollBeginHandler wires the enrolment gate around the begin handler exactly as
// the composition root does, and returns a ready-to-serve handler.
func enrollBeginHandler(authStore *auth.Store, saStore *superadmin.Store) http.Handler {
	mw := superadmin.RequireSuperAdminEnroll(saStore, authStore, nil)
	return mw(superadmin.HandleAdminWebAuthnRegisterBegin(saStore, nil))
}

func loginMainToken(t *testing.T, authStore *auth.Store, email, password string) string {
	t.Helper()
	res, err := authStore.Login(context.Background(), email, password, "127.0.0.1", "test")
	if err != nil || res == nil {
		t.Fatalf("login: %v %v", res, err)
	}
	return res.Token
}

// TestAdminWebAuthnEnroll_Gate covers the security contract of the first-passkey
// enrolment endpoints: who may reach them and the bootstrap-only rule.
func TestAdminWebAuthnEnroll_Gate(t *testing.T) {
	os.Unsetenv("VULOS_ADMIN_IP_ALLOWLIST") // allow all
	os.Setenv("ADMIN_WEBAUTHN_RPID", "localhost")
	os.Setenv("ADMIN_WEBAUTHN_ORIGIN", "https://localhost")
	t.Cleanup(func() {
		os.Unsetenv("ADMIN_WEBAUTHN_RPID")
		os.Unsetenv("ADMIN_WEBAUTHN_ORIGIN")
	})

	authStore, saStore, _ := openTestStores(t)
	handler := enrollBeginHandler(authStore, saStore)

	// 1. No main session → 401.
	{
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest("POST", "/api/superadmin/webauthn/register/begin", nil))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("no session: expected 401, got %d", rr.Code)
		}
	}

	// 2. Main session but NOT a super-admin → 403.
	plebID := createUser(t, authStore, "pleb@test.example", "test-password-11111")
	_ = plebID
	{
		tok := loginMainToken(t, authStore, "pleb@test.example", "test-password-11111")
		req := httptest.NewRequest("POST", "/api/superadmin/webauthn/register/begin", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("non-superadmin: expected 403, got %d", rr.Code)
		}
	}

	// 3. Super-admin, WebAuthn configured, zero credentials → 200 CredentialCreation.
	adminID := createUser(t, authStore, "boot@test.example", "test-password-22222")
	promoteSuperAdmin(t, authStore, adminID)
	adminTok := loginMainToken(t, authStore, "boot@test.example", "test-password-22222")
	{
		req := httptest.NewRequest("POST", "/api/superadmin/webauthn/register/begin", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: adminTok})
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("bootstrap begin: expected 200, got %d (%s)", rr.Code, rr.Body.String())
		}
		if !containsAll(rr.Body.String(), "publicKey", "challenge") {
			t.Fatalf("bootstrap begin: missing CredentialCreation fields: %s", rr.Body.String())
		}
	}

	// 4. Bootstrap-only: once an admin credential exists, begin refuses with 409.
	insertFakeAdminCredential(t, authStore, adminID)
	{
		req := httptest.NewRequest("POST", "/api/superadmin/webauthn/register/begin", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: adminTok})
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusConflict {
			t.Fatalf("re-enrol begin: expected 409, got %d (%s)", rr.Code, rr.Body.String())
		}
	}

	// 4b. Finish is likewise bootstrap-gated (409 without ever touching the ceremony).
	{
		finish := superadmin.RequireSuperAdminEnroll(saStore, authStore, nil)(
			superadmin.HandleAdminWebAuthnRegisterFinish(saStore, nil))
		req := httptest.NewRequest("POST", "/api/superadmin/webauthn/register/finish", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: adminTok})
		rr := httptest.NewRecorder()
		finish.ServeHTTP(rr, req)
		if rr.Code != http.StatusConflict {
			t.Fatalf("re-enrol finish: expected 409, got %d (%s)", rr.Code, rr.Body.String())
		}
	}
}

// TestAdminWebAuthnEnroll_NotConfigured returns 503 when the admin RP is unset.
func TestAdminWebAuthnEnroll_NotConfigured(t *testing.T) {
	os.Unsetenv("VULOS_ADMIN_IP_ALLOWLIST")
	os.Unsetenv("ADMIN_WEBAUTHN_RPID")
	os.Unsetenv("ADMIN_WEBAUTHN_ORIGIN")
	os.Unsetenv("WEBAUTHN_RPID")
	os.Unsetenv("WEBAUTHN_ORIGIN")

	authStore, saStore, _ := openTestStores(t)
	handler := enrollBeginHandler(authStore, saStore)

	adminID := createUser(t, authStore, "boot2@test.example", "test-password-33333")
	promoteSuperAdmin(t, authStore, adminID)
	tok := loginMainToken(t, authStore, "boot2@test.example", "test-password-33333")

	req := httptest.NewRequest("POST", "/api/superadmin/webauthn/register/begin", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured: expected 503, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func insertFakeAdminCredential(t *testing.T, authStore *auth.Store, accountID string) {
	t.Helper()
	_, err := authStore.DB().Exec(
		`INSERT INTO admin_webauthn_credentials
		   (credential_id, account_id, public_key, sign_count, transports, attestation_type, created_at)
		 VALUES (?, ?, ?, 0, '[]', '', ?)`,
		"fake-cred-"+accountID, accountID, []byte("fake-pubkey"), time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert fake admin credential: %v", err)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
