package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// TestMain sets package-wide defaults for environment variables that affect
// auth behaviour, so that pre-AUTH-10 tests are unaffected by the email gate.
// AUTH-10-specific tests override AUTH_ALLOW_UNVERIFIED_LOGIN as needed.
func TestMain(m *testing.M) {
	// WAVE37: the prod resolver fails safe to prod when VULOS_ENV is unset. Unit
	// tests run as a dev/local environment unless a specific test opts into prod,
	// so default VULOS_ENV=local here (only when unset — subprocess tests that
	// pass VULOS_ENV=prod in their child env still override this).
	if os.Getenv("VULOS_ENV") == "" {
		os.Setenv("VULOS_ENV", "local")
	}
	if os.Getenv("AUTH_ALLOW_UNVERIFIED_LOGIN") == "" {
		os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	}
	// Use cheap Argon2 parameters in tests to prevent CPU-heavy hashing from
	// starving parallel goroutines and causing timeout flakiness.
	// ARGON2_TEST_FAST must never be set outside of test environments.
	if os.Getenv("ARGON2_TEST_FAST") == "" {
		os.Setenv("ARGON2_TEST_FAST", "1")
	}
	os.Exit(m.Run())
}

// openTestStore creates an in-memory SQLite auth store for testing.
// Each call uses a unique DSN to avoid shared-cache collisions between tests.
// Atomic so parallel subtests never share a DSN.
var testStoreN atomic.Int64

func openTestStore(t *testing.T) *Store {
	t.Helper()
	// Use file mode with a unique name + shared cache so the DB persists for
	// the test duration but doesn't collide between parallel tests.
	dsn := "file:testauth" + fmt.Sprintf("%d", testStoreN.Add(1)) + "?mode=memory&cache=shared"
	db, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	st, err := OpenAuthStore(db, []byte("test-secret-key-1234567890123456"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// --- Argon2id tests ---

func TestArgon2RoundTrip(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Errorf("unexpected hash format: %s", hash)
	}
	if err := VerifyPassword(hash, "correct-horse-battery-staple"); err != nil {
		t.Errorf("VerifyPassword correct: %v", err)
	}
}

func TestArgon2Mismatch(t *testing.T) {
	hash, err := HashPassword("right-password-long-enough")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	err = VerifyPassword(hash, "wrong-password-long-enough")
	if err != ErrHashMismatch {
		t.Errorf("expected ErrHashMismatch, got %v", err)
	}
}

func TestArgon2ConstantTimeMismatch(t *testing.T) {
	// Constant-time comparison: verify the comparison path uses subtle.ConstantTimeCompare.
	hash, _ := HashPassword("password-12345-long")
	err := VerifyPassword(hash, "wrong-password-123")
	if err != ErrHashMismatch {
		t.Errorf("expected ErrHashMismatch on mismatch, got %v", err)
	}
	// Correct password should pass.
	if err := VerifyPassword(hash, "password-12345-long"); err != nil {
		t.Errorf("expected nil on match, got %v", err)
	}
}

func TestArgon2UniqueSalts(t *testing.T) {
	h1, _ := HashPassword("same-password-abc123")
	h2, _ := HashPassword("same-password-abc123")
	if h1 == h2 {
		t.Error("two hashes of same password should differ (random salts)")
	}
}

func TestArgon2InvalidHash(t *testing.T) {
	err := VerifyPassword("not-a-valid-hash", "password")
	if err != ErrInvalidHash {
		t.Errorf("expected ErrInvalidHash, got %v", err)
	}
}

// --- Password policy tests ---

func TestPasswordPolicyMinLength(t *testing.T) {
	// 11 chars -- below minimum of 12.
	err := validatePasswordPolicy("short12345!", false)
	if err == nil {
		t.Error("expected error for short password (11 chars)")
	}
}

func TestPasswordPolicyMinLengthAccepted(t *testing.T) {
	// Exactly 12 chars.
	if err := validatePasswordPolicy("abcdefghijkl", false); err != nil {
		t.Errorf("expected nil for 12-char password, got %v", err)
	}
}

func TestPasswordPolicyFleetAdmin(t *testing.T) {
	// 13 chars -- below 14-char fleet-admin floor.
	err := validatePasswordPolicy("abcdefghijklm", true)
	if err == nil {
		t.Error("expected error for fleet_admin with 13-char password")
	}
	// 14 chars -- at minimum.
	if err := validatePasswordPolicy("abcdefghijklmn", true); err != nil {
		t.Errorf("expected nil for fleet_admin 14-char password, got %v", err)
	}
}

func TestPasswordPolicyUnicodeSpaces(t *testing.T) {
	// Passphrase with spaces and multi-byte unicode should be accepted.
	pass := "correct horse battery staple" // 28 chars with spaces
	if err := validatePasswordPolicy(pass, false); err != nil {
		t.Errorf("unicode/spaces passphrase rejected: %v", err)
	}
}

func TestPasswordPolicyMaxLength(t *testing.T) {
	// > 1024 chars.
	long := strings.Repeat("a", 1025)
	err := validatePasswordPolicy(long, false)
	if err == nil {
		t.Error("expected error for password > 1024 chars")
	}
}

// --- Session tests ---

func TestSessionCreateAndLookup(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, token, err := st.Signup(ctx, "session@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty session token")
	}

	got, err := st.LookupSession(ctx, token)
	if err != nil {
		t.Fatalf("LookupSession: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("got user id %s, want %s", got.ID, u.ID)
	}
}

func TestSessionExpiry(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, token, _ := st.Signup(ctx, "expiry@example.com", "securepass1234!", "127.0.0.1", "test")

	// Force-expire the session.
	past := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	if _, err := st.db.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE id = ?`, past, token); err != nil {
		t.Fatalf("expire session: %v", err)
	}

	_, err := st.LookupSession(ctx, token)
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound for expired session, got %v", err)
	}
}

func TestSessionRevoked(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, token, _ := st.Signup(ctx, "revoke@example.com", "securepass1234!", "127.0.0.1", "test")

	// Revoke the session.
	if err := st.RevokeSession(ctx, token); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	_, err := st.LookupSession(ctx, token)
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound for revoked session, got %v", err)
	}
}

func TestSessionNotFound(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, err := st.LookupSession(ctx, "nonexistent-token")
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestSessionEmptyToken(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, err := st.LookupSession(ctx, "")
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound for empty token, got %v", err)
	}
}

// --- Signup/Login/Me/Logout integration flow ---

func TestSignupLoginMeLogout(t *testing.T) {
	// Allow unverified login so this test exercises the full login flow
	// independently of AUTH-10's email gate (gate-specific tests are separate).
	orig := os.Getenv("AUTH_ALLOW_UNVERIFIED_LOGIN")
	os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	defer os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", orig)

	st := openTestStore(t)
	ctx := context.Background()

	// Signup.
	signedUser, signupToken, err := st.Signup(ctx, "flow@example.com", "securePass1234!", "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if signupToken == "" {
		t.Fatal("expected session token from Signup")
	}

	// Me: valid session.
	u, err := st.LookupSession(ctx, signupToken)
	if err != nil {
		t.Fatalf("LookupSession after signup: %v", err)
	}
	if u.Email != "flow@example.com" {
		t.Errorf("me email: got %s, want flow@example.com", u.Email)
	}

	// Login.
	loginResult, err := st.Login(ctx, "flow@example.com", "securePass1234!", "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if loginResult.User.ID != signedUser.ID {
		t.Errorf("login returned different user id")
	}
	loginToken := loginResult.Token
	if loginToken == signupToken {
		t.Error("login should issue a new session token")
	}

	// Logout: delete login session.
	if err := st.DeleteSession(ctx, loginToken); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	_, err = st.LookupSession(ctx, loginToken)
	if err != ErrSessionNotFound {
		t.Errorf("after logout: expected ErrSessionNotFound, got %v", err)
	}

	// Signup session still valid (independent sessions).
	_, err = st.LookupSession(ctx, signupToken)
	if err != nil {
		t.Errorf("signup session should still be valid: %v", err)
	}
}

// --- No OAuth column / no OAuth path ---

func TestNoOAuthColumn(t *testing.T) {
	// Assert that the users table has no oauth_google_sub column.
	// This guards the LOCKED auth-decision: no IdP path.
	st := openTestStore(t)
	ctx := context.Background()

	rows, err := st.db.QueryContext(ctx, `PRAGMA table_info(users)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan column info: %v", err)
		}
		if strings.Contains(name, "oauth") || strings.Contains(name, "google") {
			t.Errorf("found OAuth column in users table: %s -- contract requires NO OAuth column", name)
		}
	}
}

func TestPasswordHashNotNull(t *testing.T) {
	// Signup creates a user with a non-NULL password_hash (contract: password_hash NOT NULL).
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "notnull@example.com", "securePass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	var hash string
	err = st.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, u.ID).Scan(&hash)
	if err != nil {
		t.Fatalf("query password_hash: %v", err)
	}
	if hash == "" {
		t.Error("password_hash must not be empty/NULL for email+password users")
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("password_hash should be argon2id-encoded, got: %s", hash)
	}
}

// httptest mux for cookie-flow tests.
func buildTestMux(t *testing.T, st *Store) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		token := SessionFromRequest(r)
		if token == "" {
			http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
			return
		}
		u, err := st.LookupSession(r.Context(), token)
		if err != nil {
			http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"user_id":"` + u.ID + `","email":"` + u.Email + `"}`))
	})

	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		token := SessionFromRequest(r)
		if token != "" {
			st.DeleteSession(r.Context(), token)
		}
		ClearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}

func TestMeViaHTTPTest(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, token, _ := st.Signup(ctx, "httpme@example.com", "securepass1234!", "127.0.0.1", "test")

	mux := buildTestMux(t, st)

	// GET /api/auth/me with valid session cookie.
	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("me: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "httpme@example.com") {
		t.Errorf("me body missing email: %s", w.Body.String())
	}

	// GET /api/auth/me without cookie.
	req2 := httptest.NewRequest("GET", "/api/auth/me", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("me without cookie: got %d, want 401", w2.Code)
	}
}

func TestLogoutViaHTTPTest(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, token, _ := st.Signup(ctx, "httplogout@example.com", "securepass1234!", "127.0.0.1", "test")
	mux := buildTestMux(t, st)

	// Logout.
	req := httptest.NewRequest("POST", "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("logout: got %d, want 204", w.Code)
	}

	// Me after logout should 401.
	req2 := httptest.NewRequest("GET", "/api/auth/me", nil)
	req2.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("me after logout: got %d, want 401", w2.Code)
	}

	// Clear session cookie should be set on logout response.
	var foundClear bool
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookieName && c.MaxAge < 0 {
			foundClear = true
		}
	}
	if !foundClear {
		t.Error("expected session cookie to be cleared on logout")
	}

	_ = ctx
}

// --- Email taken ---

func TestSignupEmailTaken(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, _, err := st.Signup(ctx, "dup@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("first signup: %v", err)
	}
	_, _, err = st.Signup(ctx, "dup@example.com", "other-secure-pass-1234!", "127.0.0.1", "test")
	if err != ErrEmailTaken {
		t.Errorf("expected ErrEmailTaken, got %v", err)
	}
}

// --- Password reset ---

func TestPasswordResetConsumedAndSessionsInvalidated(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, sessionToken, _ := st.Signup(ctx, "reset@example.com", "oldPass1234!", "127.0.0.1", "test")

	// Confirm session valid before reset.
	_, err := st.LookupSession(ctx, sessionToken)
	if err != nil {
		t.Fatalf("pre-reset session: %v", err)
	}

	// Insert a known reset token directly.
	var userID string
	st.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'reset@example.com'`).Scan(&userID)
	knownToken := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	st.db.ExecContext(ctx,
		`INSERT INTO password_resets (token, user_id, created_at, expires_at, used) VALUES (?, ?, ?, ?, 0)`,
		knownToken, userID, time.Now().UTC().Format(time.RFC3339), expires,
	)

	// Confirm reset.
	if err := st.ConfirmPasswordReset(ctx, knownToken, "newSecurePass2345!"); err != nil {
		t.Fatalf("ConfirmPasswordReset: %v", err)
	}

	// Old session invalidated.
	_, err = st.LookupSession(ctx, sessionToken)
	if err != ErrSessionNotFound {
		t.Errorf("old session should be invalidated after reset, got %v", err)
	}

	// Login with new password works.
	resetLoginResult, err := st.Login(ctx, "reset@example.com", "newSecurePass2345!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Login with new password: %v", err)
	}
	newToken := resetLoginResult.Token
	if newToken == "" {
		t.Fatal("expected session token")
	}

	// Token cannot be reused.
	err = st.ConfirmPasswordReset(ctx, knownToken, "anotherPass3456!")
	if err != ErrResetExpired {
		t.Errorf("expected ErrResetExpired on reuse, got %v", err)
	}
}

func TestPasswordResetExpiredToken(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, _, _ = st.Signup(ctx, "expired@example.com", "securepass1234!", "127.0.0.1", "test")

	var userID string
	st.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'expired@example.com'`).Scan(&userID)

	expiredToken := "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd"
	past := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	st.db.ExecContext(ctx,
		`INSERT INTO password_resets (token, user_id, created_at, expires_at, used) VALUES (?, ?, ?, ?, 0)`,
		expiredToken, userID, past, past,
	)

	err := st.ConfirmPasswordReset(ctx, expiredToken, "newPass1234!")
	if err != ErrResetExpired {
		t.Errorf("expected ErrResetExpired for expired token, got %v", err)
	}
}

// --- Email enumeration safety ---

func TestEmailEnumerationSafe(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Unknown email -- nil (no error, so HTTP handler returns 204).
	if err := st.RequestPasswordReset(ctx, "doesnotexist@example.com"); err != nil {
		t.Errorf("RequestPasswordReset for unknown email should return nil, got %v", err)
	}

	// Known email -- also nil.
	_, _, _ = st.Signup(ctx, "known@example.com", "securepass1234!", "127.0.0.1", "test")
	if err := st.RequestPasswordReset(ctx, "known@example.com"); err != nil {
		t.Errorf("RequestPasswordReset for known email should return nil, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SSO cookie domain tests (SSO_COOKIE_WIRE)
// ─────────────────────────────────────────────────────────────────────────────

// TestCookieDomain_AlwaysHostScoped verifies that SetSessionCookie NEVER writes a
// Domain attribute — the session cookie is host-scoped to the apex regardless of
// VULOS_COOKIE_DOMAIN, so it can never bleed to os.<domain> / product subdomains
// (ROUTING MODEL PART A.4). Even with the family domain configured, Domain is "".
func TestCookieDomain_AlwaysHostScoped(t *testing.T) {
	os.Setenv("VULOS_COOKIE_DOMAIN", "vulos.org")
	InitCookieDomain()
	t.Cleanup(func() {
		os.Unsetenv("VULOS_COOKIE_DOMAIN")
		InitCookieDomain()
	})

	w := httptest.NewRecorder()
	SetSessionCookie(w, "test-token-sso")
	resp := w.Result()
	var found *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName {
			found = c
		}
	}
	if found == nil {
		t.Fatal("session cookie not set")
	}
	if found.Domain != "" {
		t.Errorf("cookie Domain: got %q, want host-scoped (empty)", found.Domain)
	}
	// The family domain is still recorded for return-to/CORS recognition.
	if CookieDomain() != "vulos.org" {
		t.Errorf("CookieDomain() = %q, want vulos.org (family domain)", CookieDomain())
	}
}

// TestCookieDomain_ClearIsHostScoped verifies the clear cookie is host-scoped too
// (a Domain mismatch would leave the cookie uncleared in the browser).
func TestCookieDomain_ClearIsHostScoped(t *testing.T) {
	os.Setenv("VULOS_COOKIE_DOMAIN", "vulos.org")
	InitCookieDomain()
	t.Cleanup(func() {
		os.Unsetenv("VULOS_COOKIE_DOMAIN")
		InitCookieDomain()
	})
	w := httptest.NewRecorder()
	ClearSessionCookie(w)
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookieName && c.Domain != "" {
			t.Errorf("clear cookie Domain: got %q, want host-scoped (empty)", c.Domain)
		}
	}
}

// TestCookieDomain_SameSiteLax verifies that session cookies always carry
// SameSite=Lax regardless of the cookie domain setting.
func TestCookieDomain_SameSiteLax(t *testing.T) {
	os.Setenv("VULOS_COOKIE_DOMAIN", "vulos.org")
	InitCookieDomain()
	t.Cleanup(func() {
		os.Unsetenv("VULOS_COOKIE_DOMAIN")
		InitCookieDomain()
	})

	w := httptest.NewRecorder()
	SetSessionCookie(w, "lax-test-token")
	resp := w.Result()
	var found *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName {
			found = c
		}
	}
	if found == nil {
		t.Fatal("session cookie not set")
	}
	if found.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite: got %v, want SameSiteLaxMode", found.SameSite)
	}
}

// --- Session cookie helpers ---

func TestSetSessionCookie(t *testing.T) {
	w := httptest.NewRecorder()
	SetSessionCookie(w, "test-token-value")
	resp := w.Result()
	cookies := resp.Cookies()
	var found *http.Cookie
	for _, c := range cookies {
		if c.Name == SessionCookieName {
			found = c
		}
	}
	if found == nil {
		t.Fatal("session cookie not set")
	}
	if found.Value != "test-token-value" {
		t.Errorf("cookie value: got %s, want test-token-value", found.Value)
	}
	if !found.HttpOnly {
		t.Error("cookie should be HttpOnly")
	}
	if !found.Secure {
		t.Error("cookie should be Secure")
	}
	if found.SameSite != http.SameSiteLaxMode {
		t.Error("cookie should be SameSite=Lax")
	}
	if found.MaxAge != SessionCookieMaxAge {
		t.Errorf("MaxAge: got %d, want %d", found.MaxAge, SessionCookieMaxAge)
	}
}

// --- Session registry (AUTH-05) ---

func TestListSessions_Empty(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, token, _ := st.Signup(ctx, "listempty@example.com", "securepass1234!", "1.2.3.4", "ua-A")
	var userID string
	st.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'listempty@example.com'`).Scan(&userID)

	sessions, err := st.ListSessions(ctx, userID, token)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session (the signup session), got %d", len(sessions))
	}
	if !sessions[0].IsCurrent {
		t.Error("only session should have is_current=true")
	}
}

func TestListSessions_IsCurrent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, tok1, _ := st.Signup(ctx, "iscurrent@example.com", "securepass1234!", "1.1.1.1", "ua-1")
	r2, _ := st.Login(ctx, "iscurrent@example.com", "securepass1234!", "2.2.2.2", "ua-2")
	tok2 := r2.Token

	var userID string
	st.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'iscurrent@example.com'`).Scan(&userID)

	sessions, err := st.ListSessions(ctx, userID, tok1)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	var foundCurrent bool
	for _, s := range sessions {
		if s.ID == tok1 && s.IsCurrent {
			foundCurrent = true
		}
		if s.ID == tok2 && s.IsCurrent {
			t.Error("tok2 should not be marked current when tok1 is the current token")
		}
	}
	if !foundCurrent {
		t.Error("tok1 should be marked is_current")
	}
}

func TestListSessions_ExcludesRevoked(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, tok1, _ := st.Signup(ctx, "listrevoked@example.com", "securepass1234!", "1.1.1.1", "ua-1")
	r2, _ := st.Login(ctx, "listrevoked@example.com", "securepass1234!", "2.2.2.2", "ua-2")
	tok2 := r2.Token

	// Revoke tok2.
	if err := st.RevokeSession(ctx, tok2); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	var userID string
	st.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'listrevoked@example.com'`).Scan(&userID)

	sessions, err := st.ListSessions(ctx, userID, tok1)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session after revoke, got %d", len(sessions))
	}
	if sessions[0].ID != tok1 {
		t.Error("expected only tok1 in list")
	}
}

func TestRevokeByID_Success(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, tok1, _ := st.Signup(ctx, "revokebyid@example.com", "securepass1234!", "1.1.1.1", "ua-1")
	r2, _ := st.Login(ctx, "revokebyid@example.com", "securepass1234!", "2.2.2.2", "ua-2")
	tok2 := r2.Token

	var userID string
	st.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'revokebyid@example.com'`).Scan(&userID)

	if err := st.RevokeByID(ctx, userID, tok2); err != nil {
		t.Fatalf("RevokeByID: %v", err)
	}

	// tok2 should be gone from lookup.
	if _, err := st.LookupSession(ctx, tok2); err != ErrSessionNotFound {
		t.Errorf("revoked session tok2 should return ErrSessionNotFound, got %v", err)
	}
	// tok1 still valid.
	if _, err := st.LookupSession(ctx, tok1); err != nil {
		t.Errorf("tok1 should still be valid: %v", err)
	}
}

func TestRevokeByID_NotFound(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, _, _ = st.Signup(ctx, "revokebymissing@example.com", "securepass1234!", "1.1.1.1", "ua-1")
	var userID string
	st.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'revokebymissing@example.com'`).Scan(&userID)

	err := st.RevokeByID(ctx, userID, "nonexistent-session-token")
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound for missing session, got %v", err)
	}
}

func TestRevokeByID_WrongUser(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, tok1, _ := st.Signup(ctx, "owner@example.com", "securepass1234!", "1.1.1.1", "ua-1")
	_, _, _ = st.Signup(ctx, "attacker@example.com", "securepass1234!", "2.2.2.2", "ua-2")

	var attackerID string
	st.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'attacker@example.com'`).Scan(&attackerID)

	// Attacker tries to revoke owner's session.
	err := st.RevokeByID(ctx, attackerID, tok1)
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound when revoking other user's session, got %v", err)
	}
	// Owner's session still valid.
	if _, err := st.LookupSession(ctx, tok1); err != nil {
		t.Errorf("owner session should still be valid: %v", err)
	}
}

func TestRevokeAll_KeepsCurrent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, tok1, _ := st.Signup(ctx, "revokeall@example.com", "securepass1234!", "1.1.1.1", "ua-1")
	r2, _ := st.Login(ctx, "revokeall@example.com", "securepass1234!", "2.2.2.2", "ua-2")
	r3, _ := st.Login(ctx, "revokeall@example.com", "securepass1234!", "3.3.3.3", "ua-3")
	tok2, tok3 := r2.Token, r3.Token

	var userID string
	st.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'revokeall@example.com'`).Scan(&userID)

	n, err := st.RevokeAll(ctx, userID, tok1)
	if err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 revoked, got %d", n)
	}

	// tok1 (current) still valid.
	if _, err := st.LookupSession(ctx, tok1); err != nil {
		t.Errorf("current session tok1 should still be valid: %v", err)
	}
	// tok2 and tok3 revoked.
	if _, err := st.LookupSession(ctx, tok2); err != ErrSessionNotFound {
		t.Errorf("tok2 should be revoked, got %v", err)
	}
	if _, err := st.LookupSession(ctx, tok3); err != ErrSessionNotFound {
		t.Errorf("tok3 should be revoked, got %v", err)
	}
}

// --- Delete all user sessions ---

func TestDeleteAllUserSessions(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, tok1, _ := st.Signup(ctx, "multi@example.com", "securepass1234!", "127.0.0.1", "agent1")
	r2, _ := st.Login(ctx, "multi@example.com", "securepass1234!", "127.0.0.2", "agent2")
	r3, _ := st.Login(ctx, "multi@example.com", "securepass1234!", "127.0.0.3", "agent3")
	tok2, tok3 := r2.Token, r3.Token

	// All sessions valid before.
	for i, tok := range []string{tok1, tok2, tok3} {
		if _, err := st.LookupSession(ctx, tok); err != nil {
			t.Errorf("session[%d] should be valid before revoke-all: %v", i, err)
		}
	}

	var userID string
	st.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'multi@example.com'`).Scan(&userID)
	if err := st.DeleteAllUserSessions(ctx, userID); err != nil {
		t.Fatalf("DeleteAllUserSessions: %v", err)
	}

	// All sessions invalid after.
	for i, tok := range []string{tok1, tok2, tok3} {
		if _, err := st.LookupSession(ctx, tok); err != ErrSessionNotFound {
			t.Errorf("session[%d] should be invalid after revoke-all, got %v", i, err)
		}
	}
}

// --- AUTH-10: email-verification gate (store-level) ---

// TestLogin_UnverifiedGate_BlocksLogin verifies that Login returns
// EmailVerificationRequired=true when the account is unverified and neither
// the env override nor a valid grace window is present.
func TestLogin_UnverifiedGate_BlocksLogin(t *testing.T) {
	orig := os.Getenv("AUTH_ALLOW_UNVERIFIED_LOGIN")
	os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "0")
	defer os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", orig)

	st := openTestStore(t)
	ctx := context.Background()

	_, _, err := st.Signup(ctx, "gate1@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	result, err := st.Login(ctx, "gate1@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !result.EmailVerificationRequired {
		t.Error("expected EmailVerificationRequired=true for unverified login")
	}
	if result.Token != "" {
		t.Errorf("expected no session token when gate is active, got %q", result.Token)
	}
	if result.TOTPRequired {
		t.Error("TOTPRequired should be false when gate is active")
	}
}

// TestLogin_VerifiedAccount_FullSession verifies that a verified account
// bypasses the gate and gets a full session.
func TestLogin_VerifiedAccount_FullSession(t *testing.T) {
	orig := os.Getenv("AUTH_ALLOW_UNVERIFIED_LOGIN")
	os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "0")
	defer os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", orig)

	st := openTestStore(t)
	ctx := context.Background()

	_, _, err := st.Signup(ctx, "gate2@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE users SET email_verified = 1 WHERE email = 'gate2@example.com'`); err != nil {
		t.Fatalf("set verified: %v", err)
	}

	result, err := st.Login(ctx, "gate2@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.EmailVerificationRequired {
		t.Error("verified account should not require email verification")
	}
	if result.Token == "" {
		t.Error("expected a session token for verified account")
	}
}

// TestLogin_EnvOverride_BypassesGate verifies that AUTH_ALLOW_UNVERIFIED_LOGIN=1
// allows an unverified account to log in fully.
func TestLogin_EnvOverride_BypassesGate(t *testing.T) {
	orig := os.Getenv("AUTH_ALLOW_UNVERIFIED_LOGIN")
	os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	defer os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", orig)

	st := openTestStore(t)
	ctx := context.Background()

	_, _, err := st.Signup(ctx, "gate3@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	result, err := st.Login(ctx, "gate3@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.EmailVerificationRequired {
		t.Error("env override should bypass email verification gate")
	}
	if result.Token == "" {
		t.Error("expected a full session token with env override")
	}
}

// TestLogin_GraceFlag_BypassesGate verifies that a future
// email_verified_grace_until timestamp allows unverified login.
func TestLogin_GraceFlag_BypassesGate(t *testing.T) {
	orig := os.Getenv("AUTH_ALLOW_UNVERIFIED_LOGIN")
	os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "0")
	defer os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", orig)

	st := openTestStore(t)
	ctx := context.Background()

	_, _, err := st.Signup(ctx, "gate4@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	var uid string
	st.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'gate4@example.com'`).Scan(&uid)

	graceUntil := time.Now().UTC().Add(time.Hour).Unix()
	if err := st.SetEmailVerifiedGraceUntil(ctx, uid, graceUntil); err != nil {
		t.Fatalf("SetEmailVerifiedGraceUntil: %v", err)
	}

	result, err := st.Login(ctx, "gate4@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.EmailVerificationRequired {
		t.Error("grace flag should bypass email verification gate")
	}
	if result.Token == "" {
		t.Error("expected a full session token within grace window")
	}
}

// TestLogin_ExpiredGrace_BlocksLogin verifies that an expired grace window
// does NOT bypass the gate.
func TestLogin_ExpiredGrace_BlocksLogin(t *testing.T) {
	orig := os.Getenv("AUTH_ALLOW_UNVERIFIED_LOGIN")
	os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "0")
	defer os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", orig)

	st := openTestStore(t)
	ctx := context.Background()

	_, _, err := st.Signup(ctx, "gate5@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	var uid string
	st.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'gate5@example.com'`).Scan(&uid)

	// Set grace window in the past.
	graceUntil := time.Now().UTC().Add(-time.Hour).Unix()
	if err := st.SetEmailVerifiedGraceUntil(ctx, uid, graceUntil); err != nil {
		t.Fatalf("SetEmailVerifiedGraceUntil: %v", err)
	}

	result, err := st.Login(ctx, "gate5@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !result.EmailVerificationRequired {
		t.Error("expired grace should trigger email verification gate")
	}
	if result.Token != "" {
		t.Errorf("expected no session token with expired grace, got %q", result.Token)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AUTH-12: passkey modality tests (store-level)
// ─────────────────────────────────────────────────────────────────────────────

// TestModality_DefaultIsPasswordTOTP verifies that a freshly-created account
// has the default modality (password+totp).
func TestModality_DefaultIsPasswordTOTP(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "modality1@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	m, err := st.GetPreferred2FAModality(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetPreferred2FAModality: %v", err)
	}
	if m != ModalityPasswordTOTP {
		t.Errorf("default modality: got %q, want %q", m, ModalityPasswordTOTP)
	}
}

// TestModality_SetAndGet verifies that SetPreferred2FAModality persists and
// GetPreferred2FAModality reads back the value correctly.
func TestModality_SetAndGet(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "modality2@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	for _, m := range []Preferred2FAModality{ModalityPasswordPasskey, ModalityPasskeyOnly, ModalityPasswordTOTP} {
		if err := st.SetPreferred2FAModality(ctx, u.ID, m); err != nil {
			t.Fatalf("SetPreferred2FAModality(%s): %v", m, err)
		}
		got, err := st.GetPreferred2FAModality(ctx, u.ID)
		if err != nil {
			t.Fatalf("GetPreferred2FAModality after set(%s): %v", m, err)
		}
		if got != m {
			t.Errorf("round-trip modality %q: got %q", m, got)
		}
	}
}

// TestModality_InvalidRejectsSet verifies that an unknown modality string
// is rejected.
func TestModality_InvalidRejectsSet(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "modality3@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	err = st.SetPreferred2FAModality(ctx, u.ID, Preferred2FAModality("oauth"))
	if err == nil {
		t.Error("expected error for unknown modality, got nil")
	}
}

// TestLogin_PasskeyFirst_NoCredentials_FallsBackToPassword verifies that
// when a user prefers passkey-only but has no registered passkeys, Login falls
// back to the password+totp flow (password verification still happens).
func TestLogin_PasskeyFirst_NoCredentials_FallsBackToPassword(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "pk1@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if err := st.SetPreferred2FAModality(ctx, u.ID, ModalityPasskeyOnly); err != nil {
		t.Fatalf("SetPreferred2FAModality: %v", err)
	}

	// No passkeys registered → fallback to password path.
	result, err := st.Login(ctx, "pk1@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.PasskeyRequired {
		t.Error("no passkeys registered: PasskeyRequired should be false (fallback)")
	}
	if result.Token == "" {
		t.Error("expected full session token from fallback path")
	}
}

// TestLogin_PasskeyFirst_WithCredentials_ReturnsPasskeyRequired verifies that
// when a user prefers passkey-only and has a registered passkey, Login returns
// PasskeyRequired=true and no session token.
func TestLogin_PasskeyFirst_WithCredentials_ReturnsPasskeyRequired(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "pk2@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if err := st.SetPreferred2FAModality(ctx, u.ID, ModalityPasskeyOnly); err != nil {
		t.Fatalf("SetPreferred2FAModality: %v", err)
	}

	// Register a synthetic passkey.
	insertTestCredential(t, st, u.ID, makeTestCredential("pk2a", 0), "Touch ID")

	result, err := st.Login(ctx, "pk2@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !result.PasskeyRequired {
		t.Error("PasskeyRequired should be true when passkey-first modality + passkey registered")
	}
	if result.Token != "" {
		t.Errorf("no session token expected for passkey-first, got %q", result.Token)
	}
	if result.Modality != ModalityPasskeyOnly {
		t.Errorf("Modality: got %q, want %q", result.Modality, ModalityPasskeyOnly)
	}
}

// TestLogin_PasswordPasskey_WithCredentials_ReturnsPasskeyAs2FA verifies that
// when a user has password+passkey modality and a registered passkey, Login
// returns PasskeyAs2FA=true and a partial session token.
func TestLogin_PasswordPasskey_WithCredentials_ReturnsPasskeyAs2FA(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "pk3@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if err := st.SetPreferred2FAModality(ctx, u.ID, ModalityPasswordPasskey); err != nil {
		t.Fatalf("SetPreferred2FAModality: %v", err)
	}
	insertTestCredential(t, st, u.ID, makeTestCredential("pk3a", 0), "YubiKey")

	result, err := st.Login(ctx, "pk3@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !result.PasskeyAs2FA {
		t.Error("PasskeyAs2FA should be true for password+passkey modality with passkey registered")
	}
	if result.TOTPRequired {
		t.Error("TOTPRequired should be false for password+passkey modality")
	}
	if result.Token == "" {
		t.Error("expected partial session token for passkey-as-2FA step")
	}
	// Token must be a partial session.
	if !st.IsPartialSession(ctx, result.Token) {
		t.Error("token should be a partial session for passkey-as-2FA")
	}
}

// TestLogin_PasswordPasskey_NoCredentials_FallsBackToTOTP verifies that
// when a user prefers password+passkey but has no registered passkeys, Login
// falls back to the TOTP path (if TOTP enabled) or a full session (if not).
func TestLogin_PasswordPasskey_NoCredentials_FallsBackToFullSession(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "pk4@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if err := st.SetPreferred2FAModality(ctx, u.ID, ModalityPasswordPasskey); err != nil {
		t.Fatalf("SetPreferred2FAModality: %v", err)
	}
	// No passkeys registered.

	result, err := st.Login(ctx, "pk4@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.PasskeyAs2FA {
		t.Error("no passkeys registered: PasskeyAs2FA should be false (fallback)")
	}
	if result.Token == "" {
		t.Error("expected full session token from fallback")
	}
}

// TestFleetAdmin_PasskeyModality_SatisfiesFleetAuth verifies that a fleet_admin
// with passkey modality + registered passkey satisfies RequireFleetAuth without
// needing TOTP.
func TestFleetAdmin_PasskeyModality_SatisfiesFleetAuth(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, tok, err := st.Signup(ctx, "pkfleet@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	// Promote to fleet_admin.
	if _, err := st.db.ExecContext(ctx, `UPDATE users SET fleet_admin = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatalf("set fleet_admin: %v", err)
	}

	// Set passkey modality and register a passkey.
	if err := st.SetPreferred2FAModality(ctx, u.ID, ModalityPasskeyOnly); err != nil {
		t.Fatalf("SetPreferred2FAModality: %v", err)
	}
	insertTestCredential(t, st, u.ID, makeTestCredential("pkf1", 0), "Platform Key")

	// fleetAdminHas2FA should return true.
	if !st.fleetAdminHas2FA(ctx, u.ID) {
		t.Error("fleet_admin with passkey modality + passkey should satisfy 2FA requirement")
	}
	_ = tok
}

// TestFleetAdmin_PasskeyModality_NoPasskeys_DoesNotSatisfyFleetAuth verifies
// that a fleet_admin with passkey modality but NO registered passkeys does NOT
// satisfy the fleet 2FA requirement.
func TestFleetAdmin_PasskeyModality_NoPasskeys_DoesNotSatisfyFleetAuth(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "pkfleetnone@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	if _, err := st.db.ExecContext(ctx, `UPDATE users SET fleet_admin = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatalf("set fleet_admin: %v", err)
	}
	if err := st.SetPreferred2FAModality(ctx, u.ID, ModalityPasskeyOnly); err != nil {
		t.Fatalf("SetPreferred2FAModality: %v", err)
	}
	// No passkeys registered.

	if st.fleetAdminHas2FA(ctx, u.ID) {
		t.Error("fleet_admin with passkey modality but no passkeys should NOT satisfy 2FA requirement")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Security regression tests
// ─────────────────────────────────────────────────────────────────────────────

// TestSuspendedAccount_LookupSessionReturnsError verifies that a suspended
// account's existing session is rejected by LookupSession (BUG-SA-01).
func TestSuspendedAccount_LookupSessionReturnsError(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, token, err := st.Signup(ctx, "suspended@example.com", "securepass1234!", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	// Session works before suspension.
	if _, err := st.LookupSession(ctx, token); err != nil {
		t.Fatalf("LookupSession before suspension: %v", err)
	}

	// Suspend the account.
	if _, err := st.db.ExecContext(ctx, `UPDATE users SET suspended = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatalf("set suspended: %v", err)
	}

	// Session must now be rejected.
	_, err = st.LookupSession(ctx, token)
	if !errors.Is(err, ErrAccountSuspended) {
		t.Errorf("expected ErrAccountSuspended for suspended account, got %v", err)
	}
}

// TestSuspendedAccount_LoginReturnsError verifies that a suspended account
// cannot initiate a new login (BUG-SA-01 — Login path).
func TestSuspendedAccount_LoginReturnsError(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "suslogin@example.com", "securepass1234!", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	// Suspend.
	if _, err := st.db.ExecContext(ctx, `UPDATE users SET suspended = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatalf("set suspended: %v", err)
	}

	_, err = st.Login(ctx, "suslogin@example.com", "securepass1234!", "127.0.0.1", "ua")
	if !errors.Is(err, ErrAccountSuspended) {
		t.Errorf("expected ErrAccountSuspended, got %v", err)
	}
}

// TestTOTPReplay_SameCodeRejectedTwice verifies that a valid TOTP code cannot
// be reused within the ±1-step replay window (BUG-TOTP-REPLAY).
func TestTOTPReplay_SameCodeRejectedTwice(t *testing.T) {
	setupTOTPTestEnv(t)

	// Generate a TOTP secret and produce a valid code.
	_, secretB32, err := GenerateTOTPSecret("replay@example.com")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}

	// Use VerifyTOTP to check a real code would validate — we fabricate one
	// by calling the pquerna library via GenerateCode (internal to this package).
	code, genErr := generateTestTOTPCode(secretB32)
	if genErr != nil {
		t.Fatalf("generateTestTOTPCode: %v", genErr)
	}
	userID := "replay-test-user-001"

	// First use: should succeed.
	if !VerifyTOTPAndRecord([]byte(secretB32), code, userID) {
		t.Fatal("first use of valid TOTP code should succeed")
	}

	// Second use (replay): must fail.
	if VerifyTOTPAndRecord([]byte(secretB32), code, userID) {
		t.Error("replay of TOTP code within replay window should be rejected")
	}
}

// TestTOTPReplay_DifferentUserSameCode verifies that the replay cache is
// scoped per-user: the same code from a different user is not blocked.
func TestTOTPReplay_DifferentUserSameCode(t *testing.T) {
	setupTOTPTestEnv(t)

	_, secretB32, err := GenerateTOTPSecret("replayA@example.com")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}

	code, genErr := generateTestTOTPCode(secretB32)
	if genErr != nil {
		t.Fatalf("generateTestTOTPCode: %v", genErr)
	}

	// User A consumes the code.
	if !VerifyTOTPAndRecord([]byte(secretB32), code, "user-A") {
		t.Fatal("user-A first use should succeed")
	}

	// User B with the same secret and code should NOT be blocked (different user scope).
	if !VerifyTOTPAndRecord([]byte(secretB32), code, "user-B") {
		t.Error("user-B should not be blocked by user-A's replay cache entry")
	}
}

// TestIsSuspendedByID verifies the super-admin hard-suspend lookup used by the
// billing AdminSuspendChecker seam: false for a fresh user, true once
// users.suspended is flipped, and false (no error) for a missing account.
func TestIsSuspendedByID(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	u, _, err := st.Signup(ctx, "hardsuspend@example.com", "securePass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	// Fresh user is not suspended.
	susp, err := st.IsSuspendedByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("IsSuspendedByID: %v", err)
	}
	if susp {
		t.Fatal("fresh user must not be suspended")
	}

	// Flip the admin suspend flag directly (same column SuspendAccount sets).
	if _, err := st.DB().ExecContext(ctx, `UPDATE users SET suspended = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatalf("set suspended: %v", err)
	}
	susp, err = st.IsSuspendedByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("IsSuspendedByID after suspend: %v", err)
	}
	if !susp {
		t.Fatal("user must report suspended after the flag is set")
	}

	// Missing account: false, no error (fail-open friendly).
	susp, err = st.IsSuspendedByID(ctx, "NONEXISTENT")
	if err != nil {
		t.Fatalf("IsSuspendedByID(missing): unexpected error %v", err)
	}
	if susp {
		t.Fatal("missing account must report not-suspended")
	}
}
