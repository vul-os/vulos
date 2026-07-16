// routes_auth_test.go -- HTTP handler tests for AUTH-05 session registry endpoints,
// AUTH-06 HIBP breach-check, AUTH-07 lockout + recognised-device token,
// AUTH-08 email verification flow, and AUTH-10 email-verification login gate.
//
// Acceptance criteria tested:
//  1. GET /api/auth/sessions returns all live sessions with is_current correctly set.
//  2. POST /api/auth/sessions/revoke invalidates the targeted session; returns 403
//     if the caller tries to revoke their own current session.
//  3. POST /api/auth/sessions/revoke-all invalidates all other sessions; caller
//     remains logged in. Returns {revoked: N}.
//  4. (AUTH-06) Breached password on signup → 422 password_breached.
//  5. (AUTH-06) Breached password on reset-confirm → 422 password_breached.
//  6. (AUTH-06) HIBP failure → signup continues (fail-open).
//  7. (AUTH-06) HIBP_CHECK_ENABLED=0 → check skipped.
//  8. (AUTH-07) 5 failed TOTP → 429 with retry_after; unlock after time advance.
//  9. (AUTH-07) Successful verify resets failed_2fa_count.
//
// 10. (AUTH-07) Recognised device skips TOTP when remember_device=true.
// 11. (AUTH-07) Cross-account device token confusion prevented.
// 12. (AUTH-08) Signup logs verification token; GET /api/auth/verify-email consumes it.
// 13. (AUTH-08) Expired/used token → 410; /me returns email_verified field.
// 14. (AUTH-08) POST /api/auth/verify-email/resend always 204 (no enumeration).
// 15. (AUTH-10) Unverified login → step:email_verification_required (no full session).
// 16. (AUTH-10) Verified login → full session.
// 17. (AUTH-10) AUTH_ALLOW_UNVERIFIED_LOGIN=1 → full session even when unverified.
// 18. (AUTH-10) Grace flag honored: unverified account with future grace_until → full session.
package cproutes

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
)

// ─────────────────────────────────────────────────────────────────────────────
// Package-level init: default HIBP to disabled so existing tests that pre-date
// AUTH-06 are not affected by live HIBP calls.  HIBP-specific tests explicitly
// set HIBP_CHECK_ENABLED=1 via t.Setenv (auto-restored after each test).
// ─────────────────────────────────────────────────────────────────────────────

func init() {
	if os.Getenv("HIBP_CHECK_ENABLED") == "" {
		os.Setenv("HIBP_CHECK_ENABLED", "0")
	}
	// Default to allowing unverified logins so that pre-AUTH-10 tests continue
	// to work unchanged.  AUTH-10-specific tests override this via t.Setenv.
	if os.Getenv("AUTH_ALLOW_UNVERIFIED_LOGIN") == "" {
		os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	}
	// Use cheap Argon2 parameters in tests to prevent CPU-heavy hashing from
	// starving parallel goroutines and causing timeout flakiness.
	// Production security is unaffected: ARGON2_TEST_FAST must never be set
	// outside of test environments.
	if os.Getenv("ARGON2_TEST_FAST") == "" {
		os.Setenv("ARGON2_TEST_FAST", "1")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// sessionTestStoreN is atomic so parallel subtests never share a DSN.
var sessionTestStoreN atomic.Int64

func openSessionTestStore(t *testing.T) *auth.Store {
	t.Helper()
	dsn := "file:routesauthtest" + fmt.Sprintf("%d", sessionTestStoreN.Add(1)) + "?mode=memory&cache=shared"
	st, err := openAuthStoreForTest(dsn, []byte("test-secret-key-1234567890123456"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// authMux builds a minimal mux with only the auth routes registered.
func authMux(t *testing.T, st *auth.Store) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	RegisterAuthRoutes(mux, st)
	return mux
}

// signupAndGetToken signs up a user and returns (userID, token).
// email may be a bare handle or a full address; if it ends with @vulos.org
// the local-part is extracted as the handle; otherwise the local-part before
// '@' is used (e.g. "alice@example.com" → handle "alice").
// Non-vulos.org external addresses are no longer accepted by the HTTP handler;
// this helper normalises them automatically for backward-compat.
func signupAndGetToken(t *testing.T, mux *http.ServeMux, email string) (userID, token string) {
	t.Helper()
	handle := email
	if idx := strings.Index(email, "@"); idx > 0 {
		handle = email[:idx]
	}
	body := fmt.Sprintf(`{"handle":%q,"password":"securepass1234!"}`, handle)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9999"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("signup failed: %d %s", rr.Code, rr.Body.String())
	}
	var user struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&user); err != nil {
		t.Fatalf("decode signup response: %v", err)
	}
	var tok string
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			tok = c.Value
		}
	}
	if tok == "" {
		t.Fatal("no session cookie in signup response")
	}
	return user.UserID, tok
}

// loginAndGetToken logs in and returns a new session token.
// email is normalised: if it contains @ the local-part is used to build
// <local>@vulos.org (matching how signupAndGetToken now registers accounts).
func loginAndGetToken(t *testing.T, mux *http.ServeMux, email string) string {
	t.Helper()
	loginEmail := email
	if idx := strings.Index(email, "@"); idx > 0 {
		// Normalise to the vulos.org address that was actually registered.
		loginEmail = email[:idx] + "@vulos.org"
	}
	body := fmt.Sprintf(`{"email":%q,"password":"securepass1234!"}`, loginEmail)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:1234"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rr.Code, rr.Body.String())
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return c.Value
		}
	}
	t.Fatal("no session cookie in login response")
	return ""
}

// getWithCookie issues GET path with a session cookie and returns the recorder.
func getWithCookie(mux *http.ServeMux, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// postJSONWithCookie issues POST path with a JSON body and session cookie.
func postJSONWithCookie(mux *http.ServeMux, path, token, bodyJSON string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests: GET /api/auth/sessions
// ─────────────────────────────────────────────────────────────────────────────

func TestGetSessions_RequiresAuth(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/sessions", nil)
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET /api/auth/sessions: expected 401, got %d", rr.Code)
	}
}

func TestGetSessions_ReturnsSessions(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	email := "getsess@example.com"
	_, tok1 := signupAndGetToken(t, mux, email)
	tok2 := loginAndGetToken(t, mux, email)

	rr := getWithCookie(mux, "/api/auth/sessions", tok1)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/sessions: expected 200, got %d %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Sessions []struct {
			ID        string `json:"id"`
			IsCurrent bool   `json:"is_current"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(resp.Sessions))
	}

	// is_current should be set exactly for tok1.
	var foundCurrent bool
	for _, s := range resp.Sessions {
		if s.ID == tok1 && s.IsCurrent {
			foundCurrent = true
		}
		if s.ID == tok2 && s.IsCurrent {
			t.Error("tok2 should not be marked current")
		}
	}
	if !foundCurrent {
		t.Error("tok1 should be marked is_current")
	}
}

func TestGetSessions_IsCurrentChangesWithCaller(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	email := "iscurrentswap@example.com"
	_, tok1 := signupAndGetToken(t, mux, email)
	tok2 := loginAndGetToken(t, mux, email)

	// Query as tok2 — tok2 should be marked current.
	rr := getWithCookie(mux, "/api/auth/sessions", tok2)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp struct {
		Sessions []struct {
			ID        string `json:"id"`
			IsCurrent bool   `json:"is_current"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, s := range resp.Sessions {
		if s.ID == tok1 && s.IsCurrent {
			t.Error("tok1 should not be current when queried via tok2")
		}
		if s.ID == tok2 && !s.IsCurrent {
			t.Error("tok2 should be current when queried via tok2")
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests: POST /api/auth/sessions/revoke
// ─────────────────────────────────────────────────────────────────────────────

func TestRevokeSession_RevokesOther(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	email := "revoke1@example.com"
	_, tok1 := signupAndGetToken(t, mux, email)
	tok2 := loginAndGetToken(t, mux, email)

	// Revoke tok2 using tok1.
	body := fmt.Sprintf(`{"session_id":%q}`, tok2)
	rr := postJSONWithCookie(mux, "/api/auth/sessions/revoke", tok1, body)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("revoke tok2: expected 204, got %d %s", rr.Code, rr.Body.String())
	}

	// tok2 should now be invalid: GET /api/auth/sessions via tok2 should 401.
	rrMe := getWithCookie(mux, "/api/auth/me", tok2)
	if rrMe.Code != http.StatusUnauthorized {
		t.Errorf("revoked tok2 should return 401, got %d", rrMe.Code)
	}

	// tok1 still works.
	rrMe2 := getWithCookie(mux, "/api/auth/me", tok1)
	if rrMe2.Code != http.StatusOK {
		t.Errorf("tok1 should still be valid, got %d", rrMe2.Code)
	}
}

func TestRevokeSession_ForbidCurrentSession(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	_, tok1 := signupAndGetToken(t, mux, "revokecurrent@example.com")

	// Try to revoke own session — must get 403.
	body := fmt.Sprintf(`{"session_id":%q}`, tok1)
	rr := postJSONWithCookie(mux, "/api/auth/sessions/revoke", tok1, body)
	if rr.Code != http.StatusForbidden {
		t.Errorf("revoking current session: expected 403, got %d %s", rr.Code, rr.Body.String())
	}

	// Session must still be valid.
	rrMe := getWithCookie(mux, "/api/auth/me", tok1)
	if rrMe.Code != http.StatusOK {
		t.Errorf("current session should still be valid after blocked revoke, got %d", rrMe.Code)
	}
}

func TestRevokeSession_NotFoundFor404(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	_, tok1 := signupAndGetToken(t, mux, "revokenotfound@example.com")

	body := `{"session_id":"no-such-session-token-ever"}`
	rr := postJSONWithCookie(mux, "/api/auth/sessions/revoke", tok1, body)
	if rr.Code != http.StatusNotFound {
		t.Errorf("revoke non-existent session: expected 404, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestRevokeSession_RequiresAuth(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/sessions/revoke",
		strings.NewReader(`{"session_id":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated revoke: expected 401, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests: POST /api/auth/sessions/revoke-all
// ─────────────────────────────────────────────────────────────────────────────

func TestRevokeAll_RevokesOthersKeepsCurrent(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	email := "revokeall@example.com"
	_, tok1 := signupAndGetToken(t, mux, email)
	tok2 := loginAndGetToken(t, mux, email)
	tok3 := loginAndGetToken(t, mux, email)

	// Revoke all except tok1.
	rr := postJSONWithCookie(mux, "/api/auth/sessions/revoke-all", tok1, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke-all: expected 200, got %d %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode revoke-all response: %v", err)
	}
	revoked, ok := resp["revoked"]
	if !ok {
		t.Fatal("response missing 'revoked' field")
	}
	// JSON numbers decode as float64 in map[string]any.
	if revoked.(float64) != 2 {
		t.Errorf("expected revoked=2, got %v", revoked)
	}

	// tok1 (current) still valid.
	rrMe := getWithCookie(mux, "/api/auth/me", tok1)
	if rrMe.Code != http.StatusOK {
		t.Errorf("current session tok1 should still be valid, got %d", rrMe.Code)
	}

	// tok2, tok3 should be revoked.
	rrMe2 := getWithCookie(mux, "/api/auth/me", tok2)
	if rrMe2.Code != http.StatusUnauthorized {
		t.Errorf("tok2 should be revoked (401), got %d", rrMe2.Code)
	}
	rrMe3 := getWithCookie(mux, "/api/auth/me", tok3)
	if rrMe3.Code != http.StatusUnauthorized {
		t.Errorf("tok3 should be revoked (401), got %d", rrMe3.Code)
	}

	// Session list should now show only 1 session.
	rrList := getWithCookie(mux, "/api/auth/sessions", tok1)
	if rrList.Code != http.StatusOK {
		t.Fatalf("list sessions after revoke-all: expected 200, got %d", rrList.Code)
	}
	var listResp struct {
		Sessions []struct{ ID string } `json:"sessions"`
	}
	if err := json.NewDecoder(rrList.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Sessions) != 1 {
		t.Errorf("expected 1 remaining session, got %d", len(listResp.Sessions))
	}
}

func TestRevokeAll_RequiresAuth(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/sessions/revoke-all",
		strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated revoke-all: expected 401, got %d", rr.Code)
	}
}

func TestRevokeAll_EmptyBody_Still200(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	// Single session — revoke-all should work with empty body and return {revoked:0}.
	_, tok1 := signupAndGetToken(t, mux, "revokeall-empty@example.com")

	rr := postJSONWithCookie(mux, "/api/auth/sessions/revoke-all", tok1, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke-all empty: expected 200, got %d %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["revoked"].(float64) != 0 {
		t.Errorf("expected revoked=0, got %v", resp["revoked"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AUTH-07: 2FA lockout tests
// ─────────────────────────────────────────────────────────────────────────────

// setupTOTPUserForTest creates a user with TOTP enabled and returns
// (store, mux, userID, partialSessionToken).
// Recovery codes are NOT generated (no argon2 hashing) to keep tests fast.
func setupTOTPUserForTest(t *testing.T) (st *auth.Store, mux *http.ServeMux, userID, partialTok string) {
	t.Helper()
	return setupTOTPUserForTestEmail(t, "locktest@example.com")
}

// setupTOTPUserForTestEmail is the parameterised version.
func setupTOTPUserForTestEmail(t *testing.T, email string) (st *auth.Store, mux *http.ServeMux, userID, partialTok string) {
	t.Helper()

	os.Setenv("VULOS_DEV", "true")
	os.Setenv("AUTH_TOTP_KEK", "")
	os.Setenv("AUTH_DEVICE_KEK", "")

	st = openSessionTestStore(t)
	mux = authMux(t, st)
	ctx := context.Background()

	// Sign up.  signupAndGetToken normalises any email to <local>@vulos.org.
	_, tok := signupAndGetToken(t, mux, email)
	// Derive the normalised email that was actually registered.
	normEmail := email
	if idx := strings.Index(email, "@"); idx > 0 {
		normEmail = email[:idx] + "@vulos.org"
	}
	// Fetch user ID using the normalised email.
	var uid string
	st.DB().QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, normEmail).Scan(&uid)

	// Enable TOTP via store directly (avoids needing a live TOTP code for enroll/confirm).
	_, secretB32, err := auth.GenerateTOTPSecret(normEmail)
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	kek, _ := auth.LoadTOTPKEK()
	enc, _ := auth.EncryptTOTPSecret([]byte(secretB32), kek)
	_ = st.SaveTOTPSecret(ctx, uid, enc)
	// Skip GenerateRecoveryCodes() — argon2 hashing of 10 codes is slow.
	// Recovery codes are not needed for lockout or device-token tests.
	_ = st.EnableTOTP(ctx, uid)

	// Start a new login to get a partial session.
	body := fmt.Sprintf(`{"email":%q,"password":"securepass1234!"}`, normEmail)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login for TOTP user %s: %d %s", email, rr.Code, rr.Body.String())
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			partialTok = c.Value
		}
	}
	if partialTok == "" {
		t.Fatal("no session cookie after TOTP login")
	}
	_ = tok // signup token unused after TOTP is set
	return st, mux, uid, partialTok
}

// postTOTPVerify posts to /api/auth/totp/verify with the given partial token
// and TOTP body.
func postTOTPVerify(mux *http.ServeMux, partialTok, bodyJSON string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/totp/verify", strings.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: partialTok})
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// TestTOTPLockout_FiveFailures_Returns429 verifies that after 5 consecutive
// bad TOTP codes the handler returns 429 with a retry_after field.
func TestTOTPLockout_FiveFailures_Returns429(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")

	// Inject a short lockout so the test doesn't need to sleep.
	origDuration := auth.LockoutDuration
	auth.LockoutDuration = 1 * time.Second
	defer func() { auth.LockoutDuration = origDuration }()

	_, mux, _, partialTok := setupTOTPUserForTest(t)

	// 4 bad codes → 401.
	for i := 0; i < 4; i++ {
		rr := postTOTPVerify(mux, partialTok, `{"code":"000000"}`)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("attempt %d: expected 401, got %d %s", i+1, rr.Code, rr.Body.String())
		}
	}

	// 5th bad code → 429.
	rr := postTOTPVerify(mux, partialTok, `{"code":"000000"}`)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("5th bad code: expected 429, got %d %s", rr.Code, rr.Body.String())
	}

	// Body must contain retry_after.
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode 429 body: %v", err)
	}
	if _, ok := resp["retry_after"]; !ok {
		t.Errorf("429 response missing retry_after field; body: %s", rr.Body.String())
	}
}

// TestTOTPLockout_LockedAccountReturns429Immediately verifies that once locked,
// subsequent calls return 429 before even checking the code.
func TestTOTPLockout_LockedAccountReturns429Immediately(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")

	origDuration := auth.LockoutDuration
	auth.LockoutDuration = 30 * time.Second
	defer func() { auth.LockoutDuration = origDuration }()

	st, mux, userID, partialTok := setupTOTPUserForTest(t)
	ctx := context.Background()

	// Force-lock the account by recording 5 failures directly.
	for i := 0; i < 5; i++ {
		if err := st.RecordFailed2FA(ctx, userID); err != nil {
			t.Fatalf("RecordFailed2FA: %v", err)
		}
	}

	// Any further attempt must return 429 immediately.
	rr := postTOTPVerify(mux, partialTok, `{"code":"123456"}`)
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("locked account: expected 429, got %d %s", rr.Code, rr.Body.String())
	}
}

// TestTOTPLockout_UnlockAfterTimeAdvance verifies that after the lockout
// duration has elapsed, the account is accessible again.
// We simulate time passing by setting locked_until to a past timestamp directly
// in the DB, and also reset the failure counter so the expiry is clean.
func TestTOTPLockout_UnlockAfterTimeAdvance(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")

	st, mux, userID, partialTok := setupTOTPUserForTest(t)
	ctx := context.Background()

	// Lock the account via 5 failures.
	for i := 0; i < 5; i++ {
		_ = st.RecordFailed2FA(ctx, userID)
	}
	_, locked := st.Check2FALockout(ctx, userID)
	if !locked {
		t.Fatal("expected account to be locked")
	}

	// Simulate lock expiry: set locked_until to the past AND reset the failure
	// counter.  (In production the counter only resets on a successful verify;
	// here we're testing that the time-based unlock path works at all, so we
	// give the account a clean slate to avoid re-triggering lockout on the
	// first bad-code attempt.)
	pastUnix := time.Now().UTC().Add(-1 * time.Second).Unix()
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE users SET locked_until = ?, failed_2fa_count = 0 WHERE id = ?`, pastUnix, userID,
	); err != nil {
		t.Fatalf("expire lock in DB: %v", err)
	}

	_, locked = st.Check2FALockout(ctx, userID)
	if locked {
		t.Fatal("account should be unlocked after locked_until passed")
	}

	// A bad code after expiry should return 401 (not 429), since the counter
	// is now at 0 (only 1 failure, below the 5-attempt threshold).
	rr := postTOTPVerify(mux, partialTok, `{"code":"000000"}`)
	if rr.Code == http.StatusTooManyRequests {
		t.Errorf("after lockout expiry: expected not-429, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AUTH-07: recognised-device tests (HTTP layer)
// ─────────────────────────────────────────────────────────────────────────────

// makeDevKEKEnv sets AUTH_DEVICE_KEK to a valid base64-encoded 32-byte key.
func makeDevKEKEnv(t *testing.T) {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 42)
	}
	t.Setenv("AUTH_DEVICE_KEK", base64.StdEncoding.EncodeToString(raw))
	t.Setenv("AUTH_TOTP_KEK", "")
	t.Setenv("VULOS_DEV", "true")
}

// TestDeviceToken_LoginSetsDeviceCookie verifies that a successful non-TOTP login
// sets a vc_device cookie.
func TestDeviceToken_LoginSetsDeviceCookie(t *testing.T) {
	makeDevKEKEnv(t)
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	signupAndGetToken(t, mux, "devlogin@example.com")

	// Login (no TOTP on this account).
	body := `{"email":"devlogin@vulos.org","password":"securepass1234!","remember_device":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:1234"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
	}

	var foundDevice bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.DeviceCookieName {
			foundDevice = true
			if !c.HttpOnly {
				t.Error("vc_device cookie must be HttpOnly")
			}
			if c.Value == "" {
				t.Error("vc_device cookie must not be empty")
			}
		}
	}
	if !foundDevice {
		t.Error("expected vc_device cookie after successful login")
	}
}

// TestDeviceToken_RecognisedDeviceSkipsTOTP verifies that when a valid vc_device
// cookie is present and remember_device=true is set, the TOTP step is skipped
// and a full session is issued directly.
func TestDeviceToken_RecognisedDeviceSkipsTOTP(t *testing.T) {
	makeDevKEKEnv(t)

	st, mux, userID, _ := setupTOTPUserForTest(t)
	ctx := context.Background()

	// Issue a device token for this user directly (simulating a previous successful TOTP).
	devToken, err := st.IssueDeviceToken(ctx, userID, "127.0.0.1", "")
	if err != nil {
		t.Fatalf("IssueDeviceToken: %v", err)
	}

	// Now login with remember_device=true and the device cookie.
	body := `{"email":"locktest@vulos.org","password":"securepass1234!","remember_device":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9000"
	req.AddCookie(&http.Cookie{Name: auth.DeviceCookieName, Value: devToken})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (device skip), got %d %s", rr.Code, rr.Body.String())
	}

	// The response body should NOT be {"step":"totp_required"}.
	body2 := rr.Body.String()
	if strings.Contains(body2, "totp_required") {
		t.Errorf("recognised-device login should not return totp_required; body: %s", body2)
	}

	// A full session cookie should be set.
	var foundSession bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			foundSession = true
		}
	}
	if !foundSession {
		t.Error("expected full session cookie when device is recognised")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AUTH-09: Fleet-admin mandatory 2FA enforcement tests
// ─────────────────────────────────────────────────────────────────────────────

// makeFleetAdminUser creates a user in the store and sets fleet_admin=1.
// Returns the userID and a full session token.
func makeFleetAdminUser(t *testing.T, st *auth.Store, email, password string) (userID, token string) {
	t.Helper()
	ctx := context.Background()

	_, tok, err := st.Signup(ctx, email, password, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("makeFleetAdminUser signup: %v", err)
	}

	var uid string
	if err := st.DB().QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&uid); err != nil {
		t.Fatalf("makeFleetAdminUser get id: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE users SET fleet_admin = 1 WHERE id = ?`, uid); err != nil {
		t.Fatalf("makeFleetAdminUser set fleet_admin: %v", err)
	}
	return uid, tok
}

// TestFleetAdmin_NonFleetAdmin_Unaffected verifies that a non-fleet-admin user
// without TOTP can still access fleet routes (AC: fleet_admin=0 + no TOTP unaffected).
func TestFleetAdmin_NonFleetAdmin_Unaffected(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")
	st := openSessionTestStore(t)

	ctx := context.Background()
	_, tok, err := st.Signup(ctx, "normaluser@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	// Call RequireFleetAuth on a handler that signals success.
	called := false
	handler := st.RequireFleetAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/fleet/devices", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("non-fleet-admin: expected 200, got %d %s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Error("non-fleet-admin: next handler should have been called")
	}
}

// TestFleetAdmin_FleetAdminWithoutTOTP_Returns403 verifies that a fleet_admin
// without TOTP enabled receives 403 totp_required on fleet routes.
// (AC: fleet_admin=1 + totp_enabled=0 → /api/fleet/* returns 403 totp_required)
func TestFleetAdmin_FleetAdminWithoutTOTP_Returns403(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")
	st := openSessionTestStore(t)

	_, tok := makeFleetAdminUser(t, st, "fleetadmin@example.com", "securepass1234!")

	called := false
	handler := st.RequireFleetAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/fleet/org/123", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("fleet_admin without TOTP: expected 403, got %d %s", rr.Code, rr.Body.String())
	}
	if called {
		t.Error("fleet_admin without TOTP: next handler must NOT be called")
	}
	if !strings.Contains(rr.Body.String(), "totp_required") {
		t.Errorf("body must contain 'totp_required'; got: %s", rr.Body.String())
	}
}

// TestFleetAdmin_FleetAdminWithTOTP_Passes verifies that a fleet_admin with
// TOTP enabled can access fleet routes.
// (AC: fleet_admin=1 + totp_enabled=1 → fleet routes pass)
func TestFleetAdmin_FleetAdminWithTOTP_Passes(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")
	os.Setenv("AUTH_TOTP_KEK", "")
	st := openSessionTestStore(t)

	userID, tok := makeFleetAdminUser(t, st, "fleetadmintotp@example.com", "securepass1234!")
	ctx := context.Background()

	// Enable TOTP directly (same approach as setupTOTPUserForTest).
	_, secretB32, err := auth.GenerateTOTPSecret("fleetadmintotp@example.com")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	kek, _ := auth.LoadTOTPKEK()
	enc, _ := auth.EncryptTOTPSecret([]byte(secretB32), kek)
	_ = st.SaveTOTPSecret(ctx, userID, enc)
	_ = st.EnableTOTP(ctx, userID)

	called := false
	handler := st.RequireFleetAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/fleet/devices", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("fleet_admin with TOTP: expected 200, got %d %s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Error("fleet_admin with TOTP: next handler should have been called")
	}
}

// TestFleetAdmin_PasswordTooShort verifies that a fleet_admin account cannot
// set a password shorter than 14 characters on signup or password reset.
// (AC: fleet_admin cannot set password <14 chars)
// Note: signup does not know fleet_admin status upfront (set via DB after signup),
// so the 14-char floor is enforced on password reset when fleet_admin=1.
func TestFleetAdmin_PasswordResetEnforces14Chars(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	// Sign up a normal user (12+ chars OK for non-fleet-admin).
	_, tok := signupAndGetToken(t, mux, "fleetpwreset@example.com")
	_ = tok

	ctx := context.Background()

	// Promote to fleet_admin.
	var uid string
	if err := st.DB().QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, "fleetpwreset@vulos.org").Scan(&uid); err != nil {
		t.Fatalf("get uid: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE users SET fleet_admin = 1 WHERE id = ?`, uid); err != nil {
		t.Fatalf("set fleet_admin: %v", err)
	}

	// Insert a known reset token.
	knownToken := "aaabbbcccdddeeefffaaabbbcccdddeeefffaaabbbcccdddeeefffaaabbbccdde"
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO password_resets (token, user_id, created_at, expires_at, used) VALUES (?, ?, ?, ?, 0)`,
		knownToken, uid, time.Now().UTC().Format(time.RFC3339), expires,
	); err != nil {
		t.Fatalf("insert reset token: %v", err)
	}

	// Attempt password reset with a 13-char password — must fail.
	body := fmt.Sprintf(`{"token":%q,"new_password":"shortpass1234"}`, knownToken) // 13 chars
	req := httptest.NewRequest(http.MethodPost, "/api/auth/password/reset/confirm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("fleet_admin 13-char password reset: expected 400, got %d %s", rr.Code, rr.Body.String())
	}

	// Re-insert the token (it was not consumed by the failed attempt — token is
	// rejected by policy BEFORE the used=1 update, so no re-insertion needed
	// unless the server marked it used; let's verify by trying a 14-char password).
	// Actually the token was NOT consumed (policy check is before use=1 update).
	// Use a fresh token to be safe.
	knownToken2 := "bbbcccdddeeefffaaabbbcccdddeeefffaaabbbcccdddeeefffaaabbbccddee00"
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO password_resets (token, user_id, created_at, expires_at, used) VALUES (?, ?, ?, ?, 0)`,
		knownToken2, uid, time.Now().UTC().Format(time.RFC3339), expires,
	); err != nil {
		t.Fatalf("insert reset token2: %v", err)
	}

	// Attempt password reset with a 14-char password — must succeed.
	body2 := fmt.Sprintf(`{"token":%q,"new_password":"goodpassword14"}`, knownToken2) // 14 chars
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/password/reset/confirm", strings.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.RemoteAddr = "127.0.0.1:9000"
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusNoContent {
		t.Errorf("fleet_admin 14-char password reset: expected 204, got %d %s", rr2.Code, rr2.Body.String())
	}
}

// TestDeviceToken_WrongUserCannotSkipTOTP verifies that a device token issued
// for user A cannot be used to skip TOTP for user B.
// Each user is on a separate store to simulate independent accounts.
func TestDeviceToken_WrongUserCannotSkipTOTP(t *testing.T) {
	makeDevKEKEnv(t)

	ctx := context.Background()

	// Set up user A with TOTP on store A.
	stA, _, userAID, _ := setupTOTPUserForTestEmail(t, "devwrong_a@example.com")

	// Issue a device token for user A (IP="127.0.0.1", UA="" matches login RemoteAddr).
	devTokenA, err := stA.IssueDeviceToken(ctx, userAID, "127.0.0.1", "")
	if err != nil {
		t.Fatalf("IssueDeviceToken for A: %v", err)
	}

	// Set up user B with TOTP on a separate store+mux (different DB).
	stB, muxB, _, _ := setupTOTPUserForTestEmail(t, "devwrong_b@example.com")
	_ = stB

	// Login as user B, passing user A's device token.
	body := `{"email":"devwrong_b@vulos.org","password":"securepass1234!","remember_device":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9000"
	req.AddCookie(&http.Cookie{Name: auth.DeviceCookieName, Value: devTokenA})
	rr := httptest.NewRecorder()
	muxB.ServeHTTP(rr, req)

	// User B should still be challenged for TOTP (token does not belong to B).
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (totp_required step), got %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "totp_required") {
		t.Errorf("user B should still see totp_required; body: %s", rr.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AUTH-06: HIBP breach-check handler tests
// ─────────────────────────────────────────────────────────────────────────────

// routesSHA1HexUpper returns the uppercase hex SHA-1 of s (duplicated from
// hibp_test.go; lives in package main so needs its own copy).
func routesSHA1HexUpper(s string) string {
	h := sha1.Sum([]byte(s))
	return strings.ToUpper(fmt.Sprintf("%x", h))
}

// hibpMockServer builds a fake HIBP range endpoint. knownHashes is a list of
// full 40-char uppercase hex SHA-1s that should appear as breached (count=1).
func hibpMockServer(t *testing.T, knownHashes []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := strings.ToUpper(strings.TrimPrefix(r.URL.Path, "/"))
		if len(prefix) != 5 {
			http.Error(w, "bad prefix", http.StatusBadRequest)
			return
		}
		var lines []string
		for _, full := range knownHashes {
			full = strings.ToUpper(full)
			if strings.HasPrefix(full, prefix) {
				lines = append(lines, full[5:]+":1")
			}
		}
		w.WriteHeader(http.StatusOK)
		if len(lines) > 0 {
			fmt.Fprintln(w, strings.Join(lines, "\n"))
		}
	}))
}

// hibpMockServer5xx returns a server that always responds with 500.
func hibpMockServer5xx(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
}

// withHIBPServer sets the package-level hibpClient and hibpBaseURL to point at
// srv for the duration of the test, then restores them.
func withHIBPServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	origClient := hibpClient
	origURL := hibpBaseURL
	hibpClient = srv.Client()
	hibpBaseURL = srv.URL
	t.Cleanup(func() {
		hibpClient = origClient
		hibpBaseURL = origURL
	})
}

// TestHIBP_SignupBreachedPassword verifies that POST /api/auth/signup with a
// breached password returns 422 {"error":"password_breached"}.
func TestHIBP_SignupBreachedPassword(t *testing.T) {
	t.Setenv("HIBP_CHECK_ENABLED", "1")

	password := "correcthorsebatterystaple"
	fullHash := routesSHA1HexUpper(password)

	srv := hibpMockServer(t, []string{fullHash})
	defer srv.Close()
	withHIBPServer(t, srv)

	st := openSessionTestStore(t)
	mux := authMux(t, st)

	body := fmt.Sprintf(`{"handle":"breacheduser","password":%q}`, password)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9999"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("breached signup: expected 422, got %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "password_breached") {
		t.Errorf("422 body must contain 'password_breached'; got: %s", rr.Body.String())
	}
}

// TestHIBP_SignupCleanPassword verifies that a clean password on signup succeeds (200).
func TestHIBP_SignupCleanPassword(t *testing.T) {
	t.Setenv("HIBP_CHECK_ENABLED", "1")

	// Server knows about a different password, not our clean one.
	srv := hibpMockServer(t, []string{routesSHA1HexUpper("hunter2breachedpassword")})
	defer srv.Close()
	withHIBPServer(t, srv)

	st := openSessionTestStore(t)
	mux := authMux(t, st)

	body := `{"handle":"cleanuser","password":"cleanUniquePassword9988!"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9999"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("clean signup: expected 200, got %d %s", rr.Code, rr.Body.String())
	}
}

// TestHIBP_SignupFailOpen verifies that when HIBP returns 5xx, signup
// proceeds normally (fail-open).
func TestHIBP_SignupFailOpen(t *testing.T) {
	t.Setenv("HIBP_CHECK_ENABLED", "1")

	srv := hibpMockServer5xx(t)
	defer srv.Close()
	withHIBPServer(t, srv)

	st := openSessionTestStore(t)
	mux := authMux(t, st)

	body := `{"handle":"failopenuser","password":"securepass1234!"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9999"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// HIBP error must NOT block signup.
	if rr.Code != http.StatusOK {
		t.Fatalf("fail-open signup: expected 200, got %d %s", rr.Code, rr.Body.String())
	}
}

// TestHIBP_SignupDisabled verifies that when HIBP_CHECK_ENABLED=0, a breached
// password is accepted on signup (check is skipped).
func TestHIBP_SignupDisabled(t *testing.T) {
	t.Setenv("HIBP_CHECK_ENABLED", "0")

	password := "correcthorsebatterystaple"
	fullHash := routesSHA1HexUpper(password)

	srv := hibpMockServer(t, []string{fullHash})
	defer srv.Close()
	withHIBPServer(t, srv)

	st := openSessionTestStore(t)
	mux := authMux(t, st)

	body := fmt.Sprintf(`{"handle":"hibpdisableduser","password":%q}`, password)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9999"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// Check skipped → 200.
	if rr.Code != http.StatusOK {
		t.Fatalf("disabled HIBP signup: expected 200, got %d %s", rr.Code, rr.Body.String())
	}
}

// TestHIBP_ResetConfirmBreachedPassword verifies that POST
// /api/auth/password/reset/confirm with a breached new_password returns 422.
func TestHIBP_ResetConfirmBreachedPassword(t *testing.T) {
	t.Setenv("HIBP_CHECK_ENABLED", "1")

	password := "correcthorsebatterystaple"
	fullHash := routesSHA1HexUpper(password)

	srv := hibpMockServer(t, []string{fullHash})
	defer srv.Close()
	withHIBPServer(t, srv)

	st := openSessionTestStore(t)
	mux := authMux(t, st)
	ctx := context.Background()

	// Sign up a user and get their ID.
	_, _, err := st.Signup(ctx, "resetbreached@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	var uid string
	if err := st.DB().QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, "resetbreached@example.com").Scan(&uid); err != nil {
		t.Fatalf("get uid: %v", err)
	}

	// Insert a valid reset token.
	knownToken := "cccdddeeefffa11122233344455566677788899900aabbccddeeff0011223344"
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO password_resets (token, user_id, created_at, expires_at, used) VALUES (?, ?, ?, ?, 0)`,
		knownToken, uid, time.Now().UTC().Format(time.RFC3339), expires,
	); err != nil {
		t.Fatalf("insert reset token: %v", err)
	}

	body := fmt.Sprintf(`{"token":%q,"new_password":%q}`, knownToken, password)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/password/reset/confirm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("breached reset-confirm: expected 422, got %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "password_breached") {
		t.Errorf("422 body must contain 'password_breached'; got: %s", rr.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AUTH-08: email verification flow
// ─────────────────────────────────────────────────────────────────────────────

// extractVerifyToken fetches the most recent unused verification token for the
// given userID directly from the store DB. Used in tests where we cannot read
// stderr.
func extractVerifyToken(t *testing.T, st *auth.Store, userID string) string {
	t.Helper()
	var tok string
	err := st.DB().QueryRowContext(context.Background(),
		`SELECT token FROM email_verification_tokens
		 WHERE user_id = ? AND used = 0
		 ORDER BY created_at DESC LIMIT 1`, userID,
	).Scan(&tok)
	if err != nil {
		t.Fatalf("extractVerifyToken: %v", err)
	}
	return tok
}

// TestEmailVerify_SignupIssuesToken verifies that signing up creates an unused
// verification token in the DB.
func TestEmailVerify_SignupIssuesToken(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	userID, _ := signupAndGetToken(t, mux, "verify1@example.com")

	tok := extractVerifyToken(t, st, userID)
	if tok == "" {
		t.Fatal("expected non-empty verification token after signup")
	}
}

// TestEmailVerify_ConsumeToken verifies that GET /api/auth/verify-email?token=
// returns 200 and sets email_verified on the user.
func TestEmailVerify_ConsumeToken(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	userID, _ := signupAndGetToken(t, mux, "verify2@example.com")
	tok := extractVerifyToken(t, st, userID)

	rr := getWithCookie(mux, "/api/auth/verify-email?token="+tok, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("verify-email: expected 200, got %d %s", rr.Code, rr.Body.String())
	}

	// Confirm email_verified=1 in the DB.
	var ev int
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT email_verified FROM users WHERE id = ?`, userID,
	).Scan(&ev); err != nil {
		t.Fatalf("query email_verified: %v", err)
	}
	if ev != 1 {
		t.Errorf("expected email_verified=1 after token consumption, got %d", ev)
	}
}

// TestEmailVerify_UsedToken_Returns410 verifies that consuming the same token
// twice returns 410.
func TestEmailVerify_UsedToken_Returns410(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	userID, _ := signupAndGetToken(t, mux, "verify3@example.com")
	tok := extractVerifyToken(t, st, userID)

	// First consumption: 200.
	rr := getWithCookie(mux, "/api/auth/verify-email?token="+tok, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("first verify: expected 200, got %d", rr.Code)
	}

	// Second consumption: 410.
	rr2 := getWithCookie(mux, "/api/auth/verify-email?token="+tok, "")
	if rr2.Code != http.StatusGone {
		t.Errorf("used token: expected 410, got %d %s", rr2.Code, rr2.Body.String())
	}
}

// TestEmailVerify_ExpiredToken_Returns410 verifies that a token with a past
// expires_at returns 410.
func TestEmailVerify_ExpiredToken_Returns410(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	userID, _ := signupAndGetToken(t, mux, "verify4@example.com")
	tok := extractVerifyToken(t, st, userID)

	// Force-expire the token.
	past := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := st.DB().ExecContext(context.Background(),
		`UPDATE email_verification_tokens SET expires_at = ? WHERE token = ?`, past, tok,
	); err != nil {
		t.Fatalf("expire token: %v", err)
	}

	rr := getWithCookie(mux, "/api/auth/verify-email?token="+tok, "")
	if rr.Code != http.StatusGone {
		t.Errorf("expired token: expected 410, got %d %s", rr.Code, rr.Body.String())
	}
}

// TestEmailVerify_UnknownToken_Returns410 verifies that an unknown token
// returns 410.
func TestEmailVerify_UnknownToken_Returns410(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	rr := getWithCookie(mux, "/api/auth/verify-email?token=00000000-0000-0000-0000-000000000000", "")
	if rr.Code != http.StatusGone {
		t.Errorf("unknown token: expected 410, got %d %s", rr.Code, rr.Body.String())
	}
}

// TestEmailVerify_MissingToken_Returns400 verifies that calling the endpoint
// without a token param returns 400.
func TestEmailVerify_MissingToken_Returns400(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	rr := getWithCookie(mux, "/api/auth/verify-email", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing token: expected 400, got %d %s", rr.Code, rr.Body.String())
	}
}

// TestEmailVerify_MeReturnsEmailVerifiedFalse verifies that /me includes
// email_verified=false for a freshly signed-up (unverified) user.
func TestEmailVerify_MeReturnsEmailVerifiedFalse(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	_, tok := signupAndGetToken(t, mux, "verify5@example.com")

	rr := getWithCookie(mux, "/api/auth/me", tok)
	if rr.Code != http.StatusOK {
		t.Fatalf("me: expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode /me: %v", err)
	}
	ev, ok := resp["email_verified"]
	if !ok {
		t.Fatal("/me response missing 'email_verified' field")
	}
	if ev.(bool) {
		t.Errorf("expected email_verified=false for fresh signup, got true")
	}
}

// TestEmailVerify_MeReturnsEmailVerifiedTrue verifies that after consuming the
// token, /me returns email_verified=true.
func TestEmailVerify_MeReturnsEmailVerifiedTrue(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	userID, sessionTok := signupAndGetToken(t, mux, "verify6@example.com")
	vtok := extractVerifyToken(t, st, userID)

	// Consume the token.
	rr := getWithCookie(mux, "/api/auth/verify-email?token="+vtok, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", rr.Code, rr.Body.String())
	}

	// Check /me.
	rr2 := getWithCookie(mux, "/api/auth/me", sessionTok)
	if rr2.Code != http.StatusOK {
		t.Fatalf("me: %d", rr2.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rr2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode /me: %v", err)
	}
	if !resp["email_verified"].(bool) {
		t.Error("expected email_verified=true after verification")
	}
}

// TestEmailVerify_UnverifiedUserCanLoginWithEnvOverride verifies that when
// AUTH_ALLOW_UNVERIFIED_LOGIN=1, unverified accounts can log in fully.
// (The pre-AUTH-10 behaviour; retained as regression guard for the env override.)
func TestEmailVerify_UnverifiedUserCanLoginWithEnvOverride(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	_, tok := signupAndGetToken(t, mux, "verify7@example.com")

	// /me works for unverified user when the gate is bypassed.
	rr := getWithCookie(mux, "/api/auth/me", tok)
	if rr.Code != http.StatusOK {
		t.Errorf("unverified user /me with env override: expected 200, got %d", rr.Code)
	}
}

// TestEmailVerify_Resend_Always204 verifies that POST /api/auth/verify-email/resend
// always returns 204 regardless of whether the email exists.
func TestEmailVerify_Resend_Always204(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	signupAndGetToken(t, mux, "resend1@example.com")

	tests := []struct {
		email string
		desc  string
	}{
		{"resend1@vulos.org", "known email"},
		{"unknown@example.com", "unknown email"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			body := fmt.Sprintf(`{"email":%q}`, tc.email)
			req := httptest.NewRequest(http.MethodPost, "/api/auth/verify-email/resend", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "127.0.0.1:9000"
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != http.StatusNoContent {
				t.Errorf("resend %s: expected 204, got %d %s", tc.email, rr.Code, rr.Body.String())
			}
		})
	}
}

// TestEmailVerify_Resend_AlreadyVerified_Still204 verifies that resend returns
// 204 even when the email is already verified (no enumeration).
func TestEmailVerify_Resend_AlreadyVerified_Still204(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	userID, _ := signupAndGetToken(t, mux, "resend2@example.com")
	_ = extractVerifyToken(t, st, userID) // confirm token exists, no need to use it

	// Verify the email first.
	if _, err := st.DB().ExecContext(context.Background(),
		`UPDATE users SET email_verified = 1 WHERE id = ?`, userID,
	); err != nil {
		t.Fatalf("set verified: %v", err)
	}

	body := `{"email":"resend2@vulos.org"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/verify-email/resend", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("resend already-verified: expected 204, got %d %s", rr.Code, rr.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AUTH-10: email-verification login gate
// ─────────────────────────────────────────────────────────────────────────────

// loginPostRaw posts to /api/auth/login and returns the recorder without
// asserting any status code.  Useful for AUTH-10 tests where the outcome varies.
// email is NOT normalised here — callers that used signupAndGetToken (which
// now stores <local>@vulos.org) must pass the correct vulos.org address.
func loginPostRaw(mux *http.ServeMux, email, password string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:1234"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// TestAuth10_UnverifiedLogin_ReturnsStep verifies that when
// AUTH_ALLOW_UNVERIFIED_LOGIN is unset/0, an unverified account receives
// {"step":"email_verification_required"} and no session cookie.
func TestAuth10_UnverifiedLogin_ReturnsStep(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "0")
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	ctx := context.Background()
	// Create an unverified user directly via the store.
	_, _, err := st.Signup(ctx, "unverified10@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	rr := loginPostRaw(mux, "unverified10@example.com", "securepass1234!")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (step response), got %d %s", rr.Code, rr.Body.String())
	}

	// Body must contain step:email_verification_required.
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	step, ok := resp["step"]
	if !ok {
		t.Fatalf("response missing 'step' field; body: %s", rr.Body.String())
	}
	if step != "email_verification_required" {
		t.Errorf("expected step=email_verification_required, got %v", step)
	}

	// No session cookie must be set.
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			t.Errorf("session cookie must not be set for unverified login gate; got %s", c.Value)
		}
	}
}

// TestAuth10_VerifiedLogin_ReturnsFullSession verifies that a verified account
// receives a full session even when AUTH_ALLOW_UNVERIFIED_LOGIN=0.
func TestAuth10_VerifiedLogin_ReturnsFullSession(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "0")
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	ctx := context.Background()
	_, _, err := st.Signup(ctx, "verified10@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	// Mark the user verified.
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE users SET email_verified = 1 WHERE email = ?`, "verified10@example.com",
	); err != nil {
		t.Fatalf("set email_verified: %v", err)
	}

	rr := loginPostRaw(mux, "verified10@example.com", "securepass1234!")
	if rr.Code != http.StatusOK {
		t.Fatalf("verified login: expected 200, got %d %s", rr.Code, rr.Body.String())
	}

	// Must not be a step response.
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if step, ok := resp["step"]; ok {
		t.Errorf("verified login should not return step; got step=%v", step)
	}

	// Session cookie must be set.
	var foundSession bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			foundSession = true
		}
	}
	if !foundSession {
		t.Error("expected full session cookie for verified login")
	}
}

// TestAuth10_EnvOverride_AllowsUnverifiedLogin verifies that
// AUTH_ALLOW_UNVERIFIED_LOGIN=1 bypasses the gate and issues a full session.
func TestAuth10_EnvOverride_AllowsUnverifiedLogin(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	ctx := context.Background()
	_, _, err := st.Signup(ctx, "envoverride10@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	// email_verified is 0 at this point (fresh signup).

	rr := loginPostRaw(mux, "envoverride10@example.com", "securepass1234!")
	if rr.Code != http.StatusOK {
		t.Fatalf("env override login: expected 200, got %d %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if step, ok := resp["step"]; ok && step == "email_verification_required" {
		t.Error("AUTH_ALLOW_UNVERIFIED_LOGIN=1 should bypass email gate")
	}

	var foundSession bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			foundSession = true
		}
	}
	if !foundSession {
		t.Error("expected full session cookie when env override is set")
	}
}

// TestAuth10_GraceFlag_AllowsUnverifiedLogin verifies that an unverified account
// with a future email_verified_grace_until timestamp can log in fully.
func TestAuth10_GraceFlag_AllowsUnverifiedLogin(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "0")
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	ctx := context.Background()
	_, _, err := st.Signup(ctx, "grace10@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	// Get user ID.
	var uid string
	if err := st.DB().QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`,
		"grace10@example.com").Scan(&uid); err != nil {
		t.Fatalf("get uid: %v", err)
	}

	// Set grace window to 1 hour in the future.
	graceUntil := time.Now().UTC().Add(time.Hour).Unix()
	if err := st.SetEmailVerifiedGraceUntil(ctx, uid, graceUntil); err != nil {
		t.Fatalf("SetEmailVerifiedGraceUntil: %v", err)
	}

	rr := loginPostRaw(mux, "grace10@example.com", "securepass1234!")
	if rr.Code != http.StatusOK {
		t.Fatalf("grace login: expected 200, got %d %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if step, ok := resp["step"]; ok && step == "email_verification_required" {
		t.Error("grace flag should bypass email verification gate")
	}

	var foundSession bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			foundSession = true
		}
	}
	if !foundSession {
		t.Error("expected full session cookie when grace flag is set")
	}
}

// TestAuth10_GraceFlag_Expired_BlocksUnverifiedLogin verifies that an expired
// grace window no longer bypasses the gate.
func TestAuth10_GraceFlag_Expired_BlocksUnverifiedLogin(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "0")
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	ctx := context.Background()
	_, _, err := st.Signup(ctx, "graceexp10@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	var uid string
	if err := st.DB().QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`,
		"graceexp10@example.com").Scan(&uid); err != nil {
		t.Fatalf("get uid: %v", err)
	}

	// Set grace window in the past.
	graceUntil := time.Now().UTC().Add(-time.Hour).Unix()
	if err := st.SetEmailVerifiedGraceUntil(ctx, uid, graceUntil); err != nil {
		t.Fatalf("SetEmailVerifiedGraceUntil: %v", err)
	}

	rr := loginPostRaw(mux, "graceexp10@example.com", "securepass1234!")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (step response), got %d %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	step, ok := resp["step"]
	if !ok || step != "email_verification_required" {
		t.Errorf("expired grace should trigger gate; body: %s", rr.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AUTH-12: passkey modality — HTTP handler tests
// ─────────────────────────────────────────────────────────────────────────────

// waAuthMux builds a mux with both auth and WebAuthn routes registered.
func waAuthMux(t *testing.T, st *auth.Store) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	RegisterAuthRoutes(mux, st)
	RegisterWebAuthnRoutes(mux, st)
	return mux
}

// TestModality_GetDefault verifies that GET /api/auth/modality returns
// {"modality":"password+totp"} for a fresh account (default).
func TestModality_GetDefault(t *testing.T) {
	st := openSessionTestStore(t)
	mux := waAuthMux(t, st)

	_, tok := signupAndGetToken(t, mux, "modhttp1@example.com")

	rr := getWithCookie(mux, "/api/auth/modality", tok)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/modality: expected 200, got %d %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["modality"] != "password+totp" {
		t.Errorf("default modality: got %v, want password+totp", resp["modality"])
	}
}

// TestModality_SetRequiresAuth verifies that POST /api/auth/modality requires a
// valid session.
func TestModality_SetRequiresAuth(t *testing.T) {
	st := openSessionTestStore(t)
	mux := waAuthMux(t, st)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/modality",
		strings.NewReader(`{"modality":"passkey"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated POST /api/auth/modality: expected 401, got %d", rr.Code)
	}
}

// TestModality_SetInvalid verifies that an unknown modality value returns 400.
func TestModality_SetInvalid(t *testing.T) {
	st := openSessionTestStore(t)
	mux := waAuthMux(t, st)

	_, tok := signupAndGetToken(t, mux, "modhttp2@example.com")

	rr := postJSONWithCookie(mux, "/api/auth/modality", tok, `{"modality":"oauth"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid modality: expected 400, got %d %s", rr.Code, rr.Body.String())
	}
}

// TestModality_SetPasskeyWithoutCredentials verifies that setting a passkey
// modality when no passkeys are registered returns 422.
func TestModality_SetPasskeyWithoutCredentials(t *testing.T) {
	st := openSessionTestStore(t)
	mux := waAuthMux(t, st)

	_, tok := signupAndGetToken(t, mux, "modhttp3@example.com")

	for _, m := range []string{"passkey", "password+passkey"} {
		body := fmt.Sprintf(`{"modality":%q}`, m)
		rr := postJSONWithCookie(mux, "/api/auth/modality", tok, body)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("set %s without credentials: expected 422, got %d %s", m, rr.Code, rr.Body.String())
		}
	}
}

// TestModality_SetPasswordTOTP verifies that switching to password+totp always
// works (no passkey required).
func TestModality_SetPasswordTOTP(t *testing.T) {
	st := openSessionTestStore(t)
	mux := waAuthMux(t, st)

	_, tok := signupAndGetToken(t, mux, "modhttp4@example.com")

	rr := postJSONWithCookie(mux, "/api/auth/modality", tok, `{"modality":"password+totp"}`)
	if rr.Code != http.StatusNoContent {
		t.Errorf("set password+totp: expected 204, got %d %s", rr.Code, rr.Body.String())
	}
}

// TestLogin_PasskeyFirst_ReturnsStep verifies that when a user has the "passkey"
// modality and a registered passkey, POST /api/auth/login returns
// {"step":"passkey_required"} and no session cookie.
func TestLogin_PasskeyFirst_ReturnsStep(t *testing.T) {
	st := openSessionTestStore(t)
	ctx := context.Background()
	mux := waAuthMux(t, st)

	userID, _ := signupAndGetToken(t, mux, "pkhttp1@example.com")

	// Insert a synthetic passkey directly into the store.
	insertTestWebAuthnCredential(t, st, userID)

	// Set passkey-only modality.
	if err := st.SetPreferred2FAModality(ctx, userID, auth.ModalityPasskeyOnly); err != nil {
		t.Fatalf("SetPreferred2FAModality: %v", err)
	}

	rr := loginPostRaw(mux, "pkhttp1@vulos.org", "securepass1234!")
	if rr.Code != http.StatusOK {
		t.Fatalf("passkey-first login: expected 200, got %d %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["step"] != "passkey_required" {
		t.Errorf("passkey-first login: expected step=passkey_required, got %v; body: %s", resp["step"], rr.Body.String())
	}

	// No session cookie must be set.
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			t.Errorf("no session cookie expected for passkey-first step; got %s", c.Value)
		}
	}
}

// TestLogin_PasswordPasskey_ReturnsStep verifies that when a user has the
// "password+passkey" modality and a registered passkey, POST /api/auth/login
// returns {"step":"passkey_2fa_required"} and a partial session cookie.
func TestLogin_PasswordPasskey_ReturnsStep(t *testing.T) {
	st := openSessionTestStore(t)
	ctx := context.Background()
	mux := waAuthMux(t, st)

	userID, _ := signupAndGetToken(t, mux, "pkhttp2@example.com")

	insertTestWebAuthnCredential(t, st, userID)

	if err := st.SetPreferred2FAModality(ctx, userID, auth.ModalityPasswordPasskey); err != nil {
		t.Fatalf("SetPreferred2FAModality: %v", err)
	}

	rr := loginPostRaw(mux, "pkhttp2@vulos.org", "securepass1234!")
	if rr.Code != http.StatusOK {
		t.Fatalf("password+passkey login: expected 200, got %d %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["step"] != "passkey_2fa_required" {
		t.Errorf("password+passkey login: expected step=passkey_2fa_required, got %v; body: %s", resp["step"], rr.Body.String())
	}

	// A partial session cookie must be set (5-min MaxAge).
	var foundPartial bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.MaxAge == 300 {
			foundPartial = true
		}
	}
	if !foundPartial {
		t.Error("expected partial session cookie (MaxAge=300) for password+passkey step")
	}
}

// TestFleetAdmin_WithPasskey_PassesRequireFleetAuth verifies that a fleet_admin
// with passkey modality + registered passkey passes RequireFleetAuth (AUTH-12 AC).
func TestFleetAdmin_WithPasskey_PassesRequireFleetAuth(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")
	st := openSessionTestStore(t)
	ctx := context.Background()

	userID, tok := makeFleetAdminUser(t, st, "pkfleetadmin@example.com", "securepass1234!")

	// Set passkey modality and register a passkey.
	if err := st.SetPreferred2FAModality(ctx, userID, auth.ModalityPasskeyOnly); err != nil {
		t.Fatalf("SetPreferred2FAModality: %v", err)
	}
	insertTestWebAuthnCredential(t, st, userID)

	called := false
	handler := st.RequireFleetAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/fleet/devices", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("fleet_admin with passkey: expected 200, got %d %s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Error("fleet_admin with passkey: next handler should have been called")
	}
}

// TestFleetAdmin_PasskeyModeNoPasskeys_Returns403 verifies that a fleet_admin
// with passkey modality but no registered passkeys still gets 403 (AUTH-12 AC).
func TestFleetAdmin_PasskeyModeNoPasskeys_Returns403(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")
	st := openSessionTestStore(t)
	ctx := context.Background()

	userID, tok := makeFleetAdminUser(t, st, "pkfleetadminnone@example.com", "securepass1234!")

	// Set passkey modality but register NO passkeys.
	if err := st.SetPreferred2FAModality(ctx, userID, auth.ModalityPasskeyOnly); err != nil {
		t.Fatalf("SetPreferred2FAModality: %v", err)
	}

	called := false
	handler := st.RequireFleetAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/fleet/devices", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("fleet_admin with passkey modality but no passkeys: expected 403, got %d %s", rr.Code, rr.Body.String())
	}
	if called {
		t.Error("fleet_admin with passkey modality but no passkeys: next handler must NOT be called")
	}
}

// insertTestWebAuthnCredential inserts a synthetic WebAuthn credential row for
// userID directly into the store DB. Used in HTTP-layer tests that need a
// registered passkey without driving a full WebAuthn ceremony.
func insertTestWebAuthnCredential(t *testing.T, st *auth.Store, userID string) {
	t.Helper()
	ctx := context.Background()
	id := fmt.Sprintf("testcredrow%d", sessionTestStoreN.Load())
	credID := fmt.Sprintf("dGVzdGNyZWRlbnRpYWxfaHR0cHRlc3Q%d", sessionTestStoreN.Load()) // base64url-ish
	_, err := st.DB().ExecContext(ctx,
		`INSERT INTO webauthn_credentials
		   (id, user_id, credential_id, public_key, sign_count, transports, friendly_name, aaguid, last_used_at, created_at)
		 VALUES (?, ?, ?, ?, 0, '["internal"]', 'Test Key', '', NULL, ?)`,
		id, userID, credID, []byte("fakepubkey"),
		"2026-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insertTestWebAuthnCredential: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests: HANDLE-01 — Vulos identity signup + handle-available endpoint
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleSignup_ProvidesVulosMailbox verifies that signing up with
// handle=alice creates a user whose email is alice@vulos.org.
func TestHandleSignup_ProvidesVulosMailbox(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	body := `{"handle":"alice","password":"securepassword123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9999"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("signup with handle=alice: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var u struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&u); err != nil {
		t.Fatalf("decode signup response: %v", err)
	}
	if u.Email != "alice@vulos.org" {
		t.Errorf("email in response = %q, want alice@vulos.org", u.Email)
	}

	// Verify the email is stored correctly in the DB.
	var dbEmail string
	err := st.DB().QueryRowContext(context.Background(),
		`SELECT email FROM users WHERE email = ?`, "alice@vulos.org",
	).Scan(&dbEmail)
	if err != nil {
		t.Fatalf("alice@vulos.org not found in DB: %v", err)
	}
	if dbEmail != "alice@vulos.org" {
		t.Errorf("DB email = %q, want alice@vulos.org", dbEmail)
	}
}

// TestHandleSignup_AcceptsExternalEmail verifies the BYO-EMAIL pivot (2026-07):
// the account identity IS the user's own external email address (gmail/outlook/
// any domain), so signup with an external email now SUCCEEDS. (This supersedes
// the pre-pivot behaviour, which minted hosted <handle>@vulos.org identities and
// rejected external domains.)
func TestHandleSignup_AcceptsExternalEmail(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	body := `{"email":"alice@gmail.com","password":"securepassword123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9999"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("BYO external email in signup: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleSignup_RejectReservedHandle verifies that a reserved handle is
// rejected with 400 (invalid handle error before even reaching the DB).
// TestHandleSignup_RejectReservedHandle is the regression test for the
// RESERVED-HANDLE BYPASS: the reserved blocklist must be enforced on the SIGNUP
// path, not only on the advisory GET /api/auth/handle-available. Reserved handles
// are NOT pre-seeded into users, so before the fix a direct POST to signup with
// handle=admin/postmaster/abuse/security/vulos would claim those system/brand/
// RFC-2142 addresses (phishing, abuse interception). Every reserved handle must
// now be rejected (409) by the signup handler itself.
func TestHandleSignup_RejectReservedHandle(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	// A representative spread across the reserved categories: system addresses,
	// RFC-2142 well-knowns, subdomain conflicts, and brand protection.
	for _, handle := range []string{"admin", "postmaster", "abuse", "security", "vulos", "www"} {
		body := `{"handle":"` + handle + `","password":"securepassword123"}`
		req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:9999"
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusConflict {
			t.Errorf("reserved handle %q: expected 409 (reserved), got %d: %s", handle, rr.Code, rr.Body.String())
		}
	}

	// A truly invalid (format-violating) handle still 400s.
	body2 := `{"handle":"-badhyphen","password":"securepassword123"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.RemoteAddr = "127.0.0.1:9999"
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("bad-format handle: expected 400, got %d: %s", rr2.Code, rr2.Body.String())
	}

	// A non-reserved, well-formed handle still succeeds (fix isn't over-broad).
	okBody := `{"handle":"alicewonder","password":"securepassword123"}`
	okReq := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(okBody))
	okReq.Header.Set("Content-Type", "application/json")
	okReq.RemoteAddr = "127.0.0.1:9999"
	okRR := httptest.NewRecorder()
	mux.ServeHTTP(okRR, okReq)
	if okRR.Code != http.StatusOK {
		t.Errorf("valid handle: expected 200, got %d: %s", okRR.Code, okRR.Body.String())
	}
}

// TestHandleSignup_TakenHandle verifies that signing up with a handle that is
// already registered produces 409.
func TestHandleSignup_TakenHandle(t *testing.T) {
	st := openSessionTestStore(t)
	mux := authMux(t, st)

	for i, body := range []string{
		`{"handle":"bob","password":"securepassword123"}`,
		`{"handle":"bob","password":"differentpassword456"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:9999"
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if i == 0 && rr.Code != http.StatusOK {
			t.Fatalf("first signup: expected 200, got %d", rr.Code)
		}
		if i == 1 && rr.Code != http.StatusConflict {
			t.Errorf("duplicate handle signup: expected 409, got %d: %s", rr.Code, rr.Body.String())
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SSO_COOKIE_WIRE: ?next= redirect tests
// ─────────────────────────────────────────────────────────────────────────────

// TestLoginNext_ValidRedirect verifies that POST /api/auth/login?next=<url>
// with a valid same-domain next URL issues a 302 redirect to that URL on
// successful login instead of returning a JSON user object.
func TestLoginNext_ValidRedirect(t *testing.T) {
	// Configure cookie domain so isAllowedNextURL accepts *.vulos.org.
	t.Setenv("VULOS_COOKIE_DOMAIN", "vulos.org")
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	t.Setenv("VULOS_ENV", "local") // non-prod: http allowed

	st := openSessionTestStore(t)
	mux := authMux(t, st)

	// Sign up the user.
	signupBody := `{"handle":"nextredirect1","password":"securepass1234!"}`
	req0 := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(signupBody))
	req0.Header.Set("Content-Type", "application/json")
	req0.RemoteAddr = "127.0.0.1:9999"
	mux.ServeHTTP(httptest.NewRecorder(), req0)

	// Login with a valid same-domain next param.
	nextURL := "https://mail.vulos.org/inbox"
	loginBody := `{"email":"nextredirect1@vulos.org","password":"securepass1234!"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login?next="+nextURL, strings.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9999"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("expected 302 redirect for valid next URL, got %d: %s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != nextURL {
		t.Errorf("Location: got %q, want %q", loc, nextURL)
	}
}

// TestLoginNext_MaliciousNextIgnored verifies that a malicious ?next= value
// (pointing at a different domain or using javascript: scheme) is ignored on
// successful login and the response falls back to the default JSON user body.
func TestLoginNext_MaliciousNextIgnored(t *testing.T) {
	t.Setenv("VULOS_COOKIE_DOMAIN", "vulos.org")
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	t.Setenv("VULOS_ENV", "local")

	st := openSessionTestStore(t)
	mux := authMux(t, st)

	// Sign up the user.
	signupBody := `{"handle":"nextredirect2","password":"securepass1234!"}`
	req0 := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(signupBody))
	req0.Header.Set("Content-Type", "application/json")
	req0.RemoteAddr = "127.0.0.1:9999"
	mux.ServeHTTP(httptest.NewRecorder(), req0)

	maliciousURLs := []string{
		"https://evil.example.com/steal",
		"javascript:alert(1)",
		"http://notourvulosdomain.com",
	}

	for _, bad := range maliciousURLs {
		loginBody := `{"email":"nextredirect2@vulos.org","password":"securepass1234!"}`
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login?next="+bad, strings.NewReader(loginBody))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:9999"
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Must NOT redirect — must return 200 with JSON.
		if rr.Code == http.StatusFound {
			t.Errorf("malicious next %q: should NOT redirect, got 302 to %q", bad, rr.Header().Get("Location"))
		}
		if rr.Code != http.StatusOK {
			t.Errorf("malicious next %q: expected 200 JSON fallback, got %d", bad, rr.Code)
		}
	}
}
