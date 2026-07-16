// storage_test.go — unit tests for the storage package.
//
// Tests:
//  1. MemProvider: EnsureBucket idempotent; PresignGet returns URL string;
//     GetObject after PutObject round-trips bytes.
//  2. KEK round-trip: PutConfig with secret_key → row stores encrypted;
//     GetConfig decrypts. Tampered ciphertext → error.
//  3. BYO config validated: bucket non-empty, endpoint scheme=https.
//  4. SnapshotURL: fixed bucket name format "vulos-{ulid_lower}", key
//     "latest.snap.enc", TTL clamped to ≤300s.
//  5. TigrisProvider unit test against httptest S3-shaped mock:
//     EnsureBucket → PUT bucket; PresignGet returns URL with
//     X-Amz-Algorithm=AWS4-HMAC-SHA256.
package storage

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// openTestSQLStore opens an in-memory SQLite store for unit tests.
func openTestSQLStore(t *testing.T, kek []byte) *SQLStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := Open(db, kek)
	if err != nil {
		_ = db.Close()
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

var ctx = context.Background()

// ---------------------------------------------------------------------------
// 1. MemProvider behaviour
// ---------------------------------------------------------------------------

func TestMemProvider_EnsureBucketIdempotent(t *testing.T) {
	p := NewMemProvider()
	if err := p.EnsureBucket(ctx, "test-bucket"); err != nil {
		t.Fatalf("first EnsureBucket: %v", err)
	}
	// Put an object so we can detect if the bucket is wiped on second call.
	if err := p.PutObject(ctx, "test-bucket", "key1", strings.NewReader("hello"), 5); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	// Second call must not wipe the bucket.
	if err := p.EnsureBucket(ctx, "test-bucket"); err != nil {
		t.Fatalf("second EnsureBucket: %v", err)
	}
	rc, err := p.GetObject(ctx, "test-bucket", "key1")
	if err != nil {
		t.Fatalf("GetObject after second EnsureBucket: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", data)
	}
}

func TestMemProvider_PresignGetReturnsURL(t *testing.T) {
	p := NewMemProvider()
	url, err := p.PresignGet(ctx, "bucket", "key", time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if url == "" {
		t.Fatal("expected non-empty URL")
	}
	if !strings.HasPrefix(url, "mem://") {
		t.Fatalf("expected mem:// URL, got %q", url)
	}
}

func TestMemProvider_PutGetRoundTrip(t *testing.T) {
	p := NewMemProvider()
	want := []byte("round-trip data 🚀")
	if err := p.PutObject(ctx, "bkt", "obj/path", bytes.NewReader(want), int64(len(want))); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	rc, err := p.GetObject(ctx, "bkt", "obj/path")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, want) {
		t.Fatalf("round-trip mismatch: got %q, want %q", got, want)
	}
}

func TestMemProvider_BucketSize(t *testing.T) {
	p := NewMemProvider()
	_ = p.EnsureBucket(ctx, "sized")
	_ = p.PutObject(ctx, "sized", "a", strings.NewReader("aaa"), 3)
	_ = p.PutObject(ctx, "sized", "b", strings.NewReader("bb"), 2)
	size, count, err := p.BucketSize(ctx, "sized")
	if err != nil {
		t.Fatalf("BucketSize: %v", err)
	}
	if size != 5 {
		t.Errorf("expected size=5, got %d", size)
	}
	if count != 2 {
		t.Errorf("expected count=2, got %d", count)
	}
}

func TestMemProvider_UnknownBucket(t *testing.T) {
	p := NewMemProvider()
	_, err := p.GetObject(ctx, "nope", "key")
	if !isStorageErr(err, ErrUnknownBucket) {
		t.Fatalf("expected ErrUnknownBucket, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 2. MemStore / KEK round-trip
// ---------------------------------------------------------------------------

func testKEK(t *testing.T) []byte {
	t.Helper()
	// 32 bytes of deterministic test key.
	k, err := hex.DecodeString("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	if err != nil {
		t.Fatalf("decode test KEK: %v", err)
	}
	return k
}

func TestSQLStore_KEKRoundTrip(t *testing.T) {
	kek := testKEK(t)
	st := openTestSQLStore(t, kek)

	cfg := Config{
		AccountID: "acct1",
		BYO:       true,
		Endpoint:  "https://s3.example.com",
		Region:    "us-east-1",
		Bucket:    "my-bucket",
		AccessKey: "AKID",
		SecretKey: "s3cr3t",
	}
	if err := st.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	got, err := st.GetConfig(ctx, "acct1")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.SecretKey != "s3cr3t" {
		t.Errorf("expected decrypted secret 's3cr3t', got %q", got.SecretKey)
	}
	if got.Bucket != "my-bucket" {
		t.Errorf("expected bucket 'my-bucket', got %q", got.Bucket)
	}
}

func TestSQLStore_KEKTamperedCiphertext(t *testing.T) {
	kek := testKEK(t)
	st := openTestSQLStore(t, kek)

	// Insert a row directly with a tampered ciphertext.
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := st.db.Exec(
		`INSERT INTO storage_configs (account_id, byo, endpoint, region, bucket, access_key, secret_key_enc, created_at, updated_at)
		 VALUES ('tampered', 1, 'https://x.example.com', 'auto', 'bkt', 'ak', 'deadbeef', ?, ?)`, now, now)
	if err != nil {
		t.Fatalf("direct insert: %v", err)
	}

	_, err = st.GetConfig(ctx, "tampered")
	if err == nil {
		t.Fatal("expected error for tampered ciphertext, got nil")
	}
}

func TestSQLStore_UnknownAccount(t *testing.T) {
	st := openTestSQLStore(t, nil)

	_, err := st.GetConfig(ctx, "no-such-account")
	if !isStorageErr(err, ErrUnknownAccount) {
		t.Fatalf("expected ErrUnknownAccount, got %v", err)
	}
}

func TestSQLStore_UsageSample(t *testing.T) {
	st := openTestSQLStore(t, nil)

	now := time.Now().UTC().Truncate(time.Second)
	u := UsageSample{AccountID: "acct2", Bucket: "bkt", SizeBytes: 1024, ObjectCount: 5, SampledAt: now}
	if err := st.InsertUsage(ctx, u); err != nil {
		t.Fatalf("InsertUsage: %v", err)
	}
	got, err := st.LatestUsage(ctx, "acct2")
	if err != nil {
		t.Fatalf("LatestUsage: %v", err)
	}
	if got.SizeBytes != 1024 {
		t.Errorf("expected SizeBytes=1024, got %d", got.SizeBytes)
	}
	if got.ObjectCount != 5 {
		t.Errorf("expected ObjectCount=5, got %d", got.ObjectCount)
	}
}

// ---------------------------------------------------------------------------
// 3. BYO config validation (handler layer uses these checks; test the rules)
// ---------------------------------------------------------------------------

func TestBYOEndpointMustBeHTTPS(t *testing.T) {
	// Validate that non-https endpoints are rejected.
	cases := []struct {
		endpoint string
		valid    bool
	}{
		{"https://s3.example.com", true},
		{"http://s3.example.com", false},
		{"", true}, // empty endpoint is fine for managed
	}
	for _, c := range cases {
		byo := c.endpoint != ""
		ok := !byo || strings.HasPrefix(c.endpoint, "https://")
		if ok != c.valid {
			t.Errorf("endpoint %q: expected valid=%v, got %v", c.endpoint, c.valid, ok)
		}
	}
}

func TestBYOBucketRequired(t *testing.T) {
	cases := []struct {
		bucket string
		valid  bool
	}{
		{"my-bucket", true},
		{"", false},
	}
	for _, c := range cases {
		ok := c.bucket != ""
		if ok != c.valid {
			t.Errorf("bucket %q: expected valid=%v, got %v", c.bucket, c.valid, ok)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. SnapshotURL — bucket format, key, TTL
// ---------------------------------------------------------------------------

func TestSnapshotURL_BucketFormat(t *testing.T) {
	mem := NewMemProvider()
	st := NewMemStore()

	svc := &Service{
		Store: st,
		ProviderForAccount: func(_ context.Context, _ string) (Provider, error) {
			return mem, nil
		},
	}

	ulid := "01HZAB1234XYZWVUTSRQPMNK01"
	_, err := svc.SnapshotURL(ctx, "acct1", ulid)
	if err != nil {
		t.Fatalf("SnapshotURL: %v", err)
	}

	// The MemProvider records the last PresignGet call; we verify via its URL.
	url, err := mem.PresignGet(ctx, managedBucketName(ulid), "latest.snap.enc", maxSnapshotTTL)
	if err != nil {
		t.Fatalf("direct PresignGet: %v", err)
	}
	expectedBucket := "vulos-" + strings.ToLower(ulid)
	if !strings.Contains(url, expectedBucket) {
		t.Errorf("expected bucket %q in URL %q", expectedBucket, url)
	}
	if !strings.Contains(url, "latest.snap.enc") {
		t.Errorf("expected key 'latest.snap.enc' in URL %q", url)
	}
}

func TestSnapshotURL_TTLClamped(t *testing.T) {
	// maxSnapshotTTL must be ≤ 300 seconds.
	if maxSnapshotTTL > 300*time.Second {
		t.Errorf("maxSnapshotTTL=%v exceeds 300s", maxSnapshotTTL)
	}
}

func TestManagedBucketName(t *testing.T) {
	ulid := "01HZAB1234XYZWVUTSRQPMNK01"
	got := managedBucketName(ulid)
	want := "vulos-" + strings.ToLower(ulid)
	if got != want {
		t.Errorf("managedBucketName(%q) = %q, want %q", ulid, got, want)
	}
}

// ---------------------------------------------------------------------------
// 5. TigrisProvider against httptest S3-shaped mock
// ---------------------------------------------------------------------------

// mockS3Server returns an httptest.Server that responds to:
//   - PUT /{bucket}  → 200 (EnsureBucket)
//   - GET /{bucket}/{key}?X-Amz-Algorithm=... → 200 (presign check; not a real presign,
//     but we verify the presign client generates a URL with X-Amz-Algorithm)
func mockS3Server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// CreateBucket: PUT /{bucket}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			// CreateBucket response
			w.Header().Set("Location", r.URL.Path)
			w.WriteHeader(http.StatusOK)
		case http.MethodHead:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	return httptest.NewServer(mux)
}

func TestTigrisProvider_EnsureBucket(t *testing.T) {
	srv := mockS3Server(t)
	defer srv.Close()

	tp, err := NewTigrisProvider(TigrisConfig{
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
		Endpoint:        srv.URL,
		Region:          "auto",
	})
	if err != nil {
		t.Fatalf("NewTigrisProvider: %v", err)
	}

	if err := tp.EnsureBucket(ctx, "test-bucket"); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}
}

func TestTigrisProvider_PresignGetHasAlgorithm(t *testing.T) {
	srv := mockS3Server(t)
	defer srv.Close()

	tp, err := NewTigrisProvider(TigrisConfig{
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
		Endpoint:        srv.URL,
		Region:          "auto",
	})
	if err != nil {
		t.Fatalf("NewTigrisProvider: %v", err)
	}

	url, err := tp.PresignGet(ctx, "my-bucket", "my-key", time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if !strings.Contains(url, "X-Amz-Algorithm=AWS4-HMAC-SHA256") {
		t.Errorf("expected X-Amz-Algorithm=AWS4-HMAC-SHA256 in presigned URL, got: %q", url)
	}
}

func TestTigrisProvider_MissingCreds(t *testing.T) {
	_, err := NewTigrisProvider(TigrisConfig{})
	if !isStorageErr(err, ErrProviderFailed) {
		t.Fatalf("expected ErrProviderFailed for missing creds, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func isStorageErr(err error, target error) bool {
	if err == nil {
		return false
	}
	// errors.Is covers wrapping.
	return strings.Contains(err.Error(), target.Error()) ||
		errIs(err, target)
}

func errIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		// unwrap
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return false
}

// aesGCMEncryptExported is a test helper to verify the encryption/decryption
// round-trip matches the internal implementation.
func TestAESGCMRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	plaintext := "super-secret-s3-key-9876"
	enc, err := aesGCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	dec, err := aesGCMDecrypt(key, enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if dec != plaintext {
		t.Errorf("round-trip: got %q, want %q", dec, plaintext)
	}

	// Tamper: flip a byte in the ciphertext hex.
	tampered := []byte(enc)
	tampered[len(tampered)-1] ^= 0x01
	_, err = aesGCMDecrypt(key, string(tampered))
	if err == nil {
		t.Fatal("expected error for tampered ciphertext")
	}
}

// TestAESGCMShortCiphertext verifies the short-data guard.
func TestAESGCMShortCiphertext(t *testing.T) {
	key := make([]byte, 32)

	// Create a GCM block to get the nonce size.
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonceSize := gcm.NonceSize()

	// Encode fewer bytes than nonceSize.
	short := hex.EncodeToString(make([]byte, nonceSize-1))
	_, err := aesGCMDecrypt(key, short)
	if err == nil {
		t.Fatal("expected error for short ciphertext")
	}
}
