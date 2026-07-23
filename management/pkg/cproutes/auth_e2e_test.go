// auth_e2e_test.go -- end-to-end integration tests for the Vulos Cloud auth surface.
//
// Exercises the full auth flow against an in-process server using httptest.
// All tests run against an isolated in-memory SQLite store so they are
// hermetic, parallel-safe, and require no external services.
//
// Flows covered:
//
//  1. Signup with email + password (≥12 chars) → email-verification challenge.
//  2. Verify email via token → success; repeated use → 410.
//  3. Login without 2FA → full session issued (no step challenge).
//  4. Enroll TOTP (HTTP flow: /totp/enroll then /totp/confirm with live code).
//  5. Login again with TOTP-enrolled account → 2FA challenge → submit TOTP → session.
//  6. Enroll passkey (store-level via synthetic credential insert; no browser).
//  7. Login via passkey (passkey-first modality) → passkey_required step issued.
//  8. PIN-auth analogue — short-lived token issued on demand for authenticated user
//     (implemented as a custom helper wrapping the session store; no dedicated PIN
//     endpoint exists in the current server, so we test the create/validate cycle
//     at the store level and verify the session is full-scoped).
//  9. Reset password via email-link flow → all sessions revoked afterwards.
//  10. Mandatory 2FA gate — fleet_admin without TOTP → login succeeds but
//     RequireFleetAuth returns 403; fleet route must be refused.
//  11. Login broker (cross-instance) — issue signed JWT for instance-id, verify
//     audience + expiry + signature using the same key that signed it.
package cproutes

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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

	"github.com/pquerna/otp/totp"
	"github.com/vul-os/vulos-management/pkg/auth"
)

// ─────────────────────────────────────────────────────────────────────────────
// Package-level setup
// ─────────────────────────────────────────────────────────────────────────────

func init() {
	// Allow unverified logins by default so baseline tests are unaffected;
	// tests that need the gate explicitly override with t.Setenv.
	if os.Getenv("AUTH_ALLOW_UNVERIFIED_LOGIN") == "" {
		os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	}
	if os.Getenv("HIBP_CHECK_ENABLED") == "" {
		os.Setenv("HIBP_CHECK_ENABLED", "0")
	}
	// Dev-mode KEK fallback so TOTP + device tests work without env secrets.
	if os.Getenv("VULOS_DEV") == "" {
		os.Setenv("VULOS_DEV", "true")
	}
	// Use cheap Argon2 parameters in tests to prevent CPU-heavy hashing from
	// starving parallel goroutines and causing timeout flakiness.
	// ARGON2_TEST_FAST must never be set outside of test environments.
	if os.Getenv("ARGON2_TEST_FAST") == "" {
		os.Setenv("ARGON2_TEST_FAST", "1")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// e2eStoreN is atomic so parallel subtests never share a DSN.
var e2eStoreN atomic.Int64

// openE2EStore creates an isolated in-memory auth store.
func openE2EStore(t *testing.T) *auth.Store {
	t.Helper()
	dsn := fmt.Sprintf("file:e2eauth%d?mode=memory&cache=shared", e2eStoreN.Add(1))
	st, err := openAuthStoreForTest(dsn, []byte("e2e-secret-key-1234567890123456"))
	if err != nil {
		t.Fatalf("openE2EStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// e2eMux builds a mux with both auth and WebAuthn routes registered.
func e2eMux(t *testing.T, st *auth.Store) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	RegisterAuthRoutes(mux, st)
	RegisterWebAuthnRoutes(mux, st)
	return mux
}

// post issues a POST to path with a JSON body, optionally attaching a session cookie.
func e2ePost(mux *http.ServeMux, path, bodyJSON, sessionTok string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9000"
	if sessionTok != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionTok})
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// get issues a GET to path with an optional session cookie.
func e2eGet(mux *http.ServeMux, path, sessionTok string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:9000"
	if sessionTok != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionTok})
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// cookieValue extracts the named cookie value from a response recorder.
func cookieValue(rr *httptest.ResponseRecorder, name string) string {
	for _, c := range rr.Result().Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// jsonBody decodes the response body as map[string]any, failing on parse error.
func jsonBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&m); err != nil {
		t.Fatalf("jsonBody decode: %v (status=%d body=%s)", err, rr.Code, rr.Body.String())
	}
	return m
}

// extractLatestVerifyToken reads the most-recently-created unused email
// verification token for userID directly from the DB.
func extractLatestVerifyToken(t *testing.T, st *auth.Store, userID string) string {
	t.Helper()
	var tok string
	err := st.DB().QueryRowContext(context.Background(),
		`SELECT token FROM email_verification_tokens
		 WHERE user_id = ? AND used = 0
		 ORDER BY created_at DESC LIMIT 1`, userID,
	).Scan(&tok)
	if err != nil {
		t.Fatalf("extractLatestVerifyToken: %v", err)
	}
	return tok
}

// extractLatestResetToken reads the most-recently-created unused password
// reset token for the given email directly from the DB.
func extractLatestResetToken(t *testing.T, st *auth.Store, email string) string {
	t.Helper()
	var tok string
	err := st.DB().QueryRowContext(context.Background(),
		`SELECT pr.token FROM password_resets pr
		 JOIN users u ON u.id = pr.user_id
		 WHERE u.email = ? AND pr.used = 0
		 ORDER BY pr.created_at DESC LIMIT 1`, email,
	).Scan(&tok)
	if err != nil {
		t.Fatalf("extractLatestResetToken: %v", err)
	}
	return tok
}

// e2eEnableTOTP installs an encrypted TOTP secret directly on userID and
// returns the raw base32 secret so tests can generate live codes.
// Does NOT hash recovery codes (argon2 would be too slow for a helper function).
func e2eEnableTOTP(t *testing.T, st *auth.Store, userID, email string) string {
	t.Helper()
	ctx := context.Background()
	_, secretB32, err := auth.GenerateTOTPSecret(email)
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	kek, _ := auth.LoadTOTPKEK()
	enc, err := auth.EncryptTOTPSecret([]byte(secretB32), kek)
	if err != nil {
		t.Fatalf("EncryptTOTPSecret: %v", err)
	}
	if err := st.SaveTOTPSecret(ctx, userID, enc); err != nil {
		t.Fatalf("SaveTOTPSecret: %v", err)
	}
	if err := st.EnableTOTP(ctx, userID); err != nil {
		t.Fatalf("EnableTOTP: %v", err)
	}
	return secretB32
}

// insertWebAuthnCredential inserts a synthetic WebAuthn credential row
// directly into the DB.  Used to avoid needing a browser for WebAuthn tests.
func insertWebAuthnCredential(t *testing.T, st *auth.Store, userID string) (rowID, credentialID string) {
	t.Helper()
	ctx := context.Background()
	rowID = fmt.Sprintf("e2ecred%d", e2eStoreN.Load())
	credentialID = base64.RawURLEncoding.EncodeToString([]byte("e2e_test_credential_" + rowID))
	_, err := st.DB().ExecContext(ctx,
		`INSERT INTO webauthn_credentials
		   (id, user_id, credential_id, public_key, sign_count,
		    transports, friendly_name, aaguid, last_used_at, created_at)
		 VALUES (?, ?, ?, ?, 0, '["internal"]', 'E2E Key', '', NULL, ?)`,
		rowID, userID, credentialID, []byte("fakepubkey_e2e"),
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insertWebAuthnCredential: %v", err)
	}
	return rowID, credentialID
}

// getUserID fetches the internal ID for an email directly from the DB.
func getUserID(t *testing.T, st *auth.Store, email string) string {
	t.Helper()
	var uid string
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT id FROM users WHERE email = ?`, email,
	).Scan(&uid); err != nil {
		t.Fatalf("getUserID(%s): %v", email, err)
	}
	return uid
}

// liveTOTPCode generates the current 6-digit TOTP code for secretB32.
func liveTOTPCode(t *testing.T, secretB32 string) string {
	t.Helper()
	code, err := totp.GenerateCode(secretB32, time.Now().UTC())
	if err != nil {
		t.Fatalf("liveTOTPCode: %v", err)
	}
	return code
}

// ─────────────────────────────────────────────────────────────────────────────
// Flow 1: Signup → email-verification challenge
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_Signup_EmailVerificationChallenge verifies that:
//   - POST /api/auth/signup with email+password (≥12 chars) returns 200 with user object.
//   - An email verification token is created in the DB.
//   - The signup session cookie is issued.
func TestE2E_Signup_EmailVerificationChallenge(t *testing.T) {
	st := openE2EStore(t)
	mux := e2eMux(t, st)

	rr := e2ePost(mux, "/api/auth/signup",
		`{"handle":"e2e_signup","password":"strongpassword1234!"}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("signup: expected 200, got %d %s", rr.Code, rr.Body.String())
	}

	body := jsonBody(t, rr)
	userID, ok := body["user_id"].(string)
	if !ok || userID == "" {
		t.Fatalf("signup: expected user_id in response; got %v", body)
	}

	// Session cookie must be present.
	tok := cookieValue(rr, auth.SessionCookieName)
	if tok == "" {
		t.Fatal("signup: expected session cookie; got none")
	}

	// An unused email verification token must exist.
	vtok := extractLatestVerifyToken(t, st, userID)
	if vtok == "" {
		t.Error("signup: expected email verification token in DB")
	}
}

// TestE2E_Signup_ShortPassword_Rejected verifies that a password shorter than
// 12 chars is rejected with 400.
func TestE2E_Signup_ShortPassword_Rejected(t *testing.T) {
	st := openE2EStore(t)
	mux := e2eMux(t, st)

	rr := e2ePost(mux, "/api/auth/signup",
		`{"handle":"e2e_shortpw","password":"short"}`, "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("short password signup: expected 400, got %d %s", rr.Code, rr.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Flow 2: Verify email → success; repeated use → 410
// ─────────────────────────────────────────────────────────────────────────────

func TestE2E_VerifyEmail_Success(t *testing.T) {
	st := openE2EStore(t)
	mux := e2eMux(t, st)

	rr := e2ePost(mux, "/api/auth/signup",
		`{"handle":"e2e_verify","password":"strongpassword1234!"}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("signup: %d %s", rr.Code, rr.Body.String())
	}
	body := jsonBody(t, rr)
	userID := body["user_id"].(string)

	vtok := extractLatestVerifyToken(t, st, userID)

	// Consume the token.
	rr2 := e2eGet(mux, "/api/auth/verify-email?token="+vtok, "")
	if rr2.Code != http.StatusOK {
		t.Fatalf("verify-email: expected 200, got %d %s", rr2.Code, rr2.Body.String())
	}
	resp2 := jsonBody(t, rr2)
	if resp2["email_verified"] != true {
		t.Errorf("verify-email response: expected email_verified=true; got %v", resp2)
	}

	// Consuming a second time must return 410.
	rr3 := e2eGet(mux, "/api/auth/verify-email?token="+vtok, "")
	if rr3.Code != http.StatusGone {
		t.Errorf("second use of verify token: expected 410, got %d", rr3.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Flow 3: Login without 2FA → full session issued
// ─────────────────────────────────────────────────────────────────────────────

func TestE2E_Login_NoTOTP_IssuesFullSession(t *testing.T) {
	// Ensure unverified logins are allowed so we don't need the email-verify step.
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")

	st := openE2EStore(t)
	mux := e2eMux(t, st)

	e2ePost(mux, "/api/auth/signup",
		`{"handle":"e2e_login_nototp","password":"strongpassword1234!"}`, "")

	rr := e2ePost(mux, "/api/auth/login",
		`{"email":"e2e_login_nototp@vulos.org","password":"strongpassword1234!"}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("login: expected 200, got %d %s", rr.Code, rr.Body.String())
	}

	// Body must NOT be a step challenge.
	resp := jsonBody(t, rr)
	if step, ok := resp["step"]; ok {
		t.Errorf("no-TOTP login returned unexpected step: %v", step)
	}

	// Full session cookie must be set.
	tok := cookieValue(rr, auth.SessionCookieName)
	if tok == "" {
		t.Fatal("login: expected session cookie; got none")
	}

	// /me must succeed with the session.
	rr2 := e2eGet(mux, "/api/auth/me", tok)
	if rr2.Code != http.StatusOK {
		t.Fatalf("/me: expected 200, got %d %s", rr2.Code, rr2.Body.String())
	}
	me := jsonBody(t, rr2)
	if me["email"] != "e2e_login_nototp@vulos.org" {
		t.Errorf("/me email mismatch: %v", me["email"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Flow 4: Enroll TOTP via HTTP endpoints
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_TOTPEnroll_Via_HTTP tests the full HTTP /totp/enroll → /totp/confirm
// cycle using a live TOTP code generated from the secret returned by /enroll.
func TestE2E_TOTPEnroll_Via_HTTP(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	t.Setenv("VULOS_DEV", "true")
	t.Setenv("AUTH_TOTP_KEK", "")

	st := openE2EStore(t)
	mux := e2eMux(t, st)

	// Sign up and log in.
	rr := e2ePost(mux, "/api/auth/signup",
		`{"handle":"e2e_totp_enroll","password":"strongpassword1234!"}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("signup: %d", rr.Code)
	}
	sessionTok := cookieValue(rr, auth.SessionCookieName)

	// POST /totp/enroll — must return uri + secret + recovery_codes.
	rr2 := e2ePost(mux, "/api/auth/totp/enroll", "", sessionTok)
	if rr2.Code != http.StatusOK {
		t.Fatalf("/totp/enroll: expected 200, got %d %s", rr2.Code, rr2.Body.String())
	}
	enroll := jsonBody(t, rr2)
	secretB32, ok := enroll["secret"].(string)
	if !ok || secretB32 == "" {
		t.Fatalf("/totp/enroll: expected secret in response; got %v", enroll)
	}
	codes, ok := enroll["recovery_codes"].([]interface{})
	if !ok || len(codes) == 0 {
		t.Fatalf("/totp/enroll: expected recovery_codes in response; got %v", enroll)
	}

	// Generate a live TOTP code from the returned secret.
	code := liveTOTPCode(t, secretB32)

	// POST /totp/confirm.
	confirmBody := fmt.Sprintf(`{"code":%q}`, code)
	rr3 := e2ePost(mux, "/api/auth/totp/confirm", confirmBody, sessionTok)
	if rr3.Code != http.StatusNoContent {
		t.Fatalf("/totp/confirm: expected 204, got %d %s", rr3.Code, rr3.Body.String())
	}

	// TOTP must now be enabled in the DB.
	userID := getUserID(t, st, "e2e_totp_enroll@vulos.org")
	if !st.IsTOTPEnabled(context.Background(), userID) {
		t.Error("TOTP should be enabled after /totp/confirm")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Flow 5: Login with TOTP → step:totp_required → verify → full session
// ─────────────────────────────────────────────────────────────────────────────

func TestE2E_Login_TOTPFlow(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	t.Setenv("VULOS_DEV", "true")
	t.Setenv("AUTH_TOTP_KEK", "")

	st := openE2EStore(t)
	mux := e2eMux(t, st)

	// Sign up.
	rr := e2ePost(mux, "/api/auth/signup",
		`{"handle":"e2e_totp_login","password":"strongpassword1234!"}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("signup: %d %s", rr.Code, rr.Body.String())
	}
	userID := getUserID(t, st, "e2e_totp_login@vulos.org")

	// Install TOTP directly.
	secretB32 := e2eEnableTOTP(t, st, userID, "e2e_totp_login@vulos.org")

	// Login → must return step:totp_required + partial session cookie.
	rr2 := e2ePost(mux, "/api/auth/login",
		`{"email":"e2e_totp_login@vulos.org","password":"strongpassword1234!"}`, "")
	if rr2.Code != http.StatusOK {
		t.Fatalf("login with TOTP: expected 200, got %d %s", rr2.Code, rr2.Body.String())
	}
	resp2 := jsonBody(t, rr2)
	if resp2["step"] != "totp_required" {
		t.Fatalf("login with TOTP: expected step=totp_required, got %v", resp2["step"])
	}
	partialTok := cookieValue(rr2, auth.SessionCookieName)
	if partialTok == "" {
		t.Fatal("login with TOTP: expected partial session cookie; got none")
	}

	// POST /totp/verify with live code → must upgrade to full session.
	code := liveTOTPCode(t, secretB32)
	verifyBody := fmt.Sprintf(`{"code":%q}`, code)
	rr3 := e2ePost(mux, "/api/auth/totp/verify", verifyBody, partialTok)
	if rr3.Code != http.StatusNoContent {
		t.Fatalf("/totp/verify: expected 204, got %d %s", rr3.Code, rr3.Body.String())
	}
	fullTok := cookieValue(rr3, auth.SessionCookieName)
	if fullTok == "" {
		t.Fatal("/totp/verify: expected full session cookie; got none")
	}

	// Verify the full session works.
	rr4 := e2eGet(mux, "/api/auth/me", fullTok)
	if rr4.Code != http.StatusOK {
		t.Fatalf("/me after TOTP verify: expected 200, got %d %s", rr4.Code, rr4.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Flow 6: Enroll passkey (store-level synthetic insert)
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_PasskeyEnroll_StoreLevel verifies that after inserting a synthetic
// WebAuthn credential, the credential appears in the list endpoint and the
// account shows at least one passkey.
func TestE2E_PasskeyEnroll_StoreLevel(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")

	st := openE2EStore(t)
	mux := e2eMux(t, st)

	rr := e2ePost(mux, "/api/auth/signup",
		`{"handle":"e2e_passkey_enroll","password":"strongpassword1234!"}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("signup: %d", rr.Code)
	}
	sessionTok := cookieValue(rr, auth.SessionCookieName)
	userID := getUserID(t, st, "e2e_passkey_enroll@vulos.org")

	// Insert a synthetic credential.
	rowID, credID := insertWebAuthnCredential(t, st, userID)
	if rowID == "" || credID == "" {
		t.Fatal("insertWebAuthnCredential returned empty IDs")
	}

	// GET /api/auth/webauthn/credentials must list the credential.
	rr2 := e2eGet(mux, "/api/auth/webauthn/credentials", sessionTok)
	if rr2.Code != http.StatusOK {
		t.Fatalf("list credentials: expected 200, got %d %s", rr2.Code, rr2.Body.String())
	}
	resp := jsonBody(t, rr2)
	credsRaw, ok := resp["credentials"].([]interface{})
	if !ok {
		t.Fatalf("list credentials: expected credentials array; got %T %v", resp["credentials"], resp["credentials"])
	}
	if len(credsRaw) == 0 {
		t.Error("list credentials: expected at least one credential after insert")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Flow 7: Login via passkey (passkey-first modality) → step returned
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_PasskeyLogin_StepReturned verifies that when the "passkey" modality
// is set and a passkey is registered, POST /api/auth/login returns
// {"step":"passkey_required"} and no session cookie.
//
// A full passkey assertion ceremony requires a real FIDO2 authenticator / browser,
// which is not available in unit tests. We verify the protocol-level step signal
// here; the WebAuthn store tests (webauthn_test.go) cover the credential CRUD.
func TestE2E_PasskeyLogin_StepReturned(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")

	st := openE2EStore(t)
	mux := e2eMux(t, st)

	rr := e2ePost(mux, "/api/auth/signup",
		`{"handle":"e2e_pk_login","password":"strongpassword1234!"}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("signup: %d", rr.Code)
	}
	userID := getUserID(t, st, "e2e_pk_login@vulos.org")

	// Install synthetic passkey + set passkey-only modality.
	insertWebAuthnCredential(t, st, userID)
	ctx := context.Background()
	if err := st.SetPreferred2FAModality(ctx, userID, auth.ModalityPasskeyOnly); err != nil {
		t.Fatalf("SetPreferred2FAModality: %v", err)
	}

	// Login must return passkey_required step.
	rr2 := e2ePost(mux, "/api/auth/login",
		`{"email":"e2e_pk_login@vulos.org","password":"strongpassword1234!"}`, "")
	if rr2.Code != http.StatusOK {
		t.Fatalf("passkey-first login: expected 200, got %d %s", rr2.Code, rr2.Body.String())
	}
	resp := jsonBody(t, rr2)
	if resp["step"] != "passkey_required" {
		t.Errorf("passkey-first login: expected step=passkey_required, got %v", resp["step"])
	}

	// No full session cookie.
	for _, c := range rr2.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			t.Errorf("no session cookie expected for passkey-first step; got value %s", c.Value)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Flow 8: Short-lived token (PIN analogue)
//
// No dedicated PIN endpoint exists in the current server; however the store
// provides all the primitives for a short-lived scoped token: create a
// partial session (short TTL), validate it, then upgrade it to a full session.
// This test verifies that the short-TTL partial session round-trip works and
// that the upgraded token is a full (non-partial) session.
// ─────────────────────────────────────────────────────────────────────────────

func TestE2E_ShortLivedToken_PartialSessionRoundTrip(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")

	st := openE2EStore(t)
	ctx := context.Background()

	// Create a user directly.
	u, _, err := st.Signup(ctx, "e2e_pin@vulos.org", "strongpassword1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	// Issue a short-lived partial session (mirrors what the login handler does).
	partialTok, err := st.CreatePartialSession(ctx, u.ID, "127.0.0.1", "testua")
	if err != nil {
		t.Fatalf("CreatePartialSession: %v", err)
	}
	if partialTok == "" {
		t.Fatal("CreatePartialSession returned empty token")
	}

	// The partial session must be recognised as partial.
	if !st.IsPartialSession(ctx, partialTok) {
		t.Error("partial session token should be recognised as partial")
	}

	// LookupSession (full session path) must REJECT the partial token.
	_, err = st.LookupSession(ctx, partialTok)
	if err == nil {
		t.Error("LookupSession should reject partial session token")
	}

	// LookupPartialSession must succeed.
	gotUID, err := st.LookupPartialSession(ctx, partialTok)
	if err != nil {
		t.Fatalf("LookupPartialSession: %v", err)
	}
	if gotUID != u.ID {
		t.Errorf("LookupPartialSession userID: got %q, want %q", gotUID, u.ID)
	}

	// Upgrade to a full session.
	fullTok, err := st.UpgradePartialSession(ctx, partialTok, "127.0.0.1", "testua")
	if err != nil {
		t.Fatalf("UpgradePartialSession: %v", err)
	}

	// Full session must work.
	user, err := st.LookupSession(ctx, fullTok)
	if err != nil {
		t.Fatalf("LookupSession after upgrade: %v", err)
	}
	if user.ID != u.ID {
		t.Errorf("full session userID: got %q, want %q", user.ID, u.ID)
	}

	// Old partial token must be invalid now.
	if _, err := st.LookupPartialSession(ctx, partialTok); err == nil {
		t.Error("old partial token should be invalid after upgrade")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Flow 9: Password reset → all sessions revoked
// ─────────────────────────────────────────────────────────────────────────────

func TestE2E_PasswordReset_RevokesSessions(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")

	st := openE2EStore(t)
	mux := e2eMux(t, st)

	const email = "e2e_pwreset@vulos.org"
	const pw = "strongpassword1234!"
	const newpw = "newstrongpassword5678!"

	// Signup → get session A.
	rr := e2ePost(mux, "/api/auth/signup",
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, pw), "")
	if rr.Code != http.StatusOK {
		t.Fatalf("signup: %d %s", rr.Code, rr.Body.String())
	}
	sessionA := cookieValue(rr, auth.SessionCookieName)

	// Login again → get session B.
	rr2 := e2ePost(mux, "/api/auth/login",
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, pw), "")
	if rr2.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rr2.Code, rr2.Body.String())
	}
	sessionB := cookieValue(rr2, auth.SessionCookieName)

	// Both sessions should be valid now.
	if rr3 := e2eGet(mux, "/api/auth/me", sessionA); rr3.Code != http.StatusOK {
		t.Fatalf("sessionA should be valid before reset: %d", rr3.Code)
	}
	if rr4 := e2eGet(mux, "/api/auth/me", sessionB); rr4.Code != http.StatusOK {
		t.Fatalf("sessionB should be valid before reset: %d", rr4.Code)
	}

	// Initiate password reset.
	rr5 := e2ePost(mux, "/api/auth/password/reset/request",
		fmt.Sprintf(`{"email":%q}`, email), "")
	if rr5.Code != http.StatusNoContent {
		t.Fatalf("/password/reset/request: expected 204, got %d %s", rr5.Code, rr5.Body.String())
	}

	// Extract the reset token from the DB.
	resetTok := extractLatestResetToken(t, st, email)
	if resetTok == "" {
		t.Fatal("no password reset token found in DB")
	}

	// Confirm the reset with new password.
	confirmBody := fmt.Sprintf(`{"token":%q,"new_password":%q}`, resetTok, newpw)
	rr6 := e2ePost(mux, "/api/auth/password/reset/confirm", confirmBody, "")
	if rr6.Code != http.StatusNoContent {
		t.Fatalf("/password/reset/confirm: expected 204, got %d %s", rr6.Code, rr6.Body.String())
	}

	// Both old sessions must now be invalid (revoked by password reset).
	if rr7 := e2eGet(mux, "/api/auth/me", sessionA); rr7.Code != http.StatusUnauthorized {
		t.Errorf("sessionA after reset: expected 401, got %d", rr7.Code)
	}
	if rr8 := e2eGet(mux, "/api/auth/me", sessionB); rr8.Code != http.StatusUnauthorized {
		t.Errorf("sessionB after reset: expected 401, got %d", rr8.Code)
	}

	// Login with new password must work.
	rr9 := e2ePost(mux, "/api/auth/login",
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, newpw), "")
	if rr9.Code != http.StatusOK {
		t.Fatalf("login with new password: expected 200, got %d %s", rr9.Code, rr9.Body.String())
	}
	newSess := cookieValue(rr9, auth.SessionCookieName)
	if newSess == "" {
		t.Fatal("expected session cookie after login with new password")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Flow 10: Mandatory 2FA gate — fleet_admin without TOTP must be refused
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_FleetAdmin_Mandatory2FA_Gate verifies that:
//   - A fleet_admin can log in (password check passes, session is issued).
//   - Accessing fleet routes via RequireFleetAuth returns 403 when TOTP is absent.
func TestE2E_FleetAdmin_Mandatory2FA_Gate(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")

	st := openE2EStore(t)
	ctx := context.Background()

	// Create user and promote to fleet_admin.
	_, tok, err := st.Signup(ctx, "e2e_fleetadmin@vulos.org", "strongpassword1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	uid := getUserID(t, st, "e2e_fleetadmin@vulos.org")
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE users SET fleet_admin = 1 WHERE id = ?`, uid,
	); err != nil {
		t.Fatalf("set fleet_admin: %v", err)
	}

	// The session is valid (login succeeded).
	u, err := st.LookupSession(ctx, tok)
	if err != nil {
		t.Fatalf("LookupSession: %v", err)
	}
	if !u.FleetAdmin {
		t.Fatal("expected fleet_admin=true on user")
	}

	// RequireFleetAuth must block access (no TOTP).
	reached := false
	handler := st.RequireFleetAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/fleet/devices", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("fleet_admin without TOTP: expected 403, got %d %s", rr.Code, rr.Body.String())
	}
	if reached {
		t.Error("fleet_admin without TOTP: handler must NOT be reached")
	}
	if !strings.Contains(rr.Body.String(), "totp_required") {
		t.Errorf("fleet 403 body must contain 'totp_required'; got: %s", rr.Body.String())
	}
}

// TestE2E_FleetAdmin_With2FA_Passes verifies that a fleet_admin WITH TOTP
// enabled is allowed through RequireFleetAuth.
func TestE2E_FleetAdmin_With2FA_Passes(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	t.Setenv("VULOS_DEV", "true")
	t.Setenv("AUTH_TOTP_KEK", "")

	st := openE2EStore(t)
	ctx := context.Background()

	_, tok, err := st.Signup(ctx, "e2e_fleetadmin2fa@example.com", "strongpassword1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	uid := getUserID(t, st, "e2e_fleetadmin2fa@example.com")
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE users SET fleet_admin = 1 WHERE id = ?`, uid,
	); err != nil {
		t.Fatalf("set fleet_admin: %v", err)
	}

	// Enable TOTP.
	e2eEnableTOTP(t, st, uid, "e2e_fleetadmin2fa@example.com")

	// RequireFleetAuth must now allow access.
	reached := false
	handler := st.RequireFleetAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
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
	if !reached {
		t.Error("fleet_admin with TOTP: handler should have been reached")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Flow 11: Login broker (cross-instance) — signed JWT
//
// No existing /api/auth/broker endpoint exists yet; we implement the signing
// and verification logic here in test helpers to demonstrate the pattern and
// verify correctness of the HMAC-SHA256 JWT.  This test would be extended when
// the actual endpoint is added.
// ─────────────────────────────────────────────────────────────────────────────

// brokerToken is a minimal HS256 JWT-compatible signed token for cross-instance auth.
type brokerToken struct {
	Header    map[string]string
	Claims    map[string]any
	Signature []byte
	Raw       string
}

// issueBrokerToken creates a signed broker token for the given instanceID.
// key is the shared HMAC-SHA256 key; ttl is the token lifetime.
func issueBrokerToken(t *testing.T, key []byte, issuer, audience, instanceID string, ttl time.Duration) brokerToken {
	t.Helper()
	now := time.Now().UTC()

	header := map[string]string{
		"alg": "HS256",
		"typ": "JWT",
	}
	claims := map[string]any{
		"iss":         issuer,
		"aud":         audience,
		"instance_id": instanceID,
		"iat":         now.Unix(),
		"exp":         now.Add(ttl).Unix(),
		"jti":         func() string { b := make([]byte, 8); rand.Read(b); return base64.RawURLEncoding.EncodeToString(b) }(),
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerB64 + "." + claimsB64

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signingInput))
	sig := mac.Sum(nil)

	return brokerToken{
		Header:    header,
		Claims:    claims,
		Signature: sig,
		Raw:       signingInput + "." + base64.RawURLEncoding.EncodeToString(sig),
	}
}

// verifyBrokerToken validates a raw broker token string.
// Returns the claims on success, or an error.
func verifyBrokerToken(raw string, key []byte, expectedAudience string) (map[string]any, error) {
	parts := strings.SplitN(raw, ".", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed token: expected 3 dot-separated parts")
	}

	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signingInput))
	expected := mac.Sum(nil)

	gotSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("base64 decode signature: %w", err)
	}
	if !hmac.Equal(expected, gotSig) {
		return nil, fmt.Errorf("signature mismatch")
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("base64 decode claims: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("unmarshal claims: %w", err)
	}

	// Check expiry.
	exp, ok := claims["exp"].(float64)
	if !ok {
		return nil, fmt.Errorf("missing exp claim")
	}
	if time.Now().Unix() > int64(exp) {
		return nil, fmt.Errorf("token expired")
	}

	// Check audience.
	aud, _ := claims["aud"].(string)
	if aud != expectedAudience {
		return nil, fmt.Errorf("audience mismatch: got %q, want %q", aud, expectedAudience)
	}

	return claims, nil
}

// TestE2E_LoginBroker_SignedToken verifies that a broker token:
//   - Contains the correct audience and instance_id.
//   - Has a valid expiry in the future.
//   - Passes signature verification with the signing key.
//   - Fails verification with a different key.
//   - Fails verification with a tampered audience.
func TestE2E_LoginBroker_SignedToken(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	const issuer = "vulos-cloud-cp"
	const audience = "instance-abc123"
	const instanceID = "inst_01HXTEST0000000001"
	const ttl = 5 * time.Minute

	tok := issueBrokerToken(t, key, issuer, audience, instanceID, ttl)

	// Raw token must be non-empty.
	if tok.Raw == "" {
		t.Fatal("issueBrokerToken: empty raw token")
	}

	// Verify correct audience + signature.
	claims, err := verifyBrokerToken(tok.Raw, key, audience)
	if err != nil {
		t.Fatalf("verifyBrokerToken: %v", err)
	}

	// audience claim.
	if claims["aud"] != audience {
		t.Errorf("audience: got %v, want %v", claims["aud"], audience)
	}

	// instance_id claim.
	if claims["instance_id"] != instanceID {
		t.Errorf("instance_id: got %v, want %v", claims["instance_id"], instanceID)
	}

	// exp must be in the future.
	exp, _ := claims["exp"].(float64)
	if int64(exp) <= time.Now().Unix() {
		t.Errorf("exp should be in the future; got %v (now=%v)", int64(exp), time.Now().Unix())
	}

	// exp must be within ttl + small tolerance.
	delta := int64(exp) - time.Now().Unix()
	if delta > int64(ttl.Seconds())+5 || delta < int64(ttl.Seconds())-5 {
		t.Errorf("exp delta out of range: got %ds, want ~%ds", delta, int64(ttl.Seconds()))
	}

	// Wrong key → signature failure.
	wrongKey := make([]byte, 32)
	if _, err := verifyBrokerToken(tok.Raw, wrongKey, audience); err == nil {
		t.Error("verifyBrokerToken with wrong key: expected error, got nil")
	}

	// Tampered audience → verification failure.
	if _, err := verifyBrokerToken(tok.Raw, key, "wrong-audience"); err == nil {
		t.Error("verifyBrokerToken with wrong audience: expected error, got nil")
	}
}

// TestE2E_LoginBroker_ExpiredToken_Rejected verifies that an expired broker
// token is rejected by the verifier.
func TestE2E_LoginBroker_ExpiredToken_Rejected(t *testing.T) {
	key := []byte("test-key-32-bytes-for-hmac-sha256")

	// Issue a token that is already expired (negative TTL).
	tok := issueBrokerToken(t, key, "cp", "target-instance", "inst_expired", -1*time.Minute)

	_, err := verifyBrokerToken(tok.Raw, key, "target-instance")
	if err == nil {
		t.Error("expired broker token: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected 'expired' in error, got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Auxiliary: logout clears session
// ─────────────────────────────────────────────────────────────────────────────

func TestE2E_Logout_ClearsSession(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")

	st := openE2EStore(t)
	mux := e2eMux(t, st)

	rr := e2ePost(mux, "/api/auth/signup",
		`{"handle":"e2e_logout","password":"strongpassword1234!"}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("signup: %d", rr.Code)
	}
	tok := cookieValue(rr, auth.SessionCookieName)

	// /me works before logout.
	if rr2 := e2eGet(mux, "/api/auth/me", tok); rr2.Code != http.StatusOK {
		t.Fatalf("/me before logout: expected 200, got %d", rr2.Code)
	}

	// POST /logout.
	rr3 := e2ePost(mux, "/api/auth/logout", "", tok)
	if rr3.Code != http.StatusNoContent {
		t.Fatalf("logout: expected 204, got %d %s", rr3.Code, rr3.Body.String())
	}

	// /me must fail after logout.
	if rr4 := e2eGet(mux, "/api/auth/me", tok); rr4.Code != http.StatusUnauthorized {
		t.Errorf("/me after logout: expected 401, got %d", rr4.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Auxiliary: session listing + revocation
// ─────────────────────────────────────────────────────────────────────────────

func TestE2E_SessionManagement(t *testing.T) {
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")

	st := openE2EStore(t)
	mux := e2eMux(t, st)

	const email = "e2e_sessions@vulos.org"
	rr := e2ePost(mux, "/api/auth/signup",
		fmt.Sprintf(`{"email":%q,"password":"strongpassword1234!"}`, email), "")
	if rr.Code != http.StatusOK {
		t.Fatalf("signup: %d", rr.Code)
	}
	tok1 := cookieValue(rr, auth.SessionCookieName)

	// Second login → second session.
	rr2 := e2ePost(mux, "/api/auth/login",
		fmt.Sprintf(`{"email":%q,"password":"strongpassword1234!"}`, email), "")
	if rr2.Code != http.StatusOK {
		t.Fatalf("login: %d", rr2.Code)
	}
	tok2 := cookieValue(rr2, auth.SessionCookieName)

	// List sessions — must see 2.
	rr3 := e2eGet(mux, "/api/auth/sessions", tok1)
	if rr3.Code != http.StatusOK {
		t.Fatalf("list sessions: expected 200, got %d", rr3.Code)
	}
	sessResp := jsonBody(t, rr3)
	sessions, ok := sessResp["sessions"].([]interface{})
	if !ok || len(sessions) < 2 {
		t.Fatalf("expected ≥2 sessions; got %v", sessResp["sessions"])
	}

	// Revoke tok2 from tok1.
	revokeBody := fmt.Sprintf(`{"session_id":%q}`, tok2)
	rr4 := e2ePost(mux, "/api/auth/sessions/revoke", revokeBody, tok1)
	if rr4.Code != http.StatusNoContent {
		t.Fatalf("revoke tok2: expected 204, got %d %s", rr4.Code, rr4.Body.String())
	}

	// tok2 must now be invalid.
	if rr5 := e2eGet(mux, "/api/auth/me", tok2); rr5.Code != http.StatusUnauthorized {
		t.Errorf("revoked session tok2: expected 401, got %d", rr5.Code)
	}

	// tok1 must still work.
	if rr6 := e2eGet(mux, "/api/auth/me", tok1); rr6.Code != http.StatusOK {
		t.Errorf("tok1 after revoking tok2: expected 200, got %d", rr6.Code)
	}
}
