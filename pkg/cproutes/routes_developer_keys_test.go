// routes_developer_keys_test.go — ENTITLEMENT-SEAM-01: billingEntitlementFilter,
// the apikeys.EntitlementFilter implementation wired into introspect. In the
// box / KILL-FLAT-TIERS model the operational filter is a pass-through backed by
// the provider-agnostic billingport.EntitlementResolver seam: every Class-P app
// is entitled for every account, so introspect never narrows the claimed set.
package cproutes

import (
	"context"
	"testing"

	"github.com/vul-os/vulos-management/pkg/apikeys"
	"github.com/vul-os/vulos-management/pkg/billingport"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// TestBillingEntitlementFilter_KeepsApps verifies the operational filter keeps
// every claimed app regardless of the resolver's tier answer (box model: apps
// run on your box and are entitled for every account — there is no tier gate).
func TestBillingEntitlementFilter_KeepsApps(t *testing.T) {
	f := billingEntitlementFilter{ent: billingport.NewNoopResolver()}

	got, err := f.FilterProducts(context.Background(), "acct-x", []string{"office", "board"})
	if err != nil {
		t.Fatalf("FilterProducts: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("filtered products = %v, want both [office board] (apps are entitled for every account)", got)
	}
}

// TestBillingEntitlementFilter_NilResolverPassesThrough verifies the zero-value
// filter (ent==nil) never panics and returns products unchanged.
func TestBillingEntitlementFilter_NilResolverPassesThrough(t *testing.T) {
	var f billingEntitlementFilter // ent == nil
	got, err := f.FilterProducts(context.Background(), "acct-x", []string{"office", "board"})
	if err != nil {
		t.Fatalf("FilterProducts: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("nil resolver must pass products through unchanged, got %v", got)
	}
}

// TestWireDeveloperKeys_IntrospectKeepsProducts is an end-to-end check through
// the real apikeys.Store + billingEntitlementFilter wiring (the same seam
// WireDeveloperKeys installs): a key's introspected products are preserved,
// since in the box model every Class-P app is entitled for every account.
func TestWireDeveloperKeys_IntrospectKeepsProducts(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	ks, err := apikeys.Open(db)
	if err != nil {
		t.Fatalf("apikeys.Open: %v", err)
	}
	t.Cleanup(func() { _ = ks.Close() })
	ks.SetEntitlementFilter(billingEntitlementFilter{ent: billingport.NewNoopResolver()})

	rawKey, _, err := ks.IssueKey(context.Background(), "acct-1", "k", nil, []string{"office", "board"})
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}

	res, err := ks.Introspect(context.Background(), rawKey)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if len(res.Products) != 2 {
		t.Fatalf("want both products kept, got %v", res.Products)
	}
}
