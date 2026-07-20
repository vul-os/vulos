package cproutes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/enroll"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/routing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// bootEnrollMux builds a fresh test mux with in-memory stores and a
// MemCertSigner. Returns the mux, the enroll MemStore, routing store, session
// token, and the fleet MemStore (non-nil; use bootEnrollMuxNoFleet for nil).
func bootEnrollMux(t *testing.T) (mux *http.ServeMux, est *enroll.MemStore, routingSt routing.Store, sessionToken string, fleetSt *fleet.MemStore) {
	t.Helper()
	est = enroll.NewMemStore()
	routingSt = routing.NewMemStore()
	signer := enroll.NewMemCertSigner()
	fleetSt = fleet.NewMemStore()

	authSt, err := openAuthStoreForTest(":memory:", []byte("testsecret"))
	if err != nil {
		t.Fatalf("OpenAuthStore: %v", err)
	}
	t.Cleanup(func() { authSt.Close() }) //nolint:errcheck

	// Sign up a test user and capture the session token.
	_, token, err := authSt.Signup(
		context.Background(), "user@example.com", "supersecretpassword1", "127.0.0.1", "testua",
	)
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	sessionToken = token

	mux = http.NewServeMux()
	RegisterBootEnrollRoutes(mux, est, signer, routingSt, authSt, fleetSt)
	return
}

// bootEnrollMuxNoFleet builds a mux with a nil fleet store to test the
// nil-safe code path.
func bootEnrollMuxNoFleet(t *testing.T) (mux *http.ServeMux, sessionToken string) {
	t.Helper()
	est := enroll.NewMemStore()
	routingSt := routing.NewMemStore()
	signer := enroll.NewMemCertSigner()

	authSt, err := openAuthStoreForTest(":memory:", []byte("testsecret"))
	if err != nil {
		t.Fatalf("OpenAuthStore: %v", err)
	}
	t.Cleanup(func() { authSt.Close() }) //nolint:errcheck

	_, token, err := authSt.Signup(
		context.Background(), "user@example.com", "supersecretpassword1", "127.0.0.1", "testua",
	)
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	sessionToken = token

	mux = http.NewServeMux()
	RegisterBootEnrollRoutes(mux, est, signer, routingSt, authSt, nil)
	return
}

// errFleetStore is a fleet.Store that always returns an error from UpsertDevice.
// All other methods delegate to an embedded MemStore.
type errFleetStore struct {
	*fleet.MemStore
}

func (e *errFleetStore) UpsertDevice(_ context.Context, _ fleet.Device) error {
	return errors.New("simulated fleet store failure")
}

// doPost sends a POST with JSON body to the mux, optionally setting a session cookie.
func doPost(t *testing.T, mux *http.ServeMux, path string, body any, sessionToken string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if sessionToken != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// doPostAnon sends a POST with no session cookie.
func doPostAnon(t *testing.T, mux *http.ServeMux, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doPost(t, mux, path, body, "")
}

// validPubKeyB64 returns a base64-encoded valid ed25519 public key.
func validPubKeyB64(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(pub)
}

// decodeBootEnrollResp decodes JSON response body into v.
func decodeBootEnrollResp(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(v); err != nil {
		t.Fatalf("decode response (status %d, body %q): %v", rr.Code, rr.Body.String(), err)
	}
}

// startGrant is a convenience: calls /enroll/start and returns the parsed grant.
func startGrant(t *testing.T, mux *http.ServeMux, pubKeyB64 string) enroll.EnrollGrant {
	t.Helper()
	rr := doPostAnon(t, mux, "/enroll/start", map[string]string{"device_pubkey": pubKeyB64})
	if rr.Code != http.StatusOK {
		t.Fatalf("start: want 200, got %d: %s", rr.Code, rr.Body)
	}
	var g enroll.EnrollGrant
	decodeBootEnrollResp(t, rr, &g)
	if g.DeviceCode == "" {
		t.Fatal("start: device_code is empty")
	}
	if g.UserCode == "" {
		t.Fatal("start: user_code is empty")
	}
	return g
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: POST /enroll/start returns valid grant JSON with user_code in WXYZ-1234 format
// ─────────────────────────────────────────────────────────────────────────────

func TestBootEnroll_Start_ValidGrant(t *testing.T) {
	mux, _, _, _, _ := bootEnrollMux(t)
	pubKey := validPubKeyB64(t)

	rr := doPostAnon(t, mux, "/enroll/start", map[string]string{"device_pubkey": pubKey})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}

	var g enroll.EnrollGrant
	decodeBootEnrollResp(t, rr, &g)

	if g.DeviceCode == "" {
		t.Error("device_code must not be empty")
	}
	if len(g.UserCode) != 9 || g.UserCode[4] != '-' {
		t.Errorf("user_code format: want XXXX-XXXX, got %q", g.UserCode)
	}
	if g.VerificationURI == "" {
		t.Error("verification_uri must not be empty")
	}
	if g.Interval <= 0 {
		t.Errorf("interval must be positive, got %d", g.Interval)
	}
	if g.ExpiresIn <= 0 {
		t.Errorf("expires_in must be positive, got %d", g.ExpiresIn)
	}
}

func TestBootEnroll_Start_InvalidPubKey(t *testing.T) {
	mux, _, _, _, _ := bootEnrollMux(t)

	// Empty key
	rr := doPostAnon(t, mux, "/enroll/start", map[string]string{"device_pubkey": ""})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("empty key: want 400, got %d", rr.Code)
	}

	// Bad base64
	rr = doPostAnon(t, mux, "/enroll/start", map[string]string{"device_pubkey": "!!!notbase64!!!"})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad base64: want 400, got %d", rr.Code)
	}

	// Right base64 but wrong length (16 bytes)
	rr = doPostAnon(t, mux, "/enroll/start", map[string]string{
		"device_pubkey": base64.StdEncoding.EncodeToString(make([]byte, 16)),
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("short key: want 400, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: poll before approve → 200 {"error":"authorization_pending"}
// ─────────────────────────────────────────────────────────────────────────────

func TestBootEnroll_Poll_BeforeApprove(t *testing.T) {
	mux, _, _, _, _ := bootEnrollMux(t)
	g := startGrant(t, mux, validPubKeyB64(t))

	rr := doPostAnon(t, mux, "/enroll/poll", map[string]string{"device_code": g.DeviceCode})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}
	var resp map[string]string
	decodeBootEnrollResp(t, rr, &resp)
	if resp["error"] != "authorization_pending" {
		t.Errorf("want error=authorization_pending, got %q", resp["error"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: poll after approve → 200 EnrollResult with ulid + device_cert
// ─────────────────────────────────────────────────────────────────────────────

func TestBootEnroll_Poll_AfterApprove(t *testing.T) {
	mux, _, _, sessionToken, _ := bootEnrollMux(t)
	g := startGrant(t, mux, validPubKeyB64(t))

	// Approve
	rr := doPost(t, mux, "/enroll/approve", map[string]string{"user_code": g.UserCode}, sessionToken)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("approve: want 204, got %d: %s", rr.Code, rr.Body)
	}

	// Poll → should return EnrollResult
	rr = doPostAnon(t, mux, "/enroll/poll", map[string]string{"device_code": g.DeviceCode})
	if rr.Code != http.StatusOK {
		t.Fatalf("poll after approve: want 200, got %d: %s", rr.Code, rr.Body)
	}
	var result enroll.EnrollResult
	decodeBootEnrollResp(t, rr, &result)
	if result.ULID == "" {
		t.Error("result.ulid must not be empty")
	}
	if len(result.DeviceCert) == 0 {
		t.Error("result.device_cert must not be empty")
	}
	if result.AccountID == "" {
		t.Error("result.account_id must not be empty")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: poll after deny → 403 access_denied
// ─────────────────────────────────────────────────────────────────────────────

func TestBootEnroll_Poll_AfterDeny(t *testing.T) {
	mux, _, _, sessionToken, _ := bootEnrollMux(t)
	g := startGrant(t, mux, validPubKeyB64(t))

	// Deny
	rr := doPost(t, mux, "/enroll/deny", map[string]string{"user_code": g.UserCode}, sessionToken)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("deny: want 204, got %d: %s", rr.Code, rr.Body)
	}

	// Poll → 403 access_denied
	rr = doPostAnon(t, mux, "/enroll/poll", map[string]string{"device_code": g.DeviceCode})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("poll after deny: want 403, got %d: %s", rr.Code, rr.Body)
	}
	var resp map[string]string
	decodeBootEnrollResp(t, rr, &resp)
	if resp["error"] != "access_denied" {
		t.Errorf("want error=access_denied, got %q", resp["error"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: poll after expiry → 410 expired_token
// ─────────────────────────────────────────────────────────────────────────────

func TestBootEnroll_Poll_AfterExpiry(t *testing.T) {
	// Use the MemStore directly to insert an already-expired grant.
	mux, est, _, _, _ := bootEnrollMux(t)

	pub, _, _ := ed25519.GenerateKey(nil)
	expiredGrant := enroll.EnrollGrant{
		DeviceCode:      enroll.GenDeviceCode(),
		UserCode:        enroll.GenUserCode(),
		VerificationURI: "https://vulos.org/activate",
		Interval:        5,
		ExpiresIn:       -1,
		ExpiresAt:       time.Now().UTC().Add(-1 * time.Second), // already expired
	}
	if err := est.StartGrant(context.Background(), expiredGrant, pub); err != nil { //nolint:staticcheck
		t.Fatalf("StartGrant: %v", err)
	}

	rr := doPostAnon(t, mux, "/enroll/poll", map[string]string{"device_code": expiredGrant.DeviceCode})
	if rr.Code != http.StatusGone {
		t.Fatalf("poll after expiry: want 410, got %d: %s", rr.Code, rr.Body)
	}
	var resp map[string]string
	decodeBootEnrollResp(t, rr, &resp)
	if resp["error"] != "expired_token" {
		t.Errorf("want error=expired_token, got %q", resp["error"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: approve requires valid session (401 otherwise)
// ─────────────────────────────────────────────────────────────────────────────

func TestBootEnroll_Approve_RequiresSession(t *testing.T) {
	mux, _, _, _, _ := bootEnrollMux(t)
	g := startGrant(t, mux, validPubKeyB64(t))

	// No session cookie
	rr := doPostAnon(t, mux, "/enroll/approve", map[string]string{"user_code": g.UserCode})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("approve without session: want 401, got %d: %s", rr.Code, rr.Body)
	}
}

func TestBootEnroll_Deny_RequiresSession(t *testing.T) {
	mux, _, _, _, _ := bootEnrollMux(t)
	g := startGrant(t, mux, validPubKeyB64(t))

	// No session cookie
	rr := doPostAnon(t, mux, "/enroll/deny", map[string]string{"user_code": g.UserCode})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("deny without session: want 401, got %d: %s", rr.Code, rr.Body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: poll unknown device_code → 404
// ─────────────────────────────────────────────────────────────────────────────

func TestBootEnroll_Poll_UnknownCode(t *testing.T) {
	mux, _, _, _, _ := bootEnrollMux(t)

	rr := doPostAnon(t, mux, "/enroll/poll", map[string]string{"device_code": "nosuchcode"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("poll unknown: want 404, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: approve unknown user_code → 404
// ─────────────────────────────────────────────────────────────────────────────

func TestBootEnroll_Approve_UnknownUserCode(t *testing.T) {
	mux, _, _, sessionToken, _ := bootEnrollMux(t)

	rr := doPost(t, mux, "/enroll/approve", map[string]string{"user_code": "XXXX-9999"}, sessionToken)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("approve unknown user_code: want 404, got %d: %s", rr.Code, rr.Body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: second poll after approve → ErrAlreadyConsumed → 409
// ─────────────────────────────────────────────────────────────────────────────

func TestBootEnroll_Poll_AlreadyConsumed(t *testing.T) {
	mux, _, _, sessionToken, _ := bootEnrollMux(t)
	g := startGrant(t, mux, validPubKeyB64(t))

	// Approve
	rr := doPost(t, mux, "/enroll/approve", map[string]string{"user_code": g.UserCode}, sessionToken)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("approve: want 204, got %d: %s", rr.Code, rr.Body)
	}

	// First poll — consumes the grant
	rr = doPostAnon(t, mux, "/enroll/poll", map[string]string{"device_code": g.DeviceCode})
	if rr.Code != http.StatusOK {
		t.Fatalf("first poll: want 200, got %d: %s", rr.Code, rr.Body)
	}

	// Second poll → 409
	rr = doPostAnon(t, mux, "/enroll/poll", map[string]string{"device_code": g.DeviceCode})
	if rr.Code != http.StatusConflict {
		t.Fatalf("second poll: want 409, got %d: %s", rr.Code, rr.Body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: approve wires routing store (ULID bound to account)
// ─────────────────────────────────────────────────────────────────────────────

func TestBootEnroll_Approve_BindsRoutingStore(t *testing.T) {
	mux, _, routingSt, sessionToken, _ := bootEnrollMux(t)
	g := startGrant(t, mux, validPubKeyB64(t))

	rr := doPost(t, mux, "/enroll/approve", map[string]string{"user_code": g.UserCode}, sessionToken)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("approve: want 204, got %d: %s", rr.Code, rr.Body)
	}

	// Poll to get the ULID
	rr = doPostAnon(t, mux, "/enroll/poll", map[string]string{"device_code": g.DeviceCode})
	if rr.Code != http.StatusOK {
		t.Fatalf("poll: want 200, got %d: %s", rr.Code, rr.Body)
	}
	var result enroll.EnrollResult
	decodeBootEnrollResp(t, rr, &result)

	// The ULID should be enrolled in the routing store.
	b, err := routingSt.GetBinding(context.Background(), result.ULID) //nolint:staticcheck
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if b.AccountID != result.AccountID {
		t.Errorf("binding account_id: want %q, got %q", result.AccountID, b.AccountID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: missing device_code on poll → 400
// ─────────────────────────────────────────────────────────────────────────────

func TestBootEnroll_Poll_MissingCode(t *testing.T) {
	mux, _, _, _, _ := bootEnrollMux(t)

	rr := doPostAnon(t, mux, "/enroll/poll", map[string]string{})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing device_code: want 400, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ENROLL-06: fleet device row auto-creation on successful enroll
// ─────────────────────────────────────────────────────────────────────────────

// AC: approve creates a fleet_devices row with decommissioned=false, and the
// device is visible via fleet store after approve.
func TestEnroll06_Approve_CreatesFleetDeviceRow(t *testing.T) {
	mux, _, _, sessionToken, fleetSt := bootEnrollMux(t)
	g := startGrant(t, mux, validPubKeyB64(t))

	rr := doPost(t, mux, "/enroll/approve", map[string]string{"user_code": g.UserCode}, sessionToken)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("approve: want 204, got %d: %s", rr.Code, rr.Body)
	}

	// Poll to get the ULID assigned during approval.
	rr = doPostAnon(t, mux, "/enroll/poll", map[string]string{"device_code": g.DeviceCode})
	if rr.Code != http.StatusOK {
		t.Fatalf("poll: want 200, got %d: %s", rr.Code, rr.Body)
	}
	var result enroll.EnrollResult
	decodeBootEnrollResp(t, rr, &result)

	// The device should be present in the fleet store.
	d, err := fleetSt.GetDevice(context.Background(), result.ULID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if d.ULID != result.ULID {
		t.Errorf("device ULID: want %q, got %q", result.ULID, d.ULID)
	}
	if d.AccountID != result.AccountID {
		t.Errorf("device AccountID: want %q, got %q", result.AccountID, d.AccountID)
	}
	if d.Channel != "stable" {
		t.Errorf("device Channel: want %q, got %q", "stable", d.Channel)
	}
	if d.Health != "unknown" {
		t.Errorf("device Health: want %q, got %q", "unknown", d.Health)
	}
	if d.Decommissioned {
		t.Error("device Decommissioned: want false, got true")
	}
	if d.CreatedAt.IsZero() {
		t.Error("device CreatedAt must not be zero")
	}
}

// AC: UpsertDevice error → enroll still returns 204 (non-fatal, log only).
func TestEnroll06_Approve_FleetUpsertError_DoesNotFailEnroll(t *testing.T) {
	est := enroll.NewMemStore()
	routingSt := routing.NewMemStore()
	signer := enroll.NewMemCertSigner()
	errFleet := &errFleetStore{MemStore: fleet.NewMemStore()}

	authSt, err := openAuthStoreForTest(":memory:", []byte("testsecret"))
	if err != nil {
		t.Fatalf("OpenAuthStore: %v", err)
	}
	t.Cleanup(func() { authSt.Close() }) //nolint:errcheck

	_, token, err := authSt.Signup(
		context.Background(), "user@example.com", "supersecretpassword1", "127.0.0.1", "testua",
	)
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	mux := http.NewServeMux()
	RegisterBootEnrollRoutes(mux, est, signer, routingSt, authSt, errFleet)

	g := startGrant(t, mux, validPubKeyB64(t))

	rr := doPost(t, mux, "/enroll/approve", map[string]string{"user_code": g.UserCode}, token)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("approve with errFleetStore: want 204, got %d: %s", rr.Code, rr.Body)
	}
}

// AC: nil fleet store → enroll succeeds (204), no panic.
func TestEnroll06_Approve_NilFleetStore_EnrollSucceeds(t *testing.T) {
	mux, sessionToken := bootEnrollMuxNoFleet(t)
	g := startGrant(t, mux, validPubKeyB64(t))

	rr := doPost(t, mux, "/enroll/approve", map[string]string{"user_code": g.UserCode}, sessionToken)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("approve with nil fleet store: want 204, got %d: %s", rr.Code, rr.Body)
	}
}
