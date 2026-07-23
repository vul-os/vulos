package osrouter

import (
	"context"
	"testing"
)

// Adversarial: a member of org Beta must NOT be able to reach org Acme's box via
// a direct box handle (cross-org isolation).
func TestSecurity_BoxHandle_CrossOrgForbidden(t *testing.T) {
	d := newDir(t)
	d.AddMember(acctUser, orgBeta, "member") // user is in Beta only
	d.AddBox(Box{ID: ulidA, OrgID: orgAcme, Healthy: true})
	r := NewRouter(d)
	dec, err := r.Resolve(context.Background(), ResolveInput{AccountID: acctUser, Host: ulidA + ".os.vulos.org", OSHost: "os.vulos.org"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != DecideForbidden {
		t.Fatalf("kind = %v, want forbidden (cross-org box access)", dec.Kind)
	}
}

// Adversarial: a non-member reaching an org HANDLE they don't belong to must be
// NOT routed, and must NOT disclose whether the org exists — an org handle is
// resolved only within the caller's OWN orgs, so a non-member sees NotFound
// (identical to a nonexistent handle: no cross-account enumeration).
func TestSecurity_OrgHandle_NonMemberNotFound(t *testing.T) {
	d := newDir(t)
	d.AddMember(acctUser, orgBeta, "member")
	d.AddBox(Box{ID: ulidA, OrgID: orgAcme, Healthy: true})
	r := NewRouter(d)
	dec, err := r.Resolve(context.Background(), ResolveInput{AccountID: acctUser, Host: "acme.os.vulos.org", OSHost: "os.vulos.org"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != DecideNotFound {
		t.Fatalf("kind = %v, want not_found (non-member org handle, no enumeration)", dec.Kind)
	}
}

// Adversarial: a PreferOrgID the caller is not a member of must be ignored (no
// privilege escalation via ?org=). With a single real org, it routes to that.
func TestSecurity_PreferOrg_NonMemberIgnored(t *testing.T) {
	d := newDir(t)
	d.AddMember(acctSolo, orgAcme, "owner")
	d.AddBox(Box{ID: ulidA, OrgID: orgAcme, Healthy: true})
	r := NewRouter(d)
	dec, err := r.Resolve(context.Background(), ResolveInput{
		AccountID: acctSolo, Host: "os.vulos.org", OSHost: "os.vulos.org",
		PreferOrgID: orgBeta, // NOT a member of Beta
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != DecideRoute || dec.Org.ID != orgAcme {
		t.Fatalf("kind=%v org=%q — a non-member preference escalated", dec.Kind, dec.Org.ID)
	}
}

// Adversarial: host confusion — a look-alike host that merely SUFFIXES the OS host
// domain (evil-os.vulos.org, os.vulos.org.evil.com) must never classify as OS.
func TestSecurity_HostConfusion(t *testing.T) {
	const osHost = "os.vulos.org"
	for _, h := range []string{
		"os.vulos.org.evil.com",
		"notos.vulos.org",
		"xos.vulos.org",
		"os-vulos.org",
		"deep.nested.os.vulos.org",
	} {
		if got := ClassifyHost(h, osHost); got.Kind != HostNotOS {
			t.Errorf("ClassifyHost(%q) = %v, want HostNotOS", h, got.Kind)
		}
	}
}

// Adversarial: a stale box (unhealthy) reached by direct handle yields DecideNoBox,
// never a route to a dead box.
func TestSecurity_BoxHandle_UnhealthyNoBox(t *testing.T) {
	d := newDir(t)
	d.AddMember(acctSolo, orgAcme, "owner")
	d.AddBox(Box{ID: ulidA, OrgID: orgAcme, Healthy: false})
	r := NewRouter(d)
	dec, err := r.Resolve(context.Background(), ResolveInput{AccountID: acctSolo, Host: ulidA + ".os.vulos.org", OSHost: "os.vulos.org"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != DecideNoBox {
		t.Fatalf("kind = %v, want no_box for unhealthy direct box", dec.Kind)
	}
}
