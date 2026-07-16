package oauthprovider

// capstone_secret_scope_test.go — CAPSTONE coverage for the confidential-client
// secret lifecycle (rotate + verify) and the closed scope set. These gate who can
// call the token endpoint as a confidential client and which scopes the provider
// will ever issue, so the deny directions are asserted explicitly.

import (
	"context"
	"strings"
	"testing"
)

// TestRotateSecret rotates a confidential client's secret: the new plaintext
// verifies, the OLD secret stops verifying, and public clients are refused.
func TestRotateSecret(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	c, oldSecret, err := svc.RegisterClient(ctx, "user-1", "Conf App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID, ScopeEmail}, false)
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}

	newSecret, err := svc.RotateSecret(ctx, c.ClientID)
	if err != nil {
		t.Fatalf("RotateSecret: %v", err)
	}
	if newSecret == oldSecret || !strings.HasPrefix(newSecret, "vcsk_") {
		t.Fatalf("rotated secret invalid: %q (old %q)", newSecret, oldSecret)
	}

	got, err := svc.Store().GetClient(ctx, c.ClientID)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	// New secret verifies; old secret must be dead.
	if !VerifyClientSecretExported(got, newSecret) {
		t.Fatal("rotated secret does not verify")
	}
	if VerifyClientSecretExported(got, oldSecret) {
		t.Fatal("OLD secret still verifies after rotation (rotation did not invalidate it)")
	}

	// Rotating a nonexistent client errors.
	if _, err := svc.RotateSecret(ctx, "nope-client"); err == nil {
		t.Fatal("RotateSecret on unknown client must error")
	}

	// Public clients have no secret to rotate.
	pub, _, err := svc.RegisterClient(ctx, "user-1", "Public App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID}, true)
	if err != nil {
		t.Fatalf("RegisterClient public: %v", err)
	}
	if _, err := svc.RotateSecret(ctx, pub.ClientID); err == nil {
		t.Fatal("RotateSecret on public client must error")
	}
}

// TestVerifyClientSecretExported covers the constant-time secret check used by
// the revoke handler (which authenticates clients outside authenticateClient).
func TestVerifyClientSecretExported(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	c, secret, err := svc.RegisterClient(ctx, "user-1", "App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID}, false)
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	got, err := svc.Store().GetClient(ctx, c.ClientID)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if !VerifyClientSecretExported(got, secret) {
		t.Fatal("correct secret must verify")
	}
	if VerifyClientSecretExported(got, "vcsk_wrong") {
		t.Fatal("wrong secret must not verify")
	}
	if VerifyClientSecretExported(got, "") {
		t.Fatal("empty secret must not verify")
	}
	// A public client (no stored hash) must never verify any secret.
	pub, _, _ := svc.RegisterClient(ctx, "user-1", "Pub",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID}, true)
	pc, _ := svc.Store().GetClient(ctx, pub.ClientID)
	if VerifyClientSecretExported(pc, "anything") {
		t.Fatal("public client (empty hash) must never verify a secret")
	}
}

// TestRevokeTokenStore covers the by-hash single-token revocation used by the
// RFC 7009 revoke endpoint: revoking is idempotent and never errors on an absent
// hash (per the spec, revoking an unknown token is a success).
func TestRevokeTokenStore(t *testing.T) {
	t.Setenv("VULOS_DEV", "true")
	st := newTestStore(t)
	ctx := context.Background()
	// Revoking an absent token hash is a no-op success (RFC 7009).
	if err := st.RevokeToken(ctx, "no-such-hash"); err != nil {
		t.Fatalf("RevokeToken(absent) must not error, got %v", err)
	}
	// Idempotent second call.
	if err := st.RevokeToken(ctx, "no-such-hash"); err != nil {
		t.Fatalf("RevokeToken second call: %v", err)
	}
}

// TestScopeSet pins the closed scope set: exactly the four known scopes are
// recognised, anything else is rejected (invalid_scope), and AllScopes returns
// them in a stable order.
func TestScopeSet(t *testing.T) {
	all := AllScopes()
	want := []string{ScopeOpenID, ScopeEmail, ScopeMailRead, ScopeMailSend}
	if len(all) != len(want) {
		t.Fatalf("AllScopes len = %d, want %d: %v", len(all), len(want), all)
	}
	for i := range want {
		if all[i] != want[i] {
			t.Fatalf("AllScopes[%d] = %q, want %q (order not stable)", i, all[i], want[i])
		}
		if !IsKnownScope(want[i]) {
			t.Fatalf("known scope %q reported unknown", want[i])
		}
	}
	for _, bad := range []string{"", "drive", "mail.admin", "openid ", "OPENID", "profile"} {
		if IsKnownScope(bad) {
			t.Fatalf("scope %q must NOT be recognised (closed set)", bad)
		}
	}
}
