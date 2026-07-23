package fly

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// redirectTransport rewrites every outgoing request's host/scheme to a fixed
// test server, so we can intercept calls without changing the const endpoint.
type redirectTransport struct {
	target string
}

func (rt *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	parsed, err := url.Parse(rt.target)
	if err != nil {
		return nil, err
	}
	req2 := req.Clone(req.Context())
	req2.URL.Scheme = parsed.Scheme
	req2.URL.Host = parsed.Host
	return http.DefaultTransport.RoundTrip(req2)
}

// Test 1: poll happy path — mock HTTP returns valid billing response.
func TestPoll_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"data": map[string]any{
				"organization": map[string]any{
					"id":   "org-123",
					"slug": "my-org",
					"currentBillingPeriod": map[string]any{
						"totalBilled": map[string]any{
							"amount":   42.50,
							"currency": "USD",
						},
					},
					"apps": map[string]any{
						"nodes": []map[string]any{
							{
								"name": "my-app",
								"currentSpend": map[string]any{
									"amount":   10.25,
									"currency": "USD",
								},
								"regionSpend": []map[string]any{
									{"region": "iad", "amount": 7.00},
									{"region": "lhr", "amount": 3.25},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	st := openTestStore(t)
	poller := &Poller{
		Store:   st,
		OrgSlug: "my-org",
		HTTPClient: &http.Client{
			Transport: &redirectTransport{target: srv.URL},
		},
	}
	t.Setenv("FLY_API_TOKEN", "test-token")

	ctx := context.Background()
	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	snaps, err := st.Snapshots(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	if len(snaps) == 0 {
		t.Fatal("expected snapshots, got none")
	}

	var found bool
	for _, sn := range snaps {
		if sn.AppName == "_org_total" && sn.AmountUSD == 42.50 {
			found = true
		}
	}
	if !found {
		t.Errorf("org total snapshot not found; got %+v", snaps)
	}
}

// Test 2: defensive parsing — unknown fields in response must not crash.
func TestPoll_DefensiveParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"data": {
				"organization": {
					"id": "org-xyz",
					"slug": "xyz",
					"unknownField": {"nested": true},
					"anotherNewField": 99,
					"currentBillingPeriod": {
						"totalBilled": {"amount": 5.0, "currency": "USD"},
						"extraData": "ignored"
					},
					"apps": {"nodes": []}
				}
			},
			"newTopLevelField": "should-be-ignored"
		}`))
	}))
	defer srv.Close()

	st := openTestStore(t)
	poller := &Poller{
		Store:   st,
		OrgSlug: "xyz",
		HTTPClient: &http.Client{
			Transport: &redirectTransport{target: srv.URL},
		},
	}
	t.Setenv("FLY_API_TOKEN", "test-token")

	ctx := context.Background()
	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("should not crash on unknown fields: %v", err)
	}
}

// Test 3: no-token → no-op (no HTTP call made).
func TestPoll_NoToken_NoOp(t *testing.T) {
	t.Setenv("FLY_API_TOKEN", "") //nolint:errcheck
	os.Unsetenv("FLY_API_TOKEN")  //nolint:errcheck

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	st := openTestStore(t)
	poller := &Poller{
		Store:   st,
		OrgSlug: "my-org",
		HTTPClient: &http.Client{
			Transport: &redirectTransport{target: srv.URL},
		},
	}

	ctx := context.Background()
	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("no-op poll should not error: %v", err)
	}
	if called {
		t.Error("HTTP server should not have been called when token is absent")
	}
}

// Test 4: retention sweep keeps only last 90 days.
func TestSweep_Retention90Days(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -91)
	recent := now.AddDate(0, 0, -10)

	if err := st.Insert(ctx, []Snapshot{
		{TakenAt: old, OrgID: "org1", AppName: "app1", Region: "iad", AmountUSD: 1.0, Currency: "USD"},
		{TakenAt: recent, OrgID: "org1", AppName: "app1", Region: "iad", AmountUSD: 2.0, Currency: "USD"},
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := st.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	snaps, err := st.Snapshots(ctx, old.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Errorf("expected 1 snapshot after sweep, got %d", len(snaps))
	}
	if len(snaps) == 1 && snaps[0].AmountUSD != 2.0 {
		t.Errorf("expected recent snapshot to survive, got %+v", snaps[0])
	}
}
