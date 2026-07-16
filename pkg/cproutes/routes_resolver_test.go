// routes_resolver_test.go — HTTP-level tests for the resolver routes.
//
// Tests:
//  1. GET /api/resolve/backend — unauthenticated → 401
//  2. GET /api/resolve/backend — hosted user → hosted endpoint returned
//  3. GET /api/resolve/backend — selfhost user → fabric route returned
//  4. GET /api/resolve/backend — invalid service → 400
//  5. /backend/mail/ — unauthenticated → 401
//  6. /backend/mail/ — hosted request proxied to upstream JMAP
//  7. /backend/mail/ — selfhost request routed to fabric (mock upstream)
//  8. E2E opaque forward — proxy streams bytes without decrypting (assert body intact)
package cproutes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/resolver"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestResolverHandlers builds a resolverHandlers with in-memory stubs and
// a real auth store backed by :memory: SQLite.
func newTestResolverHandlers(t *testing.T) (*resolverHandlers, *auth.Store, *resolver.MemStorageSource, *resolver.MemEnrollmentSource) {
	t.Helper()

	authStore, err := openAuthStoreForTest("file::memory:?cache=shared&mode=memory", []byte("test-secret"))
	if err != nil {
		t.Fatalf("open auth store: %v", err)
	}
	t.Cleanup(func() { authStore.Close() })

	storageSrc := resolver.NewMemStorageSource()
	enrollSrc := resolver.NewMemEnrollmentSource()

	res := &resolver.Resolver{
		HostedEndpointBase: "https://mail.vulos.org/jmap",
		Storage:            storageSrc,
		Enrollment:         enrollSrc,
	}

	h := &resolverHandlers{res: res, auth: authStore}
	return h, authStore, storageSrc, enrollSrc
}

// createTestSession creates a user + session in the auth store and returns the
// session token and user ID for use in tests.
func createTestSession(t *testing.T, authStore *auth.Store, email string) (token, userID string) {
	t.Helper()
	u, tok, err := authStore.Signup(context.Background(), email, "TestPass12!!", "127.0.0.1", "test-ua")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	return tok, u.ID
}

// setSessionCookie sets the session cookie on the request.
func setSessionCookie(r *http.Request, token string) {
	r.AddCookie(&http.Cookie{Name: "vc_session", Value: token})
}

// ---------------------------------------------------------------------------
// Test 1: unauthenticated → 401 on resolve endpoint
// ---------------------------------------------------------------------------

func TestResolveBackend_Unauthenticated(t *testing.T) {
	h, _, _, _ := newTestResolverHandlers(t)

	req := httptest.NewRequest("GET", "/api/resolve/backend?service=mail", nil)
	w := httptest.NewRecorder()

	h.resolveBackend(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 2: hosted user → resolve returns hosted endpoint
// ---------------------------------------------------------------------------

func TestResolveBackend_Hosted(t *testing.T) {
	h, authStore, storageSrc, _ := newTestResolverHandlers(t)
	h.res.HostedEndpointBase = "https://mail.vulos.org/jmap"

	tok, uid := createTestSession(t, authStore, "hosted@test.vulos.org")
	storageSrc.Set(uid, false) // hosted

	req := httptest.NewRequest("GET", "/api/resolve/backend?service=mail", nil)
	setSessionCookie(req, tok)
	w := httptest.NewRecorder()

	h.resolveBackend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var target resolver.BackendTarget
	if err := json.Unmarshal(w.Body.Bytes(), &target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if target.Kind != resolver.KindHosted {
		t.Errorf("expected kind=hosted, got %q", target.Kind)
	}
	if !strings.HasSuffix(target.Endpoint, "/mail") {
		t.Errorf("expected endpoint to end with /mail, got %q", target.Endpoint)
	}
	if target.ProxyPath != "/backend/mail" {
		t.Errorf("unexpected ProxyPath: %q", target.ProxyPath)
	}
}

// ---------------------------------------------------------------------------
// Test 3: selfhost user → resolve returns fabric route
// ---------------------------------------------------------------------------

func TestResolveBackend_SelfHost(t *testing.T) {
	h, authStore, storageSrc, enrollSrc := newTestResolverHandlers(t)

	tok, uid := createTestSession(t, authStore, "selfhost@test.vulos.org")
	storageSrc.Set(uid, true) // self-hosted
	enrollSrc.Set(uid, "https://mybox.example.com:443")

	req := httptest.NewRequest("GET", "/api/resolve/backend?service=mail", nil)
	setSessionCookie(req, tok)
	w := httptest.NewRecorder()

	h.resolveBackend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var target resolver.BackendTarget
	if err := json.Unmarshal(w.Body.Bytes(), &target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if target.Kind != resolver.KindSelfHostFabric {
		t.Errorf("expected kind=selfhost-fabric, got %q", target.Kind)
	}
	if target.FabricRoute != "https://mybox.example.com:443" {
		t.Errorf("unexpected FabricRoute: %q", target.FabricRoute)
	}
}

// ---------------------------------------------------------------------------
// Test 4: invalid service → 400
// ---------------------------------------------------------------------------

func TestResolveBackend_InvalidService(t *testing.T) {
	h, authStore, storageSrc, _ := newTestResolverHandlers(t)

	tok, uid := createTestSession(t, authStore, "svc@test.vulos.org")
	storageSrc.Set(uid, false)

	req := httptest.NewRequest("GET", "/api/resolve/backend?service=chat", nil)
	setSessionCookie(req, tok)
	w := httptest.NewRecorder()

	h.resolveBackend(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 5: proxy endpoint unauthenticated → 401
// ---------------------------------------------------------------------------

func TestProxyBackend_Unauthenticated(t *testing.T) {
	h, _, _, _ := newTestResolverHandlers(t)

	req := httptest.NewRequest("GET", "/backend/mail/", nil)
	w := httptest.NewRecorder()

	h.proxyBackend(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 6: hosted proxy — request forwarded to upstream JMAP mock
// ---------------------------------------------------------------------------

func TestProxyBackend_HostedForwarded(t *testing.T) {
	// Start a mock upstream JMAP server.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true,"from":"upstream-jmap"}`)
	}))
	defer upstream.Close()

	h, authStore, storageSrc, _ := newTestResolverHandlers(t)
	h.res.HostedEndpointBase = upstream.URL + "/jmap"

	tok, uid := createTestSession(t, authStore, "proxy@test.vulos.org")
	storageSrc.Set(uid, false) // hosted

	req := httptest.NewRequest("GET", "/backend/mail/messages", nil)
	setSessionCookie(req, tok)
	w := httptest.NewRecorder()

	h.proxyBackend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from proxy, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "upstream-jmap") {
		t.Errorf("expected upstream body, got: %s", body)
	}
}

// ---------------------------------------------------------------------------
// Test 7: selfhost proxy — request routed to fabric mock
// ---------------------------------------------------------------------------

func TestProxyBackend_SelfHostFabricRouted(t *testing.T) {
	// Start a mock fabric endpoint (represents the self-hosted box).
	fabricBox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true,"from":"selfhost-box"}`)
	}))
	defer fabricBox.Close()

	h, authStore, storageSrc, enrollSrc := newTestResolverHandlers(t)

	tok, uid := createTestSession(t, authStore, "sh@test.vulos.org")
	storageSrc.Set(uid, true) // self-hosted
	enrollSrc.Set(uid, fabricBox.URL)

	req := httptest.NewRequest("POST", "/backend/mail/jmap", strings.NewReader(`{"using":["urn:ietf:params:jmap:core"]}`))
	req.Header.Set("Content-Type", "application/json")
	setSessionCookie(req, tok)
	w := httptest.NewRecorder()

	h.proxyBackend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from fabric proxy, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "selfhost-box") {
		t.Errorf("expected fabric box body, got: %s", body)
	}
}

// ---------------------------------------------------------------------------
// Test 8: E2E opaque forward — proxy streams encrypted bytes without decrypting
//
// We send a binary payload that looks like ciphertext. The proxy must forward
// it byte-for-byte. We verify:
//   a. The response body is identical to what the upstream sent (no decoding).
//   b. The upstream received the exact bytes the client sent (no re-encoding).
// ---------------------------------------------------------------------------

func TestProxyBackend_E2EOpaqueForward(t *testing.T) {
	// Simulate an "encrypted" JMAP payload (binary blob).
	simulatedCiphertext := []byte{0x00, 0xDE, 0xAD, 0xBE, 0xEF, 0xFF, 0xFE, 0x01, 0x23}
	simulatedResponse := []byte{0x99, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11}

	var receivedBody []byte

	fabricBox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture what the proxy sent to us (must be the raw ciphertext).
		b, _ := io.ReadAll(r.Body)
		receivedBody = b
		// Respond with a different binary blob (encrypted response).
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(simulatedResponse)
	}))
	defer fabricBox.Close()

	h, authStore, storageSrc, enrollSrc := newTestResolverHandlers(t)

	tok, uid := createTestSession(t, authStore, "e2e@test.vulos.org")
	storageSrc.Set(uid, true)
	enrollSrc.Set(uid, fabricBox.URL)

	req := httptest.NewRequest("POST", "/backend/mail/jmap", strings.NewReader(string(simulatedCiphertext)))
	req.Header.Set("Content-Type", "application/octet-stream")
	setSessionCookie(req, tok)
	w := httptest.NewRecorder()

	h.proxyBackend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Assert: the proxy forwarded the ciphertext unchanged.
	if string(receivedBody) != string(simulatedCiphertext) {
		t.Errorf("proxy modified the request body: got %v, want %v", receivedBody, simulatedCiphertext)
	}

	// Assert: the proxy returned the response bytes unchanged (no decoding/re-encoding).
	respBody := w.Body.Bytes()
	if string(respBody) != string(simulatedResponse) {
		t.Errorf("proxy modified the response body: got %v, want %v", respBody, simulatedResponse)
	}
}

// ---------------------------------------------------------------------------
// RESOLVE-LAN-01: LAN-candidate failover (HTTP-level)
// ---------------------------------------------------------------------------

// Selfhost user with a registered box: response includes LAN candidate AND
// preserves the existing FabricRoute field (backward compat).
func TestResolveBackend_LANCandidate_SelfHost(t *testing.T) {
	h, authStore, storageSrc, enrollSrc := newTestResolverHandlers(t)

	tok, uid := createTestSession(t, authStore, "lan-sh@test.vulos.org")
	storageSrc.Set(uid, true)
	enrollSrc.Set(uid, "https://mybox.example.com:443") // Set() also seeds BoxID = uid

	req := httptest.NewRequest("GET", "/api/resolve/backend?service=mail", nil)
	setSessionCookie(req, tok)
	w := httptest.NewRecorder()

	h.resolveBackend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var target resolver.BackendTarget
	if err := json.Unmarshal(w.Body.Bytes(), &target); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if target.FabricRoute != "https://mybox.example.com:443" {
		t.Errorf("backward-compat: FabricRoute lost, got %q", target.FabricRoute)
	}
	if target.LANCandidate == nil {
		t.Fatal("expected LANCandidate in response")
	}
	if target.LANCandidate.BoxID != uid {
		t.Errorf("LANCandidate.BoxID: want %q, got %q", uid, target.LANCandidate.BoxID)
	}
	wantLAN := "https://box." + uid + ".lan.vulos.org/jmap/mail"
	if target.LANCandidate.Endpoint != wantLAN {
		t.Errorf("LANCandidate.Endpoint: want %q, got %q", wantLAN, target.LANCandidate.Endpoint)
	}
}

// Hosted user with a co-located box registered: LAN candidate alongside the
// existing hosted Endpoint (backward compat).
func TestResolveBackend_LANCandidate_HostedWithBox(t *testing.T) {
	h, authStore, storageSrc, enrollSrc := newTestResolverHandlers(t)

	tok, uid := createTestSession(t, authStore, "lan-hosted@test.vulos.org")
	storageSrc.Set(uid, false) // hosted
	enrollSrc.SetBoxID(uid, uid)

	req := httptest.NewRequest("GET", "/api/resolve/backend?service=office", nil)
	setSessionCookie(req, tok)
	w := httptest.NewRecorder()

	h.resolveBackend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var target resolver.BackendTarget
	if err := json.Unmarshal(w.Body.Bytes(), &target); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if target.Kind != resolver.KindHosted {
		t.Errorf("expected KindHosted, got %q", target.Kind)
	}
	if !strings.HasSuffix(target.Endpoint, "/office") {
		t.Errorf("backward-compat: Endpoint lost, got %q", target.Endpoint)
	}
	if target.LANCandidate == nil {
		t.Fatal("expected LANCandidate for hosted-with-box")
	}
}

// Hosted user with NO registered box: LAN candidate field is omitted from
// the JSON entirely (omitempty), so existing clients see exactly what they
// saw before this change.
func TestResolveBackend_LANCandidate_OmittedForNoBox(t *testing.T) {
	h, authStore, storageSrc, _ := newTestResolverHandlers(t)

	tok, uid := createTestSession(t, authStore, "no-box@test.vulos.org")
	storageSrc.Set(uid, false) // hosted, no enrollment

	req := httptest.NewRequest("GET", "/api/resolve/backend?service=mail", nil)
	setSessionCookie(req, tok)
	w := httptest.NewRecorder()

	h.resolveBackend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "lan_candidate") {
		t.Errorf("expected lan_candidate field to be omitted, got: %s", w.Body.String())
	}
	var target resolver.BackendTarget
	if err := json.Unmarshal(w.Body.Bytes(), &target); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if target.LANCandidate != nil {
		t.Errorf("expected nil LANCandidate, got %+v", target.LANCandidate)
	}
	if target.Endpoint == "" {
		t.Error("backward-compat: Endpoint must still be populated")
	}
}

// ---------------------------------------------------------------------------
// Test 9: parseFabricPath helper
// ---------------------------------------------------------------------------

func TestParseFabricPath(t *testing.T) {
	cases := []struct {
		path     string
		wantSvc  string
		wantRest string
	}{
		{"/backend/mail", "mail", ""},
		{"/backend/mail/", "mail", ""},
		{"/backend/office/docs/1", "office", "docs/1"},
		{"/backend/talk/rooms/abc/messages", "talk", "rooms/abc/messages"},
		{"/backend/", "", ""},
	}
	for _, tc := range cases {
		svc, rest := parseFabricPath(tc.path)
		if svc != tc.wantSvc || rest != tc.wantRest {
			t.Errorf("parseFabricPath(%q): got (%q, %q), want (%q, %q)",
				tc.path, svc, rest, tc.wantSvc, tc.wantRest)
		}
	}
}
