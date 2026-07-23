package cdn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fetcherWithFakeEndpoints returns a RangeFetcher whose fetch* methods are
// exercised indirectly by pointing the real provider URLs is not possible in
// a unit test (no network) — instead we test the parse/validate logic that
// fetchCloudflare/fetchFastly/fetchBunny share via f.get against a local
// httptest server, proving the response-shape parsing for each vendor.
func TestFetcher_ParsesCloudflareShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result":{"ipv4_cidrs":["1.2.3.0/24","not-a-cidr"],"ipv6_cidrs":["2001:db8::/32"]}}`))
	}))
	defer srv.Close()

	f := NewRangeFetcher(nil)
	body, err := f.get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var resp cfIPsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	all := append(resp.Result.IPv4CIDRs, resp.Result.IPv6CIDRs...)
	valid := filterValidCIDRs(all)
	if len(valid) != 2 {
		t.Fatalf("valid CIDRs = %v, want 2 entries", valid)
	}
}

func TestFetcher_ParsesFastlyShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"addresses":["10.0.0.0/24"],"ipv6_addresses":["2001:db8::/32"]}`))
	}))
	defer srv.Close()

	f := NewRangeFetcher(nil)
	body, err := f.get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var resp fastlyIPsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Addresses) != 1 || len(resp.IPv6Addresses) != 1 {
		t.Fatalf("unexpected fastly parse: %+v", resp)
	}
}

func TestFetcher_ParsesBunnyShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`["5.6.7.0/24","8.9.10.0/24"]`))
	}))
	defer srv.Close()

	f := NewRangeFetcher(nil)
	body, err := f.get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var cidrs []string
	if err := json.Unmarshal(body, &cidrs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cidrs) != 2 {
		t.Fatalf("bunny cidrs = %v, want 2", cidrs)
	}
}

func TestFetcher_GetRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := NewRangeFetcher(nil)
	if _, err := f.get(context.Background(), srv.URL); err == nil {
		t.Fatal("get did not error on HTTP 500")
	}
}

// TestFetcher_RefreshAllContinuesAfterOneProviderFails is an integration-style
// test using fetch() indirectly via RefreshAll against a Store, with a
// zero-timeout client so all three real provider calls fail fast (offline
// test environment) — asserting RefreshAll never panics and completes
// without storing anything when every fetch fails, i.e. it fails open rather
// than wiping out a previously-good cache with an empty replace.
func TestFetcher_RefreshAllDoesNotPanicOnAllFailures(t *testing.T) {
	st := openTestStore(t)
	// Seed a previously-good cache.
	if err := st.SetIPRanges(context.Background(), ProviderCloudflare, []string{"1.2.3.0/24"}); err != nil {
		t.Fatalf("seed SetIPRanges: %v", err)
	}

	f := NewRangeFetcher(st)
	f.HTTPClient = &http.Client{Timeout: 1 * time.Millisecond} // force every real call to fail fast

	f.RefreshAll(context.Background())

	// A failed fetch must NOT clear the previously-cached ranges — RefreshAll
	// only calls SetIPRanges on a successful fetch (see RefreshAll's
	// `if err != nil { continue }` before the SetIPRanges call).
	got, err := st.GetIPRanges(context.Background(), ProviderCloudflare)
	if err != nil {
		t.Fatalf("GetIPRanges: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("previously-cached ranges were lost after a failed refresh: got %v", got)
	}
}
