package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"vulos/backend/internal/webpush"
)

// verifyCPAuth re-implements the CP's verification independently (NOT by calling
// the cell's computeCPAuth) so the test proves the cell's header would be
// ACCEPTED by the CP's exact construction:
//
//	hex(HMAC-SHA256(secret, method"\n"path"\n"hex(sha256(body))))
func verifyCPAuth(secret []byte, method, path string, body []byte, presented string) bool {
	bd := sha256.Sum256(body)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(method))
	mac.Write([]byte("\n"))
	mac.Write([]byte(path))
	mac.Write([]byte("\n"))
	mac.Write([]byte(hex.EncodeToString(bd[:])))
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(presented))
}

const (
	testSecret = "edge-cp-shared-secret"
	testULID   = "01HZZZZULIDCELL0000000000"
)

// captured records what a fake CP received.
type captured struct {
	method   string
	path     string
	auth     string
	body     []byte
	authOK   bool
	hitCount int32
}

// fakeCP stands in for the CP register/unregister endpoints, verifying auth the
// way the real CP does and capturing the last request.
func fakeCP(t *testing.T, cap *captured) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&cap.hitCount, 1)
		body, _ := io.ReadAll(r.Body)
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.auth = r.Header.Get(cpAuthHeader)
		cap.body = body
		cap.authOK = verifyCPAuth([]byte(testSecret), r.Method, r.URL.Path, body, cap.auth)
		if !cap.authOK {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent) // CP returns 204 on success
	}))
}

func liveRegistrar(t *testing.T, srv *httptest.Server) *CPRegistrar {
	t.Helper()
	r := NewCPRegistrar(CPRegistrarConfig{BaseURL: srv.URL, Secret: testSecret, ULID: testULID})
	if r == nil {
		t.Fatal("expected a live registrar for fully-configured managed mode")
	}
	// The production client blocks loopback (SSRF guard); use the httptest client.
	r.setClientForTest(srv.Client())
	return r
}

// TestCPRegistrar_Disabled_SelfHost verifies the registrar is inert unless ALL
// of URL/secret/ULID are set — the self-host / no-CP default. A nil registrar's
// methods must be safe no-ops.
func TestCPRegistrar_Disabled_SelfHost(t *testing.T) {
	cases := []CPRegistrarConfig{
		{},
		{BaseURL: "https://cp.vulos.org"},
		{BaseURL: "https://cp.vulos.org", Secret: testSecret},      // no ULID
		{Secret: testSecret, ULID: testULID},                       // no URL
		{BaseURL: "https://cp.vulos.org", ULID: testULID},          // no secret
		{BaseURL: "not a url", Secret: testSecret, ULID: testULID}, // bad URL scheme
		{BaseURL: "ftp://cp", Secret: testSecret, ULID: testULID},  // non-http scheme
		{BaseURL: "https://", Secret: testSecret, ULID: testULID},  // no host
	}
	for _, cfg := range cases {
		r := NewCPRegistrar(cfg)
		if r.Enabled() {
			t.Fatalf("registrar unexpectedly enabled for cfg %+v", cfg)
		}
		// Nil-safe no-ops.
		if err := r.Register(context.Background(), sampleSub("https://push.example/x")); err != nil {
			t.Fatalf("nil Register should be a no-op, got %v", err)
		}
		if err := r.Unregister(context.Background(), "https://push.example/x"); err != nil {
			t.Fatalf("nil Unregister should be a no-op, got %v", err)
		}
	}
}

// TestCPRegistrar_Register_HMACAndBody is the core loop-closing test: the cell's
// register POST must (1) carry a header the CP's own construction ACCEPTS, and
// (2) carry the exact content-blind body the CP decodes (ulid + CP-keyed
// subscription only — NO VAPID key material, NO mail content).
func TestCPRegistrar_Register_HMACAndBody(t *testing.T) {
	var cap captured
	srv := fakeCP(t, &cap)
	defer srv.Close()

	r := liveRegistrar(t, srv)
	sub := webpush.Subscription{Endpoint: "https://fcm.googleapis.com/x/abc", P256DH: "p256", Auth: "authsalt"}
	if err := r.Register(context.Background(), sub); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != cpRegisterPath {
		t.Fatalf("wrong method/path: %s %s", cap.method, cap.path)
	}
	if !cap.authOK {
		t.Fatalf("CP would REJECT the cell's HMAC header (contract mismatch); header=%q", cap.auth)
	}
	var got registerBody
	if err := json.Unmarshal(cap.body, &got); err != nil {
		t.Fatalf("CP could not decode register body: %v; raw=%s", err, cap.body)
	}
	want := registerBody{
		ULID: testULID, Endpoint: sub.Endpoint, P256DH: sub.P256DH, Auth: sub.Auth,
	}
	if got != want {
		t.Fatalf("register body mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

// TestCPRegistrar_Register_NoVAPIDMaterial is the SPEC 1 wire guarantee: the
// register body carries ONLY {ulid, endpoint, p256dh, auth} — NEVER any VAPID key
// material (the CP signs with its OWN key and must never receive the cell's).
func TestCPRegistrar_Register_NoVAPIDMaterial(t *testing.T) {
	var cap captured
	srv := fakeCP(t, &cap)
	defer srv.Close()
	r := liveRegistrar(t, srv)
	if err := r.Register(context.Background(), sampleSub("https://push.example/z")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(cap.body, &m); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"ulid": true, "endpoint": true, "p256dh": true, "auth": true}
	for k := range m {
		if !allowed[k] {
			t.Fatalf("register body carries disallowed field %q: %s", k, cap.body)
		}
	}
	// The body must NOT contain any VAPID key material or mail-content field.
	if strings.Contains(strings.ToLower(string(cap.body)), "vapid") {
		t.Fatalf("register wire leaked VAPID key material: %s", cap.body)
	}
	for _, banned := range []string{"subject", "sender", "from", "snippet", "body", "title", "message"} {
		if strings.Contains(string(cap.body), `"`+banned+`"`) {
			t.Fatalf("register body leaked field %q: %s", banned, cap.body)
		}
	}
}

// TestCPRegistrar_Register_SkipsEmptySubscription verifies that with no usable
// subscription (empty endpoint/keys) the cell makes no CP call.
func TestCPRegistrar_Register_SkipsEmptySubscription(t *testing.T) {
	var cap captured
	srv := fakeCP(t, &cap)
	defer srv.Close()
	r := liveRegistrar(t, srv)
	if err := r.Register(context.Background(), webpush.Subscription{}); err != nil {
		t.Fatalf("Register (empty sub) should be a benign no-op, got %v", err)
	}
	if atomic.LoadInt32(&cap.hitCount) != 0 {
		t.Fatalf("expected NO CP call for an empty subscription, got %d", cap.hitCount)
	}
}

// TestCPRegistrar_Unregister_HMACAndBody verifies the unregister POST auth +
// body (ulid + endpoint only).
func TestCPRegistrar_Unregister_HMACAndBody(t *testing.T) {
	var cap captured
	srv := fakeCP(t, &cap)
	defer srv.Close()
	r := liveRegistrar(t, srv)
	const ep = "https://push.example/rm"
	if err := r.Unregister(context.Background(), ep); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != cpUnregisterPath {
		t.Fatalf("wrong method/path: %s %s", cap.method, cap.path)
	}
	if !cap.authOK {
		t.Fatalf("CP would REJECT the cell's unregister HMAC header")
	}
	var got unregisterBody
	if err := json.Unmarshal(cap.body, &got); err != nil {
		t.Fatal(err)
	}
	if got.ULID != testULID || got.Endpoint != ep {
		t.Fatalf("unregister body mismatch: %+v", got)
	}
}

// TestCPRegistrar_WrongSecretRejected proves the auth is real: a registrar keyed
// with the wrong secret produces a header the CP construction REJECTS, and the
// non-2xx surfaces as an error.
func TestCPRegistrar_WrongSecretRejected(t *testing.T) {
	var cap captured
	srv := fakeCP(t, &cap)
	defer srv.Close()
	r := NewCPRegistrar(CPRegistrarConfig{BaseURL: srv.URL, Secret: "WRONG", ULID: testULID})
	if r == nil {
		t.Fatal("registrar should build with a (wrong) secret set")
	}
	r.setClientForTest(srv.Client())
	err := r.Register(context.Background(), sampleSub("https://push.example/z"))
	if err == nil {
		t.Fatal("expected a non-2xx error when the CP rejects the forged HMAC")
	}
	if cap.authOK {
		t.Fatal("CP unexpectedly accepted a wrong-secret HMAC")
	}
}

// TestCPRegistrar_OriginFromFullURL verifies a full register URL (not just an
// origin) is accepted and normalised to the origin, then the path is appended.
func TestCPRegistrar_OriginFromFullURL(t *testing.T) {
	var cap captured
	srv := fakeCP(t, &cap)
	defer srv.Close()
	// Pass a FULL register URL as BaseURL; the registrar must normalise to origin.
	r := NewCPRegistrar(CPRegistrarConfig{BaseURL: srv.URL + cpRegisterPath, Secret: testSecret, ULID: testULID})
	if r == nil {
		t.Fatal("registrar should build from a full register URL")
	}
	r.setClientForTest(srv.Client())
	if err := r.Register(context.Background(), sampleSub("https://push.example/z")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if cap.path != cpRegisterPath {
		t.Fatalf("expected path %s, got %s", cpRegisterPath, cap.path)
	}
	if !cap.authOK {
		t.Fatal("HMAC must still verify when BaseURL was a full URL")
	}
}
