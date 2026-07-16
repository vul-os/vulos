package tigris

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

// redirectTransport redirects all requests to a fixed test server.
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

// Test 1: poll happy path — mock HTTP returns valid metrics response.
func TestTigrisPoll_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"buckets": []map[string]any{
				{
					"bucket":           "tenant-a-data",
					"storage_gb_hours": 120.5,
					"egress_gb":        3.2,
					"request_count":    15000,
					"amount_usd":       0.85,
				},
				{
					"bucket":           "tenant-b-data",
					"storage_gb_hours": 50.0,
					"egress_gb":        1.1,
					"request_count":    4200,
					"amount_usd":       0.30,
				},
			},
			"summary": map[string]any{
				"storage_gb_hours": 170.5,
				"egress_gb":        4.3,
				"request_count":    19200,
				"amount_usd":       1.15,
			},
		})
	}))
	defer srv.Close()

	st := openTestStore(t)
	poller := &Poller{
		Store:    st,
		Endpoint: srv.URL,
		HTTPClient: &http.Client{
			Transport: &redirectTransport{target: srv.URL},
		},
	}
	t.Setenv("TIGRIS_ACCESS_KEY_ID", "test-ak")
	t.Setenv("TIGRIS_SECRET_ACCESS_KEY", "test-sk")

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

	var foundA bool
	for _, sn := range snaps {
		if sn.Bucket == "tenant-a-data" && sn.StorageGBHrs == 120.5 {
			foundA = true
		}
	}
	if !foundA {
		t.Errorf("tenant-a snapshot not found; got %+v", snaps)
	}
}

// Test 2: defensive parsing — unknown fields must not crash.
func TestTigrisPoll_DefensiveParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"buckets": [
				{
					"bucket": "bkt1",
					"storage_gb_hours": 10.0,
					"egress_gb": 0.5,
					"request_count": 100,
					"amount_usd": 0.05,
					"unknown_future_field": "should be ignored",
					"nested_unknown": {"a": 1, "b": [1,2,3]}
				}
			],
			"new_top_level_field": true,
			"version": 42
		}`))
	}))
	defer srv.Close()

	st := openTestStore(t)
	poller := &Poller{
		Store:    st,
		Endpoint: srv.URL,
		HTTPClient: &http.Client{
			Transport: &redirectTransport{target: srv.URL},
		},
	}
	t.Setenv("TIGRIS_ACCESS_KEY_ID", "test-ak")
	t.Setenv("TIGRIS_SECRET_ACCESS_KEY", "test-sk")

	ctx := context.Background()
	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("should not crash on unknown fields: %v", err)
	}
}

// Test 3: no credentials → no-op (no HTTP call made).
func TestTigrisPoll_NoCreds_NoOp(t *testing.T) {
	os.Unsetenv("TIGRIS_ACCESS_KEY_ID")     //nolint:errcheck
	os.Unsetenv("TIGRIS_SECRET_ACCESS_KEY") //nolint:errcheck

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	st := openTestStore(t)
	poller := &Poller{
		Store:    st,
		Endpoint: srv.URL,
		HTTPClient: &http.Client{
			Transport: &redirectTransport{target: srv.URL},
		},
	}

	ctx := context.Background()
	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("no-op poll should not error: %v", err)
	}
	if called {
		t.Error("HTTP server should not have been called when credentials are absent")
	}
}

// Test 4: retention sweep keeps only last 90 days.
func TestTigrisSweep_Retention90Days(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -91)
	recent := now.AddDate(0, 0, -5)

	if err := st.Insert(ctx, []Snapshot{
		{TakenAt: old, Bucket: "bkt-a", StorageGBHrs: 100.0, EgressGB: 1.0, RequestCount: 500, AmountUSD: 0.50},
		{TakenAt: recent, Bucket: "bkt-a", StorageGBHrs: 200.0, EgressGB: 2.0, RequestCount: 1000, AmountUSD: 1.00},
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
	if len(snaps) == 1 && snaps[0].AmountUSD != 1.00 {
		t.Errorf("expected recent snapshot to survive, got %+v", snaps[0])
	}
}
