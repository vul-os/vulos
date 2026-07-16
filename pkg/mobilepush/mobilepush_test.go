package mobilepush

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// openTestSubscriber opens an in-memory SQLite database for tests.
func openTestSubscriber(t *testing.T) *Subscriber {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	sub, err := OpenSubscriber(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("OpenSubscriber: %v", err)
	}
	t.Cleanup(func() { sub.Close() })
	return sub
}

// genTestKey generates a P-256 EC key and returns (key, PEM-encoded PKCS#8 bytes).
func genTestKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return key, pemBytes
}

// ─── Test 1: Subscriber roundtrip ────────────────────────────────────────────

func TestSubscriberRegisterAndList(t *testing.T) {
	sub := openTestSubscriber(t)
	ctx := context.Background()

	// Register two tokens for the same account.
	dt1, err := sub.Register(ctx, "acc-001", "tok-apns-aaa", PlatformAPNs)
	if err != nil {
		t.Fatalf("Register apns: %v", err)
	}
	if dt1.AccountID != "acc-001" || dt1.Platform != PlatformAPNs {
		t.Errorf("unexpected token: %+v", dt1)
	}

	_, err = sub.Register(ctx, "acc-001", "tok-fcm-bbb", PlatformFCM)
	if err != nil {
		t.Fatalf("Register fcm: %v", err)
	}

	// ListTokens should return both.
	tokens, err := sub.ListTokens(ctx, "acc-001")
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(tokens) != 2 {
		t.Errorf("expected 2 tokens, got %d", len(tokens))
	}

	// Idempotent re-register must not create a duplicate.
	_, err = sub.Register(ctx, "acc-001", "tok-apns-aaa", PlatformAPNs)
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	tokens, _ = sub.ListTokens(ctx, "acc-001")
	if len(tokens) != 2 {
		t.Errorf("re-register created duplicate: got %d tokens", len(tokens))
	}

	// Register with bad platform must fail.
	_, err = sub.Register(ctx, "acc-001", "tok-xxx", "bad-platform")
	if err == nil {
		t.Error("expected error for invalid platform")
	}

	// Register with empty token must fail.
	_, err = sub.Register(ctx, "acc-001", "", PlatformAPNs)
	if err == nil {
		t.Error("expected error for empty token")
	}
}

// ─── Test 2: Dispatch fanout ──────────────────────────────────────────────────

func TestDispatchFanout(t *testing.T) {
	sub := openTestSubscriber(t)
	ctx := context.Background()

	// Register one APNs and one FCM token.
	if _, err := sub.Register(ctx, "acc-002", "apns-token-fanout", PlatformAPNs); err != nil {
		t.Fatalf("Register APNs: %v", err)
	}
	if _, err := sub.Register(ctx, "acc-002", "fcm-token-fanout", PlatformFCM); err != nil {
		t.Fatalf("Register FCM: %v", err)
	}

	// Both platforms unconfigured in non-prod — sendAPNs/sendFCM are no-ops,
	// so all tokens are counted as "sent" (no error returned).
	cfg := &Config{Prod: false}
	d := NewDispatcher(sub, cfg)

	event := JMAPPushEvent{
		AccountID:  "acc-002",
		TypeStates: map[string]string{"Email": "state-1", "Mailbox": "state-2"},
	}

	result := d.Dispatch(ctx, event)
	if result.AccountID != "acc-002" {
		t.Errorf("unexpected account: %s", result.AccountID)
	}
	if result.TokensFailed != 0 {
		t.Errorf("expected 0 failures in non-prod no-op mode, got %d: %v", result.TokensFailed, result.Errors)
	}
	if result.TokensSent != 2 {
		t.Errorf("expected 2 tokens sent (no-op), got %d", result.TokensSent)
	}
}

// ─── Test 3: APNs payload shape ───────────────────────────────────────────────

func TestAPNsPayloadShape(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/3/device/") {
			t.Errorf("unexpected APNs path: %s", r.URL.Path)
		}
		if r.Header.Get("apns-push-type") != "alert" {
			t.Errorf("missing apns-push-type header")
		}
		if !strings.HasPrefix(r.Header.Get("authorization"), "bearer ") {
			t.Errorf("missing bearer auth header, got: %s", r.Header.Get("authorization"))
		}
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		captured = make([]byte, n)
		copy(captured, buf[:n])
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, p8Bytes := genTestKey(t)

	cfg := &Config{
		APNsKeyID:  "TESTKEYID1",
		APNsTeamID: "TESTTEAMID",
		APNsKeyP8:  p8Bytes,
		Prod:       false,
	}

	sub := openTestSubscriber(t)
	ctx := context.Background()

	d := NewDispatcher(sub, cfg)
	d.client = &http.Client{Transport: &singleHostTransport{base: srv.URL}}

	if _, err := sub.Register(ctx, "acc-003", "device-token-apns", PlatformAPNs); err != nil {
		t.Fatalf("Register: %v", err)
	}

	event := JMAPPushEvent{
		AccountID:  "acc-003",
		TypeStates: map[string]string{"Email": "new-state"},
	}
	result := d.Dispatch(ctx, event)
	if len(result.Errors) > 0 {
		t.Fatalf("dispatch errors: %v", result.Errors)
	}

	var payload apnsPayload
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Aps.Alert == "" {
		t.Error("expected non-empty aps.alert")
	}
	if payload.Aps.Sound != "default" {
		t.Errorf("expected aps.sound=default, got %q", payload.Aps.Sound)
	}
}

// ─── Test 4: FCM payload shape ────────────────────────────────────────────────

func TestFCMPayloadShape(t *testing.T) {
	var capturedMsg fcmRequest

	// Fake FCM send endpoint.
	fcmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected JSON content-type, got: %s", r.Header.Get("Content-Type"))
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("expected Bearer auth, got: %s", r.Header.Get("Authorization"))
		}
		json.NewDecoder(r.Body).Decode(&capturedMsg)
		w.WriteHeader(http.StatusOK)
	}))
	defer fcmSrv.Close()

	// Fake Google token endpoint.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": "fake-google-token"})
	}))
	defer tokenSrv.Close()

	_, privPEM := genTestKey(t)
	sa := map[string]string{
		"client_email": "test@project.iam.gserviceaccount.com",
		"private_key":  string(privPEM),
		"token_uri":    tokenSrv.URL,
	}
	saJSON, _ := json.Marshal(sa)

	cfg := &Config{
		FCMProjectID:          "test-project",
		FCMServiceAccountJSON: saJSON,
		Prod:                  false,
	}

	sub := openTestSubscriber(t)
	ctx := context.Background()

	d := NewDispatcher(sub, cfg)
	d.client = &http.Client{Transport: &dualHostTransport{
		fcmBase:   fcmSrv.URL,
		tokenBase: tokenSrv.URL,
	}}

	if _, err := sub.Register(ctx, "acc-004", "device-token-fcm", PlatformFCM); err != nil {
		t.Fatalf("Register: %v", err)
	}

	event := JMAPPushEvent{
		AccountID:  "acc-004",
		TypeStates: map[string]string{"Mailbox": "mb-state"},
	}
	result := d.Dispatch(ctx, event)
	if len(result.Errors) > 0 {
		t.Fatalf("dispatch errors: %v", result.Errors)
	}

	if capturedMsg.Message.Token != "device-token-fcm" {
		t.Errorf("unexpected FCM token: %s", capturedMsg.Message.Token)
	}
	if capturedMsg.Message.Notification.Title == "" {
		t.Error("expected non-empty notification title")
	}
	if capturedMsg.Message.Notification.Body == "" {
		t.Error("expected non-empty notification body")
	}
}

// ─── Test 5: Missing config in prod fail-closed ───────────────────────────────

func TestMissingConfigInProdFailClosed(t *testing.T) {
	// Ensure env vars are not set.
	for _, key := range []string{
		"APNS_KEY_ID", "APNS_TEAM_ID", "APNS_KEY_P8_PATH",
		"FCM_PROJECT_ID", "FCM_SERVICE_ACCOUNT_PATH",
	} {
		old := os.Getenv(key)
		os.Unsetenv(key)
		t.Cleanup(func() { os.Setenv(key, old) })
	}

	_, err := NewConfig(true /* isProd */)
	if err == nil {
		t.Fatal("expected ErrMissingConfig in prod with no env vars, got nil")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("expected error mentioning 'required', got: %v", err)
	}

	// In non-prod, missing vars must not error.
	cfg, err := NewConfig(false)
	if err != nil {
		t.Fatalf("expected no error in non-prod with missing vars, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config in non-prod")
	}

	// A Dispatcher with Prod=true but no keys should fail-closed on Dispatch.
	sub := openTestSubscriber(t)
	ctx := context.Background()
	if _, err := sub.Register(ctx, "acc-005", "tok-test", PlatformAPNs); err != nil {
		t.Fatalf("Register: %v", err)
	}

	prodCfg := &Config{Prod: true} // no keys
	d := NewDispatcher(sub, prodCfg)
	event := JMAPPushEvent{AccountID: "acc-005", TypeStates: map[string]string{"Email": "x"}}
	result := d.Dispatch(ctx, event)
	if result.TokensFailed != 1 {
		t.Errorf("expected 1 failure in prod with empty config, got %d (errors: %v)", result.TokensFailed, result.Errors)
	}
}

// ─── Test 6: RSA service-account key is parsed and JWT is signed RS256 ───────

// genTestRSAKey generates a 2048-bit RSA key and returns (key, PEM-encoded PKCS#8 bytes).
func genTestRSAKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal RSA key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return key, pemBytes
}

// jwtHeaderAlg extracts the "alg" field from the JWT header (first segment).
func jwtHeaderAlg(t *testing.T, jwtStr string) string {
	t.Helper()
	parts := strings.SplitN(jwtStr, ".", 3)
	if len(parts) != 3 {
		t.Fatalf("invalid JWT: %q", jwtStr)
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode JWT header: %v", err)
	}
	var h struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerBytes, &h); err != nil {
		t.Fatalf("parse JWT header: %v", err)
	}
	return h.Alg
}

// TestFCMRSAKeyParsedAndSignedRS256 verifies that fcmAccessToken uses RS256 when
// the service account carries an RSA private key (the Google default).
func TestFCMRSAKeyParsedAndSignedRS256(t *testing.T) {
	_, rsaPEM := genTestRSAKey(t)

	// Capture the JWT sent to the token endpoint so we can inspect its header.
	var capturedJWT string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			capturedJWT = r.FormValue("assertion")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": "fake-rsa-token"})
	}))
	defer tokenSrv.Close()

	sa := map[string]string{
		"client_email": "rsa-test@project.iam.gserviceaccount.com",
		"private_key":  string(rsaPEM),
		"token_uri":    tokenSrv.URL,
	}
	saJSON, _ := json.Marshal(sa)

	cfg := &Config{
		FCMProjectID:          "test-project-rsa",
		FCMServiceAccountJSON: saJSON,
		Prod:                  false,
	}

	sub := openTestSubscriber(t)
	d := NewDispatcher(sub, cfg)
	d.client = &http.Client{Transport: &singleHostTransport{base: tokenSrv.URL}}

	_, err := d.fcmAccessToken(context.Background())
	if err != nil {
		t.Fatalf("fcmAccessToken with RSA key: %v", err)
	}

	if capturedJWT == "" {
		t.Fatal("token endpoint did not receive an assertion JWT")
	}
	alg := jwtHeaderAlg(t, capturedJWT)
	if alg != "RS256" {
		t.Errorf("expected JWT alg=RS256 for RSA key, got %q", alg)
	}
}

// TestFCMECDSAKeyStillWorksES256 verifies that ECDSA service accounts continue
// to produce ES256 JWTs after the RSA path was added.
func TestFCMECDSAKeyStillWorksES256(t *testing.T) {
	_, ecPEM := genTestKey(t) // genTestKey produces a P-256 EC key

	var capturedJWT string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			capturedJWT = r.FormValue("assertion")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": "fake-ec-token"})
	}))
	defer tokenSrv.Close()

	sa := map[string]string{
		"client_email": "ec-test@project.iam.gserviceaccount.com",
		"private_key":  string(ecPEM),
		"token_uri":    tokenSrv.URL,
	}
	saJSON, _ := json.Marshal(sa)

	cfg := &Config{
		FCMProjectID:          "test-project-ec",
		FCMServiceAccountJSON: saJSON,
		Prod:                  false,
	}

	sub := openTestSubscriber(t)
	d := NewDispatcher(sub, cfg)
	d.client = &http.Client{Transport: &singleHostTransport{base: tokenSrv.URL}}

	_, err := d.fcmAccessToken(context.Background())
	if err != nil {
		t.Fatalf("fcmAccessToken with ECDSA key: %v", err)
	}

	if capturedJWT == "" {
		t.Fatal("token endpoint did not receive an assertion JWT")
	}
	alg := jwtHeaderAlg(t, capturedJWT)
	if alg != "ES256" {
		t.Errorf("expected JWT alg=ES256 for ECDSA key, got %q", alg)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Test transports
// ──────────────────────────────────────────────────────────────────────────────

// singleHostTransport rewrites all requests to a single test server.
type singleHostTransport struct{ base string }

func (t *singleHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.URL.Scheme = "http"
	// Extract the host from base URL.
	base := strings.TrimPrefix(t.base, "http://")
	newReq.URL.Host = base
	return http.DefaultTransport.RoundTrip(newReq)
}

// dualHostTransport routes FCM send requests to fcmBase and token requests to tokenBase.
type dualHostTransport struct {
	fcmBase   string
	tokenBase string
}

func (t *dualHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.URL.Scheme = "http"

	// Route by path pattern.
	if strings.Contains(req.URL.Host, "oauth2.googleapis.com") || req.URL.Host == strings.TrimPrefix(t.tokenBase, "http://") {
		newReq.URL.Host = strings.TrimPrefix(t.tokenBase, "http://")
	} else {
		newReq.URL.Host = strings.TrimPrefix(t.fcmBase, "http://")
	}
	return http.DefaultTransport.RoundTrip(newReq)
}
