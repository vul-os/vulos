// cloudunified_test.go — UNIFIED-SIGNIN end-to-end handler tests against a
// fake CP that REALLY enforces the CSRF Origin allowlist, REALLY verifies the
// PoW hashcash, and REALLY signs login tokens with a broker Ed25519 key.
//
// Covered (the founder's "well tested" bar):
//   - happy path: challenge → login → pubkey TOFU-pin → issue → OS session
//   - the PoW is actually solved and consumed by the fake CP
//   - totp_required / email_verification_required steps surface to the UI
//   - invalid credentials / invalid TOTP → 401 (rate-limiter fed)
//   - enrollment_required (not enrolled; and enrollment unavailable)
//   - wrong broker key (env inline override) → signature fail-closed 401
//   - pinned-key mismatch → REFUSES with 503, pin file NOT overwritten
//   - token minted for another ULID → 401 when VULOS_DEVICE_ULID is set
//   - replay of the exact same signed token → ErrTokenReplay (same verifier
//     path the handler uses)
//   - enroll start/status endpoints drive the RFC 8628 flow
package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/services/cloudclient"
	"vulos/backend/services/cloudclient/cloudclienttest"
)

const unifiedTestULID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// fakeEnrollment is a canned auth.CloudEnrollment.
type fakeEnrollment struct {
	ulid, account string
	userCode, uri string
	beginErr      error
	status        CloudEnrollStatus
}

func (f *fakeEnrollment) Identity() (string, string, error) { return f.ulid, f.account, nil }
func (f *fakeEnrollment) Begin(context.Context) (string, string, error) {
	return f.userCode, f.uri, f.beginErr
}
func (f *fakeEnrollment) Status() CloudEnrollStatus { return f.status }

// setupUnified wires a Handler + mux against a fresh fake CP. The broker pin
// file and offline-token cache live in a per-test temp dir.
func setupUnified(t *testing.T) (*Handler, *cloudclienttest.FakeCP, *http.ServeMux) {
	t.Helper()
	fc := cloudclienttest.NewFakeCP()
	t.Cleanup(fc.Server.Close)

	dir := t.TempDir()
	t.Setenv("VULOS_CLOUD_API_URL", fc.URL())
	t.Setenv("VULOS_CLOUD_ORIGIN", fc.AllowedOrigin)
	t.Setenv("VULOS_CLOUD_BROKER_PUBKEY", filepath.Join(dir, "broker.pub"))
	t.Setenv("VULOS_CLOUDTOKEN_CACHE", filepath.Join(dir, "cloudtoken-cache.json"))
	t.Setenv("VULOS_DEVICE_ULID", "")

	store := makeTestStore(t)
	h := NewHandler(store)
	h.CloudEnroll = &fakeEnrollment{ulid: unifiedTestULID, account: "acc"}
	mux := http.NewServeMux()
	h.Register(mux)
	return h, fc, mux
}

func postCloudLogin(t *testing.T, mux *http.ServeMux, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/cloud/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.1.2.3:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// ─── Happy path ───────────────────────────────────────────────────────────────

func TestUnifiedLogin_HappyPath_EndToEnd(t *testing.T) {
	h, fc, mux := setupUnified(t)
	acc := fc.AddAccount(cloudclienttest.Account{
		Email: "ada@vulos.test", Password: "correct horse battery", EmailVerified: true,
	})
	fc.BindULID(unifiedTestULID, acc.ID)

	rr := postCloudLogin(t, mux, map[string]string{
		"email": "ada@vulos.test", "password": "correct horse battery",
	})
	if rr.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var info CloudSessionInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.SessionToken == "" || info.AccountID != acc.ID || info.Email != "ada@vulos.test" {
		t.Fatalf("unexpected session info: %+v", info)
	}

	// The OS session actually works.
	if _, ok := h.store.ValidateToken(info.SessionToken); !ok {
		t.Fatal("issued OS session token does not validate")
	}

	// Session cookie was set.
	cookieSet := false
	for _, c := range rr.Result().Cookies() {
		if c.Value == info.SessionToken {
			cookieSet = true
		}
	}
	if !cookieSet {
		t.Fatal("OS session cookie not set")
	}

	// The fake CP really verified a solved PoW and really minted a token.
	if fc.PoWAccepted() < 1 {
		t.Fatal("PoW was never solved/accepted — the CaptchaGate was bypassed?")
	}
	if fc.IssuedTokens() != 1 {
		t.Fatalf("issued tokens = %d, want 1", fc.IssuedTokens())
	}

	// The broker pubkey was TOFU-pinned to the env-configured path.
	pinPath := os.Getenv("VULOS_CLOUD_BROKER_PUBKEY")
	raw, err := os.ReadFile(pinPath)
	if err != nil {
		t.Fatalf("broker pin file not written: %v", err)
	}
	pinned, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || !fc.BrokerPub.Equal(ed25519.PublicKey(pinned)) {
		t.Fatal("pinned key does not match the CP broker key")
	}
}

// ─── Structured steps ────────────────────────────────────────────────────────

func TestUnifiedLogin_TOTPStep_ThenSuccessWithCode(t *testing.T) {
	h, fc, mux := setupUnified(t)
	acc := fc.AddAccount(cloudclienttest.Account{
		Email: "sec@vulos.test", Password: "pw", EmailVerified: true, TOTPCode: "424242",
	})
	fc.BindULID(unifiedTestULID, acc.ID)
	_ = h

	// No code: the step surfaces so the UI can show a TOTP input.
	rr := postCloudLogin(t, mux, map[string]string{"email": "sec@vulos.test", "password": "pw"})
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"step":"totp_required"`) {
		t.Fatalf("want totp_required step, got %d: %s", rr.Code, rr.Body.String())
	}

	// With the code: full OS session.
	rr2 := postCloudLogin(t, mux, map[string]string{
		"email": "sec@vulos.test", "password": "pw", "totp_code": "424242",
	})
	if rr2.Code != 200 || !strings.Contains(rr2.Body.String(), "session_token") {
		t.Fatalf("want session, got %d: %s", rr2.Code, rr2.Body.String())
	}

	// Wrong code: 401 invalid_totp.
	rr3 := postCloudLogin(t, mux, map[string]string{
		"email": "sec@vulos.test", "password": "pw", "totp_code": "111111",
	})
	if rr3.Code != 401 || !strings.Contains(rr3.Body.String(), "invalid_totp") {
		t.Fatalf("want 401 invalid_totp, got %d: %s", rr3.Code, rr3.Body.String())
	}
}

func TestUnifiedLogin_EmailVerificationStep(t *testing.T) {
	_, fc, mux := setupUnified(t)
	fc.AddAccount(cloudclienttest.Account{Email: "new@vulos.test", Password: "pw", EmailVerified: false})

	rr := postCloudLogin(t, mux, map[string]string{"email": "new@vulos.test", "password": "pw"})
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"step":"email_verification_required"`) {
		t.Fatalf("want email_verification_required, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestUnifiedLogin_InvalidCredentials(t *testing.T) {
	_, fc, mux := setupUnified(t)
	fc.AddAccount(cloudclienttest.Account{Email: "ada@vulos.test", Password: "right", EmailVerified: true})

	rr := postCloudLogin(t, mux, map[string]string{"email": "ada@vulos.test", "password": "wrong"})
	if rr.Code != 401 || !strings.Contains(rr.Body.String(), "invalid_credentials") {
		t.Fatalf("want 401 invalid_credentials, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ─── Enrollment gating ───────────────────────────────────────────────────────

func TestUnifiedLogin_EnrollmentRequired(t *testing.T) {
	h, fc, mux := setupUnified(t)
	fc.AddAccount(cloudclienttest.Account{Email: "ada@vulos.test", Password: "pw", EmailVerified: true})

	// Not enrolled (empty ULID) → enrollment_required with the flow available.
	h.CloudEnroll = &fakeEnrollment{}
	rr := postCloudLogin(t, mux, map[string]string{"email": "ada@vulos.test", "password": "pw"})
	if rr.Code != 200 ||
		!strings.Contains(rr.Body.String(), `"step":"enrollment_required"`) ||
		!strings.Contains(rr.Body.String(), `"enroll_available":true`) {
		t.Fatalf("want enrollment_required+available, got %d: %s", rr.Code, rr.Body.String())
	}

	// No enrollment machinery at all → enrollment_required, unavailable.
	h.CloudEnroll = nil
	rr2 := postCloudLogin(t, mux, map[string]string{"email": "ada@vulos.test", "password": "pw"})
	if rr2.Code != 200 || !strings.Contains(rr2.Body.String(), `"enroll_available":false`) {
		t.Fatalf("want enrollment unavailable, got %d: %s", rr2.Code, rr2.Body.String())
	}
}

func TestUnifiedEnroll_StartAndStatus(t *testing.T) {
	h, _, mux := setupUnified(t)
	h.CloudEnroll = &fakeEnrollment{
		userCode: "ABCD-1234",
		uri:      "https://vulos.org/activate",
		status:   CloudEnrollStatus{State: "pending", UserCode: "ABCD-1234"},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/cloud/enroll/start", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.1.2.3:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "ABCD-1234") {
		t.Fatalf("enroll/start: got %d: %s", rr.Code, rr.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/auth/cloud/enroll/status", nil)
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	if rr2.Code != 200 || !strings.Contains(rr2.Body.String(), `"state":"pending"`) {
		t.Fatalf("enroll/status: got %d: %s", rr2.Code, rr2.Body.String())
	}
}

// ─── Broker key fail-closed paths ────────────────────────────────────────────

// A WRONG broker key (operator inline env override) must fail signature
// verification — the token the CP signed does not verify, 401, no session.
func TestUnifiedLogin_WrongBrokerKey_FailsClosed(t *testing.T) {
	h, fc, mux := setupUnified(t)
	acc := fc.AddAccount(cloudclienttest.Account{Email: "ada@vulos.test", Password: "pw", EmailVerified: true})
	fc.BindULID(unifiedTestULID, acc.ID)
	_ = h

	wrongPub, _, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv("VULOS_CLOUD_BROKER_PUBKEY", base64.StdEncoding.EncodeToString(wrongPub))

	rr := postCloudLogin(t, mux, map[string]string{"email": "ada@vulos.test", "password": "pw"})
	if rr.Code != 401 || !strings.Contains(rr.Body.String(), "signature") {
		t.Fatalf("want 401 bad-signature, got %d: %s", rr.Code, rr.Body.String())
	}
}

// A pinned key that DIFFERS from what the cloud now serves must REFUSE the
// login and must NOT overwrite the pin (key-swap defence).
func TestUnifiedLogin_PinnedKeyMismatch_RefusesNoOverwrite(t *testing.T) {
	_, fc, mux := setupUnified(t)
	acc := fc.AddAccount(cloudclienttest.Account{Email: "ada@vulos.test", Password: "pw", EmailVerified: true})
	fc.BindULID(unifiedTestULID, acc.ID)

	// Pre-pin a DIFFERENT key at the pin path.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	pinPath := os.Getenv("VULOS_CLOUD_BROKER_PUBKEY")
	pinB64 := base64.StdEncoding.EncodeToString(otherPub)
	if err := os.WriteFile(pinPath, []byte(pinB64+"\n"), 0o600); err != nil {
		t.Fatalf("write pin: %v", err)
	}

	rr := postCloudLogin(t, mux, map[string]string{"email": "ada@vulos.test", "password": "pw"})
	if rr.Code != 503 || !strings.Contains(rr.Body.String(), "broker key mismatch") {
		t.Fatalf("want 503 broker key mismatch, got %d: %s", rr.Code, rr.Body.String())
	}

	// The pin file is untouched.
	raw, err := os.ReadFile(pinPath)
	if err != nil || strings.TrimSpace(string(raw)) != pinB64 {
		t.Fatalf("pin file was modified! err=%v content=%q", err, raw)
	}
}

// ─── Device binding ──────────────────────────────────────────────────────────

// When VULOS_DEVICE_ULID names THIS device, a token minted for a DIFFERENT
// ULID must be rejected by the verifier (device binding, not bypassed).
func TestUnifiedLogin_TokenForOtherULID_RejectedWithDeviceBinding(t *testing.T) {
	h, fc, mux := setupUnified(t)
	acc := fc.AddAccount(cloudclienttest.Account{Email: "ada@vulos.test", Password: "pw", EmailVerified: true})

	const otherULID = "01BX5ZZKBKACTAV9WEVGEMMVS0"
	fc.BindULID(otherULID, acc.ID) // the CP will happily mint for otherULID…
	h.CloudEnroll = &fakeEnrollment{ulid: otherULID, account: acc.ID}

	t.Setenv("VULOS_DEVICE_ULID", unifiedTestULID) // …but THIS box is a different device

	rr := postCloudLogin(t, mux, map[string]string{"email": "ada@vulos.test", "password": "pw"})
	if rr.Code != 401 || !strings.Contains(rr.Body.String(), "not valid for this device") {
		t.Fatalf("want 401 device mismatch, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ─── Replay ──────────────────────────────────────────────────────────────────

// The exact same signed token must not mint two OS sessions. This exercises
// the very verifier path the handler uses (CloudOfflineVerifier → Login).
func TestUnifiedLogin_ReplayRejected(t *testing.T) {
	_, fc, _ := setupUnified(t)
	acc := fc.AddAccount(cloudclienttest.Account{Email: "ada@vulos.test", Password: "pw", EmailVerified: true})
	fc.BindULID(unifiedTestULID, acc.ID)

	// Mint one real token via the CP client.
	c := cloudclient.New(fc.URL())
	if _, err := c.Login(context.Background(), "ada@vulos.test", "pw", ""); err != nil {
		t.Fatalf("cp login: %v", err)
	}
	payload, sig, err := c.IssueLoginToken(context.Background(), unifiedTestULID, "ada")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	store := makeTestStore(t)
	verifier := NewCloudOfflineVerifier(store, fc.BrokerPub, 0)
	if _, err := verifier.Login(payload, sig); err != nil {
		t.Fatalf("first use should succeed: %v", err)
	}
	if _, err := verifier.Login(payload, sig); err != ErrTokenReplay {
		t.Fatalf("second use: want ErrTokenReplay, got %v", err)
	}
}

// ─── Signup proxy (PoW + Origin fixed) ───────────────────────────────────────

// The signup proxy must now clear the CP's CSRF + PoW gates (it used to be
// hard-403'd) and relay the created account.
func TestCloudSignupProxy_ClearsGatesAndCreatesAccount(t *testing.T) {
	_, fc, mux := setupUnified(t)

	body, _ := json.Marshal(map[string]string{
		"email":     "newuser@vulos.test",
		"password":  "a long enough password",
		"full_name": "New User",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/cloud/signup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.1.2.3:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != 201 {
		t.Fatalf("signup proxy: want 201, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "newuser@vulos.test") {
		t.Fatalf("signup response missing created account: %s", rr.Body.String())
	}
	if fc.PoWAccepted() < 1 {
		t.Fatal("signup proxy did not solve the PoW")
	}
}
