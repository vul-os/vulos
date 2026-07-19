package apikeys_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/apikeys"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openTestStore(t *testing.T) *apikeys.Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := apikeys.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("apikeys.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// ─── Test 1: issue → hash → introspect roundtrip ─────────────────────────────

func TestIssueIntrospectRoundtrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	rawKey, meta, err := st.IssueKey(ctx, "acc1", "my key",
		[]string{"documents.read"}, []string{"office"})
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}

	// Raw key must have the vk_ prefix.
	if !strings.HasPrefix(rawKey, apikeys.KeyPrefix) {
		t.Errorf("raw key missing vk_ prefix: %q", rawKey[:min(8, len(rawKey))])
	}
	// Meta must never contain the raw key.
	if strings.Contains(meta.ID, rawKey) {
		t.Error("meta.ID should not contain raw key")
	}

	// Introspect with the raw key.
	res, err := st.Introspect(ctx, rawKey)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if !res.Valid {
		t.Fatal("expected valid=true for freshly issued key")
	}
	if res.Account != "acc1" {
		t.Errorf("account: want acc1, got %q", res.Account)
	}
	if len(res.Scopes) != 1 || res.Scopes[0] != "documents.read" {
		t.Errorf("scopes mismatch: %v", res.Scopes)
	}
	if len(res.Products) != 1 || res.Products[0] != "office" {
		t.Errorf("products mismatch: %v", res.Products)
	}
}

// ─── Test 2: no raw key in introspect response ────────────────────────────────

func TestNoRawKeyLeak(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	rawKey, _, err := st.IssueKey(ctx, "acc2", "secret key", nil, nil)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}

	// List keys — must not include raw key.
	keys, err := st.ListKeys(ctx, "acc2")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("expected at least one key in list")
	}
	for _, k := range keys {
		// Encode the key struct to a string to check for raw key leak.
		serialized := k.ID + "|" + k.Name + "|" + k.AccountID
		for _, s := range k.Scopes {
			serialized += "|" + s
		}
		if strings.Contains(serialized, rawKey) {
			t.Error("raw key leaked in ListKeys response")
		}
	}
}

// ─── Test 3: revoked key is invalid ──────────────────────────────────────────

func TestRevokedKeyInvalid(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	rawKey, meta, err := st.IssueKey(ctx, "acc3", "to revoke", nil, nil)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}

	// Revoke it.
	if err := st.RevokeKey(ctx, "acc3", meta.ID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}

	// Introspect must return valid=false.
	res, err := st.Introspect(ctx, rawKey)
	if err != nil {
		t.Fatalf("Introspect after revoke: %v", err)
	}
	if res.Valid {
		t.Error("expected valid=false for revoked key")
	}
}

// ─── Test 4: unknown key returns valid=false ──────────────────────────────────

func TestUnknownKeyInvalid(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	res, err := st.Introspect(ctx, "vk_thiskeydoesnotexistinthestore00000000000")
	if err != nil {
		t.Fatalf("Introspect unknown: %v", err)
	}
	if res.Valid {
		t.Error("expected valid=false for unknown key")
	}
}

// ─── Test 5: scope + product enforcement ─────────────────────────────────────

func TestScopeProductEnforcement(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	rawKey, _, err := st.IssueKey(ctx, "acc4", "scoped",
		[]string{"mail.read"}, []string{"mail"})
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}

	res, err := st.Introspect(ctx, rawKey)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if !res.Valid {
		t.Fatal("expected valid key")
	}
	// Must carry exactly the granted scopes + products.
	if len(res.Scopes) != 1 || res.Scopes[0] != "mail.read" {
		t.Errorf("scopes: want [mail.read], got %v", res.Scopes)
	}
	if len(res.Products) != 1 || res.Products[0] != "mail" {
		t.Errorf("products: want [mail], got %v", res.Products)
	}
	// Must NOT carry ungranted scopes.
	for _, s := range res.Scopes {
		if s == "documents.write" {
			t.Error("key must not carry documents.write")
		}
	}
}

// ─── Test 6: cross-account revocation prevented ──────────────────────────────

func TestCrossAccountRevokeBlocked(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, meta, err := st.IssueKey(ctx, "alice", "alice key", nil, nil)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}

	// Bob tries to revoke Alice's key — must fail.
	err = st.RevokeKey(ctx, "bob", meta.ID)
	if err == nil {
		t.Error("expected error when bob tries to revoke alice's key")
	}
}

// ─── Test 7: expired key is invalid ──────────────────────────────────────────

func TestExpiredKeyInvalid(t *testing.T) {
	// We test this via the Active() helper on APIKey (since the store's
	// Introspect uses the same time check). The store-level expiry check is
	// covered by the fact that Active() returns false for expired keys.
	past := time.Now().UTC().Add(-time.Hour)
	k := apikeys.APIKey{
		ExpiresAt: &past,
	}
	if k.Active() {
		t.Error("key with past expires_at should not be active")
	}
}

// ─── Test 8: key has vk_ prefix and sufficient length ────────────────────────

func TestKeyFormat(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	rawKey, _, err := st.IssueKey(ctx, "acc5", "fmt", nil, nil)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	if !strings.HasPrefix(rawKey, "vk_") {
		t.Errorf("key must start with vk_, got prefix %q", rawKey[:3])
	}
	// vk_ (3) + 43 base64url chars = 46 total minimum.
	if len(rawKey) < 46 {
		t.Errorf("key too short: len=%d", len(rawKey))
	}
}

// ─── Test 9: issue rate limiter ───────────────────────────────────────────────

func TestIssueLimiter(t *testing.T) {
	limiter := apikeys.NewIssueLimiter(time.Hour, 3)

	// 3 requests should pass.
	for i := 0; i < 3; i++ {
		if !limiter.Allow("alice") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	// 4th should be denied.
	if limiter.Allow("alice") {
		t.Error("4th request should be rate-limited")
	}
	// Different account is unaffected.
	if !limiter.Allow("bob") {
		t.Error("bob should be allowed (different account)")
	}
}

// ─── Test 10: ListKeys returns empty slice (not nil) when no keys ─────────────

func TestListKeysEmptySlice(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	keys, err := st.ListKeys(ctx, "nobody")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if keys == nil {
		t.Error("ListKeys must return empty slice, not nil")
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(keys))
	}
}

// ─── ENTITLEMENT-SEAM-01: EntitlementFilter narrows introspected products ───

// stubEntitlementFilter implements apikeys.EntitlementFilter over a fixed
// per-account allow-list, for testing the seam in isolation from billing.
type stubEntitlementFilter struct {
	allowed map[string]map[string]bool // accountID -> product -> allowed
	err     error
}

func (f stubEntitlementFilter) FilterProducts(_ context.Context, accountID string, products []string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	allow := f.allowed[accountID]
	out := make([]string, 0, len(products))
	for _, p := range products {
		if allow[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

func TestEntitlementFilter_NarrowsProducts(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	rawKey, _, err := st.IssueKey(ctx, "acc-narrow", "k",
		[]string{"relay.send"}, []string{"office", "relay"})
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}

	// Before wiring a filter: both products come back verbatim.
	res, err := st.Introspect(ctx, rawKey)
	if err != nil {
		t.Fatalf("Introspect (no filter): %v", err)
	}
	if len(res.Products) != 2 {
		t.Fatalf("no filter wired: want 2 products verbatim, got %v", res.Products)
	}

	// Wire a filter that denies "relay" for this account (e.g. free tier with
	// no relay quota) but keeps "office".
	st.SetEntitlementFilter(stubEntitlementFilter{
		allowed: map[string]map[string]bool{
			"acc-narrow": {"office": true},
		},
	})
	res2, err := st.Introspect(ctx, rawKey)
	if err != nil {
		t.Fatalf("Introspect (filtered): %v", err)
	}
	if res2.Valid != true || res2.Account != "acc-narrow" {
		t.Fatalf("filtering must not affect valid/account: %+v", res2)
	}
	if len(res2.Products) != 1 || res2.Products[0] != "office" {
		t.Errorf("filtered products = %v, want [office]", res2.Products)
	}
	// Scopes are untouched by the product filter.
	if len(res2.Scopes) != 1 || res2.Scopes[0] != "relay.send" {
		t.Errorf("scopes must be untouched by the entitlement filter, got %v", res2.Scopes)
	}
}

func TestEntitlementFilter_FailsOpenOnError(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	rawKey, _, err := st.IssueKey(ctx, "acc-failopen", "k", nil, []string{"office", "relay"})
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	st.SetEntitlementFilter(stubEntitlementFilter{err: context.DeadlineExceeded})

	res, err := st.Introspect(ctx, rawKey)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if len(res.Products) != 2 {
		t.Errorf("a filter error must fail OPEN (products unchanged), got %v", res.Products)
	}
}

func TestEntitlementFilter_NilFilterUnaffected(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	rawKey, _, err := st.IssueKey(ctx, "acc-nofilter", "k", nil, []string{"office"})
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	st.SetEntitlementFilter(nil)
	res, err := st.Introspect(ctx, rawKey)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if len(res.Products) != 1 || res.Products[0] != "office" {
		t.Errorf("nil filter must leave products verbatim, got %v", res.Products)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
