package peering

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// wkTempDir creates a temp directory for peering data and returns a cleanup
// function.
func wkTempDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	for _, sub := range []string{"identity", "profile"} {
		if err := os.MkdirAll(filepath.Join(d, sub), 0700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	return d
}

// --------------------------------------------------------------------------
// wkFormatVulaID
// --------------------------------------------------------------------------

func TestWKFormatVulaID(t *testing.T) {
	dir := wkTempDir(t)
	id, err := wkLoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatalf("wkLoadOrCreateIdentity: %v", err)
	}
	if !strings.HasPrefix(id.VulaID, wkVulaIDPrefix) {
		t.Errorf("VulaID %q does not start with %q", id.VulaID, wkVulaIDPrefix)
	}
}

// --------------------------------------------------------------------------
// Identity: load / create / persist
// --------------------------------------------------------------------------

func TestWKLoadOrCreateIdentity_CreatesFreshKeypair(t *testing.T) {
	dir := wkTempDir(t)
	id, err := wkLoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if id.VulaID == "" || id.PublicKey == "" || id.PrivateKey == "" {
		t.Fatalf("identity fields empty: %+v", id)
	}
	if !strings.HasPrefix(id.VulaID, wkVulaIDPrefix) {
		t.Errorf("unexpected VulaID prefix: %s", id.VulaID)
	}
}

func TestWKLoadOrCreateIdentity_Idempotent(t *testing.T) {
	dir := wkTempDir(t)
	id1, err := wkLoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	id2, err := wkLoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if id1.VulaID != id2.VulaID {
		t.Errorf("identity not idempotent: %s != %s", id1.VulaID, id2.VulaID)
	}
}

func TestWKLoadOrCreateIdentity_WrittenToDisk(t *testing.T) {
	dir := wkTempDir(t)
	_, err := wkLoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := filepath.Join(dir, wkIdentityFile)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("identity file not written at %s", path)
	}
}

// --------------------------------------------------------------------------
// Own profile
// --------------------------------------------------------------------------

func TestWKLoadOwnProfile_DefaultsWhenMissing(t *testing.T) {
	dir := wkTempDir(t)
	p := wkLoadOwnProfile(dir, "vula:ed25519:test")
	if p.VulaID != "vula:ed25519:test" {
		t.Errorf("VulaID: got %q", p.VulaID)
	}
	if p.Visibility.Image != WKVisibilityPublic {
		t.Errorf("default image visibility should be public, got %q", p.Visibility.Image)
	}
	if p.Visibility.Bio != WKVisibilityPeers {
		t.Errorf("default bio visibility should be peers, got %q", p.Visibility.Bio)
	}
	if p.Visibility.Email != WKVisibilityNobody {
		t.Errorf("default email visibility should be nobody, got %q", p.Visibility.Email)
	}
}

func TestWKLoadOwnProfile_LoadsFromDisk(t *testing.T) {
	dir := wkTempDir(t)
	stored := WKOwnProfile{
		VulaID:        "vula:ed25519:abc",
		DisplayName:   "Alice",
		Bio:           "Building things",
		VerifiedEmail: true,
		Slug:          "alice.vulos.org",
		Visibility: WKProfileVisibility{
			Image: WKVisibilityPublic,
			Bio:   WKVisibilityPublic,
			Email: WKVisibilityNobody,
		},
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.MarshalIndent(stored, "", "  ")
	os.WriteFile(filepath.Join(dir, wkProfileFile), raw, 0600)

	p := wkLoadOwnProfile(dir, "vula:ed25519:abc")
	if p.DisplayName != "Alice" {
		t.Errorf("DisplayName: got %q", p.DisplayName)
	}
	if p.Bio != "Building things" {
		t.Errorf("Bio: got %q", p.Bio)
	}
}

// --------------------------------------------------------------------------
// Public response filtering
// --------------------------------------------------------------------------

func TestWKBuildPublicResponse_PublicBio(t *testing.T) {
	p := WKOwnProfile{
		VulaID:        "vula:ed25519:xyz",
		DisplayName:   "Bob",
		Bio:           "My public bio",
		VerifiedEmail: true,
		Visibility: WKProfileVisibility{
			Image: WKVisibilityPublic,
			Bio:   WKVisibilityPublic,
			Email: WKVisibilityNobody,
		},
	}
	resp := wkBuildPublicResponse(p)
	if resp.Bio != "My public bio" {
		t.Errorf("public bio should appear, got %q", resp.Bio)
	}
	if resp.Image == "" {
		t.Errorf("public image URL should appear")
	}
}

func TestWKBuildPublicResponse_PrivateBio(t *testing.T) {
	p := WKOwnProfile{
		VulaID:      "vula:ed25519:xyz",
		DisplayName: "Carol",
		Bio:         "Secret bio",
		Visibility: WKProfileVisibility{
			Image: WKVisibilityNobody,
			Bio:   WKVisibilityPeers,
			Email: WKVisibilityNobody,
		},
	}
	resp := wkBuildPublicResponse(p)
	if resp.Bio != "" {
		t.Errorf("peers-only bio must not appear in public response, got %q", resp.Bio)
	}
	if resp.Image != "" {
		t.Errorf("nobody image must not appear in public response, got %q", resp.Image)
	}
}

func TestWKBuildPublicResponse_NeverExposesEmail(t *testing.T) {
	p := WKOwnProfile{
		VulaID:      "vula:ed25519:xyz",
		DisplayName: "Dave",
		Email:       "dave@example.com",
		Visibility: WKProfileVisibility{
			Image: WKVisibilityPublic,
			Bio:   WKVisibilityPublic,
			Email: WKVisibilityNobody,
		},
	}
	resp := wkBuildPublicResponse(p)
	raw, _ := json.Marshal(resp)
	if strings.Contains(string(raw), "dave@example.com") {
		t.Errorf("email must never appear in public response")
	}
}

// --------------------------------------------------------------------------
// /.well-known/vula-id handler
// --------------------------------------------------------------------------

func TestWKWellKnownHandler_NoAuth(t *testing.T) {
	dir := wkTempDir(t)
	os.Setenv("VULOS_PEERING_DIR", dir)
	defer os.Unsetenv("VULOS_PEERING_DIR")

	mux := http.NewServeMux()
	RegisterWellKnownHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/vula-id", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp WKIdentityResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.HasPrefix(resp.VulaID, wkVulaIDPrefix) {
		t.Errorf("VulaID %q does not start with prefix", resp.VulaID)
	}
}

func TestWKWellKnownHandler_CacheControl(t *testing.T) {
	dir := wkTempDir(t)
	os.Setenv("VULOS_PEERING_DIR", dir)
	defer os.Unsetenv("VULOS_PEERING_DIR")

	mux := http.NewServeMux()
	RegisterWellKnownHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/vula-id", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "public") {
		t.Errorf("Cache-Control should contain 'public', got %q", cc)
	}
}

func TestWKWellKnownHandler_OnlyPublicFields(t *testing.T) {
	dir := wkTempDir(t)
	os.Setenv("VULOS_PEERING_DIR", dir)
	defer os.Unsetenv("VULOS_PEERING_DIR")

	// Write a profile where bio is peers-only and email is nobody.
	stored := WKOwnProfile{
		VulaID:      "vula:ed25519:testhidden",
		DisplayName: "HiddenUser",
		Bio:         "secret bio",
		Email:       "hidden@example.com",
		Visibility: WKProfileVisibility{
			Image: WKVisibilityNobody,
			Bio:   WKVisibilityPeers,
			Email: WKVisibilityNobody,
		},
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.MarshalIndent(stored, "", "  ")
	os.WriteFile(filepath.Join(dir, wkProfileFile), raw, 0600)

	// Also write identity so VulaID matches.
	idStored := wkStoredIdentity{
		VulaID:     "vula:ed25519:testhidden",
		PublicKey:  "dGVzdA",
		PrivateKey: "dGVzdA",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	idRaw, _ := json.MarshalIndent(idStored, "", "  ")
	os.WriteFile(filepath.Join(dir, wkIdentityFile), idRaw, 0600)

	mux := http.NewServeMux()
	RegisterWellKnownHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/vula-id", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "secret bio") {
		t.Errorf("peers-only bio must not appear in public response")
	}
	if strings.Contains(body, "hidden@example.com") {
		t.Errorf("email must not appear in public response")
	}
}

// --------------------------------------------------------------------------
// /api/peering/profile/{vula_id} handler
// --------------------------------------------------------------------------

func TestWKPeerProfileHandler_CacheHit(t *testing.T) {
	dir := wkTempDir(t)
	os.Setenv("VULOS_PEERING_DIR", dir)
	defer os.Unsetenv("VULOS_PEERING_DIR")

	// Pre-populate cache.
	const testID = "vula:ed25519:cached"
	wkCacheMu.Lock()
	wkCache[testID] = &WKPeerProfile{
		VulaID:      testID,
		DisplayName: "CachedUser",
		CachedAt:    time.Now(),
	}
	wkCacheMu.Unlock()
	t.Cleanup(func() {
		wkCacheMu.Lock()
		delete(wkCache, testID)
		wkCacheMu.Unlock()
	})

	mux := http.NewServeMux()
	RegisterWellKnownHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/profile/"+testID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("X-Cache") != "HIT" {
		t.Errorf("expected X-Cache: HIT, got %q", rec.Header().Get("X-Cache"))
	}
	var p WKPeerProfile
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.DisplayName != "CachedUser" {
		t.Errorf("DisplayName: got %q", p.DisplayName)
	}
}

func TestWKPeerProfileHandler_MissingServer(t *testing.T) {
	dir := wkTempDir(t)
	os.Setenv("VULOS_PEERING_DIR", dir)
	defer os.Unsetenv("VULOS_PEERING_DIR")

	mux := http.NewServeMux()
	RegisterWellKnownHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/profile/vula:ed25519:xyz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestWKPeerProfileHandler_LiveFetch(t *testing.T) {
	// Spin up a fake peer server.
	fakeID := "vula:ed25519:fakepeer"
	fakePeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/vula-id" {
			http.NotFound(w, r)
			return
		}
		resp := WKIdentityResponse{
			VulaID:        fakeID,
			DisplayName:   "FakePeer",
			VerifiedEmail: true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer fakePeer.Close()

	// Evict any stale cache entry for fakeID.
	wkCacheMu.Lock()
	delete(wkCache, fakeID)
	wkCacheMu.Unlock()

	dir := wkTempDir(t)
	os.Setenv("VULOS_PEERING_DIR", dir)
	defer os.Unsetenv("VULOS_PEERING_DIR")

	mux := http.NewServeMux()
	RegisterWellKnownHandlers(mux)

	url := "/api/peering/profile/" + fakeID + "?server=" + fakePeer.URL
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("X-Cache") != "MISS" {
		t.Errorf("expected X-Cache: MISS, got %q", rec.Header().Get("X-Cache"))
	}
	var p WKPeerProfile
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.DisplayName != "FakePeer" {
		t.Errorf("DisplayName: got %q", p.DisplayName)
	}
	if !p.VerifiedEmail {
		t.Errorf("verified_email should be true")
	}

	// Second request should be a cache hit.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, url, nil)
	mux.ServeHTTP(rec2, req2)
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Errorf("second request should be a cache hit, got %q", rec2.Header().Get("X-Cache"))
	}

	// Cleanup.
	wkCacheMu.Lock()
	delete(wkCache, fakeID)
	wkCacheMu.Unlock()
}

// --------------------------------------------------------------------------
// FetchPeerProfile helper
// --------------------------------------------------------------------------

func TestFetchPeerProfile_ReturnsPublicFields(t *testing.T) {
	const fakeID = "vula:ed25519:fetch_test"
	fakePeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := WKIdentityResponse{
			VulaID:        fakeID,
			DisplayName:   "FetchUser",
			VerifiedEmail: false,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer fakePeer.Close()

	// Clear cache.
	wkCacheMu.Lock()
	delete(wkCache, fakeID)
	wkCacheMu.Unlock()

	p, err := FetchPeerProfile(context.Background(), fakeID, fakePeer.URL)
	if err != nil {
		t.Fatalf("FetchPeerProfile: %v", err)
	}
	if p.VulaID != fakeID {
		t.Errorf("VulaID: got %q", p.VulaID)
	}
	if p.DisplayName != "FetchUser" {
		t.Errorf("DisplayName: got %q", p.DisplayName)
	}

	// Cleanup.
	wkCacheMu.Lock()
	delete(wkCache, fakeID)
	wkCacheMu.Unlock()
}

func TestFetchPeerProfile_IDMismatchRejected(t *testing.T) {
	fakePeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := WKIdentityResponse{
			VulaID:      "vula:ed25519:DIFFERENT",
			DisplayName: "Impersonator",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer fakePeer.Close()

	const expected = "vula:ed25519:EXPECTED"
	wkCacheMu.Lock()
	delete(wkCache, expected)
	wkCacheMu.Unlock()

	_, err := FetchPeerProfile(context.Background(), expected, fakePeer.URL)
	if err == nil {
		t.Error("expected error on Vula ID mismatch")
	}
}

func TestFetchPeerProfile_CachesResult(t *testing.T) {
	const fakeID = "vula:ed25519:cache_result"
	calls := 0
	var mu sync.Mutex

	fakePeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		resp := WKIdentityResponse{VulaID: fakeID, DisplayName: "CacheMe"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer fakePeer.Close()

	// Clear any existing cache entry.
	wkCacheMu.Lock()
	delete(wkCache, fakeID)
	wkCacheMu.Unlock()

	// First fetch → network call.
	if _, err := FetchPeerProfile(context.Background(), fakeID, fakePeer.URL); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	// Second fetch → should come from cache.
	if _, err := FetchPeerProfile(context.Background(), fakeID, fakePeer.URL); err != nil {
		t.Fatalf("second fetch: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("expected 1 network call (cache), got %d", calls)
	}

	// Cleanup.
	wkCacheMu.Lock()
	delete(wkCache, fakeID)
	wkCacheMu.Unlock()
}

// --------------------------------------------------------------------------
// notify-change endpoint
// --------------------------------------------------------------------------

func TestWKNotifyChange_EvictsCache(t *testing.T) {
	const evictID = "vula:ed25519:evict_me"
	wkCacheMu.Lock()
	wkCache[evictID] = &WKPeerProfile{VulaID: evictID, CachedAt: time.Now()}
	wkCacheMu.Unlock()

	dir := wkTempDir(t)
	os.Setenv("VULOS_PEERING_DIR", dir)
	defer os.Unsetenv("VULOS_PEERING_DIR")

	mux := http.NewServeMux()
	RegisterWellKnownHandlers(mux)

	body := `{"vula_id":"` + evictID + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/peering/profile/notify-change", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}

	wkCacheMu.RLock()
	_, stillCached := wkCache[evictID]
	wkCacheMu.RUnlock()
	if stillCached {
		t.Error("expected cache entry to be evicted")
	}
}

// --------------------------------------------------------------------------
// wkNormaliseServerAddr
// --------------------------------------------------------------------------

func TestWKNormaliseServerAddr(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://alice.vulos.org", "https://alice.vulos.org"},
		{"https://alice.vulos.org/", "https://alice.vulos.org"},
		{"http://localhost:8080", "http://localhost:8080"},
		{"localhost:8080", "http://localhost:8080"},
		{"127.0.0.1:9000", "http://127.0.0.1:9000"},
		{"alice.vulos.org:8080", "https://alice.vulos.org:8080"},
		{"alice.vulos.org", "https://alice.vulos.org"},
	}
	for _, c := range cases {
		got := wkNormaliseServerAddr(c.in)
		if got != c.want {
			t.Errorf("wkNormaliseServerAddr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --------------------------------------------------------------------------
// Cache TTL / stale eviction
// --------------------------------------------------------------------------

func TestWKCacheGet_StaleReturnsNil(t *testing.T) {
	const staleID = "vula:ed25519:stale"
	wkCacheMu.Lock()
	wkCache[staleID] = &WKPeerProfile{
		VulaID:   staleID,
		CachedAt: time.Now().Add(-10 * time.Minute), // older than wkCacheTTL
	}
	wkCacheMu.Unlock()
	t.Cleanup(func() {
		wkCacheMu.Lock()
		delete(wkCache, staleID)
		wkCacheMu.Unlock()
	})

	if p, ok := wkCacheGet(staleID); ok || p != nil {
		t.Errorf("stale entry should not be returned, got %v", p)
	}
}

// --------------------------------------------------------------------------
// Visibility constants
// --------------------------------------------------------------------------

func TestWKVisibilityConstants(t *testing.T) {
	if WKVisibilityPublic != "public" {
		t.Errorf("WKVisibilityPublic = %q", WKVisibilityPublic)
	}
	if WKVisibilityPeers != "peers" {
		t.Errorf("WKVisibilityPeers = %q", WKVisibilityPeers)
	}
	if WKVisibilityNobody != "nobody" {
		t.Errorf("WKVisibilityNobody = %q", WKVisibilityNobody)
	}
}

// --------------------------------------------------------------------------
// PEER-40: endpoint advertisement in /.well-known/vula-id
// --------------------------------------------------------------------------

// TestWKWellKnownHandler_IncludesEndpoints verifies that registered endpoints
// for the node's own Vula ID appear in the /.well-known/vula-id response.
func TestWKWellKnownHandler_IncludesEndpoints(t *testing.T) {
	dir := wkTempDir(t)
	os.Setenv("VULOS_PEERING_DIR", dir)
	defer os.Unsetenv("VULOS_PEERING_DIR")

	// Write a known identity so we control the Vula ID.
	idStored := wkStoredIdentity{
		VulaID:     "vula:ed25519:ep-advert-test",
		PublicKey:  "dGVzdA",
		PrivateKey: "dGVzdA",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	idRaw, _ := json.MarshalIndent(idStored, "", "  ")
	os.WriteFile(filepath.Join(dir, wkIdentityFile), idRaw, 0600)

	// Register two endpoints for this Vula ID in the package-level registry.
	epRegistry.epRegister(&epEndpoint{
		ID:       "wk-test-ep1",
		VulaID:   "vula:ed25519:ep-advert-test",
		Server:   "home.alice:8080",
		Priority: 1,
		Healthy:  true,
	})
	epRegistry.epRegister(&epEndpoint{
		ID:       "wk-test-ep2",
		VulaID:   "vula:ed25519:ep-advert-test",
		Server:   "cloud.alice:8080",
		Priority: 2,
		Healthy:  true,
	})
	t.Cleanup(func() {
		epRegistry.epRemove("wk-test-ep1")
		epRegistry.epRemove("wk-test-ep2")
	})

	mux := http.NewServeMux()
	RegisterWellKnownHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/vula-id", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}

	var resp WKIdentityResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Endpoints) != 2 {
		t.Fatalf("expected 2 endpoints in well-known response, got %d: %v", len(resp.Endpoints), resp.Endpoints)
	}
	// Must be sorted by priority: priority 1 first.
	if resp.Endpoints[0].Priority != 1 {
		t.Errorf("first endpoint priority should be 1, got %d", resp.Endpoints[0].Priority)
	}
	if resp.Endpoints[0].Server != "home.alice:8080" {
		t.Errorf("first endpoint server: got %q", resp.Endpoints[0].Server)
	}
}

// TestWKWellKnownHandler_NoEndpointsOmitted verifies that the "endpoints" key
// is absent (omitempty) when no endpoints are registered for this node.
func TestWKWellKnownHandler_NoEndpointsOmitted(t *testing.T) {
	dir := wkTempDir(t)
	os.Setenv("VULOS_PEERING_DIR", dir)
	defer os.Unsetenv("VULOS_PEERING_DIR")

	mux := http.NewServeMux()
	RegisterWellKnownHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/vula-id", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `"endpoints"`) {
		t.Errorf("endpoints key should be absent when registry is empty, body: %s", body)
	}
}

// TestFetchPeerProfile_CachesEndpoints verifies that endpoints advertised in a
// peer's well-known response are stored in the cached WKPeerProfile.
func TestFetchPeerProfile_CachesEndpoints(t *testing.T) {
	const fakeID = "vula:ed25519:ep-cache-test"
	fakePeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := WKIdentityResponse{
			VulaID:      fakeID,
			DisplayName: "EPCacheUser",
			Endpoints: []epWellKnownEndpoint{
				{Server: "home.peer:8080", Priority: 1},
				{Server: "cloud.peer:8080", Priority: 2},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer fakePeer.Close()

	wkCacheMu.Lock()
	delete(wkCache, fakeID)
	wkCacheMu.Unlock()
	t.Cleanup(func() {
		wkCacheMu.Lock()
		delete(wkCache, fakeID)
		wkCacheMu.Unlock()
	})

	p, err := FetchPeerProfile(context.Background(), fakeID, fakePeer.URL)
	if err != nil {
		t.Fatalf("FetchPeerProfile: %v", err)
	}
	if len(p.Endpoints) != 2 {
		t.Fatalf("expected 2 cached endpoints, got %d", len(p.Endpoints))
	}
	if p.Endpoints[0].Server != "home.peer:8080" {
		t.Errorf("first cached endpoint server: got %q", p.Endpoints[0].Server)
	}
	if p.Endpoints[0].Priority != 1 {
		t.Errorf("first cached endpoint priority: got %d", p.Endpoints[0].Priority)
	}
}
