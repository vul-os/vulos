package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// s3ConfigFromEnv builds an S3Config from VULOS_S3_* env vars.
// Returns (cfg, true) when configured, (zero, false) when not.
func s3ConfigFromEnv() (S3Config, bool) {
	cfg := LoadS3Config()
	return cfg, cfg.Configured()
}

// skipIfNoS3 skips the test when VULOS_S3_* env vars are not configured.
func skipIfNoS3(t *testing.T) S3Config {
	t.Helper()
	cfg, ok := s3ConfigFromEnv()
	if !ok {
		t.Skip("skipping: VULOS_S3_ACCESS_KEY / VULOS_S3_SECRET_KEY / VULOS_S3_BUCKET not set")
	}
	return cfg
}

// uniqueKey returns a test-scoped S3 key that is unlikely to collide between runs.
func uniqueKey(t *testing.T, suffix string) string {
	t.Helper()
	return fmt.Sprintf("test/%s/%d/%s", t.Name(), time.Now().UnixNano(), suffix)
}

// --- Unit tests (no S3 required) ---

func TestDeriveKeyDeterministic(t *testing.T) {
	passphrase := "correct-horse-battery-staple"
	salt := make([]byte, saltLen)
	for i := range salt {
		salt[i] = byte(i)
	}

	k1 := deriveKey(passphrase, salt)
	k2 := deriveKey(passphrase, salt)

	if !bytes.Equal(k1, k2) {
		t.Fatal("deriveKey is not deterministic")
	}
	if len(k1) != int(argonKeyLen) {
		t.Fatalf("expected key length %d, got %d", argonKeyLen, len(k1))
	}
}

func TestDeriveKeyDifferentPassphrases(t *testing.T) {
	salt := make([]byte, saltLen)
	k1 := deriveKey("passphrase-A", salt)
	k2 := deriveKey("passphrase-B", salt)

	if bytes.Equal(k1, k2) {
		t.Fatal("different passphrases produced identical keys")
	}
}

func TestDeriveKeyDifferentSalts(t *testing.T) {
	saltA := make([]byte, saltLen)
	saltB := make([]byte, saltLen)
	saltB[0] = 1

	k1 := deriveKey("same-passphrase", saltA)
	k2 := deriveKey("same-passphrase", saltB)

	if bytes.Equal(k1, k2) {
		t.Fatal("different salts produced identical keys")
	}
}

func TestNewClientWithKeyLengthCheck(t *testing.T) {
	cfg := S3Config{
		Endpoint:  "localhost:9000",
		Bucket:    "test",
		AccessKey: "test",
		SecretKey: "test",
	}

	shortKey := make([]byte, 16)
	_, err := NewClientWithKey(cfg, shortKey)
	if err == nil {
		t.Fatal("expected error for short key, got nil")
	}
	if !strings.Contains(err.Error(), "32 bytes") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadS3Config(t *testing.T) {
	// Temporarily override env vars for this test.
	t.Setenv("VULOS_S3_ENDPOINT", "test-endpoint:9000")
	t.Setenv("VULOS_S3_BUCKET", "test-bucket")
	t.Setenv("VULOS_S3_ACCESS_KEY", "AKID")
	t.Setenv("VULOS_S3_SECRET_KEY", "SECRET")
	t.Setenv("VULOS_S3_USE_SSL", "true")

	cfg := LoadS3Config()

	if cfg.Endpoint != "test-endpoint:9000" {
		t.Errorf("endpoint: got %q", cfg.Endpoint)
	}
	if cfg.Bucket != "test-bucket" {
		t.Errorf("bucket: got %q", cfg.Bucket)
	}
	if cfg.AccessKey != "AKID" {
		t.Errorf("access key: got %q", cfg.AccessKey)
	}
	if cfg.SecretKey != "SECRET" {
		t.Errorf("secret key: got %q", cfg.SecretKey)
	}
	if !cfg.UseSSL {
		t.Error("expected UseSSL=true")
	}
	if !cfg.Configured() {
		t.Error("expected Configured()=true")
	}
}

func TestLoadS3ConfigNotConfigured(t *testing.T) {
	// Clear the relevant env vars.
	os.Unsetenv("VULOS_S3_ACCESS_KEY")
	os.Unsetenv("VULOS_S3_SECRET_KEY")
	os.Unsetenv("VULOS_S3_BUCKET")

	cfg := LoadS3Config()
	if cfg.Configured() {
		t.Error("expected Configured()=false when credentials are absent")
	}
}

// --- Integration tests (require VULOS_S3_* env vars) ---

func TestPutEncryptedGetEncryptedRoundTrip(t *testing.T) {
	cfg := skipIfNoS3(t)
	ctx := context.Background()

	const passphrase = "test-passphrase-roundtrip"
	client, err := NewClient(ctx, cfg, passphrase)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	want := []byte("Hello, encrypted Vulos cluster world!")
	key := uniqueKey(t, "roundtrip.bin")

	if err := client.PutEncrypted(ctx, key, want); err != nil {
		t.Fatalf("PutEncrypted: %v", err)
	}

	got, err := client.GetEncrypted(ctx, key)
	if err != nil {
		t.Fatalf("GetEncrypted: %v", err)
	}

	if !bytes.Equal(want, got) {
		t.Fatalf("round-trip mismatch: want %q, got %q", want, got)
	}
}

func TestPutEncryptedGetEncryptedBinaryData(t *testing.T) {
	cfg := skipIfNoS3(t)
	ctx := context.Background()

	const passphrase = "binary-data-passphrase"
	client, err := NewClient(ctx, cfg, passphrase)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Use a random 4 KiB payload to test binary fidelity.
	want := make([]byte, 4096)
	if _, err := rand.Read(want); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	key := uniqueKey(t, "binary.bin")

	if err := client.PutEncrypted(ctx, key, want); err != nil {
		t.Fatalf("PutEncrypted: %v", err)
	}

	got, err := client.GetEncrypted(ctx, key)
	if err != nil {
		t.Fatalf("GetEncrypted: %v", err)
	}

	if !bytes.Equal(want, got) {
		t.Fatal("binary round-trip mismatch")
	}
}

func TestWrongPassphraseFails(t *testing.T) {
	cfg := skipIfNoS3(t)
	ctx := context.Background()

	// Upload with "correct" passphrase.
	correct, err := NewClient(ctx, cfg, "correct-passphrase")
	if err != nil {
		t.Fatalf("NewClient(correct): %v", err)
	}
	key := uniqueKey(t, "wrongpass.bin")
	payload := []byte("secret payload that must not be readable with wrong key")

	if err := correct.PutEncrypted(ctx, key, payload); err != nil {
		t.Fatalf("PutEncrypted: %v", err)
	}

	// Attempt to read with a different key (wrong passphrase).
	// We use NewClientWithKey to bypass the shared salt and construct an
	// independent 32-byte wrong key directly.
	wrongKey := make([]byte, 32)
	if _, err := rand.Read(wrongKey); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	wrong, err := NewClientWithKey(cfg, wrongKey)
	if err != nil {
		t.Fatalf("NewClientWithKey: %v", err)
	}

	_, err = wrong.GetEncrypted(ctx, key)
	if err == nil {
		t.Fatal("expected decryption to fail with wrong key, but got nil error")
	}
	t.Logf("got expected error with wrong key: %v", err)
}

func TestSaltReusedAcrossClients(t *testing.T) {
	cfg := skipIfNoS3(t)
	ctx := context.Background()

	const passphrase = "salt-reuse-test-passphrase"

	// Create two independent clients from the same passphrase and config.
	// Both should derive the same key because they share the S3 salt.
	c1, err := NewClient(ctx, cfg, passphrase)
	if err != nil {
		t.Fatalf("NewClient c1: %v", err)
	}
	c2, err := NewClient(ctx, cfg, passphrase)
	if err != nil {
		t.Fatalf("NewClient c2: %v", err)
	}

	if !bytes.Equal(c1.encKey, c2.encKey) {
		t.Fatal("two clients with same passphrase derived different keys — salt was not reused")
	}

	// Write with c1, read with c2.
	want := []byte("cross-client encrypted message")
	key := uniqueKey(t, "cross.bin")

	if err := c1.PutEncrypted(ctx, key, want); err != nil {
		t.Fatalf("c1.PutEncrypted: %v", err)
	}

	got, err := c2.GetEncrypted(ctx, key)
	if err != nil {
		t.Fatalf("c2.GetEncrypted: %v", err)
	}

	if !bytes.Equal(want, got) {
		t.Fatalf("cross-client round-trip mismatch: want %q, got %q", want, got)
	}
}
