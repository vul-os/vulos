package billingport

import (
	"context"
	"errors"
	"testing"
)

func TestNoopResolver_GrantsUnlimitedSelfHost(t *testing.T) {
	r := NewNoopResolver()
	ctx := context.Background()

	tier, err := r.EffectiveTierFor(ctx, "acct-123")
	if err != nil {
		t.Fatalf("EffectiveTierFor: unexpected error: %v", err)
	}
	if tier != TierSelfHost {
		t.Fatalf("EffectiveTierFor = %q, want %q", tier, TierSelfHost)
	}

	// 0 means "no cap" — self-host must never limit active users.
	if cap := r.MaxActiveUsersForTier(tier); cap != 0 {
		t.Fatalf("MaxActiveUsersForTier = %d, want 0 (unlimited)", cap)
	}

	// Storage is never capped, for any hosting kind.
	for _, hk := range []string{HostingCloud, HostingBox, HostingSelfHost, "anything"} {
		if err := r.CheckStorageQuota(ctx, "acct-123", 1<<40, hk); err != nil {
			t.Fatalf("CheckStorageQuota(%s) = %v, want nil", hk, err)
		}
	}
}

func TestNoopProvider_NeverPretendsCharge(t *testing.T) {
	p := NewNoopProvider()
	ctx := context.Background()

	if p.Name() != "noop" {
		t.Fatalf("Name = %q, want noop", p.Name())
	}
	if _, err := p.InitTransaction(ctx, InitRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("InitTransaction err = %v, want ErrUnsupported", err)
	}
	if _, err := p.ChargeAuthorization(ctx, ChargeAuthRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ChargeAuthorization err = %v, want ErrUnsupported", err)
	}
	// Verify must never report success for an unconfigured rail.
	v, err := p.VerifyTransaction(ctx, "ref-1")
	if err != nil {
		t.Fatalf("VerifyTransaction err = %v", err)
	}
	if v.Status == "success" {
		t.Fatalf("VerifyTransaction status = success; no-op rail must never confirm payment")
	}
	if err := p.VerifyWebhookSignature("k", []byte("b"), "sig"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("VerifyWebhookSignature err = %v, want ErrUnsupported", err)
	}
}
