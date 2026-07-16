package storage

import (
	"context"
	"strings"
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

func TestBucketScopedPolicyDoc_PinsBuckets(t *testing.T) {
	doc := bucketScopedPolicyDoc([]string{"vulos-abc", "vulos-backup-abc"})
	// Must reference both buckets, restrict to the exact ARNs, and only grant the
	// Vulos action set (no wildcard * action, no cross-bucket ARN).
	for _, want := range []string{
		`arn:aws:s3:::vulos-abc`,
		`arn:aws:s3:::vulos-abc/*`,
		`arn:aws:s3:::vulos-backup-abc`,
		`arn:aws:s3:::vulos-backup-abc/*`,
		`s3:GetObject`, `s3:PutObject`, `s3:DeleteObject`, `s3:ListBucket`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("policy doc missing %q\ndoc=%s", want, doc)
		}
	}
	if strings.Contains(doc, `"s3:*"`) || strings.Contains(doc, `"*"`) {
		t.Errorf("policy doc must not grant wildcard actions/resources: %s", doc)
	}
}

func TestNewTigrisIAMMinter_RequiresMasterKey(t *testing.T) {
	if _, err := NewTigrisIAMMinter(TigrisIAMConfig{}); err == nil {
		t.Fatal("expected error when master key missing")
	}
	m, err := NewTigrisIAMMinter(TigrisIAMConfig{AccessKeyID: "k", SecretAccessKey: "s"})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	if m.cfg.IAMEndpoint != defaultTigrisIAMEndpoint {
		t.Fatalf("default IAM endpoint: got %q", m.cfg.IAMEndpoint)
	}
}
