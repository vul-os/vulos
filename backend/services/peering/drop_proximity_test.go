// drop_proximity_test.go — tests for drop_proximity.go (PEER-35)
package peering

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// proxFakeHTTPClient implements proxHTTPClient and serves canned responses.
type proxFakeHTTPClient struct {
	// handler is called for every Do invocation.
	handler func(req *http.Request) (*http.Response, error)
}

func (f *proxFakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return f.handler(req)
}

// proxJSONResponse constructs an *http.Response with a JSON body.
func proxJSONResponse(status int, v interface{}) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(b)),
	}
}

// proxErrResponse constructs a minimal non-OK response.
func proxErrResponse(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// proxNewTestService creates a ProxService with a no-op HTTP client (no
// real outbound calls) and a deterministic base URL for rendezvous.
func proxNewTestService(t *testing.T) *ProxService {
	t.Helper()
	svc := NewProxService("vula:ed25519:testprox", "127.0.0.1:8080")
	// Replace HTTP client with a stub that returns 404 for every call
	// (rendezvous not reachable in unit tests; overridden per-test as needed).
	svc.httpClient = &proxFakeHTTPClient{
		handler: func(req *http.Request) (*http.Response, error) {
			return proxErrResponse(http.StatusNotFound), nil
		},
	}
	svc.rendezvousBase = "http://fake-rendezvous.test"
	return svc
}

// proxPost sends a POST request to mux and returns the recorder.
func proxPost(t *testing.T, mux *http.ServeMux, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// Tests: proxRandCode
// ---------------------------------------------------------------------------

func TestProxRandCodeLength(t *testing.T) {
	for i := 0; i < 20; i++ {
		code, err := proxRandCode()
		if err != nil {
			t.Fatalf("proxRandCode: %v", err)
		}
		if len(code) != proxCodeDigits {
			t.Fatalf("want %d digits, got %d: %q", proxCodeDigits, len(code), code)
		}
		for _, ch := range code {
			if ch < '0' || ch > '9' {
				t.Fatalf("non-digit character %q in code %q", ch, code)
			}
		}
	}
}

func TestProxRandCodeUniqueness(t *testing.T) {
	seen := make(map[string]bool, 50)
	for i := 0; i < 50; i++ {
		code, err := proxRandCode()
		if err != nil {
			t.Fatalf("proxRandCode: %v", err)
		}
		seen[code] = true
	}
	// With 1 000 000 possible codes and 50 draws the collision probability
	// is astronomically low — any collision means the generator is broken.
	if len(seen) < 49 {
		t.Fatalf("too many collisions (%d unique in 50 draws)", len(seen))
	}
}

// ---------------------------------------------------------------------------
// Tests: ProxGenerate
// ---------------------------------------------------------------------------

func TestProxGenerateReturnsCode(t *testing.T) {
	svc := proxNewTestService(t)
	entry, err := svc.ProxGenerate("")
	if err != nil {
		t.Fatalf("ProxGenerate: %v", err)
	}
	if len(entry.Code) != proxCodeDigits {
		t.Fatalf("want %d-digit code, got %q", proxCodeDigits, entry.Code)
	}
	if entry.VulaID != svc.selfVulaID {
		t.Fatalf("want vula_id %q, got %q", svc.selfVulaID, entry.VulaID)
	}
}

func TestProxGenerateTTL(t *testing.T) {
	svc := proxNewTestService(t)
	before := time.Now()
	entry, err := svc.ProxGenerate("")
	if err != nil {
		t.Fatalf("ProxGenerate: %v", err)
	}
	after := time.Now()
	low := before.Add(proxCodeTTL - time.Second)
	high := after.Add(proxCodeTTL + time.Second)
	if entry.ExpiresAt.Before(low) || entry.ExpiresAt.After(high) {
		t.Fatalf("ExpiresAt %v not within expected TTL window [%v, %v]",
			entry.ExpiresAt, low, high)
	}
}

func TestProxGenerateOverridesSelfAddr(t *testing.T) {
	svc := proxNewTestService(t)
	entry, err := svc.ProxGenerate("10.0.0.1:9090")
	if err != nil {
		t.Fatalf("ProxGenerate: %v", err)
	}
	if entry.OwnerAddr != "10.0.0.1:9090" {
		t.Fatalf("want owner_addr 10.0.0.1:9090, got %q", entry.OwnerAddr)
	}
}

func TestProxGenerateDefaultSelfAddr(t *testing.T) {
	svc := proxNewTestService(t)
	svc.selfAddr = "192.168.1.100:8080"
	entry, err := svc.ProxGenerate("")
	if err != nil {
		t.Fatalf("ProxGenerate: %v", err)
	}
	if entry.OwnerAddr != "192.168.1.100:8080" {
		t.Fatalf("want default self addr, got %q", entry.OwnerAddr)
	}
}

func TestProxGenerateStoresCode(t *testing.T) {
	svc := proxNewTestService(t)
	if svc.ProxCodeCount() != 0 {
		t.Fatal("want 0 codes initially")
	}
	_, err := svc.ProxGenerate("")
	if err != nil {
		t.Fatalf("ProxGenerate: %v", err)
	}
	if svc.ProxCodeCount() != 1 {
		t.Fatalf("want 1 stored code, got %d", svc.ProxCodeCount())
	}
}

// ---------------------------------------------------------------------------
// Tests: ProxRedeem — local
// ---------------------------------------------------------------------------

func TestProxRedeemLocalSuccess(t *testing.T) {
	svc := proxNewTestService(t)
	entry, _ := svc.ProxGenerate("10.0.0.5:8080")

	result, err := svc.ProxRedeem(context.Background(), entry.Code)
	if err != nil {
		t.Fatalf("ProxRedeem: %v", err)
	}
	if result.OwnerAddr != "10.0.0.5:8080" {
		t.Fatalf("want 10.0.0.5:8080, got %q", result.OwnerAddr)
	}
	if result.VulaID != svc.selfVulaID {
		t.Fatalf("want vula_id %q, got %q", svc.selfVulaID, result.VulaID)
	}
}

// TestProxRedeemSingleUse verifies that a code can only be redeemed once.
func TestProxRedeemSingleUse(t *testing.T) {
	svc := proxNewTestService(t)
	entry, _ := svc.ProxGenerate("")

	// First redemption — must succeed (locally).
	_, err := svc.ProxRedeem(context.Background(), entry.Code)
	if err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	// Code should be gone from local store.
	if svc.ProxCodeCount() != 0 {
		t.Fatal("code should be consumed after first use")
	}
	// Second redemption — falls through to rendezvous (returns 404 from stub).
	_, err = svc.ProxRedeem(context.Background(), entry.Code)
	if err == nil {
		t.Fatal("want error on second redeem, got nil")
	}
}

// TestProxRedeemExpiredCode verifies that an expired code is rejected.
func TestProxRedeemExpiredCode(t *testing.T) {
	svc := proxNewTestService(t)
	entry, _ := svc.ProxGenerate("")

	// Back-date the expiry to force expiration.
	svc.mu.Lock()
	svc.codes[entry.Code].ExpiresAt = time.Now().Add(-1 * time.Second)
	svc.mu.Unlock()

	// Redeem should not find it locally (pruned) and rendezvous stub returns 404.
	_, err := svc.ProxRedeem(context.Background(), entry.Code)
	if err == nil {
		t.Fatal("want error for expired code, got nil")
	}
}

// TestProxRedeemInvalidLength verifies codes of wrong length are rejected immediately.
func TestProxRedeemInvalidLength(t *testing.T) {
	svc := proxNewTestService(t)
	_, err := svc.ProxRedeem(context.Background(), "123")
	if err == nil {
		t.Fatal("want error for short code, got nil")
	}
}

// ---------------------------------------------------------------------------
// Tests: ProxRedeem — remote rendezvous fallback
// ---------------------------------------------------------------------------

// TestProxRedeemRemoteFallback verifies that when a code is not found locally
// the service calls the rendezvous and returns the result.
func TestProxRedeemRemoteFallback(t *testing.T) {
	svc := proxNewTestService(t)

	wantAddr := "remote.vulos.org:8080"
	wantVulaID := "vula:ed25519:remoteid"

	svc.httpClient = &proxFakeHTTPClient{
		handler: func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Errorf("rendezvous GET expected, got %s", req.Method)
			}
			if !strings.Contains(req.URL.Path, "123456") {
				t.Errorf("expected code in URL path, got %s", req.URL.Path)
			}
			return proxJSONResponse(http.StatusOK, proxRendezvousPayload{
				Code:      "123456",
				OwnerAddr: wantAddr,
				VulaID:    wantVulaID,
				ExpiresAt: time.Now().Add(proxCodeTTL),
			}), nil
		},
	}

	result, err := svc.ProxRedeem(context.Background(), "123456")
	if err != nil {
		t.Fatalf("ProxRedeem remote: %v", err)
	}
	if result.OwnerAddr != wantAddr {
		t.Fatalf("want %q, got %q", wantAddr, result.OwnerAddr)
	}
	if result.VulaID != wantVulaID {
		t.Fatalf("want %q, got %q", wantVulaID, result.VulaID)
	}
}

// TestProxRedeemRemoteNotFound verifies that a 404 from rendezvous produces an error.
func TestProxRedeemRemoteNotFound(t *testing.T) {
	svc := proxNewTestService(t)
	// httpClient already returns 404 by default.
	_, err := svc.ProxRedeem(context.Background(), "999999")
	if err == nil {
		t.Fatal("want error for 404 from rendezvous, got nil")
	}
}

// TestProxRedeemRemoteServerError verifies that a 500 from rendezvous produces an error.
func TestProxRedeemRemoteServerError(t *testing.T) {
	svc := proxNewTestService(t)
	svc.httpClient = &proxFakeHTTPClient{
		handler: func(_ *http.Request) (*http.Response, error) {
			return proxErrResponse(http.StatusInternalServerError), nil
		},
	}
	_, err := svc.ProxRedeem(context.Background(), "111111")
	if err == nil {
		t.Fatal("want error for 500 from rendezvous, got nil")
	}
}

// ---------------------------------------------------------------------------
// Tests: ProxPublishToRendezvous
// ---------------------------------------------------------------------------

// TestProxPublishToRendezvousSuccess verifies that a successful POST is
// accepted (200 or 201).
func TestProxPublishToRendezvousSuccess(t *testing.T) {
	svc := proxNewTestService(t)

	var capturedBody proxRendezvousPayload
	svc.httpClient = &proxFakeHTTPClient{
		handler: func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", req.Method)
			}
			json.NewDecoder(req.Body).Decode(&capturedBody) //nolint:errcheck
			return proxJSONResponse(http.StatusCreated, map[string]string{"status": "ok"}), nil
		},
	}

	entry := &proxCode{
		Code:      "847291",
		OwnerAddr: "my.vulos.org:8080",
		VulaID:    svc.selfVulaID,
		ExpiresAt: time.Now().Add(proxCodeTTL),
	}
	err := svc.ProxPublishToRendezvous(context.Background(), entry)
	if err != nil {
		t.Fatalf("ProxPublishToRendezvous: %v", err)
	}
	if capturedBody.Code != "847291" {
		t.Fatalf("want code 847291, got %q", capturedBody.Code)
	}
}

func TestProxPublishToRendezvousFailure(t *testing.T) {
	svc := proxNewTestService(t)
	svc.httpClient = &proxFakeHTTPClient{
		handler: func(_ *http.Request) (*http.Response, error) {
			return proxErrResponse(http.StatusInternalServerError), nil
		},
	}
	entry := &proxCode{Code: "000000", ExpiresAt: time.Now().Add(proxCodeTTL)}
	err := svc.ProxPublishToRendezvous(context.Background(), entry)
	if err == nil {
		t.Fatal("want error for 500 from rendezvous publish, got nil")
	}
}

// ---------------------------------------------------------------------------
// Tests: HTTP handler — /api/peering/drop/code/generate
// ---------------------------------------------------------------------------

// TestProxHTTPGenerateSuccess calls the generate endpoint via httptest and
// asserts a 6-digit code and expires_at are returned.
func TestProxHTTPGenerateSuccess(t *testing.T) {
	svc := proxNewTestService(t)
	mux := http.NewServeMux()
	RegisterProximityHandlers(mux, svc)

	rr := proxPost(t, mux, "/api/peering/drop/code/generate",
		proxGenerateRequest{SelfAddr: "10.0.0.1:8080"})
	if rr.Code != http.StatusOK {
		t.Fatalf("generate: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp proxGenerateResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Code) != proxCodeDigits {
		t.Fatalf("want %d-digit code, got %q", proxCodeDigits, resp.Code)
	}
	if resp.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt should not be zero")
	}
	if resp.ExpiresAt.Before(time.Now()) {
		t.Fatal("ExpiresAt should be in the future")
	}
}

func TestProxHTTPGenerateMethodNotAllowed(t *testing.T) {
	svc := proxNewTestService(t)
	mux := http.NewServeMux()
	RegisterProximityHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/drop/code/generate", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: HTTP handler — /api/peering/drop/code/redeem
// ---------------------------------------------------------------------------

// TestProxHTTPRedeemLocalSuccess generates then redeems a code via HTTP,
// verifying the full round-trip through the handlers.
func TestProxHTTPRedeemLocalSuccess(t *testing.T) {
	svc := proxNewTestService(t)
	mux := http.NewServeMux()
	RegisterProximityHandlers(mux, svc)

	// Generate a code.
	genRR := proxPost(t, mux, "/api/peering/drop/code/generate",
		proxGenerateRequest{SelfAddr: "10.0.0.2:8080"})
	if genRR.Code != http.StatusOK {
		t.Fatalf("generate: %d %s", genRR.Code, genRR.Body.String())
	}
	var genResp proxGenerateResponse
	json.NewDecoder(genRR.Body).Decode(&genResp) //nolint:errcheck

	// Redeem it.
	redeemRR := proxPost(t, mux, "/api/peering/drop/code/redeem",
		proxRedeemRequest{Code: genResp.Code})
	if redeemRR.Code != http.StatusOK {
		t.Fatalf("redeem: want 200, got %d: %s", redeemRR.Code, redeemRR.Body.String())
	}

	var redeemResp proxRedeemResponse
	if err := json.NewDecoder(redeemRR.Body).Decode(&redeemResp); err != nil {
		t.Fatalf("decode redeem: %v", err)
	}
	if redeemResp.OwnerAddr != "10.0.0.2:8080" {
		t.Fatalf("want 10.0.0.2:8080, got %q", redeemResp.OwnerAddr)
	}
}

// TestProxHTTPRedeemSingleUse verifies that the second redeem returns 404
// (code consumed after first use; rendezvous stub also returns 404).
func TestProxHTTPRedeemSingleUse(t *testing.T) {
	svc := proxNewTestService(t)
	mux := http.NewServeMux()
	RegisterProximityHandlers(mux, svc)

	genRR := proxPost(t, mux, "/api/peering/drop/code/generate", proxGenerateRequest{})
	var genResp proxGenerateResponse
	json.NewDecoder(genRR.Body).Decode(&genResp) //nolint:errcheck

	// First redeem — OK.
	proxPost(t, mux, "/api/peering/drop/code/redeem", proxRedeemRequest{Code: genResp.Code})

	// Second redeem — must fail.
	rr := proxPost(t, mux, "/api/peering/drop/code/redeem", proxRedeemRequest{Code: genResp.Code})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("second redeem: want 404, got %d", rr.Code)
	}
}

func TestProxHTTPRedeemMissingCode(t *testing.T) {
	svc := proxNewTestService(t)
	mux := http.NewServeMux()
	RegisterProximityHandlers(mux, svc)

	rr := proxPost(t, mux, "/api/peering/drop/code/redeem", proxRedeemRequest{})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

func TestProxHTTPRedeemMethodNotAllowed(t *testing.T) {
	svc := proxNewTestService(t)
	mux := http.NewServeMux()
	RegisterProximityHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/drop/code/redeem", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: httptest rendezvous mock (cross-network scenario)
// ---------------------------------------------------------------------------

// TestProxCrossNetworkViaRendezvousMock uses an httptest.Server to simulate
// the vulos.org rendezvous service. Alice generates a code, publishes it to
// the mock rendezvous; Bob redeems it from the mock rendezvous.
func TestProxCrossNetworkViaRendezvousMock(t *testing.T) {
	// In-memory rendezvous store.
	var storeMu sync.Mutex
	store := make(map[string]proxRendezvousPayload)

	rendezvousServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/drop/code":
			var p proxRendezvousPayload
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			storeMu.Lock()
			store[p.Code] = p
			storeMu.Unlock()
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/drop/code/"):
			code := strings.TrimPrefix(r.URL.Path, "/api/drop/code/")
			storeMu.Lock()
			p, ok := store[code]
			storeMu.Unlock()
			if !ok || time.Now().After(p.ExpiresAt) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			// Single-use: remove after retrieval.
			storeMu.Lock()
			delete(store, code)
			storeMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(p) //nolint:errcheck

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer rendezvousServer.Close()

	// Alice's service — will publish to the mock rendezvous.
	alice := NewProxService("vula:ed25519:alice", "alice.local:8080")
	alice.rendezvousBase = rendezvousServer.URL
	// Alice uses the real http.Client so it can hit the httptest server.

	// Bob's service — will NOT have the code locally; redeems from rendezvous.
	bob := NewProxService("vula:ed25519:bob", "bob.local:8080")
	bob.rendezvousBase = rendezvousServer.URL

	// Alice generates a code.
	entry, err := alice.ProxGenerate("alice.local:8080")
	if err != nil {
		t.Fatalf("alice generate: %v", err)
	}

	// Alice publishes to rendezvous (synchronously in test).
	if err := alice.ProxPublishToRendezvous(context.Background(), entry); err != nil {
		t.Fatalf("alice publish: %v", err)
	}

	// Bob redeems the code — should fall through to rendezvous (not in Bob's local store).
	result, err := bob.ProxRedeem(context.Background(), entry.Code)
	if err != nil {
		t.Fatalf("bob redeem: %v", err)
	}
	if result.OwnerAddr != "alice.local:8080" {
		t.Fatalf("want alice.local:8080, got %q", result.OwnerAddr)
	}
	if result.VulaID != "vula:ed25519:alice" {
		t.Fatalf("want alice vula_id, got %q", result.VulaID)
	}

	// Third actor (Charlie) tries to redeem the same code — must fail (single-use).
	charlie := NewProxService("vula:ed25519:charlie", "charlie.local:8080")
	charlie.rendezvousBase = rendezvousServer.URL
	_, err = charlie.ProxRedeem(context.Background(), entry.Code)
	if err == nil {
		t.Fatal("charlie should not be able to redeem an already-consumed code")
	}
}

// ---------------------------------------------------------------------------
// Tests: pruning
// ---------------------------------------------------------------------------

func TestProxPruneExpired(t *testing.T) {
	svc := proxNewTestService(t)

	// Inject an already-expired entry directly.
	svc.mu.Lock()
	svc.codes["000001"] = &proxCode{
		Code:      "000001",
		ExpiresAt: time.Now().Add(-1 * time.Second),
	}
	svc.mu.Unlock()

	if svc.ProxCodeCount() != 0 {
		t.Fatal("expired code should not be counted")
	}
}

// ---------------------------------------------------------------------------
// Tests: ProxSetRendezvousBase / ProxRendezvousBase
// ---------------------------------------------------------------------------

func TestProxRendezvousBaseOverride(t *testing.T) {
	svc := proxNewTestService(t)
	svc.ProxSetRendezvousBase("https://custom.rendezvous.example")
	if svc.ProxRendezvousBase() != "https://custom.rendezvous.example" {
		t.Fatalf("want custom base, got %q", svc.ProxRendezvousBase())
	}
}

// ---------------------------------------------------------------------------
// Tests: env-based rendezvous override (NewProxService)
// ---------------------------------------------------------------------------

func TestProxNewServiceEnvOverride(t *testing.T) {
	t.Setenv(proxRendezvousEnvKey, "https://env-override.vulos.org")
	svc := NewProxService("vula:ed25519:test", "")
	if svc.rendezvousBase != "https://env-override.vulos.org" {
		t.Fatalf("want env-override URL, got %q", svc.rendezvousBase)
	}
}

func TestProxNewServiceDefaultRendezvous(t *testing.T) {
	// Unset the env var to ensure default is used.
	t.Setenv(proxRendezvousEnvKey, "")
	svc := NewProxService("vula:ed25519:test", "")
	if svc.rendezvousBase != proxDefaultRendezvousBase {
		t.Fatalf("want default rendezvous base, got %q", svc.rendezvousBase)
	}
}

// ---------------------------------------------------------------------------
// Tests: RegisterProximityHandlers wires distinct paths
// ---------------------------------------------------------------------------

func TestProxRegisterHandlersEndpoints(t *testing.T) {
	svc := proxNewTestService(t)
	mux := http.NewServeMux()
	RegisterProximityHandlers(mux, svc)

	paths := []string{
		"/api/peering/drop/code/generate",
		"/api/peering/drop/code/redeem",
	}
	for _, p := range paths {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}"))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		// Any non-404 response confirms the path is registered.
		if rr.Code == http.StatusNotFound {
			t.Errorf("path %s not registered (got 404)", p)
		}
	}
}
