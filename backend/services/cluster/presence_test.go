package cluster

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- mock store ---------------------------------------------------------------

// mockLeaseStore is an in-memory implementation of LeaseStore used in tests.
type mockLeaseStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newMockLeaseStore() *mockLeaseStore {
	return &mockLeaseStore{data: make(map[string]string)}
}

func (m *mockLeaseStore) Put(key string, r io.Reader) error {
	buf, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.data[key] = string(buf)
	m.mu.Unlock()
	return nil
}

func (m *mockLeaseStore) Get(key string) (io.ReadCloser, error) {
	m.mu.Lock()
	v, ok := m.data[key]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	return io.NopCloser(strings.NewReader(v)), nil
}

func (m *mockLeaseStore) Delete(key string) error {
	m.mu.Lock()
	delete(m.data, key)
	m.mu.Unlock()
	return nil
}

// rawLease returns the stored JSON for a (user, file) pair, or "" if absent.
// Used for low-level assertions that verify the store was written directly.
func (m *mockLeaseStore) rawLease(user, file string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[leaseKey(user, file)]
}

// --- helpers ------------------------------------------------------------------

func newPresenceMgr(nodeID string, store LeaseStore) *PresenceManager {
	return NewPresenceManager(nodeID, store)
}

// --- tests --------------------------------------------------------------------

// TestAcquireWritesLease verifies that AcquireLease immediately writes a lease
// document to the store and that a subsequent CheckLease reports the holder.
func TestAcquireWritesLease(t *testing.T) {
	store := newMockLeaseStore()
	pm := newPresenceMgr("home", store)
	ctx := context.Background()

	const user, file = "alice", "/home/alice/notes.txt"

	stop := pm.AcquireLease(ctx, user, file)
	defer stop()

	raw := store.rawLease(user, file)
	if raw == "" {
		t.Fatal("expected lease to be written to store immediately after AcquireLease")
	}

	holder, fresh, err := pm.CheckLease(ctx, user, file)
	if err != nil {
		t.Fatalf("CheckLease returned unexpected error: %v", err)
	}
	if !fresh {
		t.Error("expected lease to be fresh")
	}
	if holder != "home" {
		t.Errorf("expected holder %q, got %q", "home", holder)
	}
}

// TestAcquireRenews verifies that the writeLease method updates the stored
// heartbeat timestamp when called again. The actual heartbeat goroutine ticks
// every 30 s — too slow for tests — so we call writeLease directly to simulate
// a renewal.
func TestAcquireRenews(t *testing.T) {
	store := newMockLeaseStore()
	pm := newPresenceMgr("office", store)
	ctx := context.Background()

	const user, file = "bob", "/home/bob/report.md"

	stop := pm.AcquireLease(ctx, user, file)
	defer stop()

	firstRaw := store.rawLease(user, file)
	if firstRaw == "" {
		t.Fatal("no initial lease written")
	}

	// Simulate a heartbeat tick by writing a slightly later timestamp.
	time.Sleep(2 * time.Millisecond)
	if err := pm.writeLease(user, file, time.Now().UTC()); err != nil {
		t.Fatalf("writeLease: %v", err)
	}

	secondRaw := store.rawLease(user, file)
	if secondRaw == firstRaw {
		t.Error("expected heartbeat to update the stored lease document")
	}

	// The lease should still be fresh after the renewal.
	holder, fresh, err := pm.CheckLease(ctx, user, file)
	if err != nil {
		t.Fatalf("CheckLease: %v", err)
	}
	if !fresh {
		t.Error("expected lease to be fresh after renewal")
	}
	if holder != "office" {
		t.Errorf("holder: got %q want %q", holder, "office")
	}
}

// TestSecondNodeSeesFreshLease simulates a second node (different PresenceManager,
// same shared store) reading a lease written by the first node.
func TestSecondNodeSeesFreshLease(t *testing.T) {
	store := newMockLeaseStore()
	node1 := newPresenceMgr("home", store)
	node2 := newPresenceMgr("office", store) // shares the same S3 store

	ctx := context.Background()
	const user, file = "carol", "/data/project.go"

	stop := node1.AcquireLease(ctx, user, file)
	defer stop()

	// node2 checks the same file — must see the lease written by node1.
	holder, fresh, err := node2.CheckLease(ctx, user, file)
	if err != nil {
		t.Fatalf("node2 CheckLease: %v", err)
	}
	if !fresh {
		t.Error("node2: expected fresh lease from node1")
	}
	if holder != "home" {
		t.Errorf("node2: expected holder %q, got %q", "home", holder)
	}
}

// TestStaleLease verifies that CheckLease returns fresh=false and an empty
// holder for a lease whose heartbeat is older than leaseStaleAfter (60 s).
func TestStaleLease(t *testing.T) {
	store := newMockLeaseStore()
	pm := newPresenceMgr("laptop", store)
	ctx := context.Background()

	const user, file = "dave", "/tmp/stale.txt"

	// Write a lease with a heartbeat in the past, beyond the TTL.
	staleTime := time.Now().UTC().Add(-(leaseStaleAfter + 5*time.Second))
	if err := pm.writeLease(user, file, staleTime); err != nil {
		t.Fatalf("writeLease: %v", err)
	}

	holder, fresh, err := pm.CheckLease(ctx, user, file)
	if err != nil {
		t.Fatalf("CheckLease: %v", err)
	}
	if fresh {
		t.Error("expected stale lease to return fresh=false")
	}
	if holder != "" {
		t.Errorf("expected empty holder for stale lease, got %q", holder)
	}
}

// TestStaleLeaseIsNonBlocking verifies that the caller is not blocked or
// returned an error when a stale lease is encountered.
func TestStaleLeaseIsNonBlocking(t *testing.T) {
	store := newMockLeaseStore()
	pm := newPresenceMgr("node-x", store)
	ctx := context.Background()

	const user, file = "eve", "/data/stale-edit.txt"

	// Write a stale lease.
	if err := pm.writeLease(user, file, time.Now().UTC().Add(-2*leaseStaleAfter)); err != nil {
		t.Fatalf("writeLease: %v", err)
	}

	done := make(chan struct{})
	go func() {
		holder, fresh, err := pm.CheckLease(ctx, user, file)
		if err != nil {
			t.Errorf("CheckLease should not error for stale lease: %v", err)
		}
		if fresh || holder != "" {
			t.Errorf("stale lease should return (empty, false), got (%q, %v)", holder, fresh)
		}
		close(done)
	}()

	select {
	case <-done:
		// passed — returned promptly
	case <-time.After(2 * time.Second):
		t.Fatal("CheckLease blocked unexpectedly")
	}
}

// TestReleaseRemovesLease verifies that after ReleaseLease the key is deleted
// from the store and CheckLease returns no holder.
func TestReleaseRemovesLease(t *testing.T) {
	store := newMockLeaseStore()
	pm := newPresenceMgr("home", store)
	ctx := context.Background()

	const user, file = "frank", "/home/frank/draft.txt"

	stop := pm.AcquireLease(ctx, user, file)
	defer stop()

	if store.rawLease(user, file) == "" {
		t.Fatal("lease not written before release")
	}

	if err := pm.ReleaseLease(ctx, user, file); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}

	if store.rawLease(user, file) != "" {
		t.Error("expected lease to be deleted after ReleaseLease")
	}

	holder, fresh, err := pm.CheckLease(ctx, user, file)
	if err != nil {
		t.Fatalf("CheckLease after release: %v", err)
	}
	if fresh || holder != "" {
		t.Errorf("expected no lease after release, got holder=%q fresh=%v", holder, fresh)
	}
}

// TestReleaseWithoutAcquire verifies that calling ReleaseLease on a key that
// was never acquired (or already deleted) does not return an error.
func TestReleaseWithoutAcquire(t *testing.T) {
	store := newMockLeaseStore()
	pm := newPresenceMgr("home", store)
	ctx := context.Background()

	if err := pm.ReleaseLease(ctx, "ghost", "/nonexistent/file"); err != nil {
		t.Errorf("ReleaseLease on absent key should not error, got: %v", err)
	}
}

// TestCheckNoLease verifies that CheckLease returns (empty, false, nil) when
// no lease has ever been written.
func TestCheckNoLease(t *testing.T) {
	store := newMockLeaseStore()
	pm := newPresenceMgr("home", store)
	ctx := context.Background()

	holder, fresh, err := pm.CheckLease(ctx, "nobody", "/untracked/file.txt")
	if err != nil {
		t.Fatalf("CheckLease: %v", err)
	}
	if fresh || holder != "" {
		t.Errorf("expected (empty, false), got (%q, %v)", holder, fresh)
	}
}
