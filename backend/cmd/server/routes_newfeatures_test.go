package main

// Audit P1-4 regression test: the multiinstance registry must be opened ONCE
// and shared across all route sections, not re-opened per section. Four
// independent *sql.DB handles to one single-writer SQLite file contend for the
// writer lock and surface SQLITE_BUSY under concurrent writes.

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"vulos/backend/internal/multiinstance"
)

// TestOpenSharedRegistry_ReturnsUsableHandle verifies the shared-registry
// helper opens a working registry at the expected path.
func TestOpenSharedRegistry_ReturnsUsableHandle(t *testing.T) {
	dir := t.TempDir()
	reg := openSharedRegistry(dir)
	if reg == nil {
		t.Fatal("openSharedRegistry returned nil for a writable dir")
	}
	t.Cleanup(func() { _ = reg.Close() })

	if err := reg.Upsert(multiinstance.Instance{
		ULID:   "01HWZSHARED00000000000001",
		Kind:   multiinstance.KindDevice,
		Status: multiinstance.StatusOnline,
	}); err != nil {
		t.Fatalf("Upsert via shared handle: %v", err)
	}
	list, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(list))
	}
}

// TestSharedRegistry_ConcurrentWritesNoBusy is the core P1-4 guard: many
// concurrent writers driven through a SINGLE shared handle complete without
// SQLITE_BUSY. (The previous code opened the same file four times, giving four
// *sql.DB handles each with SetMaxOpenConns(1) — concurrent writers across
// those handles race for the single OS-level writer lock.)
func TestSharedRegistry_ConcurrentWritesNoBusy(t *testing.T) {
	dir := t.TempDir()
	reg := openSharedRegistry(dir)
	if reg == nil {
		t.Fatal("openSharedRegistry returned nil")
	}
	t.Cleanup(func() { _ = reg.Close() })

	const writers = 16
	const perWriter = 25
	var wg sync.WaitGroup
	errCh := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				err := reg.Upsert(multiinstance.Instance{
					ULID:   fmt.Sprintf("01HWZBUSY%07d%07d", w, i),
					Kind:   multiinstance.KindDevice,
					Status: multiinstance.StatusOnline,
				})
				if err != nil {
					errCh <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if strings.Contains(strings.ToUpper(err.Error()), "BUSY") {
			t.Fatalf("SQLITE_BUSY under concurrent writes through shared handle: %v", err)
		}
		t.Fatalf("unexpected write error through shared handle: %v", err)
	}
}

// TestSeparateOpensProduceDistinctHandles documents WHY sharing matters:
// multiinstance.Open on the same path yields two independent *sql.DB. This is
// the pattern the audit flagged; the fix is to open once and pass the single
// handle around.
func TestSeparateOpensProduceDistinctHandles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multiinstance.db")

	r1, err := multiinstance.Open(path)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	t.Cleanup(func() { _ = r1.Close() })
	r2, err := multiinstance.Open(path)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	t.Cleanup(func() { _ = r2.Close() })

	if r1.DB() == r2.DB() {
		t.Fatal("expected two independent *sql.DB handles from separate Open calls")
	}
}
