// storagesel_pg_test.go — Postgres integration tests for storagesel.
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN.
package storagesel_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/storagesel"
)

func openPGSelector(t *testing.T) *storagesel.Selector {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	db, err := cpdb.Open("storagesel_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	sel, err := storagesel.Open(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("storagesel.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS storagesel_pgtest CASCADE`)
		_ = sel.Close()
	})
	return sel
}

func TestPG_Storagesel_DefaultTigris(t *testing.T) {
	sel := openPGSelector(t)
	b, err := sel.Get(context.Background(), "pg-acct-new")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if b.Kind != storagesel.KindTigris {
		t.Errorf("default kind = %q, want tigris", b.Kind)
	}
}

func TestPG_Storagesel_SetGetMinIO(t *testing.T) {
	sel := openPGSelector(t)
	ctx := context.Background()

	want := storagesel.Backend{
		Kind:     storagesel.KindMinIO,
		Endpoint: "https://minio.pg-test.example.com",
		Bucket:   "pg-test-bucket",
		Region:   "us-west-2",
		CredRef:  "env:MINIO_KEY",
	}
	if err := sel.Set(ctx, "pg-acct-minio", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := sel.Get(ctx, "pg-acct-minio")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Kind != storagesel.KindMinIO {
		t.Errorf("kind: got %q want minio", got.Kind)
	}
	if got.Bucket != want.Bucket {
		t.Errorf("bucket: got %q want %q", got.Bucket, want.Bucket)
	}
}

func TestPG_Storagesel_Upsert(t *testing.T) {
	sel := openPGSelector(t)
	ctx := context.Background()

	first := storagesel.Backend{Kind: storagesel.KindMinIO, Endpoint: "https://minio.old.example", Bucket: "old-bkt"}
	second := storagesel.Backend{Kind: storagesel.KindMinIO, Endpoint: "https://minio.new.example", Bucket: "new-bkt"}

	if err := sel.Set(ctx, "pg-upsert", first); err != nil {
		t.Fatalf("Set first: %v", err)
	}
	if err := sel.Set(ctx, "pg-upsert", second); err != nil {
		t.Fatalf("Set second: %v", err)
	}
	got, err := sel.Get(ctx, "pg-upsert")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Bucket != "new-bkt" {
		t.Errorf("upsert bucket: got %q want new-bkt", got.Bucket)
	}
}

func TestPG_Storagesel_Delete(t *testing.T) {
	sel := openPGSelector(t)
	ctx := context.Background()

	if err := sel.Set(ctx, "pg-del", storagesel.Backend{
		Kind:     storagesel.KindMinIO,
		Endpoint: "https://minio.example.com",
		Bucket:   "del-bkt",
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := sel.Delete(ctx, "pg-del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	b, err := sel.Get(ctx, "pg-del")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if b.Kind != storagesel.KindTigris {
		t.Errorf("after delete kind = %q, want tigris (default)", b.Kind)
	}
}

func TestPG_Storagesel_SyncMode(t *testing.T) {
	sel := openPGSelector(t)
	ctx := context.Background()

	if err := sel.SetSyncMode(ctx, "pg-sync", storagesel.SyncModeLocal); err != nil {
		t.Fatalf("SetSyncMode: %v", err)
	}
	mode, err := sel.GetSyncMode(ctx, "pg-sync")
	if err != nil {
		t.Fatalf("GetSyncMode: %v", err)
	}
	if mode != storagesel.SyncModeLocal {
		t.Errorf("sync mode: got %q want local", mode)
	}
}
