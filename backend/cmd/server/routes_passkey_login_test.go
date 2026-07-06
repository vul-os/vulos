package main

// routes_passkey_login_test.go -- WAVE-46 HTTP coverage for the LOGINISO-01
// passkey login flow and LOGINISO-02 QR kiosk flow (routes_passkey_login.go).
//
// The login/finish path is the security-critical one: a valid passkey assertion
// for a username must mint a real OS session cookie; a bad assertion must NOT.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/auth"
	"vulos/backend/services/devicekey"
	"vulos/backend/services/passkeys"
)

func newLoginTestMux(t *testing.T) (*http.ServeMux, *passkeys.Service, *auth.Store) {
	t.Helper()
	t.Setenv("VULOS_RPID", pkTestRPID)
	t.Setenv("VULOS_ORIGIN", pkTestOrigin)

	ks, err := devicekey.Open(t.TempDir())
	if err != nil {
		t.Fatalf("devicekey.Open: %v", err)
	}
	t.Cleanup(func() { _ = ks.Close() })

	svc := passkeys.New(t.TempDir(), ks)
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	ls := passkeys.NewLoginService(svc, store)
	qr := passkeys.NewQRLoginService(store)

	mux := http.NewServeMux()
	registerPasskeyLoginRoutes(mux, ls, qr)
	return mux, svc, store
}

// enrollLoginUser creates a user (verified email → known username) and enrolls a
// passkey for them through the Service, returning username + authenticator.
func enrollLoginUser(t *testing.T, svc *passkeys.Service, store *auth.Store, email string) (string, *pkAuthenticator) {
	t.Helper()
	u := store.FindOrCreateUser("test", "pid-"+email, email, "Login User", "", true)
	if u == nil {
		t.Fatal("FindOrCreateUser returned nil")
	}
	username := u.Username
	va := newPKAuthenticator(t, pkTestRPID, pkTestOrigin)

	challenge, sessionData, err := svc.BeginRegistration(u.ID, "Login User")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	body := va.attestation(t, pkChallengeFromSvc(t, challenge))
	if _, err := svc.FinishRegistration(u.ID, body, sessionData); err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	return username, va
}

// pkChallengeFromSvc extracts the challenge from a raw Service Begin* JSON.
func pkChallengeFromSvc(t *testing.T, optionsJSON []byte) string {
	return pkChallengeFrom(t, optionsJSON)
}

// TestPasskeyLogin_RegisterRoutesUnauthenticated: register begin/finish require
// a session (X-User-ID).
func TestPasskeyLogin_RegisterRoutesUnauthenticated(t *testing.T) {
	mux, _, _ := newLoginTestMux(t)
	for _, path := range []string{
		"/api/auth/passkey/register/begin",
		"/api/auth/passkey/register/finish",
	} {
		rec := pkDoJSON(t, mux, "POST", path, "", map[string]any{})
		if rec.Code != 401 {
			t.Errorf("%s without X-User-ID: got %d want 401", path, rec.Code)
		}
	}
}

// TestPasskeyLogin_FullLoginFlow: begin/finish login for a username, verifying a
// session cookie is issued on success.
func TestPasskeyLogin_FullLoginFlow(t *testing.T) {
	mux, svc, store := newLoginTestMux(t)
	username, va := enrollLoginUser(t, svc, store, "alice@example.com")

	// --- login begin (PUBLIC) ---
	rec := pkDoJSON(t, mux, "POST", "/api/auth/passkey/login/begin", "", map[string]string{"username": username})
	if rec.Code != 200 {
		t.Fatalf("login/begin: got %d body=%s", rec.Code, rec.Body)
	}
	var lb struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &lb)
	if lb.SessionData == "" {
		t.Fatal("login/begin: empty session_data")
	}

	// --- login finish (PUBLIC) with a valid assertion ---
	va.signCount++
	rec = pkDoJSON(t, mux, "POST", "/api/auth/passkey/login/finish", "", map[string]any{
		"username":           username,
		"session_data":       lb.SessionData,
		"assertion_response": va.assertion(t, pkAssertOpts{challenge: pkChallengeFrom(t, lb.Challenge)}),
	})
	if rec.Code != 200 {
		t.Fatalf("login/finish: got %d body=%s", rec.Code, rec.Body)
	}
	// A session cookie must be set.
	if !hasSessionCookie(rec) {
		t.Fatal("login/finish: no vulos_session cookie set on success")
	}
}

// TestPasskeyLogin_BadAssertionNoSession: a tampered assertion must yield 401 and
// NO session cookie.
func TestPasskeyLogin_BadAssertionNoSession(t *testing.T) {
	mux, svc, store := newLoginTestMux(t)
	username, va := enrollLoginUser(t, svc, store, "bob@example.com")

	rec := pkDoJSON(t, mux, "POST", "/api/auth/passkey/login/begin", "", map[string]string{"username": username})
	var lb struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &lb)

	va.signCount++
	rec = pkDoJSON(t, mux, "POST", "/api/auth/passkey/login/finish", "", map[string]any{
		"username":           username,
		"session_data":       lb.SessionData,
		"assertion_response": va.assertion(t, pkAssertOpts{challenge: pkChallengeFrom(t, lb.Challenge), tamper: true}),
	})
	if rec.Code != 401 {
		t.Fatalf("tampered login/finish: got %d want 401 body=%s", rec.Code, rec.Body)
	}
	if hasSessionCookie(rec) {
		t.Fatal("tampered login/finish set a session cookie (must not)")
	}
}

// TestPasskeyLogin_UnknownUserNoEnumeration: login/begin for an unknown user must
// return 400 (not 404) to avoid username enumeration.
func TestPasskeyLogin_UnknownUserNoEnumeration(t *testing.T) {
	mux, _, _ := newLoginTestMux(t)
	rec := pkDoJSON(t, mux, "POST", "/api/auth/passkey/login/begin", "", map[string]string{"username": "ghost"})
	if rec.Code != 400 {
		t.Fatalf("unknown user login/begin: got %d want 400", rec.Code)
	}
	// missing username → 400
	rec = pkDoJSON(t, mux, "POST", "/api/auth/passkey/login/begin", "", map[string]any{})
	if rec.Code != 400 {
		t.Fatalf("missing username: got %d want 400", rec.Code)
	}
}

// TestPasskeyLogin_RegisterRoundTrip drives the (authed) LOGINISO-01 register
// begin/finish endpoints, then logs in with the freshly registered credential.
func TestPasskeyLogin_RegisterRoundTrip(t *testing.T) {
	mux, _, store := newLoginTestMux(t)
	u := store.FindOrCreateUser("test", "reg-pid", "carol@example.com", "Carol", "", true)
	va := newPKAuthenticator(t, pkTestRPID, pkTestOrigin)

	// register begin (authed via X-User-ID)
	rec := pkDoJSON(t, mux, "POST", "/api/auth/passkey/register/begin", u.ID, map[string]string{"display_name": "Carol"})
	if rec.Code != 200 {
		t.Fatalf("register/begin: got %d body=%s", rec.Code, rec.Body)
	}
	var rb struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &rb)

	// register finish
	rec = pkDoJSON(t, mux, "POST", "/api/auth/passkey/register/finish", u.ID, map[string]any{
		"session_data":         rb.SessionData,
		"attestation_response": va.attestation(t, pkChallengeFrom(t, rb.Challenge)),
	})
	if rec.Code != 200 {
		t.Fatalf("register/finish: got %d body=%s", rec.Code, rec.Body)
	}
	var fr struct {
		CredentialID string `json:"credential_id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &fr)
	if fr.CredentialID == "" {
		t.Fatal("register/finish: empty credential_id")
	}

	// the registered credential now logs in
	rec = pkDoJSON(t, mux, "POST", "/api/auth/passkey/login/begin", "", map[string]string{"username": u.Username})
	var lb struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &lb)
	va.signCount++
	rec = pkDoJSON(t, mux, "POST", "/api/auth/passkey/login/finish", "", map[string]any{
		"username":           u.Username,
		"session_data":       lb.SessionData,
		"assertion_response": va.assertion(t, pkAssertOpts{challenge: pkChallengeFrom(t, lb.Challenge)}),
	})
	if rec.Code != 200 || !hasSessionCookie(rec) {
		t.Fatalf("login after register: got %d cookie=%v body=%s", rec.Code, hasSessionCookie(rec), rec.Body)
	}
}

// TestPasskeyLogin_FinishRegisterBadBody covers the 400 guards on register/finish.
func TestPasskeyLogin_FinishRegisterBadBody(t *testing.T) {
	mux, _, store := newLoginTestMux(t)
	u := store.FindOrCreateUser("test", "bad-pid", "dave@example.com", "Dave", "", true)
	rec := pkDoJSON(t, mux, "POST", "/api/auth/passkey/register/finish", u.ID, map[string]any{})
	if rec.Code != 400 {
		t.Errorf("register/finish empty: got %d want 400", rec.Code)
	}
	// login/finish missing fields → 400
	rec = pkDoJSON(t, mux, "POST", "/api/auth/passkey/login/finish", "", map[string]any{"username": "x"})
	if rec.Code != 400 {
		t.Errorf("login/finish missing fields: got %d want 400", rec.Code)
	}
}

// ─── LOGINISO-02: QR kiosk flow ───────────────────────────────────────────────

// TestQRLoginFlow: kiosk begin → phone approve (with nonce) → kiosk poll gets a
// session cookie.
func TestQRLoginFlow(t *testing.T) {
	mux, _, store := newLoginTestMux(t)
	phone := store.FindOrCreateUser("test", "phone-1", "phone@example.com", "Phone", "", true)

	// --- kiosk begin (PUBLIC) ---
	rec := pkDoJSON(t, mux, "POST", "/api/auth/qr/begin", "", nil)
	if rec.Code != 200 {
		t.Fatalf("qr/begin: got %d body=%s", rec.Code, rec.Body)
	}
	var begin struct {
		ChallengeID string `json:"challenge_id"`
		QRData      string `json:"qr_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &begin)
	if begin.ChallengeID == "" || begin.QRData == "" {
		t.Fatalf("qr/begin: empty result %+v", begin)
	}
	nonce := decodeQRNonce(t, begin.QRData)

	// --- phone approve (authed) with the WRONG nonce → 403 ---
	rec = pkDoJSON(t, mux, "POST", "/api/auth/qr/approve", phone.ID, map[string]string{
		"challenge_id": begin.ChallengeID, "nonce": "wrong-nonce",
	})
	if rec.Code != 403 {
		t.Fatalf("qr/approve wrong nonce: got %d want 403", rec.Code)
	}

	// --- phone approve (authed) with the RIGHT nonce → 200 ---
	rec = pkDoJSON(t, mux, "POST", "/api/auth/qr/approve", phone.ID, map[string]string{
		"challenge_id": begin.ChallengeID, "nonce": nonce,
	})
	if rec.Code != 200 {
		t.Fatalf("qr/approve: got %d body=%s", rec.Code, rec.Body)
	}

	// --- kiosk poll (PUBLIC) → approved + session cookie, no token in body ---
	rec = pkDoJSON(t, mux, "GET", "/api/auth/qr/poll?id="+begin.ChallengeID, "", nil)
	if rec.Code != 200 {
		t.Fatalf("qr/poll: got %d body=%s", rec.Code, rec.Body)
	}
	var poll map[string]any
	json.Unmarshal(rec.Body.Bytes(), &poll)
	if approved, _ := poll["approved"].(bool); !approved {
		t.Fatalf("qr/poll: approved=false %+v", poll)
	}
	if _, leaked := poll["session_token"]; leaked {
		t.Fatal("qr/poll leaked session_token in JSON body")
	}
	if !hasSessionCookie(rec) {
		t.Fatal("qr/poll approved but set no session cookie")
	}
}

// TestQRApprove_Unauthenticated: approve requires a session.
func TestQRApprove_Unauthenticated(t *testing.T) {
	mux, _, _ := newLoginTestMux(t)
	rec := pkDoJSON(t, mux, "POST", "/api/auth/qr/approve", "", map[string]string{"challenge_id": "x", "nonce": "y"})
	if rec.Code != 401 {
		t.Fatalf("qr/approve without session: got %d want 401", rec.Code)
	}
}

// TestQRPoll_MissingID: poll without ?id= → 400.
func TestQRPoll_MissingID(t *testing.T) {
	mux, _, _ := newLoginTestMux(t)
	rec := pkDoJSON(t, mux, "GET", "/api/auth/qr/poll", "", nil)
	if rec.Code != 400 {
		t.Fatalf("qr/poll no id: got %d want 400", rec.Code)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func hasSessionCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == "vulos_session" && c.Value != "" {
			return true
		}
	}
	return false
}

// decodeQRNonce decodes the nonce embedded in the qr_data payload.
func decodeQRNonce(t *testing.T, qrData string) string {
	t.Helper()
	const prefix = "vulos://qr-login/"
	if !strings.HasPrefix(qrData, prefix) {
		t.Fatalf("qr_data missing prefix: %q", qrData)
	}
	raw := decodeB64URL(t, strings.TrimPrefix(qrData, prefix))
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode qr payload: %v", err)
	}
	if m["nonce"] == "" {
		t.Fatal("no nonce in qr payload")
	}
	return m["nonce"]
}
