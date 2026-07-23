package osrouter

import (
	"context"
	"testing"
	"time"
)

// Valid 26-char Crockford ULIDs (uppercase) used as box ids throughout.
const (
	ulidA    = "01HZX9K3QJ7R8V2N4C5B6D7E8F"
	ulidB    = "01HZX9K3QJ7R8V2N4C5B6D7E8G"
	orgAcme  = "org_acme"
	orgBeta  = "org_beta"
	acctUser = "acct_user"
	acctSolo = "acct_solo"
)

func TestClassifyHost(t *testing.T) {
	const osHost = "os.vulos.org"
	cases := []struct {
		host string
		kind HostKind
		lbl  string
	}{
		{"os.vulos.org", HostOSApex, ""},
		{"os.vulos.org:443", HostOSApex, ""},
		{"OS.VULOS.ORG", HostOSApex, ""},
		{"os.vulos.org.", HostOSApex, ""},
		{"acme.os.vulos.org", HostOrgHandle, "acme"},
		{"ACME.os.vulos.org", HostOrgHandle, "acme"},
		{ulidA + ".os.vulos.org", HostBoxID, ulidA},
		{toLower(ulidA) + ".os.vulos.org", HostBoxID, ulidA}, // lowercase ULID canonicalised
		{"vulos.org", HostNotOS, ""},
		{"app.vulos.org", HostNotOS, ""},
		{"a.b.os.vulos.org", HostNotOS, ""},  // nested — rejected
		{"-bad.os.vulos.org", HostNotOS, ""}, // invalid slug
		{"bad_.os.vulos.org", HostNotOS, ""}, // underscore invalid
		{".os.vulos.org", HostNotOS, ""},     // empty label
		{"os.vulos.org.evil.com", HostNotOS, ""},
		{"", HostNotOS, ""},
	}
	for _, c := range cases {
		got := ClassifyHost(c.host, osHost)
		if got.Kind != c.kind || got.Label != c.lbl {
			t.Errorf("ClassifyHost(%q) = {%v,%q}, want {%v,%q}", c.host, got.Kind, got.Label, c.kind, c.lbl)
		}
	}
}

func toLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}

func TestSelectBest_ClusterOfOne(t *testing.T) {
	boxes := []Box{{ID: ulidA, OrgID: orgAcme, Healthy: true}}
	got, err := SelectBest(boxes, 0, 0)
	if err != nil {
		t.Fatalf("SelectBest: %v", err)
	}
	if got.ID != ulidA {
		t.Fatalf("got %q, want %q", got.ID, ulidA)
	}
}

func TestSelectBest_NoneHealthy(t *testing.T) {
	boxes := []Box{{ID: ulidA, OrgID: orgAcme, Healthy: false}}
	if _, err := SelectBest(boxes, 0, 0); err != ErrNoHealthyBox {
		t.Fatalf("err = %v, want ErrNoHealthyBox", err)
	}
	if _, err := SelectBest(nil, 0, 0); err != ErrNoHealthyBox {
		t.Fatalf("empty err = %v, want ErrNoHealthyBox", err)
	}
}

func TestSelectBest_StaleHeartbeatExcluded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	boxes := []Box{
		{ID: ulidA, OrgID: orgAcme, Healthy: true, LastSeen: now.Add(-10 * time.Minute)}, // stale
		{ID: ulidB, OrgID: orgAcme, Healthy: true, LastSeen: now.Add(-10 * time.Second)}, // fresh
	}
	got, err := selectBestAt(boxes, 0, 0, now)
	if err != nil {
		t.Fatalf("selectBestAt: %v", err)
	}
	if got.ID != ulidB {
		t.Fatalf("got %q, want fresh %q", got.ID, ulidB)
	}
}

func TestSelectBest_RanksByLoadThenDistance(t *testing.T) {
	// N-box cluster: least-loaded wins; ties break by nearest, then ID.
	boxes := []Box{
		{ID: "01HZX9K3QJ7R8V2N4C5B6D7E01", OrgID: orgAcme, Healthy: true, LoadScore: 0.9, Lat: 0, Lon: 0},
		{ID: "01HZX9K3QJ7R8V2N4C5B6D7E02", OrgID: orgAcme, Healthy: true, LoadScore: 0.1, Lat: 50, Lon: 50},
		{ID: "01HZX9K3QJ7R8V2N4C5B6D7E03", OrgID: orgAcme, Healthy: true, LoadScore: 0.1, Lat: 5, Lon: 5},
	}
	// Client near (5,5): among the two low-load boxes, the nearer (E03) wins.
	got, err := SelectBest(boxes, 5, 5)
	if err != nil {
		t.Fatalf("SelectBest: %v", err)
	}
	if got.ID != "01HZX9K3QJ7R8V2N4C5B6D7E03" {
		t.Fatalf("got %q, want E03 (low load + nearest)", got.ID)
	}
}

// ── Router decision tests (0 / 1 / N orgs, direct handles) ──────────────────────

func newDir(t *testing.T) *MemDirectory {
	t.Helper()
	d := NewMemDirectory()
	d.AddOrg(Org{ID: orgAcme, Slug: "acme", Name: "Acme"})
	d.AddOrg(Org{ID: orgBeta, Slug: "beta", Name: "Beta"})
	return d
}

func TestResolve_NoSession_Login(t *testing.T) {
	r := NewRouter(newDir(t))
	d, err := r.Resolve(context.Background(), ResolveInput{AccountID: "", Host: "os.vulos.org", OSHost: "os.vulos.org"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != DecideLogin {
		t.Fatalf("kind = %v, want login", d.Kind)
	}
}

func TestResolve_Apex_SingleOrg_Routes(t *testing.T) {
	d := newDir(t)
	d.AddMember(acctSolo, orgAcme, "owner")
	d.AddBox(Box{ID: ulidA, OrgID: orgAcme, Healthy: true})
	r := NewRouter(d)
	dec, err := r.Resolve(context.Background(), ResolveInput{AccountID: acctSolo, Host: "os.vulos.org", OSHost: "os.vulos.org"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != DecideRoute {
		t.Fatalf("kind = %v, want route", dec.Kind)
	}
	if dec.Box.ID != ulidA || dec.Org.ID != orgAcme {
		t.Fatalf("routed to box=%q org=%q", dec.Box.ID, dec.Org.ID)
	}
	wantHost := toLower(ulidA) + ".os.vulos.org"
	if dec.BoxHost != wantHost {
		t.Fatalf("BoxHost = %q, want %q", dec.BoxHost, wantHost)
	}
}

func TestResolve_Apex_MultiOrg_Chooser(t *testing.T) {
	d := newDir(t)
	d.AddMember(acctUser, orgAcme, "owner")
	d.AddMember(acctUser, orgBeta, "member")
	d.AddBox(Box{ID: ulidA, OrgID: orgAcme, Healthy: true})
	d.AddBox(Box{ID: ulidB, OrgID: orgBeta, Healthy: true})
	r := NewRouter(d)
	dec, err := r.Resolve(context.Background(), ResolveInput{AccountID: acctUser, Host: "os.vulos.org", OSHost: "os.vulos.org"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != DecideChooser {
		t.Fatalf("kind = %v, want chooser", dec.Kind)
	}
	if len(dec.Orgs) != 2 {
		t.Fatalf("orgs = %d, want 2", len(dec.Orgs))
	}
}

func TestResolve_Apex_MultiOrg_ActiveOrgSkipsChooser(t *testing.T) {
	d := newDir(t)
	d.AddMember(acctUser, orgAcme, "owner")
	d.AddMember(acctUser, orgBeta, "member")
	d.AddBox(Box{ID: ulidB, OrgID: orgBeta, Healthy: true})
	d.SetActive(acctUser, orgBeta)
	r := NewRouter(d)
	dec, err := r.Resolve(context.Background(), ResolveInput{AccountID: acctUser, Host: "os.vulos.org", OSHost: "os.vulos.org"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != DecideRoute || dec.Org.ID != orgBeta {
		t.Fatalf("kind=%v org=%q, want route beta", dec.Kind, dec.Org.ID)
	}
}

func TestResolve_Apex_PreferOrgWins(t *testing.T) {
	d := newDir(t)
	d.AddMember(acctUser, orgAcme, "owner")
	d.AddMember(acctUser, orgBeta, "member")
	d.SetActive(acctUser, orgBeta)
	d.AddBox(Box{ID: ulidA, OrgID: orgAcme, Healthy: true})
	r := NewRouter(d)
	dec, err := r.Resolve(context.Background(), ResolveInput{AccountID: acctUser, Host: "os.vulos.org", OSHost: "os.vulos.org", PreferOrgID: orgAcme})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != DecideRoute || dec.Org.ID != orgAcme {
		t.Fatalf("prefer ignored: kind=%v org=%q", dec.Kind, dec.Org.ID)
	}
}

func TestResolve_Apex_ZeroOrgs_NoBox(t *testing.T) {
	r := NewRouter(newDir(t))
	dec, err := r.Resolve(context.Background(), ResolveInput{AccountID: "nobody", Host: "os.vulos.org", OSHost: "os.vulos.org"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != DecideNoBox {
		t.Fatalf("kind = %v, want no_box", dec.Kind)
	}
}

func TestResolve_Apex_OrgWithNoBox(t *testing.T) {
	d := newDir(t)
	d.AddMember(acctSolo, orgAcme, "owner")
	r := NewRouter(d)
	dec, err := r.Resolve(context.Background(), ResolveInput{AccountID: acctSolo, Host: "os.vulos.org", OSHost: "os.vulos.org"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != DecideNoBox || dec.Org.ID != orgAcme {
		t.Fatalf("kind=%v org=%q, want no_box acme", dec.Kind, dec.Org.ID)
	}
}

func TestResolve_OrgHandle_SkipsChooser(t *testing.T) {
	d := newDir(t)
	d.AddMember(acctUser, orgAcme, "owner")
	d.AddMember(acctUser, orgBeta, "member") // multi-org, but direct handle pins one
	d.AddBox(Box{ID: ulidA, OrgID: orgAcme, Healthy: true})
	r := NewRouter(d)
	dec, err := r.Resolve(context.Background(), ResolveInput{AccountID: acctUser, Host: "acme.os.vulos.org", OSHost: "os.vulos.org"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != DecideRoute || dec.Org.ID != orgAcme {
		t.Fatalf("kind=%v org=%q, want route acme", dec.Kind, dec.Org.ID)
	}
}

func TestResolve_OrgHandle_UnknownNotFound(t *testing.T) {
	d := newDir(t)
	d.AddMember(acctUser, orgAcme, "owner")
	r := NewRouter(d)
	dec, err := r.Resolve(context.Background(), ResolveInput{AccountID: acctUser, Host: "ghost.os.vulos.org", OSHost: "os.vulos.org"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != DecideNotFound {
		t.Fatalf("kind = %v, want not_found", dec.Kind)
	}
}

func TestResolve_BoxHandle_Direct(t *testing.T) {
	d := newDir(t)
	d.AddMember(acctSolo, orgAcme, "owner")
	d.AddBox(Box{ID: ulidA, OrgID: orgAcme, Healthy: true})
	r := NewRouter(d)
	dec, err := r.Resolve(context.Background(), ResolveInput{AccountID: acctSolo, Host: ulidA + ".os.vulos.org", OSHost: "os.vulos.org"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != DecideRoute || dec.Box.ID != ulidA {
		t.Fatalf("kind=%v box=%q, want route %q", dec.Kind, dec.Box.ID, ulidA)
	}
}
