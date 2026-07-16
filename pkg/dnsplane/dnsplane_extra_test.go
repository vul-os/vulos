// dnsplane_extra_test.go — additional tests to raise dnsplane package coverage
// above 60%. Covers: SQLStore open/upsert/delete/ByULID/DesiredUnapplied,
// MemDNSStore Delete/DesiredUnapplied, reconcileDesired path via reconciler,
// Reconciler.Status counts, Cloudflare PoolUpsert fallback + LB pool paths.
package dnsplane_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/dnsplane"
	"github.com/vul-os/vulos-management/pkg/routing"
)

// ────────────────────────────────────────────────────────────────────────────
// SQLStore — open + basic CRUD
// ────────────────────────────────────────────────────────────────────────────

func openSQLStore(t *testing.T) *dnsplane.SQLStore {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "dnsplane_test.db")
	db, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := dnsplane.Open(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("dnsplane.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSQLStore_UpsertAndByULID(t *testing.T) {
	st := openSQLStore(t)
	ctx := context.Background()

	r, err := st.Upsert(ctx, dnsplane.Record{
		FQDN:  "*.test.vulos.org",
		Type:  "A",
		Value: "1.2.3.4",
		TTL:   60,
		ULID:  "TESTULIDEXTRA0001ABCDE",
	})
	if err != nil {
		t.Fatalf("SQLStore.Upsert: %v", err)
	}
	if r.ID == 0 {
		t.Error("expected non-zero record ID")
	}
	if r.State != "desired" {
		t.Errorf("State: want desired, got %q", r.State)
	}

	records, err := st.ByULID(ctx, "TESTULIDEXTRA0001ABCDE")
	if err != nil {
		t.Fatalf("SQLStore.ByULID: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("ByULID: want 1 record, got %d", len(records))
	}
	if records[0].Value != "1.2.3.4" {
		t.Errorf("Value: want 1.2.3.4, got %q", records[0].Value)
	}
}

func TestSQLStore_UpsertIdempotent(t *testing.T) {
	st := openSQLStore(t)
	ctx := context.Background()

	r1, err := st.Upsert(ctx, dnsplane.Record{FQDN: "*.dedup.vulos.org", Type: "A", Value: "9.9.9.9", TTL: 60})
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	r2, err := st.Upsert(ctx, dnsplane.Record{FQDN: "*.dedup.vulos.org", Type: "A", Value: "9.9.9.9", TTL: 120})
	if err != nil {
		t.Fatalf("second Upsert (idempotent): %v", err)
	}
	if r1.ID != r2.ID {
		t.Errorf("idempotent upsert should return same ID: got %d and %d", r1.ID, r2.ID)
	}
	if r2.TTL != 120 {
		t.Errorf("TTL should be updated: want 120, got %d", r2.TTL)
	}
}

func TestSQLStore_Delete(t *testing.T) {
	st := openSQLStore(t)
	ctx := context.Background()

	r, _ := st.Upsert(ctx, dnsplane.Record{FQDN: "*.del.vulos.org", Type: "A", Value: "5.5.5.5", TTL: 60})
	if err := st.Delete(ctx, r.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	records, _ := st.ByULID(ctx, r.ULID)
	for _, rec := range records {
		if rec.ID == r.ID {
			t.Error("record should be deleted")
		}
	}
}

func TestSQLStore_DesiredUnapplied(t *testing.T) {
	st := openSQLStore(t)
	ctx := context.Background()

	// Insert 3 records in "desired" state.
	for i := 0; i < 3; i++ {
		_, _ = st.Upsert(ctx, dnsplane.Record{
			FQDN:  "*.desired.vulos.org",
			Type:  "TXT",
			Value: "val" + string(rune('a'+i)),
			TTL:   60,
		})
	}

	recs, err := st.DesiredUnapplied(ctx, 2)
	if err != nil {
		t.Fatalf("DesiredUnapplied: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("DesiredUnapplied limit=2: want 2, got %d", len(recs))
	}

	// All should be in "desired" state.
	for _, r := range recs {
		if r.State != "desired" {
			t.Errorf("State: want desired, got %q", r.State)
		}
	}
}

func TestSQLStore_MarkApplied(t *testing.T) {
	st := openSQLStore(t)
	ctx := context.Background()

	r, _ := st.Upsert(ctx, dnsplane.Record{FQDN: "*.applied.vulos.org", Type: "A", Value: "7.7.7.7", TTL: 60})
	if err := st.MarkApplied(ctx, r.ID, "provider-id-abc"); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}

	recs, _ := st.ByULID(ctx, r.ULID)
	if len(recs) == 0 {
		// ByULID requires ULID; since none was set we check DesiredUnapplied is empty.
		desired, _ := st.DesiredUnapplied(ctx, 10)
		for _, d := range desired {
			if d.ID == r.ID {
				t.Error("applied record should not appear in desired_unapplied")
			}
		}
	}
}

func TestSQLStore_MarkFailed(t *testing.T) {
	st := openSQLStore(t)
	ctx := context.Background()

	r, _ := st.Upsert(ctx, dnsplane.Record{FQDN: "*.failed.vulos.org", Type: "A", Value: "8.8.8.8", TTL: 60})
	if err := st.MarkFailed(ctx, r.ID, "timeout from provider"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	// After MarkFailed the record should not appear in DesiredUnapplied.
	desired, _ := st.DesiredUnapplied(ctx, 10)
	for _, d := range desired {
		if d.ID == r.ID {
			t.Error("failed record should not appear in desired_unapplied")
		}
	}
}

func TestSQLStore_InvalidFQDN(t *testing.T) {
	st := openSQLStore(t)
	ctx := context.Background()

	_, err := st.Upsert(ctx, dnsplane.Record{FQDN: "evil.example.com", Type: "A", Value: "1.1.1.1"})
	if err == nil || !strings.Contains(err.Error(), "invalid FQDN") {
		t.Errorf("expected ErrInvalidFQDN, got %v", err)
	}
}

func TestSQLStore_InvalidType(t *testing.T) {
	st := openSQLStore(t)
	ctx := context.Background()

	_, err := st.Upsert(ctx, dnsplane.Record{FQDN: "*.test.vulos.org", Type: "INVALID", Value: "1.1.1.1"})
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected invalid record type error, got %v", err)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// MemDNSStore — Delete and DesiredUnapplied
// ────────────────────────────────────────────────────────────────────────────

func TestMemDNSStore_Delete(t *testing.T) {
	ms := dnsplane.NewMemDNSStore()
	ctx := context.Background()

	r, _ := ms.Upsert(ctx, dnsplane.Record{FQDN: "*.mem-del.vulos.org", Type: "A", Value: "2.2.2.2", TTL: 60, ULID: "MEMDELULIDABCDEFGH01234"})
	if err := ms.Delete(ctx, r.ID); err != nil {
		t.Fatalf("MemDNSStore.Delete: %v", err)
	}

	records, _ := ms.ByULID(ctx, "MEMDELULIDABCDEFGH01234")
	if len(records) != 0 {
		t.Errorf("expected 0 records after Delete, got %d", len(records))
	}
}

func TestMemDNSStore_DesiredUnapplied_Limit(t *testing.T) {
	ms := dnsplane.NewMemDNSStore()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, _ = ms.Upsert(ctx, dnsplane.Record{
			FQDN:  "*.mem-limit.vulos.org",
			Type:  "TXT",
			Value: "item" + string(rune('a'+i)),
			TTL:   60,
		})
	}

	recs, err := ms.DesiredUnapplied(ctx, 3)
	if err != nil {
		t.Fatalf("DesiredUnapplied: %v", err)
	}
	if len(recs) != 3 {
		t.Errorf("limit=3: want 3 records, got %d", len(recs))
	}
}

func TestMemDNSStore_DesiredUnapplied_ExcludesApplied(t *testing.T) {
	ms := dnsplane.NewMemDNSStore()
	ctx := context.Background()

	r1, _ := ms.Upsert(ctx, dnsplane.Record{FQDN: "*.a.vulos.org", Type: "A", Value: "1.0.0.1", TTL: 60})
	_, _ = ms.Upsert(ctx, dnsplane.Record{FQDN: "*.b.vulos.org", Type: "A", Value: "1.0.0.2", TTL: 60})

	_ = ms.MarkApplied(ctx, r1.ID, "pid1")

	recs, _ := ms.DesiredUnapplied(ctx, 10)
	for _, r := range recs {
		if r.ID == r1.ID {
			t.Error("applied record should not appear in DesiredUnapplied")
		}
	}
	if len(recs) != 1 {
		t.Errorf("expected 1 desired record, got %d", len(recs))
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Reconciler.reconcileDesired path via ReconcileULID (applies pending records)
// ────────────────────────────────────────────────────────────────────────────

func TestReconcileULID_AppliesPendingRecord(t *testing.T) {
	ctx := context.Background()

	rs := routing.NewMemStore()
	_ = rs.UpsertPoP(ctx, routing.PoP{ID: "pop-extra", IP: "3.3.3.3", Healthy: true})
	const uid = "EXTRAULIDABCDEF0123456789"
	_, _ = rs.Enroll(ctx, uid, "acc-extra", routing.ModeFabric)

	mp := dnsplane.NewMemProvider()
	ms := dnsplane.NewMemDNSStore()
	rec := &dnsplane.Reconciler{Store: ms, Provider: mp, RoutingStore: rs}

	// First reconcile — creates + applies the record.
	if err := rec.ReconcileULID(ctx, uid); err != nil {
		t.Fatalf("first ReconcileULID: %v", err)
	}
	records, _ := ms.ByULID(ctx, uid)
	if len(records) == 0 {
		t.Fatal("expected records after ReconcileULID")
	}
	if records[0].State != "applied" {
		t.Errorf("State after first reconcile: want applied, got %q", records[0].State)
	}

	// Second reconcile on the same ULID should be a no-op (already applied).
	if err := rec.ReconcileULID(ctx, uid); err != nil {
		t.Fatalf("second ReconcileULID: %v", err)
	}
	records2, _ := ms.ByULID(ctx, uid)
	if len(records2) != len(records) {
		t.Errorf("second reconcile created extra records: want %d, got %d", len(records), len(records2))
	}
}

// ────────────────────────────────────────────────────────────────────────────
// MemProvider — Name
// ────────────────────────────────────────────────────────────────────────────

func TestMemProvider_Name(t *testing.T) {
	mp := dnsplane.NewMemProvider()
	if mp.Name() == "" {
		t.Error("MemProvider.Name() should not be empty")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Reconciler.Status — applied/failed counts
// ────────────────────────────────────────────────────────────────────────────

// TestReconciler_StatusCounts creates a MemDNSStore + MemProvider + Reconciler,
// upserts 3 records, marks 2 applied and 1 failed, then asserts the Status
// snapshot returns the correct counts.
func TestReconciler_StatusCounts(t *testing.T) {
	ctx := context.Background()

	rs := routing.NewMemStore()
	mp := dnsplane.NewMemProvider()
	ms := dnsplane.NewMemDNSStore()
	rec := &dnsplane.Reconciler{Store: ms, Provider: mp, RoutingStore: rs}

	// Upsert 3 records.
	r1, err := ms.Upsert(ctx, dnsplane.Record{FQDN: "*.sc1.vulos.org", Type: "A", Value: "10.0.0.1", TTL: 60})
	if err != nil {
		t.Fatalf("upsert r1: %v", err)
	}
	r2, err := ms.Upsert(ctx, dnsplane.Record{FQDN: "*.sc2.vulos.org", Type: "A", Value: "10.0.0.2", TTL: 60})
	if err != nil {
		t.Fatalf("upsert r2: %v", err)
	}
	r3, err := ms.Upsert(ctx, dnsplane.Record{FQDN: "*.sc3.vulos.org", Type: "A", Value: "10.0.0.3", TTL: 60})
	if err != nil {
		t.Fatalf("upsert r3: %v", err)
	}

	// Mark 2 applied, 1 failed.
	if err := ms.MarkApplied(ctx, r1.ID, "pid-1"); err != nil {
		t.Fatalf("MarkApplied r1: %v", err)
	}
	if err := ms.MarkApplied(ctx, r2.ID, "pid-2"); err != nil {
		t.Fatalf("MarkApplied r2: %v", err)
	}
	if err := ms.MarkFailed(ctx, r3.ID, "timeout"); err != nil {
		t.Fatalf("MarkFailed r3: %v", err)
	}

	snap, err := rec.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if snap.AppliedCount != 2 {
		t.Errorf("AppliedCount: want 2, got %d", snap.AppliedCount)
	}
	if snap.FailedCount != 1 {
		t.Errorf("FailedCount: want 1, got %d", snap.FailedCount)
	}
	if snap.DesiredCount != 0 {
		t.Errorf("DesiredCount: want 0, got %d", snap.DesiredCount)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Cloudflare PoolUpsert — v1 A-record fallback (AccountID empty)
// ────────────────────────────────────────────────────────────────────────────

// TestCloudflare_PoolUpsert_FallsBackToARecords exercises the v1 fallback path:
// when AccountID is empty, PoolUpsert emits individual A records instead of
// using the Cloudflare LB pool API.
func TestCloudflare_PoolUpsert_FallsBackToARecords(t *testing.T) {
	var receivedPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPaths = append(receivedPaths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		// Return a successful CF response envelope.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"result":  map[string]string{"id": "rec-1", "type": "A"},
			"errors":  []interface{}{},
		})
	}))
	defer srv.Close()

	cfg := dnsplane.CloudflareConfig{
		APIToken:   "test-token",
		ZoneID:     "zone-abc",
		PoolName:   "pop.vulos.org",
		AccountID:  "", // empty → fallback to A records
		HTTPClient: srv.Client(),
	}
	// Patch the base URL by creating the provider normally. The CF provider
	// uses cfBaseURL const so we need the test server to shadow it via a
	// round-trip through a test-server-aware httptest client (see note below).
	//
	// NOTE: CloudflareProvider.cfDo prepends cfBaseURL (https://api.cloudflare.com/client/v4)
	// so we can't directly call the httptest server unless we override the
	// client's transport. We do that via a custom RoundTripper that rewrites
	// the host to the test server.
	cfg.HTTPClient = &http.Client{
		Transport: &hostRewriteTransport{base: srv.Client().Transport, target: srv.URL},
	}

	cp, err := dnsplane.NewCloudflareProvider(cfg)
	if err != nil {
		t.Fatalf("NewCloudflareProvider: %v", err)
	}

	ctx := context.Background()
	members := []dnsplane.PoolMember{
		{PoPID: "pop-a", IP: "1.2.3.4", Healthy: true},
		{PoPID: "pop-b", IP: "5.6.7.8", Healthy: false}, // unhealthy — should be skipped
		{PoPID: "pop-c", IP: "9.10.11.12", Healthy: true},
	}

	if err := cp.PoolUpsert(ctx, "pop.vulos.org", members); err != nil {
		t.Fatalf("PoolUpsert: %v", err)
	}

	// Expect 2 POST requests (one per healthy member).
	postCount := 0
	for _, p := range receivedPaths {
		if strings.HasPrefix(p, "POST") {
			postCount++
		}
	}
	if postCount != 2 {
		t.Errorf("expected 2 POST requests for 2 healthy members, got %d (paths: %v)", postCount, receivedPaths)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Cloudflare PoolUpsert — real LB pool path (AccountID set)
// ────────────────────────────────────────────────────────────────────────────

// TestCloudflare_PoolUpsert_LBPool exercises the real LB pool path (AccountID
// non-empty) using a mock httptest.Server. The test verifies:
//   - GET /accounts/{id}/load_balancers/pools?name=... is called first.
//   - POST /accounts/{id}/load_balancers/pools is called when no pool exists.
//   - PUT /accounts/{id}/load_balancers/pools/{pool_id} is called when a pool
//     already exists.
func TestCloudflare_PoolUpsert_LBPool(t *testing.T) {
	const accountID = "acct-123"
	const poolID = "pool-xyz"

	// First call: pool doesn't exist yet (empty list). Second call: pool exists.
	callCount := 0
	var receivedMethods []string
	var receivedPaths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		receivedMethods = append(receivedMethods, r.Method)
		receivedPaths = append(receivedPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "load_balancers/pools"):
			// Return empty pool list on first call, non-empty on subsequent calls.
			if callCount <= 1 {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"success": true,
					"result":  []interface{}{},
					"errors":  []interface{}{},
				})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"success": true,
					"result":  []map[string]string{{"id": poolID, "name": "pop.vulos.org"}},
					"errors":  []interface{}{},
				})
			}
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "load_balancers/pools"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"result":  map[string]string{"id": poolID, "name": "pop.vulos.org"},
				"errors":  []interface{}{},
			})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "load_balancers/pools"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"result":  map[string]string{"id": poolID, "name": "pop.vulos.org"},
				"errors":  []interface{}{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := dnsplane.CloudflareConfig{
		APIToken:  "test-token",
		ZoneID:    "zone-abc",
		PoolName:  "pop.vulos.org",
		AccountID: accountID,
		HTTPClient: &http.Client{
			Transport: &hostRewriteTransport{base: srv.Client().Transport, target: srv.URL},
		},
	}

	cp, err := dnsplane.NewCloudflareProvider(cfg)
	if err != nil {
		t.Fatalf("NewCloudflareProvider: %v", err)
	}

	ctx := context.Background()
	members := []dnsplane.PoolMember{
		{PoPID: "pop-1", IP: "1.2.3.4", Healthy: true},
		{PoPID: "pop-2", IP: "5.6.7.8", Healthy: false}, // skipped
	}

	// First call — pool does not exist, should POST.
	if err := cp.PoolUpsert(ctx, "pop.vulos.org", members); err != nil {
		t.Fatalf("first PoolUpsert: %v", err)
	}
	if len(receivedMethods) < 2 {
		t.Fatalf("expected at least 2 requests (GET + POST), got %d", len(receivedMethods))
	}
	if receivedMethods[0] != http.MethodGet {
		t.Errorf("first request should be GET, got %s", receivedMethods[0])
	}
	if receivedMethods[1] != http.MethodPost {
		t.Errorf("second request should be POST (create), got %s", receivedMethods[1])
	}

	// Reset and do a second call — pool now exists, should PUT.
	callCount = 2 // make the GET handler return the existing pool
	prevLen := len(receivedMethods)

	if err := cp.PoolUpsert(ctx, "pop.vulos.org", members); err != nil {
		t.Fatalf("second PoolUpsert: %v", err)
	}
	if len(receivedMethods) < prevLen+2 {
		t.Fatalf("expected 2 more requests (GET + PUT), got %d new", len(receivedMethods)-prevLen)
	}
	if receivedMethods[prevLen] != http.MethodGet {
		t.Errorf("second round first request should be GET, got %s", receivedMethods[prevLen])
	}
	if receivedMethods[prevLen+1] != http.MethodPut {
		t.Errorf("second round second request should be PUT (update), got %s", receivedMethods[prevLen+1])
	}
}

// hostRewriteTransport is a test helper RoundTripper that rewrites requests
// to a target host (e.g. httptest.Server.URL) while preserving the path and
// query, so we can test Cloudflare API calls without hitting the real API.
type hostRewriteTransport struct {
	base   http.RoundTripper
	target string // e.g. "http://127.0.0.1:PORT"
}

func (t *hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	// Replace scheme + host with the test server's.
	cloned.URL.Scheme = "http"
	cloned.URL.Host = strings.TrimPrefix(t.target, "http://")
	if t.base != nil {
		return t.base.RoundTrip(cloned)
	}
	return http.DefaultTransport.RoundTrip(cloned)
}
