// syncrz_pg_test.go — Postgres integration tests for the syncrz store.
//
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/syncrz/... -run TestPG_
package syncrz_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/syncrz"
)

func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	return dsn
}

func openPGTestStore(t *testing.T) *syncrz.SQLStore {
	t.Helper()
	t.Setenv("DATABASE_URL", pgDSN(t))
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("syncrz_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := syncrz.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("syncrz.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS syncrz_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func shaStr(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestPG_AppendDeltaIdempotent(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	payload := []byte("pg-delta-bytes")
	hash := shaStr(payload)

	id1, inserted1, err := st.AppendDelta(ctx, "acct-pg1", "mailbox/1", "box-1",
		1, int64(len(payload)), hash, "obj/key1", nil, now)
	if err != nil {
		t.Fatalf("AppendDelta 1: %v", err)
	}
	if !inserted1 {
		t.Error("expected inserted=true on first append")
	}
	if id1 == 0 {
		t.Error("expected non-zero id")
	}

	// Re-push same payload — must return same id, inserted=false.
	id2, inserted2, err := st.AppendDelta(ctx, "acct-pg1", "mailbox/1", "box-1",
		1, int64(len(payload)), hash, "obj/key1", nil, now)
	if err != nil {
		t.Fatalf("AppendDelta 2: %v", err)
	}
	if inserted2 {
		t.Error("expected inserted=false on dedup")
	}
	if id2 != id1 {
		t.Errorf("expected same id on dedup: got %d vs %d", id2, id1)
	}
}

func TestPG_PullDeltas(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	d1 := []byte("from-box-a")
	d2 := []byte("from-box-b")

	_, _, err := st.AppendDelta(ctx, "acct-pg2", "key/x", "box-a", 1, int64(len(d1)), shaStr(d1), "t/1", nil, now)
	if err != nil {
		t.Fatalf("append box-a: %v", err)
	}
	_, _, err = st.AppendDelta(ctx, "acct-pg2", "key/x", "box-b", 1, int64(len(d2)), shaStr(d2), "t/2", nil, now)
	if err != nil {
		t.Fatalf("append box-b: %v", err)
	}

	// box-a pulls — should only see box-b's delta.
	refs, err := st.PullDeltas(ctx, "acct-pg2", "key/x", "box-a", 0, 10)
	if err != nil {
		t.Fatalf("PullDeltas: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 delta, got %d", len(refs))
	}
	if refs[0].BoxID != "box-b" {
		t.Errorf("expected box-b, got %q", refs[0].BoxID)
	}
}

func TestPG_UpsertCursorAndLastSeen(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := st.UpsertCursor(ctx, "acct-pg3", "key/y", "box-1", 42, now); err != nil {
		t.Fatalf("UpsertCursor: %v", err)
	}

	n, err := st.LastSeen(ctx, "acct-pg3", "key/y", "box-1")
	if err != nil {
		t.Fatalf("LastSeen: %v", err)
	}
	if n != 42 {
		t.Errorf("expected 42, got %d", n)
	}

	// Upsert with a smaller id — should not regress.
	if err := st.UpsertCursor(ctx, "acct-pg3", "key/y", "box-1", 10, now); err != nil {
		t.Fatalf("UpsertCursor (lower): %v", err)
	}
	n, err = st.LastSeen(ctx, "acct-pg3", "key/y", "box-1")
	if err != nil {
		t.Fatalf("LastSeen after lower upsert: %v", err)
	}
	if n != 42 {
		t.Errorf("cursor should not regress: expected 42, got %d", n)
	}
}

func TestPG_AppendBlobIdempotent(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	blob := []byte("pg-blob")
	hash := shaStr(blob)

	ok1, err := st.AppendBlob(ctx, "acct-pg4", hash, int64(len(blob)), "obj/b1", now)
	if err != nil {
		t.Fatalf("AppendBlob 1: %v", err)
	}
	if !ok1 {
		t.Error("expected true on first append")
	}

	ok2, err := st.AppendBlob(ctx, "acct-pg4", hash, int64(len(blob)), "obj/b1", now)
	if err != nil {
		t.Fatalf("AppendBlob 2: %v", err)
	}
	if ok2 {
		t.Error("expected false on dedup")
	}
}

func TestPG_GetBlobKey(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	blob := []byte("pg-blob-key")
	hash := shaStr(blob)

	if _, err := st.AppendBlob(ctx, "acct-pg5", hash, int64(len(blob)), "obj/bkey", now); err != nil {
		t.Fatalf("AppendBlob: %v", err)
	}

	key, err := st.GetBlobKey(ctx, "acct-pg5", hash)
	if err != nil {
		t.Fatalf("GetBlobKey: %v", err)
	}
	if key != "obj/bkey" {
		t.Errorf("expected obj/bkey, got %q", key)
	}
}

func TestPG_CrossAccountIsolation(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	d := []byte("isolated")
	hash := shaStr(d)

	_, _, err := st.AppendDelta(ctx, "acct-A", "k", "box", 1, int64(len(d)), hash, "t/1", nil, now)
	if err != nil {
		t.Fatalf("acct-A append: %v", err)
	}

	refs, err := st.PullDeltas(ctx, "acct-B", "k", "peer", 0, 10)
	if err != nil {
		t.Fatalf("acct-B pull: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("expected 0 refs for acct-B, got %d (cross-account leak)", len(refs))
	}
}
