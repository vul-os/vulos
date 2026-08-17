package lanca_test

import (
	"strings"
	"testing"

	"vulos/backend/internal/lan"
	"vulos/backend/internal/lanca"
)

// TestEveryDottedNameTheBoxAdvertisesIsIssuable is the CROSS-PACKAGE guard that
// keeps the certificate's SAN list tied to reality.
//
// internal/lan owns what a box answers to (NameSet). internal/lanca owns what
// this CA may vouch for (permittedSubtrees). Those are two declarations of
// overlapping facts in two packages, which is this codebase's dominant defect
// shape — the hardcoded []string{"vulos.local", lanHost} that produced the
// two-box collision was exactly this, three times over.
//
// Nothing stops someone adding a suffix to routerSuffixes without adding it
// here. The symptom would not be a test failure; it would be an owner typing a
// name their box advertises and getting a certificate error, months later, with
// no obvious cause. This test turns that into a build failure at the moment the
// two drift.
//
// It runs in package lanca_test (not lanca) so the dependency is one-way and
// test-only: internal/lan must never import internal/lanca, because the CA is
// operator-side and has no business on the box.
func TestEveryDottedNameTheBoxAdvertisesIsIssuable(t *testing.T) {
	// A realistic instance id (26-char ULID) and a couple of hostname shapes,
	// including the empty one that forces the derived default.
	for _, hostname := range []string{"", "vulos", "kitchen", "my-box-2"} {
		ns := lan.NewNameSet("01JD8X7K3N7Q2ABCDEFGHJKMNP", hostname)
		if len(ns.DNSNames) == 0 {
			t.Fatalf("NewNameSet(%q) produced no DNS names", hostname)
		}

		var issuable int
		for _, n := range ns.DNSNames {
			err := lanca.CheckDNSName(n)
			if !strings.Contains(n, ".") {
				// Bare labels are the known, permanent gap: RFC 5280
				// permittedSubtrees cannot express "any single label". They
				// must be REFUSED, not quietly issued — a leaf carrying one
				// would be rejected by every enforcing verifier.
				if err == nil {
					t.Errorf("hostname %q: bare label %q was judged issuable; a leaf carrying it would be rejected by every enforcing verifier", hostname, n)
				}
				continue
			}
			if err != nil {
				t.Errorf("hostname %q: the box advertises %q but this CA cannot issue for it — internal/lan gained a name shape that internal/lanca's permittedSubtrees do not cover, so browsing to it would show a certificate error: %v", hostname, n, err)
				continue
			}
			issuable++
		}
		if issuable == 0 {
			t.Errorf("hostname %q: NOT ONE of the box's advertised names is issuable", hostname)
		}
	}
}

// TestFilterIssuableAcceptsRealNameSetsWholesale is the same guard at the API
// an issuer actually calls.
func TestFilterIssuableAcceptsRealNameSetsWholesale(t *testing.T) {
	ns := lan.NewNameSet("01JD8X7K3N7Q2ABCDEFGHJKMNP", "kitchen")
	got, skipped, _ := lanca.FilterIssuable(ns.DNSNames, nil)

	if len(got.DNSNames) == 0 {
		t.Fatal("FilterIssuable produced an empty set from a real NameSet")
	}
	for _, n := range skipped {
		if strings.Contains(n, ".") {
			t.Errorf("FilterIssuable skipped %q, a DOTTED name the box advertises — that name will show a certificate error in a browser", n)
		}
	}
}
