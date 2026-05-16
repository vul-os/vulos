package peering

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// resetRegistry replaces the package-level singletons with clean ones so that
// tests are isolated from each other.
func resetRegistry() {
	epRegistry = &epRegistryStore{endpoints: make(map[string]*epEndpoint)}
	epDedupe = &epDedupeCache{seen: make(map[string]time.Time), ttl: 24 * time.Hour}
}

// ep builds a minimal epEndpoint for use in tests.
func ep(id, vulaID, server string, priority int) *epEndpoint {
	return &epEndpoint{
		ID:       id,
		VulaID:   vulaID,
		Server:   server,
		Priority: priority,
		Healthy:  true,
	}
}

// ---------------------------------------------------------------------------
// Registry unit tests
// ---------------------------------------------------------------------------

func TestEpRegisterAndList(t *testing.T) {
	resetRegistry()

	epRegistry.epRegister(ep("ep1", "alice", "home.alice:8080", 1))
	epRegistry.epRegister(ep("ep2", "alice", "cloud.alice:8080", 2))
	epRegistry.epRegister(ep("ep3", "bob", "bob.server:8080", 1))

	aliceEps := epRegistry.epList("alice")
	if len(aliceEps) != 2 {
		t.Fatalf("expected 2 endpoints for alice, got %d", len(aliceEps))
	}
	// Priority 1 must come first.
	if aliceEps[0].ID != "ep1" {
		t.Errorf("expected ep1 first, got %s", aliceEps[0].ID)
	}

	all := epRegistry.epListAll()
	if len(all) != 3 {
		t.Fatalf("expected 3 total endpoints, got %d", len(all))
	}
}

func TestEpRemove(t *testing.T) {
	resetRegistry()

	epRegistry.epRegister(ep("ep1", "alice", "home.alice:8080", 1))
	if !epRegistry.epRemove("ep1") {
		t.Fatal("epRemove should return true for existing id")
	}
	if epRegistry.epRemove("ep1") {
		t.Fatal("epRemove should return false after deletion")
	}
	if len(epRegistry.epList("alice")) != 0 {
		t.Fatal("alice should have 0 endpoints after removal")
	}
}

func TestEpSetPriority(t *testing.T) {
	resetRegistry()

	epRegistry.epRegister(ep("ep1", "alice", "home:8080", 5))
	if !epRegistry.epSetPriority("ep1", 1) {
		t.Fatal("epSetPriority should return true for existing id")
	}
	eps := epRegistry.epList("alice")
	if eps[0].Priority != 1 {
		t.Errorf("expected priority 1, got %d", eps[0].Priority)
	}
	if epRegistry.epSetPriority("nonexistent", 0) {
		t.Fatal("epSetPriority should return false for unknown id")
	}
}

func TestEpListSortedByPriorityThenLatency(t *testing.T) {
	resetRegistry()

	// Same priority, different latencies.
	e1 := ep("ep1", "alice", "a1:8080", 1)
	e1.LatencyMS = 200
	e2 := ep("ep2", "alice", "a2:8080", 1)
	e2.LatencyMS = 50
	e3 := ep("ep3", "alice", "a3:8080", 2)
	e3.LatencyMS = 10

	epRegistry.epRegister(e1)
	epRegistry.epRegister(e2)
	epRegistry.epRegister(e3)

	eps := epRegistry.epList("alice")
	// ep2 (priority 1, 50ms) must beat ep1 (priority 1, 200ms) which must beat
	// ep3 (priority 2, even though faster).
	if eps[0].ID != "ep2" {
		t.Errorf("expected ep2 first, got %s", eps[0].ID)
	}
	if eps[1].ID != "ep1" {
		t.Errorf("expected ep1 second, got %s", eps[1].ID)
	}
	if eps[2].ID != "ep3" {
		t.Errorf("expected ep3 third, got %s", eps[2].ID)
	}
}

// ---------------------------------------------------------------------------
// Dedup cache tests
// ---------------------------------------------------------------------------

func TestEpDedupeFirstCallFalseSecondTrue(t *testing.T) {
	resetRegistry()

	if epDedupe.epSeen("msg-uuid-v7-001") {
		t.Fatal("first call should return false (not yet seen)")
	}
	if !epDedupe.epSeen("msg-uuid-v7-001") {
		t.Fatal("second call should return true (already seen)")
	}
}

func TestEpDedupeDistinctIDsAreIndependent(t *testing.T) {
	resetRegistry()

	epDedupe.epSeen("id-A")
	if epDedupe.epSeen("id-B") {
		t.Fatal("id-B should not be marked seen")
	}
}

func TestEpDedupeEviction(t *testing.T) {
	resetRegistry()
	// Set a very short TTL so entries expire quickly.
	epDedupe.ttl = 10 * time.Millisecond
	epDedupe.epSeen("id-X")
	time.Sleep(20 * time.Millisecond)
	// After eviction the same ID should be treated as new.
	if epDedupe.epSeen("id-X") {
		t.Fatal("id-X should have been evicted and treated as unseen")
	}
}

// ---------------------------------------------------------------------------
// HTTP handler tests (via httptest)
// ---------------------------------------------------------------------------

func newMux() *http.ServeMux {
	resetRegistry()
	mux := http.NewServeMux()
	RegisterEndpointHandlers(mux)
	return mux
}

func doJSON(t *testing.T, mux *http.ServeMux, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestHTTPRegisterEndpoint(t *testing.T) {
	mux := newMux()

	rr := doJSON(t, mux, http.MethodPost, "/api/peering/endpoints/register", map[string]interface{}{
		"id":       "ep-http-1",
		"vula_id":  "alice",
		"server":   "home.alice:8080",
		"priority": 1,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var got epEndpoint
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if got.ID != "ep-http-1" {
		t.Errorf("unexpected id: %s", got.ID)
	}
}

func TestHTTPRegisterEndpointMissingFields(t *testing.T) {
	mux := newMux()
	rr := doJSON(t, mux, http.MethodPost, "/api/peering/endpoints/register", map[string]interface{}{
		"id": "ep-missing",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestHTTPListEndpoints(t *testing.T) {
	mux := newMux()

	// Register two endpoints for alice, one for bob.
	doJSON(t, mux, http.MethodPost, "/api/peering/endpoints/register", map[string]interface{}{
		"id": "a1", "vula_id": "alice", "server": "a1:8080", "priority": 1,
	})
	doJSON(t, mux, http.MethodPost, "/api/peering/endpoints/register", map[string]interface{}{
		"id": "a2", "vula_id": "alice", "server": "a2:8080", "priority": 2,
	})
	doJSON(t, mux, http.MethodPost, "/api/peering/endpoints/register", map[string]interface{}{
		"id": "b1", "vula_id": "bob", "server": "b1:8080", "priority": 1,
	})

	// List all.
	rr := doJSON(t, mux, http.MethodGet, "/api/peering/endpoints", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var all []epEndpoint
	json.Unmarshal(rr.Body.Bytes(), &all)
	if len(all) != 3 {
		t.Fatalf("expected 3 endpoints, got %d", len(all))
	}

	// Filter by vula_id.
	rr2 := doJSON(t, mux, http.MethodGet, "/api/peering/endpoints?vula_id=alice", nil)
	var aliceEps []epEndpoint
	json.Unmarshal(rr2.Body.Bytes(), &aliceEps)
	if len(aliceEps) != 2 {
		t.Fatalf("expected 2 alice endpoints, got %d", len(aliceEps))
	}
}

func TestHTTPRemoveEndpoint(t *testing.T) {
	mux := newMux()
	doJSON(t, mux, http.MethodPost, "/api/peering/endpoints/register", map[string]interface{}{
		"id": "ep-del", "vula_id": "alice", "server": "a:8080", "priority": 1,
	})

	rr := doJSON(t, mux, http.MethodDelete, "/api/peering/endpoints/ep-del", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	// Second delete should be 404.
	rr2 := doJSON(t, mux, http.MethodDelete, "/api/peering/endpoints/ep-del", nil)
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr2.Code)
	}
}

func TestHTTPSetPriority(t *testing.T) {
	mux := newMux()
	doJSON(t, mux, http.MethodPost, "/api/peering/endpoints/register", map[string]interface{}{
		"id": "ep-pri", "vula_id": "alice", "server": "a:8080", "priority": 5,
	})

	rr := doJSON(t, mux, http.MethodPut, "/api/peering/endpoints/ep-pri/priority", map[string]interface{}{
		"priority": 1,
	})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	eps := epRegistry.epList("alice")
	if eps[0].Priority != 1 {
		t.Errorf("expected updated priority 1, got %d", eps[0].Priority)
	}
}

func TestHTTPSetPriorityNotFound(t *testing.T) {
	mux := newMux()
	rr := doJSON(t, mux, http.MethodPut, "/api/peering/endpoints/nope/priority", map[string]interface{}{
		"priority": 1,
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// EndpointFailoverDeliver tests
// ---------------------------------------------------------------------------

// liveFakeServer starts an httptest server that responds 200 to POST requests
// and counts calls.
func liveFakeServer(t *testing.T, hits *atomic.Int64, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if delay > 0 {
			time.Sleep(delay)
		}
		w.WriteHeader(http.StatusOK)
	}))
}

// deadFakeServer always returns 500.
func deadFakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
}

// hostOf strips the http:// scheme so we can feed the host:port to epDoDeliver.
func hostOf(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestFailoverDeliverSucceedsViaLiveEndpoint(t *testing.T) {
	resetRegistry()

	var hits atomic.Int64
	live := liveFakeServer(t, &hits, 0)
	defer live.Close()

	// Override the shared client to allow plain HTTP (test servers use http://).
	orig := epHTTPClient
	epHTTPClient = &http.Client{Timeout: 3 * time.Second}
	defer func() { epHTTPClient = orig }()

	// Patch epDoDeliver to use http:// for test servers.
	// We do this by making the endpoint server address match http:// scheme via
	// a custom deliver that we inject through the exported helper function directly.
	// Because epDoDeliver is unexported we test it indirectly via EndpointFailoverDeliver
	// but we must prefix with http:// in our server address.  We temporarily override
	// epDoDeliver-like behaviour by passing endpoints whose Server field already
	// includes the scheme (not valid in production but needed here).
	//
	// To keep the test realistic without changing production code we use a tiny
	// httptest.Server and directly call epDoDeliver.
	ctx := context.Background()
	err := epDoDeliver(ctx, hostOf(live), "/", []byte(`{"test":true}`))
	if err != nil {
		// The test server speaks HTTP, not HTTPS. epDoDeliver always prepends
		// https://.  Use a redirecting proxy approach by hitting a TLS server
		// or confirm the error is scheme-related only and test via the race path
		// with the http client override below.
		//
		// Accept the error as expected (scheme mismatch) and test the race path
		// separately below.
		t.Logf("epDoDeliver https-only note: %v", err)
	}

	// Test the full race via a wrapper that allows http:// URLs directly.
	// We override epHTTPClient with a transport that doesn't care about TLS.
	epHTTPClient = live.Client() // test server's own client accepts its own cert
	// Since live.Client() is configured for the TLS test server, use NewTLSServer.
	live.Close()
	var hits2 atomic.Int64
	tlsLive := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits2.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer tlsLive.Close()
	epHTTPClient = tlsLive.Client()

	tlsHost := strings.TrimPrefix(tlsLive.URL, "https://")
	endpoints := []*epEndpoint{
		{ID: "ep-live", VulaID: "alice", Server: tlsHost, Priority: 1, Healthy: true},
	}

	req := EndpointDeliveryRequest{
		MsgID:     "test-msg-uuid-001",
		Endpoints: endpoints,
		Path:      "/",
		Payload:   []byte(`{"id":"test-msg-uuid-001"}`),
	}
	if err := EndpointFailoverDeliver(ctx, req); err != nil {
		t.Fatalf("expected delivery success, got: %v", err)
	}
	if hits2.Load() != 1 {
		t.Errorf("expected exactly 1 hit on live server, got %d", hits2.Load())
	}
}

func TestFailoverDeliverUsesSecondEndpointWhenFirstFails(t *testing.T) {
	resetRegistry()

	var hits atomic.Int64
	tlsLive := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer tlsLive.Close()

	// A TLS server that always returns 503.
	tlsDead := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer tlsDead.Close()

	// Use a client that trusts both test servers' certs.
	// The TLS test servers share a common CA (x509 package), so any one's
	// client should work for both.
	epHTTPClient = tlsLive.Client()

	liveHost := strings.TrimPrefix(tlsLive.URL, "https://")
	deadHost := strings.TrimPrefix(tlsDead.URL, "https://")

	endpoints := []*epEndpoint{
		{ID: "ep-dead", VulaID: "alice", Server: deadHost, Priority: 1, Healthy: true},
		{ID: "ep-live", VulaID: "alice", Server: liveHost, Priority: 2, Healthy: true},
	}

	req := EndpointDeliveryRequest{
		MsgID:     "test-failover-msg-001",
		Endpoints: endpoints,
		Path:      "/",
		Payload:   []byte(`{"id":"test-failover-msg-001"}`),
	}
	ctx := context.Background()
	if err := EndpointFailoverDeliver(ctx, req); err != nil {
		t.Fatalf("expected delivery success via live endpoint, got: %v", err)
	}
	if hits.Load() == 0 {
		t.Error("expected at least one hit on live endpoint")
	}
}

func TestFailoverDeliverAllEndpointsDown(t *testing.T) {
	resetRegistry()

	tlsDead := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer tlsDead.Close()
	epHTTPClient = tlsDead.Client()

	deadHost := strings.TrimPrefix(tlsDead.URL, "https://")
	endpoints := []*epEndpoint{
		{ID: "ep-d1", VulaID: "alice", Server: deadHost, Priority: 1, Healthy: true},
		{ID: "ep-d2", VulaID: "alice", Server: deadHost, Priority: 2, Healthy: true},
	}

	req := EndpointDeliveryRequest{
		MsgID:     "test-all-down",
		Endpoints: endpoints,
		Path:      "/",
		Payload:   []byte(`{}`),
	}
	ctx := context.Background()
	err := EndpointFailoverDeliver(ctx, req)
	if err == nil {
		t.Fatal("expected error when all endpoints are down")
	}
}

func TestFailoverDeliverDuplicateMsgIDIsNoop(t *testing.T) {
	resetRegistry()

	var hits atomic.Int64
	tlsLive := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer tlsLive.Close()
	epHTTPClient = tlsLive.Client()

	liveHost := strings.TrimPrefix(tlsLive.URL, "https://")
	endpoints := []*epEndpoint{
		{ID: "ep-live", VulaID: "alice", Server: liveHost, Priority: 1, Healthy: true},
	}

	req := EndpointDeliveryRequest{
		MsgID:     "dedup-uuid-v7-001",
		Endpoints: endpoints,
		Path:      "/",
		Payload:   []byte(`{"id":"dedup-uuid-v7-001"}`),
	}
	ctx := context.Background()

	// First delivery – should succeed.
	if err := EndpointFailoverDeliver(ctx, req); err != nil {
		t.Fatalf("first delivery failed: %v", err)
	}
	firstHits := hits.Load()

	// Second delivery with same MsgID – should be a no-op.
	if err := EndpointFailoverDeliver(ctx, req); err != nil {
		t.Fatalf("duplicate delivery returned error: %v", err)
	}
	if hits.Load() != firstHits {
		t.Errorf("duplicate delivery should not reach server (first=%d, after=%d)", firstHits, hits.Load())
	}
}

func TestFailoverDeliverNoEndpoints(t *testing.T) {
	resetRegistry()
	req := EndpointDeliveryRequest{
		MsgID:     "no-eps",
		Endpoints: nil,
		Path:      "/",
		Payload:   []byte(`{}`),
	}
	err := EndpointFailoverDeliver(context.Background(), req)
	if err == nil {
		t.Fatal("expected error with no endpoints")
	}
}

// ---------------------------------------------------------------------------
// EPFromRegistry helper test
// ---------------------------------------------------------------------------

func TestEPFromRegistry(t *testing.T) {
	resetRegistry()

	epRegistry.epRegister(ep("r1", "alice", "home:8080", 2))
	epRegistry.epRegister(ep("r2", "alice", "cloud:8080", 1))

	wk := EPFromRegistry("alice")
	if len(wk) != 2 {
		t.Fatalf("expected 2 well-known entries, got %d", len(wk))
	}
	// Must be sorted by priority: cloud (1) before home (2).
	if wk[0].Priority != 1 {
		t.Errorf("expected first entry priority 1, got %d", wk[0].Priority)
	}
}

func TestEPFromRegistryEmpty(t *testing.T) {
	resetRegistry()
	wk := EPFromRegistry("nobody")
	if len(wk) != 0 {
		t.Fatalf("expected 0 entries for unknown vula_id, got %d", len(wk))
	}
}
