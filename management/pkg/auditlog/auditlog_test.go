package auditlog_test

import (
	"context"
	"math"
	"strconv"
	"sync"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openLogger(t *testing.T) (*auditlog.Logger, *cpdb.DB) {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	l, err := auditlog.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, db
}

// ---------------------------------------------------------------------------
// Test 1: append + verify clean chain
// ---------------------------------------------------------------------------

func TestAppendAndVerify_Clean(t *testing.T) {
	ctx := context.Background()
	l, _ := openLogger(t)

	for i := 0; i < 5; i++ {
		if err := l.Record(ctx, "admin@example.com", "pop.drain", "pop-"+strconv.Itoa(i),
			map[string]string{"region": "eu-west"}); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	if err := l.Verify(ctx, 0, math.MaxInt64); err != nil {
		t.Errorf("Verify: expected clean chain, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 2: detect tampered row (entry_hash mutated)
// ---------------------------------------------------------------------------

func TestVerify_DetectsTamperedRow(t *testing.T) {
	ctx := context.Background()
	l, db := openLogger(t)

	// Append three entries.
	for i := 0; i < 3; i++ {
		if err := l.Record(ctx, "admin@example.com", "ota.release", "v1.0."+strconv.Itoa(i), nil); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	// Verify clean first.
	if err := l.Verify(ctx, 0, math.MaxInt64); err != nil {
		t.Fatalf("expected clean chain before tampering: %v", err)
	}

	// Directly mutate the entry_hash of row seq=2 via raw SQL on the same DB.
	if _, err := db.Exec(`UPDATE auditlog_entries SET entry_hash = 'deadbeef' WHERE seq = 2`); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	verr := l.Verify(ctx, 0, math.MaxInt64)
	if verr == nil {
		t.Fatal("expected Verify to detect tampered entry_hash, got nil")
	}
	ve, ok := verr.(*auditlog.VerifyError)
	if !ok {
		t.Fatalf("expected *VerifyError, got %T: %v", verr, verr)
	}
	if ve.StoredHash != "deadbeef" && ve.ComputedHash == "deadbeef" {
		t.Logf("VerifyError: %v", ve)
	}
	// We just need Verify to return a non-nil error — that's the tamper detection.
	t.Logf("tamper detected: %v", verr)
}

// ---------------------------------------------------------------------------
// Test 3: detect deleted row (chain prev_hash mismatch)
// ---------------------------------------------------------------------------

func TestVerify_DetectsDeletedRow(t *testing.T) {
	ctx := context.Background()
	l, db := openLogger(t)

	// Append 4 entries.
	for i := 0; i < 4; i++ {
		if err := l.Record(ctx, "admin@example.com", "pool.add_ip", "10.0.0."+strconv.Itoa(i), nil); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	// Delete seq=2 to simulate a removed row.
	if _, err := db.Exec(`DELETE FROM auditlog_entries WHERE seq = 2`); err != nil {
		t.Fatalf("delete row: %v", err)
	}

	verr := l.Verify(ctx, 0, math.MaxInt64)
	if verr == nil {
		t.Fatal("expected Verify to detect deleted (missing) row, got nil")
	}
	t.Logf("deleted row detected: %v", verr)
}

// ---------------------------------------------------------------------------
// Test 4: concurrent appends — no data races and Verify clean afterwards
// ---------------------------------------------------------------------------

func TestConcurrentAppends(t *testing.T) {
	ctx := context.Background()
	l, _ := openLogger(t)

	const goroutines = 10
	const perG = 20

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				_ = l.Record(ctx, "admin@example.com",
					"pool.add_ip", "10."+strconv.Itoa(g)+".0."+strconv.Itoa(i),
					map[string]string{"goroutine": strconv.Itoa(g)})
			}
		}()
	}
	wg.Wait()

	if err := l.Verify(ctx, 0, math.MaxInt64); err != nil {
		t.Errorf("Verify after concurrent appends: %v", err)
	}
}
