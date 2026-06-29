package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testULID   = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	testSecret = "device-shared-secret"
	testUser   = "user-alice"
)

func testClient(base string) *Client {
	return &Client{
		cloudBaseURL: base,
		deviceULID:   testULID,
		deviceSecret: testSecret,
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		cache:        make(map[string]Token),
		notConnected: make(map[string]time.Time),
		now:          func() time.Time { return time.Now().UTC() },
	}
}

// brokerStub returns a CP-broker-like server that validates the device sig and
// serves a token. reqCount is incremented on every token request.
func brokerStub(t *testing.T, reqCount *int32, expiry time.Time) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(reqCount, 1)
		if r.URL.Path != "/api/integrations/google/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Verify the device signature exactly as the CP does.
		mac := hmac.New(sha256.New, []byte(testSecret))
		mac.Write([]byte("integrations:token:google:" + testULID))
		want := hex.EncodeToString(mac.Sum(nil))
		if r.Header.Get("X-Device-ULID") != testULID || r.Header.Get("X-Integration-Sig") != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-xyz",
			"expires_at":   expiry.UTC().Format(time.RFC3339),
			"scopes":       "openid email",
			"provider":     "google",
		})
	}))
}

func TestMintTokenHappyPath(t *testing.T) {
	var n int32
	srv := brokerStub(t, &n, time.Now().Add(time.Hour))
	defer srv.Close()

	c := testClient(srv.URL)
	// H3: per-user token — pass userID
	tok, err := c.MintToken(context.Background(), ProviderGoogle, testUser)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if tok.AccessToken != "access-xyz" || tok.Scopes != "openid email" {
		t.Fatalf("unexpected token: %+v", tok)
	}
}

func TestMintTokenCaches(t *testing.T) {
	var n int32
	srv := brokerStub(t, &n, time.Now().Add(time.Hour))
	defer srv.Close()

	c := testClient(srv.URL)
	for i := 0; i < 3; i++ {
		if _, err := c.MintToken(context.Background(), ProviderGoogle, testUser); err != nil {
			t.Fatalf("MintToken #%d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("expected 1 broker call (cached), got %d", got)
	}
}

func TestMintTokenRefreshesWhenExpired(t *testing.T) {
	var n int32
	// Token expires "soon" relative to the injected clock so the cache is stale.
	srv := brokerStub(t, &n, time.Unix(1_700_000_500, 0))
	defer srv.Close()

	c := testClient(srv.URL)
	base := time.Unix(1_700_000_000, 0).UTC()
	c.now = func() time.Time { return base }
	if _, err := c.MintToken(context.Background(), ProviderGoogle, testUser); err != nil {
		t.Fatal(err)
	}
	// Advance well past expiry → must hit the broker again.
	c.now = func() time.Time { return time.Unix(1_700_001_000, 0).UTC() }
	if _, err := c.MintToken(context.Background(), ProviderGoogle, testUser); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("expected 2 broker calls (cache expired), got %d", got)
	}
}

func TestMintTokenNotConfigured(t *testing.T) {
	c := &Client{cloudBaseURL: "http://unused", cache: map[string]Token{}, now: time.Now, httpClient: http.DefaultClient}
	if _, err := c.MintToken(context.Background(), ProviderGoogle, testUser); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}

func TestMintTokenNotConnected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	if _, err := c.MintToken(context.Background(), ProviderGoogle, testUser); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("want ErrNotConnected, got %v", err)
	}
}

func TestMintTokenNotConnectedIsNegativeCached(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	base := time.Unix(1_700_000_000, 0).UTC()
	c.now = func() time.Time { return base }

	// Three rapid probes within the TTL → only one broker round-trip.
	for i := 0; i < 3; i++ {
		if _, err := c.MintToken(context.Background(), ProviderGoogle, testUser); !errors.Is(err, ErrNotConnected) {
			t.Fatalf("probe %d: want ErrNotConnected, got %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("expected 1 broker call (negative-cached), got %d", got)
	}

	// After the TTL expires, it probes again.
	c.now = func() time.Time { return base.Add(notConnectedTTL + time.Second) }
	if _, err := c.MintToken(context.Background(), ProviderGoogle, testUser); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("post-TTL: want ErrNotConnected, got %v", err)
	}
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("expected 2 broker calls after TTL, got %d", got)
	}
}

func TestMintTokenFailClosedOffline(t *testing.T) {
	// Broker returns 502; the client must error and NOT cache a stale token.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	if _, err := c.MintToken(context.Background(), ProviderGoogle, testUser); !errors.Is(err, ErrCloudUnavail) {
		t.Fatalf("want ErrCloudUnavail, got %v", err)
	}
	// Cache key is now per-user (H3 fix): check the actual key, not bare ProviderGoogle.
	ckey := mintCacheKey(ProviderGoogle, testUser)
	c.mu.Lock()
	_, cached := c.cache[ckey]
	c.mu.Unlock()
	if cached {
		t.Fatal("must not cache a token on failure")
	}
}

func TestMintTokenIsolatedPerUser(t *testing.T) {
	// H3 regression: two different users must get separate cache entries.
	var n int32
	srv := brokerStub(t, &n, time.Now().Add(time.Hour))
	defer srv.Close()

	c := testClient(srv.URL)
	if _, err := c.MintToken(context.Background(), ProviderGoogle, "alice"); err != nil {
		t.Fatalf("alice MintToken: %v", err)
	}
	if _, err := c.MintToken(context.Background(), ProviderGoogle, "bob"); err != nil {
		t.Fatalf("bob MintToken: %v", err)
	}
	// Both users are distinct → 2 broker round-trips.
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("expected 2 broker calls (separate users), got %d", got)
	}
	// Each user's second call hits the cache.
	if _, err := c.MintToken(context.Background(), ProviderGoogle, "alice"); err != nil {
		t.Fatalf("alice MintToken #2: %v", err)
	}
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("expected no additional broker calls (cached), got %d", got)
	}
}

func TestStatus(t *testing.T) {
	var n int32
	srv := brokerStub(t, &n, time.Now().Add(time.Hour))
	defer srv.Close()

	// Connected.
	c := testClient(srv.URL)
	configured, connected, err := c.Status(context.Background(), ProviderGoogle)
	if err != nil || !configured || !connected {
		t.Fatalf("connected: configured=%v connected=%v err=%v", configured, connected, err)
	}

	// Not configured.
	c2 := &Client{cloudBaseURL: srv.URL, cache: map[string]Token{}, now: time.Now, httpClient: http.DefaultClient}
	configured, connected, err = c2.Status(context.Background(), ProviderGoogle)
	if configured || connected || err != nil {
		t.Fatalf("unconfigured: configured=%v connected=%v err=%v", configured, connected, err)
	}
}

func TestSignDeterministic(t *testing.T) {
	c := testClient("http://x")
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte("integrations:token:google:" + testULID))
	if c.sign(ProviderGoogle) != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("sign mismatch")
	}
}
