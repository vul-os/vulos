// routes_recovery_test.go — account recovery: the routes that break the
// "the reset link is in the inbox you cannot reach" circle.
//
// Pinned here:
//  1. Enumeration safety — an unknown address and a real one get byte-identical
//     submit responses.
//  2. The abuse gate — 3 requests in 24h freezes the account (429), which is the
//     response the Account Recovery page renders as "frozen for security review".
//  3. The 14-day clock — complete() is refused until recovery_eligible_at, and no
//     route can shorten it.
//  4. The review token is ONE-SHOT.
//  5. KEK-absent fail-closed — no RECOVERY_KEK, no ID upload. Ever.
//  6. Cancel-from-a-live-session — the whole point of the 14-day window.
//  7. A recovery code resets the PASSWORD with no mailbox, no session, no TOTP,
//     and is burned on use.
//  8. The out-of-network recovery address can NEVER be used to log in.
package cproutes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/auth/recovery"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/env"
)

const recTestPassword = "securepass1234!"

// recTestStoreN keeps parallel-safe DSNs apart.
var recTestStoreN atomic.Int64

// openRecoveryTestStore opens a recovery engine backed by an in-memory DB.
// kek may be nil — that is the fail-closed case (ID upload must refuse).
func openRecoveryTestStore(t *testing.T, kek []byte) *recovery.Store {
	t.Helper()
	dsn := fmt.Sprintf("file:recroutes%d?mode=memory&cache=shared", recTestStoreN.Add(1))
	db, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	rec, err := recovery.Open(db, kek)
	if err != nil {
		_ = db.Close()
		t.Fatalf("recovery.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	return rec
}

// recoveryMux wires the auth + recovery routes over fresh stores.
// idUploadDir configures the package-level recoveryIDDir the same way
// wireRecovery does in production: a real directory only when a KEK is present.
func recoveryMux(t *testing.T, kek []byte) (*http.ServeMux, *auth.Store, *recovery.Store) {
	t.Helper()
	st := openSessionTestStore(t)
	rec := openRecoveryTestStore(t, kek)

	prevDir := recoveryIDDir
	if len(kek) == 32 {
		recoveryIDDir = t.TempDir()
	} else {
		recoveryIDDir = ""
	}
	t.Cleanup(func() { recoveryIDDir = prevDir })

	mux := http.NewServeMux()
	RegisterAuthRoutes(mux, st)
	registerRecoveryRoutes(mux, st, rec)
	return mux, st, rec
}

// recPost posts JSON from a caller-chosen IP (the recovery limiter is per-IP, so
// tests that need many calls spread them over distinct IPs rather than weaken
// the limiter).
func recPost(t *testing.T, mux *http.ServeMux, method, path, body, ip string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":40000"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// recSignup creates an account and returns (handle, email, session cookie,
// recovery codes handed out at signup).
func recSignup(t *testing.T, mux *http.ServeMux, handle string) (string, string, *http.Cookie, []string) {
	t.Helper()
	body := fmt.Sprintf(`{"handle":%q,"password":%q}`, handle, recTestPassword)
	rr := recPost(t, mux, http.MethodPost, "/api/auth/signup", body, "10.0.0.1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("signup: %d %s", rr.Code, rr.Body.String())
	}
	var out struct {
		UserID        string   `json:"user_id"`
		Email         string   `json:"email"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode signup: %v", err)
	}
	var cookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			cookie = &http.Cookie{Name: c.Name, Value: c.Value}
		}
	}
	if cookie == nil {
		t.Fatal("signup returned no session cookie")
	}
	return handle, out.Email, cookie, out.RecoveryCodes
}

// ─────────────────────────────────────────────────────────────────────────────
// 1. Enumeration safety
// ─────────────────────────────────────────────────────────────────────────────

func TestRecoverySubmit_EnumerationSafe(t *testing.T) {
	mux, _, rec := recoveryMux(t, nil)
	_, email, _, _ := recSignup(t, mux, "enumreal")

	known := recPost(t, mux, http.MethodPost, "/api/auth/recovery/submit",
		fmt.Sprintf(`{"email":%q,"review_token":"SUPPORT-TOKEN-1"}`, email), "10.1.0.1", nil)
	unknown := recPost(t, mux, http.MethodPost, "/api/auth/recovery/submit",
		fmt.Sprintf(`{"email":"nobody-here@%s","review_token":"SUPPORT-TOKEN-1"}`, env.Domain()), "10.1.0.2", nil)

	if known.Code != http.StatusOK || unknown.Code != http.StatusOK {
		t.Fatalf("want 200/200, got known=%d unknown=%d", known.Code, unknown.Code)
	}
	if known.Body.String() != unknown.Body.String() {
		t.Fatalf("submit response distinguishes a real account from an unknown one:\n known=%s\n unknown=%s",
			known.Body.String(), unknown.Body.String())
	}

	// The pretend-200 must not have created a record for the unknown address.
	active, err := rec.ListActive(context.Background())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("want exactly 1 recovery request (the real account), got %d", len(active))
	}
	if active[0].Email != email {
		t.Fatalf("request opened for %q, want %q", active[0].Email, email)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. Abuse gate
// ─────────────────────────────────────────────────────────────────────────────

func TestRecoverySubmit_AbuseThresholdFreezes(t *testing.T) {
	mux, _, rec := recoveryMux(t, nil)
	_, email, _, _ := recSignup(t, mux, "abuser")
	ctx := context.Background()
	body := fmt.Sprintf(`{"email":%q,"review_token":"SUPPORT-TOKEN-1"}`, email)

	// The per-IP limiter is deliberately tight, so spread the attempts over
	// distinct IPs: this test is about the PER-ACCOUNT gate.
	for i := 0; i < recovery.MaxAbuse24h; i++ {
		rr := recPost(t, mux, http.MethodPost, "/api/auth/recovery/submit", body, fmt.Sprintf("10.2.0.%d", i+1), nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("submit %d: want 200, got %d %s", i+1, rr.Code, rr.Body.String())
		}
		// Cancel so the next submission is not merely "already pending".
		if err := rec.CancelAllPending(ctx, activeAccountID(t, rec, email)); err != nil {
			t.Fatalf("cancel: %v", err)
		}
	}

	rr := recPost(t, mux, http.MethodPost, "/api/auth/recovery/submit", body, "10.2.0.9", nil)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d: want 429 (account frozen), got %d %s", recovery.MaxAbuse24h+1, rr.Code, rr.Body.String())
	}
}

// activeAccountID returns the account behind the newest request for email.
func activeAccountID(t *testing.T, rec *recovery.Store, email string) string {
	t.Helper()
	active, err := rec.ListActive(context.Background())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	for _, r := range active {
		if r.Email == email {
			return r.AccountID
		}
	}
	t.Fatalf("no active recovery request for %s", email)
	return ""
}

// ─────────────────────────────────────────────────────────────────────────────
// 3 + 4. The 14-day clock, and the one-shot review token
// ─────────────────────────────────────────────────────────────────────────────

func TestRecoveryReview_ClockAndOneShotToken(t *testing.T) {
	mux, st, rec := recoveryMux(t, nil)
	_, email, _, _ := recSignup(t, mux, "reviewme")
	ctx := context.Background()

	admin, adminCookie := recFleetAdmin(t, mux, st, "recadmin")
	_ = admin

	rr := recPost(t, mux, http.MethodPost, "/api/auth/recovery/submit",
		fmt.Sprintf(`{"email":%q,"review_token":"SUPPORT-TOKEN-42"}`, email), "10.3.0.1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	reqID := activeRequestID(t, rec, email)

	// Completing before review is refused (the request has not been verified).
	rr = recPost(t, mux, http.MethodPost, "/api/admin/recovery/complete",
		fmt.Sprintf(`{"request_id":%q}`, reqID), "10.3.0.2", adminCookie)
	if rr.Code != http.StatusConflict {
		t.Fatalf("complete before verify: want 409, got %d %s", rr.Code, rr.Body.String())
	}

	// A token the submitter did not present cannot be redeemed.
	rr = recPost(t, mux, http.MethodPost, "/api/admin/recovery/verify",
		fmt.Sprintf(`{"request_id":%q,"token":"WRONG-TOKEN"}`, reqID), "10.3.0.3", adminCookie)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("verify with wrong token: want 401, got %d %s", rr.Code, rr.Body.String())
	}

	// The support-issued token the submitter pasted redeems once.
	rr = recPost(t, mux, http.MethodPost, "/api/admin/recovery/verify",
		fmt.Sprintf(`{"request_id":%q,"token":"SUPPORT-TOKEN-42"}`, reqID), "10.3.0.4", adminCookie)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("verify: want 204, got %d %s", rr.Code, rr.Body.String())
	}
	// ONE-SHOT: a replay is refused.
	rr = recPost(t, mux, http.MethodPost, "/api/admin/recovery/verify",
		fmt.Sprintf(`{"request_id":%q,"token":"SUPPORT-TOKEN-42"}`, reqID), "10.3.0.5", adminCookie)
	if rr.Code != http.StatusConflict {
		t.Fatalf("verify replay: want 409, got %d %s", rr.Code, rr.Body.String())
	}

	// THE CLOCK: verified, but the 14-day window has not elapsed.
	rr = recPost(t, mux, http.MethodPost, "/api/admin/recovery/complete",
		fmt.Sprintf(`{"request_id":%q}`, reqID), "10.3.0.6", adminCookie)
	if rr.Code != http.StatusConflict {
		t.Fatalf("complete inside the 14-day window: want 409, got %d %s", rr.Code, rr.Body.String())
	}

	// Only the passage of time opens it. RecoveryWindow is 14 days from
	// submission — nothing in the request path can shorten it.
	got, err := rec.GetRequest(ctx, reqID)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if d := got.RecoveryEligibleAt.Sub(got.SubmittedAt); d < recovery.RecoveryWindow-time.Minute || d > recovery.RecoveryWindow+time.Minute {
		t.Fatalf("recovery window is %s, want %s", d, recovery.RecoveryWindow)
	}
	if err := rec.BackdateEligibleAt(ctx, reqID, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	rr = recPost(t, mux, http.MethodPost, "/api/admin/recovery/complete",
		fmt.Sprintf(`{"request_id":%q}`, reqID), "10.3.0.7", adminCookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("complete after the window: want 200, got %d %s", rr.Code, rr.Body.String())
	}
	var out struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode complete: %v", err)
	}
	if len(out.RecoveryCodes) == 0 {
		t.Fatal("completion minted no recovery codes — auth.Store.ResetTOTP did not run")
	}

	// ResetTOTP really ran against the auth store: TOTP is off and the new codes
	// are live.
	userID := got.AccountID
	if st.IsTOTPEnabled(ctx, userID) {
		t.Fatal("TOTP still enabled after recovery completion")
	}
	burned, err := st.BurnRecoveryCode(ctx, userID, out.RecoveryCodes[0])
	if err != nil {
		t.Fatalf("BurnRecoveryCode: %v", err)
	}
	if !burned {
		t.Fatal("a code minted by completion does not verify")
	}
}

func activeRequestID(t *testing.T, rec *recovery.Store, email string) string {
	t.Helper()
	active, err := rec.ListActive(context.Background())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	for _, r := range active {
		if r.Email == email {
			return r.ID
		}
	}
	t.Fatalf("no active recovery request for %s", email)
	return ""
}

// recFleetAdmin signs up an account, promotes it to fleet_admin, and returns its
// id + session cookie.
func recFleetAdmin(t *testing.T, mux *http.ServeMux, st *auth.Store, handle string) (string, *http.Cookie) {
	t.Helper()
	_, email, cookie, _ := recSignup(t, mux, handle)
	var id string
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT id FROM users WHERE email = ?`, email).Scan(&id); err != nil {
		t.Fatalf("lookup admin: %v", err)
	}
	if _, err := st.DB().ExecContext(context.Background(),
		`UPDATE users SET fleet_admin = 1 WHERE id = ?`, id); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	return id, cookie
}

func TestRecoveryAdmin_RequiresFleetAdmin(t *testing.T) {
	mux, _, _ := recoveryMux(t, nil)
	_, _, cookie, _ := recSignup(t, mux, "notanadmin")

	rr := recPost(t, mux, http.MethodGet, "/api/admin/recovery/requests", "", "10.4.0.1", cookie)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("ordinary account on the review queue: want 403, got %d %s", rr.Code, rr.Body.String())
	}
	rr = recPost(t, mux, http.MethodGet, "/api/admin/recovery/requests", "", "10.4.0.2", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous on the review queue: want 401, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. KEK-absent fail-closed
// ─────────────────────────────────────────────────────────────────────────────

func TestRecoveryIDUpload_FailsClosedWithoutKEK(t *testing.T) {
	mux, _, rec := recoveryMux(t, nil) // no RECOVERY_KEK
	_, email, _, _ := recSignup(t, mux, "nokek")

	rr := recPost(t, mux, http.MethodPost, "/api/auth/recovery/submit",
		fmt.Sprintf(`{"email":%q,"review_token":"SUPPORT-TOKEN-1"}`, email), "10.5.0.1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	reqID := activeRequestID(t, rec, email)

	doc := base64.StdEncoding.EncodeToString([]byte("passport scan"))
	rr = recPost(t, mux, http.MethodPost, "/api/auth/recovery/id-upload",
		fmt.Sprintf(`{"request_id":%q,"document_b64":%q}`, reqID, doc), "10.5.0.2", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("id-upload without RECOVERY_KEK: want 503, got %d %s", rr.Code, rr.Body.String())
	}

	// And the engine itself refuses, even if a caller reaches past the route.
	path := filepath.Join(t.TempDir(), "id.enc")
	if err := rec.StoreIDUpload(context.Background(), reqID, []byte("passport scan"), path); err == nil {
		t.Fatal("StoreIDUpload accepted a document with no KEK")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a plaintext identity document was written without a KEK")
	}
}

func TestRecoveryIDUpload_EncryptedWithKEK(t *testing.T) {
	kek := []byte("12345678901234567890123456789012")
	mux, _, rec := recoveryMux(t, kek)
	_, email, _, _ := recSignup(t, mux, "withkek")

	rr := recPost(t, mux, http.MethodPost, "/api/auth/recovery/submit",
		fmt.Sprintf(`{"email":%q,"review_token":"SUPPORT-TOKEN-1"}`, email), "10.6.0.1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	reqID := activeRequestID(t, rec, email)

	plaintext := "PASSPORT-NUMBER-X99"
	rr = recPost(t, mux, http.MethodPost, "/api/auth/recovery/id-upload",
		fmt.Sprintf(`{"request_id":%q,"document_b64":%q}`, reqID,
			base64.StdEncoding.EncodeToString([]byte(plaintext))), "10.6.0.2", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("id-upload: want 204, got %d %s", rr.Code, rr.Body.String())
	}

	raw, err := os.ReadFile(filepath.Join(recoveryIDDir, reqID+".enc"))
	if err != nil {
		t.Fatalf("read stored document: %v", err)
	}
	if strings.Contains(string(raw), plaintext) {
		t.Fatal("the identity document is on disk in plaintext")
	}

	// One document per request.
	rr = recPost(t, mux, http.MethodPost, "/api/auth/recovery/id-upload",
		fmt.Sprintf(`{"request_id":%q,"document_b64":%q}`, reqID,
			base64.StdEncoding.EncodeToString([]byte("second"))), "10.6.0.3", nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("second id-upload: want 409, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. Cancel from a live session
// ─────────────────────────────────────────────────────────────────────────────

func TestRecoveryCancel_FromLiveSession(t *testing.T) {
	mux, _, rec := recoveryMux(t, nil)
	_, email, cookie, _ := recSignup(t, mux, "cancelme")

	rr := recPost(t, mux, http.MethodPost, "/api/auth/recovery/submit",
		fmt.Sprintf(`{"email":%q,"review_token":"SUPPORT-TOKEN-1"}`, email), "10.7.0.1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	reqID := activeRequestID(t, rec, email)

	// The owner sees the request they did not make …
	req := httptest.NewRequest(http.MethodGet, "/api/auth/recovery/status", nil)
	req.AddCookie(cookie)
	statusRR := httptest.NewRecorder()
	mux.ServeHTTP(statusRR, req)
	if statusRR.Code != http.StatusOK {
		t.Fatalf("status: %d %s", statusRR.Code, statusRR.Body.String())
	}
	if !strings.Contains(statusRR.Body.String(), reqID) {
		t.Fatalf("status does not list the open request: %s", statusRR.Body.String())
	}

	// … and kills it.
	rr = recPost(t, mux, http.MethodPost, "/api/auth/recovery/cancel",
		fmt.Sprintf(`{"request_id":%q}`, reqID), "10.7.0.2", cookie)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("cancel: want 204, got %d %s", rr.Code, rr.Body.String())
	}
	got, err := rec.GetRequest(context.Background(), reqID)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if got.Status != recovery.StatusCancelled {
		t.Fatalf("status after cancel = %q, want %q", got.Status, recovery.StatusCancelled)
	}

	// Anonymous callers cannot cancel.
	rr = recPost(t, mux, http.MethodPost, "/api/auth/recovery/cancel",
		fmt.Sprintf(`{"request_id":%q}`, reqID), "10.7.0.3", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous cancel: want 401, got %d", rr.Code)
	}
}

func TestRecoveryCancel_OtherAccountsRequest(t *testing.T) {
	mux, _, rec := recoveryMux(t, nil)
	_, victimEmail, _, _ := recSignup(t, mux, "victim")
	_, _, attackerCookie, _ := recSignup(t, mux, "attacker")

	rr := recPost(t, mux, http.MethodPost, "/api/auth/recovery/submit",
		fmt.Sprintf(`{"email":%q,"review_token":"SUPPORT-TOKEN-1"}`, victimEmail), "10.8.0.1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	reqID := activeRequestID(t, rec, victimEmail)

	rr = recPost(t, mux, http.MethodPost, "/api/auth/recovery/cancel",
		fmt.Sprintf(`{"request_id":%q}`, reqID), "10.8.0.2", attackerCookie)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cancelling another account's request: want 404, got %d", rr.Code)
	}
	got, err := rec.GetRequest(context.Background(), reqID)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if got.Status != recovery.StatusPending && got.Status != recovery.StatusReviewing {
		t.Fatalf("another account cancelled the request (status=%q)", got.Status)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 7. Recovery code → password reset (no mailbox in the loop)
// ─────────────────────────────────────────────────────────────────────────────

func TestPasswordResetWithRecoveryCode(t *testing.T) {
	mux, st, _ := recoveryMux(t, nil)
	handle, _, _, codes := recSignup(t, mux, "lockedout")
	if len(codes) == 0 {
		t.Fatal("signup handed out no recovery codes — the account has no mailbox-free way back in")
	}
	ctx := context.Background()
	const newPassword = "brand-new-password-9!"

	rr := recPost(t, mux, http.MethodPost, "/api/auth/password/recovery-code",
		fmt.Sprintf(`{"handle":%q,"recovery_code":%q,"new_password":%q}`, handle, codes[0], newPassword),
		"10.9.0.1", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("recovery-code reset: want 204, got %d %s", rr.Code, rr.Body.String())
	}

	// The new password works …
	if _, err := st.Login(ctx, handle+"@"+env.Domain(), newPassword, "127.0.0.1", "test"); err != nil {
		t.Fatalf("login with the new password: %v", err)
	}
	// … the old one does not …
	if _, err := st.Login(ctx, handle+"@"+env.Domain(), recTestPassword, "127.0.0.1", "test"); err == nil {
		t.Fatal("the old password still works after a recovery-code reset")
	}
	// … and the code is burned.
	rr = recPost(t, mux, http.MethodPost, "/api/auth/password/recovery-code",
		fmt.Sprintf(`{"handle":%q,"recovery_code":%q,"new_password":"another-password-77!"}`, handle, codes[0]),
		"10.9.0.2", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("replaying a burned recovery code: want 401, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestPasswordResetWithRecoveryCode_EnumerationSafe(t *testing.T) {
	mux, _, _ := recoveryMux(t, nil)
	handle, _, _, codes := recSignup(t, mux, "enumcode")

	unknown := recPost(t, mux, http.MethodPost, "/api/auth/password/recovery-code",
		fmt.Sprintf(`{"handle":"ghost","recovery_code":%q,"new_password":"brand-new-password-9!"}`, codes[0]),
		"10.10.0.1", nil)
	wrongCode := recPost(t, mux, http.MethodPost, "/api/auth/password/recovery-code",
		fmt.Sprintf(`{"handle":%q,"recovery_code":"NOT-A-REAL-CODE","new_password":"brand-new-password-9!"}`, handle),
		"10.10.0.2", nil)

	if unknown.Code != http.StatusUnauthorized || wrongCode.Code != http.StatusUnauthorized {
		t.Fatalf("want 401/401, got unknown=%d wrong-code=%d", unknown.Code, wrongCode.Code)
	}
	if unknown.Body.String() != wrongCode.Body.String() {
		t.Fatalf("an unknown handle is distinguishable from a wrong code:\n unknown=%s\n wrong=%s",
			unknown.Body.String(), wrongCode.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 8. The out-of-network recovery address is a DESTINATION, never an identity
// ─────────────────────────────────────────────────────────────────────────────

func TestRecoveryEmail_SetRequiresReauth_AndIsNeverALoginPath(t *testing.T) {
	mux, st, _ := recoveryMux(t, nil)
	handle, _, cookie, _ := recSignup(t, mux, "hasbackup")
	ctx := context.Background()
	const external = "someone@example.net"

	// A stolen session alone cannot redirect recovery mail.
	rr := recPost(t, mux, http.MethodPost, "/api/auth/recovery-email",
		fmt.Sprintf(`{"password":"wrong-password","recovery_email":%q}`, external), "10.11.0.1", cookie)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("set recovery address without re-auth: want 401, got %d %s", rr.Code, rr.Body.String())
	}

	// An in-network address would just redraw the circle.
	rr = recPost(t, mux, http.MethodPost, "/api/auth/recovery-email",
		fmt.Sprintf(`{"password":%q,"recovery_email":"someone-else@%s"}`, recTestPassword, env.Domain()), "10.11.0.2", cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("in-network recovery address: want 400, got %d %s", rr.Code, rr.Body.String())
	}

	rr = recPost(t, mux, http.MethodPost, "/api/auth/recovery-email",
		fmt.Sprintf(`{"password":%q,"recovery_email":%q}`, recTestPassword, external), "10.11.0.3", cookie)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("set recovery address: want 204, got %d %s", rr.Code, rr.Body.String())
	}

	// THE INVARIANT: the recovery address can never be used to reach the account.
	if _, err := st.Login(ctx, external, recTestPassword, "127.0.0.1", "test"); err == nil {
		t.Fatal("the recovery address logged in — it is an identity, not a destination")
	}
	loginRR := recPost(t, mux, http.MethodPost, "/api/auth/login",
		fmt.Sprintf(`{"email":%q,"password":%q}`, external, recTestPassword), "10.11.0.4", nil)
	if loginRR.Code != http.StatusUnauthorized {
		t.Fatalf("login with the recovery address: want 401, got %d %s", loginRR.Code, loginRR.Body.String())
	}
	if id, err := st.UserIDForEmail(ctx, external); err != nil || id != "" {
		t.Fatalf("an account resolved FROM the recovery address (id=%q err=%v)", id, err)
	}
	// The real identity still works.
	if _, err := st.Login(ctx, handle+"@"+env.Domain(), recTestPassword, "127.0.0.1", "test"); err != nil {
		t.Fatalf("login with the real identity broke: %v", err)
	}

	// It is fully declinable.
	rr = recPost(t, mux, http.MethodDelete, "/api/auth/recovery-email",
		fmt.Sprintf(`{"password":%q}`, recTestPassword), "10.11.0.5", cookie)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("clear recovery address: want 204, got %d %s", rr.Code, rr.Body.String())
	}
	addr, err := st.RecoveryEmail(ctx, userIDFor(t, st, handle))
	if err != nil {
		t.Fatalf("RecoveryEmail: %v", err)
	}
	if addr != "" {
		t.Fatalf("recovery address survived a clear: %q", addr)
	}
}

func userIDFor(t *testing.T, st *auth.Store, handle string) string {
	t.Helper()
	id, err := st.UserIDForEmail(context.Background(), handle+"@"+env.Domain())
	if err != nil || id == "" {
		t.Fatalf("resolve %s: id=%q err=%v", handle, id, err)
	}
	return id
}
