// storage_pg_test.go — Postgres integration tests for the storage package.
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN.
package storage_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/storage"
)

func openPGTestStore(t *testing.T) *storage.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	db, err := cpdb.Open("storage_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := storage.Open(db, nil)
	if err != nil {
		_ = db.Close()
		t.Fatalf("storage.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS storage_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_Storage_GetConfig_UnknownAccount(t *testing.T) {
	st := openPGTestStore(t)
	_, err := st.GetConfig(context.Background(), "pg-no-such-account")
	if err == nil || err.Error() != storage.ErrUnknownAccount.Error() {
		t.Fatalf("expected ErrUnknownAccount, got %v", err)
	}
}

func TestPG_Storage_PutGetConfig(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	cfg := storage.Config{
		AccountID: "pg-acct-1",
		BYO:       false,
		Region:    "eu-west-1",
		Bucket:    "pg-bucket-1",
	}
	if err := st.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	got, err := st.GetConfig(ctx, "pg-acct-1")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.Bucket != cfg.Bucket {
		t.Errorf("bucket: got %q want %q", got.Bucket, cfg.Bucket)
	}
	if got.Region != cfg.Region {
		t.Errorf("region: got %q want %q", got.Region, cfg.Region)
	}
}

func TestPG_Storage_PutConfig_Upsert(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	first := storage.Config{AccountID: "pg-upsert", Bucket: "bucket-v1", Region: "auto"}
	second := storage.Config{AccountID: "pg-upsert", Bucket: "bucket-v2", Region: "us-east-1"}

	if err := st.PutConfig(ctx, first); err != nil {
		t.Fatalf("PutConfig first: %v", err)
	}
	if err := st.PutConfig(ctx, second); err != nil {
		t.Fatalf("PutConfig second: %v", err)
	}
	got, err := st.GetConfig(ctx, "pg-upsert")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.Bucket != "bucket-v2" {
		t.Errorf("upsert bucket: got %q want bucket-v2", got.Bucket)
	}
}

func TestPG_Storage_DeleteConfig(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	cfg := storage.Config{AccountID: "pg-del", Bucket: "del-bucket", Region: "auto"}
	if err := st.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	if err := st.DeleteConfig(ctx, "pg-del"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	_, err := st.GetConfig(ctx, "pg-del")
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}

func TestPG_Storage_InsertLatestUsage(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	u := storage.UsageSample{
		AccountID:   "pg-usage-1",
		Bucket:      "bkt",
		SizeBytes:   2048,
		ObjectCount: 7,
		SampledAt:   now,
	}
	if err := st.InsertUsage(ctx, u); err != nil {
		t.Fatalf("InsertUsage: %v", err)
	}
	got, err := st.LatestUsage(ctx, "pg-usage-1")
	if err != nil {
		t.Fatalf("LatestUsage: %v", err)
	}
	if got.SizeBytes != 2048 {
		t.Errorf("size_bytes: got %d want 2048", got.SizeBytes)
	}
	if got.ObjectCount != 7 {
		t.Errorf("object_count: got %d want 7", got.ObjectCount)
	}
}
