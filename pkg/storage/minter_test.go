package storage

import (
	"context"
	"testing"
	"time"
)

func TestMemMinter_MintAndRevoke(t *testing.T) {
	m := NewMemMinter()
	ctx := context.Background()

	c, err := m.MintBucketScoped(ctx, []string{"vulos-abc"}, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if c.Bucket != "vulos-abc" {
		t.Fatalf("bucket: got %q", c.Bucket)
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		t.Fatal("empty creds")
	}
	if c.ExpiresAt.IsZero() {
		t.Fatal("expected non-zero expiry with ttl>0")
	}
	if m.MintCount() != 1 {
		t.Fatalf("mint count: got %d", m.MintCount())
	}
	if m.WasRevoked(c.ID) {
		t.Fatal("should not be revoked yet")
	}
	if err := m.Revoke(ctx, c.ID, c.PolicyID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !m.WasRevoked(c.ID) {
		t.Fatal("expected revoked")
	}
}

func TestMemMinter_ForceErr(t *testing.T) {
	m := NewMemMinter()
	m.ForceErr = context.DeadlineExceeded
	if _, err := m.MintBucketScoped(context.Background(), []string{"b"}, 0); err == nil {
		t.Fatal("expected forced error")
	}
}

func TestStaticFallbackMinter_ReturnsMaster(t *testing.T) {
	m := StaticFallbackMinter{AccessKeyID: "AKMASTER", SecretAccessKey: "sekret", Endpoint: "https://e", Region: "auto"}
	c, err := m.MintBucketScoped(context.Background(), []string{"vulos-x"}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if c.AccessKeyID != "AKMASTER" || c.SecretAccessKey != "sekret" {
		t.Fatalf("expected master creds returned, got %q", c.AccessKeyID)
	}
	if c.Bucket != "vulos-x" {
		t.Fatalf("bucket: got %q", c.Bucket)
	}
}

