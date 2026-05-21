package lease

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ── In-memory mock backend ────────────────────────────────────────────────────

// mockObject holds one object's state.
type mockObject struct {
	body []byte
	etag string
}

// mockBackend is an in-memory S3-like store that implements the backend
// interface with correct If-Match / 412 semantics.
//
// Key invariant: putIfMatch NEVER uses If-None-Match:* — it only accepts
// explicit matchETag values and rejects mismatches with ErrLost.
type mockBackend struct {
	mu      sync.Mutex
	objects map[string]*mockObject
	// putCalls records all (key, matchETag) pairs to assert no If-None-Match:*
	putCalls []putCall
}

type putCall struct {
	key       string
	matchETag string
}

func newMockBackend() *mockBackend {
	return &mockBackend{objects: make(map[string]*mockObject)}
}

// seed pre-creates an object (simulates cluster-init pre-creation).
func (m *mockBackend) seed(key string, body []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = &mockObject{body: append([]byte(nil), body...), etag: computeETag(body)}
}

func computeETag(body []byte) string {
	h := md5.Sum(body)
	return fmt.Sprintf("%x", h)
}

func (m *mockBackend) getWithETag(ctx context.Context, key string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	if !ok {
		return nil, "", ErrLeaseMissing
	}
	return append([]byte(nil), obj.body...), obj.etag, nil
}

// putUnconditional stores the object without any ETag check.
// Used only by EnsureLease for the initial bootstrap write.
func (m *mockBackend) putUnconditional(ctx context.Context, key string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = &mockObject{body: append([]byte(nil), body...), etag: computeETag(body)}
	return nil
}

// putIfMatch performs a CAS update.
// It asserts that matchETag is never the string "*" (no If-None-Match:* semantics).
func (m *mockBackend) putIfMatch(ctx context.Context, key string, body []byte, matchETag string) (string, error) {
	// ASSERTION: we must never be called with the wildcard "*"
	if matchETag == "*" {
		return "", fmt.Errorf("test assertion FAILED: If-None-Match:* used — matchETag was \"*\"")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.putCalls = append(m.putCalls, putCall{key: key, matchETag: matchETag})

	obj, ok := m.objects[key]
	if !ok {
		// Object doesn't exist at all — since we require If-Match on an existing object,
		// treat as 412 (nothing to match against).
		return "", ErrLost
	}

	if obj.etag != matchETag {
		return "", ErrLost
	}

	newETag := computeETag(body)
	m.objects[key] = &mockObject{body: append([]byte(nil), body...), etag: newETag}
	return newETag, nil
}

// assertNoWildcard fails the test if any put call used "*" as matchETag.
func (m *mockBackend) assertNoWildcard(t *testing.T) {
	t.Helper()
	for _, c := range m.putCalls {
		if c.matchETag == "*" {
			t.Errorf("VIOLATION: putIfMatch called with matchETag=\"*\" on key %q — If-None-Match:* is forbidden", c.key)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func seedFreeDoc(b *mockBackend, scope string, fence int64) {
	doc := leaseDoc{State: stateFree, Fence: fence}
	data, _ := json.Marshal(doc)
	b.seed(objectKey(scope), data)
}

func newTestManager(b *mockBackend) *Manager {
	return NewWithBackend(b)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestAcquire_HappyPath(t *testing.T) {
	b := newMockBackend()
	seedFreeDoc(b, "test-scope", 0)
	m := newTestManager(b)

	held, err := m.Acquire(context.Background(), "test-scope", "node-A", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire: unexpected error: %v", err)
	}
	if held.HolderID != "node-A" {
		t.Errorf("HolderID: want node-A, got %s", held.HolderID)
	}
	if held.Fence != 1 {
		t.Errorf("Fence: want 1, got %d", held.Fence)
	}

	b.assertNoWildcard(t)
}

func TestAcquire_LostRace_Returns412(t *testing.T) {
	b := newMockBackend()
	seedFreeDoc(b, "race-scope", 0)

	// Simulate a racing writer by directly updating the object after the mock read
	// but before our put. We do this by having two managers share the same backend
	// and making node-B win first.
	m := newTestManager(b)

	// node-B acquires first.
	_, err := m.Acquire(context.Background(), "race-scope", "node-B", 30*time.Second)
	if err != nil {
		t.Fatalf("node-B acquire: %v", err)
	}

	// Now node-A tries to acquire with a stale ETag by bypassing the read:
	// We simulate the race by calling putIfMatch with the original ETag
	// (which is now stale because node-B already mutated it).
	originalETag := computeETag(func() []byte {
		doc := leaseDoc{State: stateFree, Fence: 0}
		data, _ := json.Marshal(doc)
		return data
	}())

	_, err = b.putIfMatch(context.Background(), objectKey("race-scope"), []byte(`{}`), originalETag)
	if !errors.Is(err, ErrLost) {
		t.Errorf("stale-ETag put: want ErrLost, got %v", err)
	}

	b.assertNoWildcard(t)
}

func TestAcquire_TwoConcurrentRacers_OnlyOneWins(t *testing.T) {
	b := newMockBackend()
	seedFreeDoc(b, "concurrent-scope", 0)

	// Read the ETag before either racer mutates
	body, originalETag, err := b.getWithETag(context.Background(), objectKey("concurrent-scope"))
	if err != nil {
		t.Fatalf("seed read: %v", err)
	}
	_ = body

	var doc leaseDoc
	_ = json.Unmarshal(body, &doc)

	newDoc := leaseDoc{State: stateHeld, Holder: "racer-1", Fence: doc.Fence + 1, ExpiresAt: time.Now().Add(30 * time.Second)}
	newBody, _ := json.Marshal(newDoc)

	// racer-1 wins
	_, err = b.putIfMatch(context.Background(), objectKey("concurrent-scope"), newBody, originalETag)
	if err != nil {
		t.Fatalf("racer-1 put: %v", err)
	}

	// racer-2 tries with the same (now stale) ETag
	newDoc2 := leaseDoc{State: stateHeld, Holder: "racer-2", Fence: doc.Fence + 1, ExpiresAt: time.Now().Add(30 * time.Second)}
	newBody2, _ := json.Marshal(newDoc2)
	_, err = b.putIfMatch(context.Background(), objectKey("concurrent-scope"), newBody2, originalETag)
	if !errors.Is(err, ErrLost) {
		t.Errorf("racer-2 should get ErrLost, got %v", err)
	}

	b.assertNoWildcard(t)
}

func TestRenew_BumpsFenceAndExpiresAt(t *testing.T) {
	b := newMockBackend()
	seedFreeDoc(b, "renew-scope", 5) // pre-existing fence=5
	m := newTestManager(b)

	held, err := m.Acquire(context.Background(), "renew-scope", "node-A", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if held.Fence != 6 {
		t.Errorf("after Acquire, fence want 6, got %d", held.Fence)
	}

	if err := m.Renew(context.Background(), held, 60*time.Second); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if held.Fence != 7 {
		t.Errorf("after Renew, fence want 7, got %d", held.Fence)
	}

	b.assertNoWildcard(t)
}

func TestRenew_WrongHolder_ReturnsErrNotHolder(t *testing.T) {
	b := newMockBackend()
	seedFreeDoc(b, "renew-nothold", 0)
	m := newTestManager(b)

	held, err := m.Acquire(context.Background(), "renew-nothold", "node-A", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Tamper: change the HolderID in the Held token to simulate a stale token.
	held.HolderID = "impostor"

	if err := m.Renew(context.Background(), held, 60*time.Second); !errors.Is(err, ErrNotHolder) {
		t.Errorf("Renew with wrong holder: want ErrNotHolder, got %v", err)
	}
}

func TestRelease_FlipsToFree(t *testing.T) {
	b := newMockBackend()
	seedFreeDoc(b, "rel-scope", 0)
	m := newTestManager(b)

	held, err := m.Acquire(context.Background(), "rel-scope", "node-A", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	if err := m.Release(context.Background(), held); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Verify the stored doc is now free.
	body, _, err := b.getWithETag(context.Background(), objectKey("rel-scope"))
	if err != nil {
		t.Fatalf("post-release read: %v", err)
	}
	var doc leaseDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("post-release unmarshal: %v", err)
	}
	if doc.State != stateFree {
		t.Errorf("post-release state: want free, got %s", doc.State)
	}

	b.assertNoWildcard(t)
}

func TestRelease_WrongHolder(t *testing.T) {
	b := newMockBackend()
	seedFreeDoc(b, "rel-nothold", 0)
	m := newTestManager(b)

	held, err := m.Acquire(context.Background(), "rel-nothold", "node-A", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	held.HolderID = "impostor"

	if err := m.Release(context.Background(), held); !errors.Is(err, ErrNotHolder) {
		t.Errorf("Release with wrong holder: want ErrNotHolder, got %v", err)
	}
}

func TestFenceIsMonotonic(t *testing.T) {
	b := newMockBackend()
	seedFreeDoc(b, "mono-scope", 0)
	m := newTestManager(b)

	var lastFence int64
	var held *Held
	var err error

	// Acquire + Renew x3: fence must increase each time.
	held, err = m.Acquire(context.Background(), "mono-scope", "node-A", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if held.Fence <= lastFence {
		t.Errorf("fence not monotonic after Acquire: %d <= %d", held.Fence, lastFence)
	}
	lastFence = held.Fence

	for i := 0; i < 3; i++ {
		if err := m.Renew(context.Background(), held, 30*time.Second); err != nil {
			t.Fatalf("Renew %d: %v", i, err)
		}
		if held.Fence <= lastFence {
			t.Errorf("fence not monotonic after Renew %d: %d <= %d", i, held.Fence, lastFence)
		}
		lastFence = held.Fence
	}

	b.assertNoWildcard(t)
}

func TestValidateFence_RejectsStale(t *testing.T) {
	tests := []struct {
		incoming int64
		lastSeen int64
		wantErr  bool
	}{
		{incoming: 5, lastSeen: 3, wantErr: false},
		{incoming: 5, lastSeen: 5, wantErr: false},
		{incoming: 4, lastSeen: 5, wantErr: true},
		{incoming: 0, lastSeen: 1, wantErr: true},
	}
	for _, tt := range tests {
		err := ValidateFence(tt.incoming, tt.lastSeen)
		if tt.wantErr && !errors.Is(err, ErrStaleFence) {
			t.Errorf("ValidateFence(%d, %d): want ErrStaleFence, got %v", tt.incoming, tt.lastSeen, err)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("ValidateFence(%d, %d): unexpected error %v", tt.incoming, tt.lastSeen, err)
		}
	}
}

func TestErrLeaseMissing_WhenObjectAbsent(t *testing.T) {
	b := newMockBackend() // nothing seeded
	m := newTestManager(b)

	_, err := m.Acquire(context.Background(), "missing-scope", "node-A", 30*time.Second)
	if !errors.Is(err, ErrLeaseMissing) {
		t.Errorf("Acquire on missing key: want ErrLeaseMissing, got %v", err)
	}
}

func TestAcquireExpiredLease(t *testing.T) {
	b := newMockBackend()
	// Seed a held-but-expired lease.
	doc := leaseDoc{
		State:     stateHeld,
		Holder:    "old-holder",
		Fence:     10,
		ExpiresAt: time.Now().Add(-1 * time.Minute), // already expired
	}
	data, _ := json.Marshal(doc)
	b.seed(objectKey("expired-scope"), data)

	m := newTestManager(b)

	held, err := m.Acquire(context.Background(), "expired-scope", "node-B", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire on expired lease: unexpected error: %v", err)
	}
	if held.HolderID != "node-B" {
		t.Errorf("holder: want node-B, got %s", held.HolderID)
	}
	if held.Fence != 11 {
		t.Errorf("fence: want 11 (expired fence 10 + 1), got %d", held.Fence)
	}

	b.assertNoWildcard(t)
}

func TestNoIfNoneMatchStar_ExplicitAssertion(t *testing.T) {
	// This test directly asserts the constraint at the mock layer.
	// Any call to putIfMatch with matchETag=="*" is a protocol violation.
	b := newMockBackend()
	seedFreeDoc(b, "star-check", 0)

	_, err := b.putIfMatch(context.Background(), objectKey("star-check"), []byte(`{}`), "*")
	if err == nil {
		t.Error("expected error when using wildcard matchETag, got nil")
	}
	if !strings.Contains(err.Error(), "If-None-Match:*") {
		t.Errorf("error should mention If-None-Match:*, got: %v", err)
	}
}

// ── MinIO integration test (skipped unless LEASE_MINIO_TEST is set) ───────────

func TestMinIOIntegration(t *testing.T) {
	endpoint := os.Getenv("LEASE_MINIO_TEST")
	if endpoint == "" {
		t.Skip("LEASE_MINIO_TEST not set; skipping MinIO integration test")
	}

	accessKey := os.Getenv("LEASE_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("LEASE_MINIO_SECRET_KEY")
	bucket := os.Getenv("LEASE_MINIO_BUCKET")
	if accessKey == "" {
		accessKey = "minioadmin"
	}
	if secretKey == "" {
		secretKey = "minioadmin"
	}
	if bucket == "" {
		bucket = "vulos-lease-test"
	}

	cfg := S3Config{
		Endpoint:  endpoint,
		Bucket:    bucket,
		AccessKey: accessKey,
		SecretKey: secretKey,
		UseSSL:    false,
	}

	mgr, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: false,
	})
	if err != nil {
		t.Fatalf("minio.New: %v", err)
	}

	ctx := context.Background()

	// Ensure bucket exists.
	if err := mc.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code != "BucketAlreadyOwnedByYou" && errResp.Code != "BucketAlreadyExists" {
			t.Fatalf("MakeBucket: %v", err)
		}
	}

	// Pre-create the lease object (simulating LEASE-02 cluster init).
	scope := fmt.Sprintf("integration-test-%d", time.Now().UnixNano())
	key := objectKey(scope)
	initDoc := leaseDoc{State: stateFree, Fence: 0}
	initBody, _ := json.Marshal(initDoc)
	_, err = mc.PutObject(ctx, bucket, key, bytes.NewReader(initBody), int64(len(initBody)),
		minio.PutObjectOptions{ContentType: "application/json"})
	if err != nil {
		t.Fatalf("pre-create lease object: %v", err)
	}
	t.Cleanup(func() {
		_ = mc.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
	})

	// 1. Acquire.
	held, err := mgr.Acquire(ctx, scope, "integration-node-A", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if held.Fence != 1 {
		t.Errorf("fence after Acquire: want 1, got %d", held.Fence)
	}

	// 2. Renew.
	if err := mgr.Renew(ctx, held, 60*time.Second); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if held.Fence != 2 {
		t.Errorf("fence after Renew: want 2, got %d", held.Fence)
	}

	// 3. Concurrent racer should get ErrLost.
	_, err = mgr.Acquire(ctx, scope, "integration-node-B", 30*time.Second)
	if !errors.Is(err, ErrNotHolder) {
		t.Errorf("concurrent Acquire: want ErrNotHolder, got %v", err)
	}

	// 4. Release.
	if err := mgr.Release(ctx, held); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// 5. Verify it's free again via a fresh Acquire.
	held2, err := mgr.Acquire(ctx, scope, "integration-node-B", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	if held2.Fence <= held.Fence {
		t.Errorf("fence not monotonic after second Acquire: %d <= %d", held2.Fence, held.Fence)
	}

	// 6. Fence monotonicity: lastSeen from node-A (held.Fence) must be rejected
	//    by ValidateFence when compared to node-B's higher fence.
	if err := ValidateFence(held.Fence, held2.Fence); !errors.Is(err, ErrStaleFence) {
		t.Errorf("ValidateFence(old, new): want ErrStaleFence, got %v", err)
	}

	t.Logf("MinIO integration: scope=%s final fence=%d", scope, held2.Fence)
}

// ── EnsureLease tests ─────────────────────────────────────────────────────────

// TestEnsureLease_CreatesFreeObject verifies that EnsureLease writes a free
// lease doc when the object does not yet exist.
func TestEnsureLease_CreatesFreeObject(t *testing.T) {
	b := newMockBackend() // nothing seeded
	m := newTestManager(b)

	if err := m.EnsureLease(context.Background(), "init-scope"); err != nil {
		t.Fatalf("EnsureLease: unexpected error: %v", err)
	}

	body, _, err := b.getWithETag(context.Background(), objectKey("init-scope"))
	if err != nil {
		t.Fatalf("read after EnsureLease: %v", err)
	}

	var doc leaseDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.State != stateFree {
		t.Errorf("state: want free, got %s", doc.State)
	}
	if doc.Fence != 0 {
		t.Errorf("fence: want 0, got %d", doc.Fence)
	}
	if doc.Holder != "" {
		t.Errorf("holder: want empty, got %q", doc.Holder)
	}
}

// TestEnsureLease_IdempotentWhenFree verifies that calling EnsureLease on an
// already-free object is a no-op (no error, state unchanged).
func TestEnsureLease_IdempotentWhenFree(t *testing.T) {
	b := newMockBackend()
	seedFreeDoc(b, "idem-free-scope", 3)
	m := newTestManager(b)

	// Record etag before.
	_, etagBefore, err := b.getWithETag(context.Background(), objectKey("idem-free-scope"))
	if err != nil {
		t.Fatalf("pre-read: %v", err)
	}

	if err := m.EnsureLease(context.Background(), "idem-free-scope"); err != nil {
		t.Fatalf("EnsureLease on existing free: %v", err)
	}

	// Object must be unchanged.
	body, etagAfter, err := b.getWithETag(context.Background(), objectKey("idem-free-scope"))
	if err != nil {
		t.Fatalf("post-read: %v", err)
	}
	if etagAfter != etagBefore {
		t.Errorf("ETag changed after idempotent EnsureLease: want %q, got %q", etagBefore, etagAfter)
	}
	var doc leaseDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Fence != 3 {
		t.Errorf("fence tampered: want 3, got %d", doc.Fence)
	}
}

// TestEnsureLease_NoClobberHeldLease verifies that EnsureLease does NOT overwrite
// an existing held lease — a held lease must remain held after re-running init.
func TestEnsureLease_NoClobberHeldLease(t *testing.T) {
	b := newMockBackend()

	// Seed a held lease.
	heldDoc := leaseDoc{
		State:     stateHeld,
		Holder:    "node-X",
		Fence:     42,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	data, _ := json.Marshal(heldDoc)
	b.seed(objectKey("held-scope"), data)

	m := newTestManager(b)

	// Re-running EnsureLease must be a no-op.
	if err := m.EnsureLease(context.Background(), "held-scope"); err != nil {
		t.Fatalf("EnsureLease on held lease: unexpected error: %v", err)
	}

	// Lease must still be held with the original values.
	body, _, err := b.getWithETag(context.Background(), objectKey("held-scope"))
	if err != nil {
		t.Fatalf("post-read: %v", err)
	}
	var doc leaseDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.State != stateHeld {
		t.Errorf("state clobbered: want held, got %s", doc.State)
	}
	if doc.Holder != "node-X" {
		t.Errorf("holder clobbered: want node-X, got %q", doc.Holder)
	}
	if doc.Fence != 42 {
		t.Errorf("fence clobbered: want 42, got %d", doc.Fence)
	}
}

// ensure io.ReadAll is referenced to avoid unused-import in environments
// where the compiler sees the import only via this test.
var _ = io.ReadAll
