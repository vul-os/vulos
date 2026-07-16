// storage_extra_test.go — additional tests to raise storage package coverage
// above 60%. Covers: MemStore.DeleteConfig, MemProvider.PresignPut/ListBucket/
// DeleteObject, SQLStore.DeleteConfig, KEKFromHex.
package storage_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/storage"
)

// ────────────────────────────────────────────────────────────────────────────
// MemStore — DeleteConfig
// ────────────────────────────────────────────────────────────────────────────

func TestMemStore_DeleteConfig(t *testing.T) {
	st := storage.NewMemStore()
	ctx := context.Background()

	cfg := storage.Config{AccountID: "acc-del", Bucket: "bkt-del"}
	if err := st.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	// Should exist.
	if _, err := st.GetConfig(ctx, "acc-del"); err != nil {
		t.Fatalf("GetConfig before delete: %v", err)
	}

	// Delete.
	if err := st.DeleteConfig(ctx, "acc-del"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}

	// Should be gone.
	_, err := st.GetConfig(ctx, "acc-del")
	if !errors.Is(err, storage.ErrUnknownAccount) {
		t.Errorf("expected ErrUnknownAccount after DeleteConfig, got %v", err)
	}
}

func TestMemStore_DeleteConfig_NonExistent(t *testing.T) {
	st := storage.NewMemStore()
	// Deleting a non-existent key should not error.
	if err := st.DeleteConfig(context.Background(), "ghost-account"); err != nil {
		t.Errorf("DeleteConfig non-existent: want nil, got %v", err)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// MemProvider — PresignPut / ListBucket / DeleteObject
// ────────────────────────────────────────────────────────────────────────────

func TestMemProvider_PresignPut(t *testing.T) {
	mp := storage.NewMemProvider()
	ctx := context.Background()

	url, err := mp.PresignPut(ctx, "mybucket", "some/key.tar.gz", 15*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if !strings.Contains(url, "op=put") {
		t.Errorf("PresignPut URL should contain 'op=put', got %q", url)
	}
	if !strings.Contains(url, "mybucket") {
		t.Errorf("PresignPut URL should contain bucket name, got %q", url)
	}
}

func TestMemProvider_ListBucket_AfterPut(t *testing.T) {
	mp := storage.NewMemProvider()
	ctx := context.Background()

	const bucket = "listbucket"
	if err := mp.EnsureBucket(ctx, bucket); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	data := []byte("hello world")
	if err := mp.PutObject(ctx, bucket, "file-a.txt", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if err := mp.PutObject(ctx, bucket, "file-b.txt", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if err := mp.PutObject(ctx, bucket, "other/c.txt", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("PutObject file-c: %v", err)
	}

	// List all.
	objs, err := mp.ListBucket(ctx, bucket, "", 100)
	if err != nil {
		t.Fatalf("ListBucket all: %v", err)
	}
	if len(objs) != 3 {
		t.Errorf("ListBucket all: want 3, got %d", len(objs))
	}

	// List with prefix filter.
	objs2, err := mp.ListBucket(ctx, bucket, "file-", 100)
	if err != nil {
		t.Fatalf("ListBucket prefix: %v", err)
	}
	if len(objs2) != 2 {
		t.Errorf("ListBucket prefix 'file-': want 2, got %d", len(objs2))
	}
}

func TestMemProvider_ListBucket_MaxLimit(t *testing.T) {
	mp := storage.NewMemProvider()
	ctx := context.Background()

	const bucket = "maxbucket"
	_ = mp.EnsureBucket(ctx, bucket)
	for i := 0; i < 5; i++ {
		_ = mp.PutObject(ctx, bucket, strings.Repeat("f", i+1), bytes.NewReader([]byte("x")), 1)
	}

	objs, err := mp.ListBucket(ctx, bucket, "", 2)
	if err != nil {
		t.Fatalf("ListBucket max=2: %v", err)
	}
	if len(objs) > 2 {
		t.Errorf("ListBucket max=2: want ≤2, got %d", len(objs))
	}
}

func TestMemProvider_ListBucket_UnknownBucket(t *testing.T) {
	mp := storage.NewMemProvider()
	_, err := mp.ListBucket(context.Background(), "no-such-bucket", "", 10)
	if !errors.Is(err, storage.ErrUnknownBucket) {
		t.Errorf("expected ErrUnknownBucket, got %v", err)
	}
}

func TestMemProvider_DeleteObject(t *testing.T) {
	mp := storage.NewMemProvider()
	ctx := context.Background()

	const bucket = "delbucket"
	_ = mp.EnsureBucket(ctx, bucket)
	_ = mp.PutObject(ctx, bucket, "to-delete.txt", bytes.NewReader([]byte("data")), 4)

	if err := mp.DeleteObject(ctx, bucket, "to-delete.txt"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}

	// Object should no longer appear in listing.
	objs, _ := mp.ListBucket(ctx, bucket, "", 100)
	for _, o := range objs {
		if o.Key == "to-delete.txt" {
			t.Error("deleted object still appears in listing")
		}
	}
}

func TestMemProvider_BucketSize(t *testing.T) {
	mp := storage.NewMemProvider()
	ctx := context.Background()

	const bucket = "sizebucket"
	_ = mp.EnsureBucket(ctx, bucket)

	data1 := bytes.Repeat([]byte("a"), 100)
	data2 := bytes.Repeat([]byte("b"), 200)
	_ = mp.PutObject(ctx, bucket, "f1.bin", bytes.NewReader(data1), int64(len(data1)))
	_ = mp.PutObject(ctx, bucket, "f2.bin", bytes.NewReader(data2), int64(len(data2)))

	size, count, err := mp.BucketSize(ctx, bucket)
	if err != nil {
		t.Fatalf("BucketSize: %v", err)
	}
	if size != 300 {
		t.Errorf("BucketSize bytes: want 300, got %d", size)
	}
	if count != 2 {
		t.Errorf("BucketSize count: want 2, got %d", count)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// SQLStore — DeleteConfig
// ────────────────────────────────────────────────────────────────────────────

func openSQLStore(t *testing.T) storage.Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := storage.Open(db, nil)
	if err != nil {
		_ = db.Close()
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSQLStore_DeleteConfig(t *testing.T) {
	st := openSQLStore(t)
	ctx := context.Background()

	cfg := storage.Config{AccountID: "sql-del", Bucket: "bkt-sql-del"}
	if err := st.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	if err := st.DeleteConfig(ctx, "sql-del"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}

	_, err := st.GetConfig(ctx, "sql-del")
	if !errors.Is(err, storage.ErrUnknownAccount) {
		t.Errorf("expected ErrUnknownAccount after DeleteConfig, got %v", err)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// KEKFromHex
// ────────────────────────────────────────────────────────────────────────────

func TestKEKFromHex_Empty(t *testing.T) {
	kek, err := storage.KEKFromHex("")
	if err != nil {
		t.Fatalf("KEKFromHex empty: %v", err)
	}
	if kek != nil {
		t.Errorf("KEKFromHex empty: want nil, got %v", kek)
	}
}

func TestKEKFromHex_Valid(t *testing.T) {
	// 32 bytes = 64 hex chars
	hex64 := strings.Repeat("ab", 32)
	kek, err := storage.KEKFromHex(hex64)
	if err != nil {
		t.Fatalf("KEKFromHex valid: %v", err)
	}
	if len(kek) != 32 {
		t.Errorf("KEKFromHex valid: want 32 bytes, got %d", len(kek))
	}
}

func TestKEKFromHex_InvalidHex(t *testing.T) {
	_, err := storage.KEKFromHex("not-hex-at-all!")
	if err == nil {
		t.Error("KEKFromHex invalid hex: expected error")
	}
}

func TestKEKFromHex_WrongLength(t *testing.T) {
	// Only 16 bytes (32 hex chars) — too short.
	hex32 := strings.Repeat("ab", 16)
	_, err := storage.KEKFromHex(hex32)
	if err == nil {
		t.Error("KEKFromHex wrong length: expected error")
	}
}
