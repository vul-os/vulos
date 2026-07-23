package storagesel_test

import (
	"context"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/storagesel"
)

func openTest(t *testing.T) *storagesel.Selector {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	sel, err := storagesel.Open(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("storagesel.Open: %v", err)
	}
	t.Cleanup(func() { _ = sel.Close() })
	return sel
}

// TestGetDefaultManaged: accounts with no row return the the managed store default.
func TestGetDefaultManaged(t *testing.T) {
	sel := openTest(t)
	b, err := sel.Get(context.Background(), "acct-new")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if b.Kind != storagesel.KindManaged {
		t.Fatalf("default kind = %q, want %q", b.Kind, storagesel.KindManaged)
	}
	if b.AccountID != "acct-new" {
		t.Fatalf("account_id = %q, want %q", b.AccountID, "acct-new")
	}
}

// TestSetGetRoundtripManaged: Set+Get round-trip for the managed store.
func TestSetGetRoundtripManaged(t *testing.T) {
	sel := openTest(t)
	want := storagesel.Backend{
		Kind:    storagesel.KindManaged,
		Bucket:  "my-managed-bucket",
		Region:  "us-east-1",
		CredRef: "env:S3_ACCESS_KEY_ID",
	}
	if err := sel.Set(context.Background(), "acct-t1", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := sel.Get(context.Background(), "acct-t1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Kind != want.Kind {
		t.Errorf("kind: got %q want %q", got.Kind, want.Kind)
	}
	if got.Bucket != want.Bucket {
		t.Errorf("bucket: got %q want %q", got.Bucket, want.Bucket)
	}
	if got.Region != want.Region {
		t.Errorf("region: got %q want %q", got.Region, want.Region)
	}
	if got.CredRef != want.CredRef {
		t.Errorf("cred_ref: got %q want %q", got.CredRef, want.CredRef)
	}
}

// TestSetGetRoundtripMinIO: Set+Get round-trip for MinIO with valid config.
func TestSetGetRoundtripMinIO(t *testing.T) {
	sel := openTest(t)
	want := storagesel.Backend{
		Kind:     storagesel.KindMinIO,
		Endpoint: "https://minio.example.com",
		Region:   "us-west-2",
		Bucket:   "my-byo-bucket",
		CredRef:  "env:MINIO_ACCESS_KEY",
	}
	if err := sel.Set(context.Background(), "acct-m1", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := sel.Get(context.Background(), "acct-m1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Kind != storagesel.KindMinIO {
		t.Errorf("kind: got %q want minio", got.Kind)
	}
	if got.Endpoint != want.Endpoint {
		t.Errorf("endpoint: got %q want %q", got.Endpoint, want.Endpoint)
	}
	if got.Bucket != want.Bucket {
		t.Errorf("bucket: got %q want %q", got.Bucket, want.Bucket)
	}
}

// TestMinIORejectHTTP: MinIO endpoints must use https.
func TestMinIORejectHTTP(t *testing.T) {
	sel := openTest(t)
	err := sel.Set(context.Background(), "acct-x", storagesel.Backend{
		Kind:     storagesel.KindMinIO,
		Endpoint: "http://minio.example.com", // must be rejected
		Bucket:   "bucket",
	})
	if err == nil {
		t.Fatal("expected error for http MinIO endpoint, got nil")
	}
}

// TestMinIORejectEmptyBucket: MinIO requires a non-empty bucket.
func TestMinIORejectEmptyBucket(t *testing.T) {
	sel := openTest(t)
	err := sel.Set(context.Background(), "acct-y", storagesel.Backend{
		Kind:     storagesel.KindMinIO,
		Endpoint: "https://minio.example.com",
		Bucket:   "", // must be rejected
	})
	if err == nil {
		t.Fatal("expected error for empty bucket, got nil")
	}
}

// TestCrossAccountIsolation: setting backend for one account does not affect another.
func TestCrossAccountIsolation(t *testing.T) {
	sel := openTest(t)
	bA := storagesel.Backend{
		Kind:     storagesel.KindMinIO,
		Endpoint: "https://minio-a.example.com",
		Bucket:   "bucket-a",
	}
	bB := storagesel.Backend{
		Kind:     storagesel.KindMinIO,
		Endpoint: "https://minio-b.example.com",
		Bucket:   "bucket-b",
	}
	if err := sel.Set(context.Background(), "acct-a", bA); err != nil {
		t.Fatalf("Set A: %v", err)
	}
	if err := sel.Set(context.Background(), "acct-b", bB); err != nil {
		t.Fatalf("Set B: %v", err)
	}

	gotA, err := sel.Get(context.Background(), "acct-a")
	if err != nil {
		t.Fatalf("Get A: %v", err)
	}
	gotB, err := sel.Get(context.Background(), "acct-b")
	if err != nil {
		t.Fatalf("Get B: %v", err)
	}

	if gotA.Bucket != "bucket-a" {
		t.Errorf("acct-a bucket = %q, want bucket-a", gotA.Bucket)
	}
	if gotB.Bucket != "bucket-b" {
		t.Errorf("acct-b bucket = %q, want bucket-b", gotB.Bucket)
	}
	if gotA.Endpoint == gotB.Endpoint {
		t.Errorf("accounts share endpoint, want isolation")
	}
}

// TestUpdateIsUpsert: Set can overwrite a prior entry (upsert).
func TestUpdateIsUpsert(t *testing.T) {
	sel := openTest(t)
	first := storagesel.Backend{
		Kind:     storagesel.KindMinIO,
		Endpoint: "https://minio.old.example.com",
		Bucket:   "old-bucket",
	}
	second := storagesel.Backend{
		Kind:     storagesel.KindMinIO,
		Endpoint: "https://minio.new.example.com",
		Bucket:   "new-bucket",
	}
	if err := sel.Set(context.Background(), "acct-upd", first); err != nil {
		t.Fatalf("Set first: %v", err)
	}
	if err := sel.Set(context.Background(), "acct-upd", second); err != nil {
		t.Fatalf("Set second: %v", err)
	}
	got, err := sel.Get(context.Background(), "acct-upd")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Bucket != "new-bucket" {
		t.Errorf("bucket after update = %q, want new-bucket", got.Bucket)
	}
	if got.Endpoint != "https://minio.new.example.com" {
		t.Errorf("endpoint after update = %q", got.Endpoint)
	}
}

// TestDeleteResetsToDefault: after Delete, Get returns the managed store default.
func TestDeleteResetsToDefault(t *testing.T) {
	sel := openTest(t)
	if err := sel.Set(context.Background(), "acct-del", storagesel.Backend{
		Kind:     storagesel.KindMinIO,
		Endpoint: "https://minio.example.com",
		Bucket:   "some-bucket",
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := sel.Delete(context.Background(), "acct-del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	b, err := sel.Get(context.Background(), "acct-del")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if b.Kind != storagesel.KindManaged {
		t.Errorf("after delete kind = %q, want managed", b.Kind)
	}
}

// TestInvalidKind: unknown kind is rejected.
func TestInvalidKind(t *testing.T) {
	sel := openTest(t)
	err := sel.Set(context.Background(), "acct-z", storagesel.Backend{
		Kind: "s3",
	})
	if err == nil {
		t.Fatal("expected error for invalid kind, got nil")
	}
}

// TestDefaultRegionAuto: empty region is normalised to "auto".
func TestDefaultRegionAuto(t *testing.T) {
	sel := openTest(t)
	if err := sel.Set(context.Background(), "acct-reg", storagesel.Backend{
		Kind:     storagesel.KindMinIO,
		Endpoint: "https://minio.example.com",
		Bucket:   "b",
		Region:   "", // should default to "auto"
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := sel.Get(context.Background(), "acct-reg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Region != "auto" {
		t.Errorf("region = %q, want auto", got.Region)
	}
}
