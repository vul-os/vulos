package storageport

import (
	"context"
	"errors"
	"testing"
)

func TestNoopProvisioner_BringYourOwnBucket(t *testing.T) {
	p := NewNoopProvisioner()
	ctx := context.Background()

	if p.Enabled() {
		t.Fatalf("Enabled = true, want false (BYO bucket default)")
	}
	if err := p.EnsureBucket(ctx, "b"); !errors.Is(err, ErrProvisioningDisabled) {
		t.Fatalf("EnsureBucket err = %v, want ErrProvisioningDisabled", err)
	}
	if err := p.EnsureBucketInRegion(ctx, "b", "r"); !errors.Is(err, ErrProvisioningDisabled) {
		t.Fatalf("EnsureBucketInRegion err = %v, want ErrProvisioningDisabled", err)
	}
}
