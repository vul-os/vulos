// Package audit_test exercises the audit log: Record, hash-chain, Query,
// and Flusher.FlushNow with a MemSink.
package audit_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/audit"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ---------------------------------------------------------------------------
// MemSink — in-memory BucketSink for tests
// ---------------------------------------------------------------------------

type memSink struct {
	objects map[string]string // key -> body
}

func newMemSink() *memSink { return &memSink{objects: map[string]string{}} }

func (m *memSink) EnsureBucket(_ context.Context, _ string) error { return nil }

func (m *memSink) PutObject(_ context.Context, _, key string, r io.Reader, _ int64) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.objects[key] = string(b)
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func openTestLog(t *testing.T, sink audit.BucketSink) *audit.Log {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	l, err := audit.Open(db, sink, "test-audit-bucket")
	if err != nil {
		db.Close()
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// hashOf computes the SHA-256 of an Entry with Hash="" and Seq=0 to replicate
// the library's own hash computation (Seq is assigned after hashing, so it
// must be excluded from the hash input).
func hashOf(t *testing.T, e audit.Entry) string {
	t.Helper()
	e.Hash = ""
	e.Seq = 0 // seq is set after hash computation; must be excluded
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRecord_BasicFields(t *testing.T) {
	l := openTestLog(t, nil)
	ctx := context.Background()

	e, err := l.Record(ctx, audit.KindLogin, "acc-1", "user@example.com",
		map[string]string{"ip": "1.2.3.4"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	if e.ID == "" {
		t.Error("entry ID is empty")
	}
	if e.Kind != audit.KindLogin {
		t.Errorf("kind: want %q, got %q", audit.KindLogin, e.Kind)
	}
	if e.AccountID != "acc-1" {
		t.Errorf("account_id: want acc-1, got %q", e.AccountID)
	}
	if e.Actor != "user@example.com" {
		t.Errorf("actor: want user@example.com, got %q", e.Actor)
	}
	if e.Detail["ip"] != "1.2.3.4" {
		t.Errorf("detail.ip: want 1.2.3.4, got %q", e.Detail["ip"])
	}
	if e.Seq <= 0 {
		t.Errorf("seq should be > 0, got %d", e.Seq)
	}
	if e.Hash == "" {
		t.Error("hash is empty")
	}
	// PrevHash for first entry should be all zeros (64 hex chars).
	if e.PrevHash != strings.Repeat("0", 64) {
		t.Errorf("first entry PrevHash should be zero-hash, got %q", e.PrevHash)
	}
}

func TestRecord_HashChain(t *testing.T) {
	l := openTestLog(t, nil)
	ctx := context.Background()

	e1, err := l.Record(ctx, audit.KindLogin, "acc-1", "user@example.com", nil)
	if err != nil {
		t.Fatalf("Record e1: %v", err)
	}
	e2, err := l.Record(ctx, audit.KindBucketWrite, "acc-1", "user@example.com",
		map[string]string{"bucket": "my-bucket"})
	if err != nil {
		t.Fatalf("Record e2: %v", err)
	}
	e3, err := l.Record(ctx, audit.KindLogout, "acc-1", "user@example.com", nil)
	if err != nil {
		t.Fatalf("Record e3: %v", err)
	}

	// Verify e1 PrevHash is zeros.
	if e1.PrevHash != strings.Repeat("0", 64) {
		t.Errorf("e1 PrevHash not zero-hash: %q", e1.PrevHash)
	}
	// Verify chain links: e2.PrevHash == e1.Hash, e3.PrevHash == e2.Hash.
	if e2.PrevHash != e1.Hash {
		t.Errorf("e2.PrevHash %q != e1.Hash %q", e2.PrevHash, e1.Hash)
	}
	if e3.PrevHash != e2.Hash {
		t.Errorf("e3.PrevHash %q != e2.Hash %q", e3.PrevHash, e2.Hash)
	}
	// Verify each hash is deterministic (recompute and compare).
	if got := hashOf(t, *e1); got != e1.Hash {
		t.Errorf("e1 hash mismatch: computed %q, stored %q", got, e1.Hash)
	}
	if got := hashOf(t, *e2); got != e2.Hash {
		t.Errorf("e2 hash mismatch: computed %q, stored %q", got, e2.Hash)
	}
}

func TestRecord_DifferentKinds(t *testing.T) {
	l := openTestLog(t, nil)
	ctx := context.Background()

	kinds := []audit.Kind{
		audit.KindLogin,
		audit.KindLoginFailed,
		audit.KindLogout,
		audit.KindAdminAction,
		audit.KindBucketWrite,
		audit.KindBucketDelete,
		audit.KindSessionRevoke,
	}
	for _, k := range kinds {
		if _, err := l.Record(ctx, k, "acc-1", "actor", nil); err != nil {
			t.Errorf("Record kind %q: %v", k, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Query
// ---------------------------------------------------------------------------

func TestQuery_All(t *testing.T) {
	l := openTestLog(t, nil)
	ctx := context.Background()

	for i := range 5 {
		_, err := l.Record(ctx, audit.KindLogin, "acc-1", "user@example.com",
			map[string]string{"n": string(rune('a' + i))})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	entries, err := l.Query(ctx, audit.QueryOptions{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("want 5 entries, got %d", len(entries))
	}
	// Verify ascending seq order.
	for i := 1; i < len(entries); i++ {
		if entries[i].Seq <= entries[i-1].Seq {
			t.Errorf("entries not in ascending seq order at index %d", i)
		}
	}
}

func TestQuery_FilterByAccountID(t *testing.T) {
	l := openTestLog(t, nil)
	ctx := context.Background()

	_, _ = l.Record(ctx, audit.KindLogin, "acc-1", "a@example.com", nil)
	_, _ = l.Record(ctx, audit.KindLogin, "acc-2", "b@example.com", nil)
	_, _ = l.Record(ctx, audit.KindLogin, "acc-1", "a@example.com", nil)

	entries, err := l.Query(ctx, audit.QueryOptions{AccountID: "acc-1"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("want 2 entries for acc-1, got %d", len(entries))
	}
	for _, e := range entries {
		if e.AccountID != "acc-1" {
			t.Errorf("unexpected account_id %q", e.AccountID)
		}
	}
}

func TestQuery_FilterByKind(t *testing.T) {
	l := openTestLog(t, nil)
	ctx := context.Background()

	_, _ = l.Record(ctx, audit.KindLogin, "acc-1", "a@example.com", nil)
	_, _ = l.Record(ctx, audit.KindBucketWrite, "acc-1", "a@example.com", nil)
	_, _ = l.Record(ctx, audit.KindLogin, "acc-1", "a@example.com", nil)

	entries, err := l.Query(ctx, audit.QueryOptions{Kind: audit.KindLogin})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("want 2 login entries, got %d", len(entries))
	}
}

func TestQuery_FilterBySince(t *testing.T) {
	l := openTestLog(t, nil)
	ctx := context.Background()

	_, _ = l.Record(ctx, audit.KindLogin, "acc-1", "a", nil)
	pivot := time.Now().UTC()
	time.Sleep(2 * time.Millisecond) // ensure ts > pivot
	_, _ = l.Record(ctx, audit.KindLogin, "acc-1", "b", nil)
	_, _ = l.Record(ctx, audit.KindLogin, "acc-1", "c", nil)

	entries, err := l.Query(ctx, audit.QueryOptions{Since: pivot})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("want 2 entries since pivot, got %d", len(entries))
	}
}

func TestQuery_Limit(t *testing.T) {
	l := openTestLog(t, nil)
	ctx := context.Background()

	for range 20 {
		_, _ = l.Record(ctx, audit.KindLogin, "acc-1", "a", nil)
	}

	entries, err := l.Query(ctx, audit.QueryOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("want 5 entries with limit=5, got %d", len(entries))
	}
}

// ---------------------------------------------------------------------------
// Flusher
// ---------------------------------------------------------------------------

func TestFlusher_FlushNow(t *testing.T) {
	sink := newMemSink()
	l := openTestLog(t, sink)
	ctx := context.Background()

	_, _ = l.Record(ctx, audit.KindLogin, "acc-1", "user@example.com", nil)
	_, _ = l.Record(ctx, audit.KindBucketWrite, "acc-1", "user@example.com",
		map[string]string{"bucket": "b"})

	f := audit.NewFlusher(l, 0)
	if err := f.FlushNow(ctx); err != nil {
		t.Fatalf("FlushNow: %v", err)
	}

	if len(sink.objects) == 0 {
		t.Fatal("expected objects in sink after flush, got none")
	}

	// Each object must contain valid NDJSON lines with the expected fields.
	for key, body := range sink.objects {
		if !strings.HasPrefix(key, "audit/") {
			t.Errorf("object key %q does not start with audit/", key)
		}
		lines := strings.Split(strings.TrimSpace(body), "\n")
		if len(lines) == 0 {
			t.Errorf("object %q has no lines", key)
		}
		for _, line := range lines {
			var e audit.Entry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Errorf("object %q line is not valid JSON: %v — %q", key, err, line)
			}
			if e.ID == "" {
				t.Errorf("object %q line has empty ID", key)
			}
			if e.Hash == "" {
				t.Errorf("object %q line has empty Hash", key)
			}
		}
	}
}

func TestFlusher_FlushNow_NoSink(t *testing.T) {
	// With nil sink, FlushNow must be a no-op (dev mode).
	l := openTestLog(t, nil)
	ctx := context.Background()

	_, _ = l.Record(ctx, audit.KindLogin, "acc-1", "user", nil)

	f := audit.NewFlusher(l, 0)
	if err := f.FlushNow(ctx); err != nil {
		t.Fatalf("FlushNow with nil sink: %v", err)
	}
}

func TestFlusher_IdempotentSecondFlush(t *testing.T) {
	sink := newMemSink()
	l := openTestLog(t, sink)
	ctx := context.Background()

	_, _ = l.Record(ctx, audit.KindLogin, "acc-1", "user", nil)

	f := audit.NewFlusher(l, 0)
	_ = f.FlushNow(ctx)
	firstCount := len(sink.objects)

	// A second flush with no new entries must produce no additional objects.
	_ = f.FlushNow(ctx)
	if len(sink.objects) != firstCount {
		t.Errorf("second flush wrote new objects: before=%d after=%d", firstCount, len(sink.objects))
	}
}

func TestFlusher_RunStop(t *testing.T) {
	sink := newMemSink()
	l := openTestLog(t, sink)
	ctx := context.Background()

	_, _ = l.Record(ctx, audit.KindLogin, "acc-1", "user", nil)

	f := audit.NewFlusher(l, 100*time.Millisecond)
	f.Run()
	// Wait at least one tick.
	time.Sleep(250 * time.Millisecond)
	f.Stop() // must not hang

	if len(sink.objects) == 0 {
		t.Error("expected flushed objects after Run/Stop, got none")
	}
}

// ---------------------------------------------------------------------------
// SQL-injection safety: Query with attacker-controlled filter values
// ---------------------------------------------------------------------------

// TestQuery_SQLInjectionSafety verifies that attacker-controlled strings
// supplied in QueryOptions fields are passed as SQL parameters (not
// interpolated into the query text).
//
// The attack string "acc-1'; DROP TABLE audit_entries; --" would destroy the
// audit log if it were interpolated.  When properly parameterised it must
// instead simply return zero results (no matching account_id), and the table
// must still be intact for subsequent operations.
func TestQuery_SQLInjectionSafety(t *testing.T) {
	l := openTestLog(t, nil)
	ctx := context.Background()

	// Insert a legitimate entry so we can verify the table survives.
	if _, err := l.Record(ctx, audit.KindLogin, "safe-account", "user@example.com", nil); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Attacker-controlled account_id with SQL injection payload.
	attackAccountID := "safe-account'; DROP TABLE audit_entries; --"

	// Query must not panic, must not error, and must return zero rows
	// (no account_id matches the literal attack string).
	entries, err := l.Query(ctx, audit.QueryOptions{AccountID: attackAccountID})
	if err != nil {
		t.Fatalf("Query with injection payload returned error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for non-existent account_id, got %d", len(entries))
	}

	// Verify the table is intact: the legitimate entry must still be queryable.
	legit, err := l.Query(ctx, audit.QueryOptions{AccountID: "safe-account"})
	if err != nil {
		t.Fatalf("Query for safe-account after injection attempt: %v", err)
	}
	if len(legit) != 1 {
		t.Errorf("expected 1 legitimate entry after injection attempt, got %d (table may have been dropped)", len(legit))
	}

	// Repeat with an injected Kind value.
	attackKind := audit.Kind("auth.login'; DROP TABLE audit_entries; --")
	entries2, err := l.Query(ctx, audit.QueryOptions{Kind: attackKind})
	if err != nil {
		t.Fatalf("Query with injected Kind returned error: %v", err)
	}
	if len(entries2) != 0 {
		t.Errorf("expected 0 entries for injected kind, got %d", len(entries2))
	}

	// Table must still be intact after kind injection attempt.
	all, err := l.Query(ctx, audit.QueryOptions{})
	if err != nil {
		t.Fatalf("Query all after kind injection attempt: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 total entry after injection attempts, got %d", len(all))
	}
}
