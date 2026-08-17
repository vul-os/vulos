package lan

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func mustAddr(s string) netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil {
		panic(err)
	}
	return a
}

// These guards exist because of three MEASURED defects, all with one root
// cause: the advertised name and the certificate's SAN list were separate
// hard-coded literals that could — and did — disagree. See names.go's header
// for the measurements. Anything that lets them drift apart again must go red
// here.

// TestEveryAdvertisedNameIsInTheCertificate is the anti-drift guard.
//
// If the box answers to a name its certificate does not carry, the user gets a
// NAME MISMATCH warning on top of the unknown-issuer one — the exact failure
// avahi's "retrying with vulos-2" rename produced on the bare-metal path, where
// vulos-2.local was in nobody's SAN list.
func TestEveryAdvertisedNameIsInTheCertificate(t *testing.T) {
	for _, tc := range []struct{ id, host string }{
		{"01HZZZZZZZZZZZZZZZZZK3N7Q2", ""},
		{"01HZZZZZZZZZZZZZZZZZK3N7Q2", "study"},
		{"01HAAAAAAAAAAAAAAAAAB4M8R3", "kitchen-box"},
		{"", ""},
	} {
		ns := NewNameSet(tc.id, tc.host)
		if len(ns.MDNS) == 0 {
			t.Fatalf("NewNameSet(%q,%q) advertises nothing", tc.id, tc.host)
		}
		sans := make(map[string]bool, len(ns.DNSNames))
		for _, n := range ns.DNSNames {
			sans[n] = true
		}
		for _, n := range ns.MDNS {
			if !sans[n] {
				t.Errorf("NewNameSet(%q,%q): advertises %q over mDNS but it is NOT a certificate SAN (%v)", tc.id, tc.host, n, ns.DNSNames)
			}
		}
	}
}

// TestTwoBoxesGetDifferentNames is the collision guard.
//
// MEASURED: two boxes, both /etc/hostname="vulos", both advertising the
// identical vulos.local, ten lookups from a third host on the same link
// returned .6 .5 .5 .5 .5 .5 .5 .5 .6 .5 — the client reached a RANDOM box, and
// since both certs carried vulos.local, TLS succeeded without a warning.
//
// The fix is that the DEFAULT name is per-instance, so two boxes out of the box
// never want the same name in the first place.
func TestTwoBoxesGetDifferentNames(t *testing.T) {
	a := NewNameSet("01HZZZZZZZZZZZZZZZZZK3N7Q2", "")
	b := NewNameSet("01HAAAAAAAAAAAAAAAAAB4M8R3", "")

	if a.Hostname == b.Hostname {
		t.Fatalf("two boxes defaulted to the SAME hostname %q — this is the collision", a.Hostname)
	}
	if a.Unique == b.Unique {
		t.Fatalf("two boxes derived the same unique name %q", a.Unique)
	}
	if a.Hostname == GenericHostname {
		t.Fatalf("the default hostname is the generic %q; every box would claim it again", GenericHostname)
	}
	if a.MDNS[0] == b.MDNS[0] {
		t.Fatalf("two boxes lead with the same mDNS name %q", a.MDNS[0])
	}
	// Each box's leading name must still be a legal, resolvable-looking label.
	for _, ns := range []NameSet{a, b} {
		if !strings.HasSuffix(ns.MDNS[0], ".local") {
			t.Errorf("leading mDNS name %q does not end in .local", ns.MDNS[0])
		}
		if !ValidHostnameLabel(strings.TrimSuffix(ns.MDNS[0], ".local")) {
			t.Errorf("leading mDNS name %q is not a valid label", ns.MDNS[0])
		}
	}
}

// TestUniqueNameAlwaysPresent: even when the owner picks a friendly name, the
// collision-free per-instance name must remain advertised, so two boxes that
// both got named "study" still each have a name that works.
func TestUniqueNameAlwaysPresent(t *testing.T) {
	id := "01HZZZZZZZZZZZZZZZZZK3N7Q2"
	ns := NewNameSet(id, "study")
	if ns.Hostname != "study" {
		t.Fatalf("Hostname = %q, want %q", ns.Hostname, "study")
	}
	want := DefaultHostname(id)
	if ns.Unique != want {
		t.Fatalf("Unique = %q, want %q", ns.Unique, want)
	}
	if !contains(ns.MDNS, want+".local") {
		t.Fatalf("MDNS %v does not include the collision-free name %q", ns.MDNS, want+".local")
	}
	if !contains(ns.DNSNames, want+".local") {
		t.Fatalf("DNSNames %v does not include the collision-free name", ns.DNSNames)
	}
}

// TestGenericNameIsLast pins the ordering the conflict probe depends on: the
// generic vulos.local is the one most likely to be taken, so it must be claimed
// last, after the names that are ours by construction.
func TestGenericNameIsLast(t *testing.T) {
	ns := NewNameSet("01HZZZZZZZZZZZZZZZZZK3N7Q2", "study")
	if got := ns.MDNS[len(ns.MDNS)-1]; got != GenericHostname+".local" {
		t.Fatalf("last mDNS name is %q, want %q (the generic name must be claimed last)", got, GenericHostname+".local")
	}
	if ns.MDNS[0] != "study.local" {
		t.Fatalf("first mDNS name is %q, want the owner's choice %q", ns.MDNS[0], "study.local")
	}
}

// TestNameSetRejectsGarbageHostnames: an unusable name must never reach a
// certificate or an mDNS record. "vulos\n" is not hypothetical — it is what
// cmd/init installed as the system hostname on every bare-metal box until
// 2026-08-17, and os.Hostname() is where config.Hostname comes from.
func TestNameSetRejectsGarbageHostnames(t *testing.T) {
	id := "01HZZZZZZZZZZZZZZZZZK3N7Q2"
	fallback := DefaultHostname(id)

	// A whitespace-polluted but otherwise valid name is RECOVERED, never
	// propagated raw. This is the cmd/init "vulos\n" value arriving via
	// os.Hostname() -> config.Hostname.
	if got := NewNameSet(id, "vulos\n").Hostname; got != "vulos" {
		t.Fatalf("NewNameSet(id, \"vulos\\n\").Hostname = %q, want %q", got, "vulos")
	}
	for _, n := range NewNameSet(id, "vulos\n").DNSNames {
		if strings.ContainsAny(n, " \t\r\n\x00") {
			t.Fatalf("the raw \"vulos\\n\" reached the certificate SAN %q", n)
		}
	}

	// NOTE: "vulos\n" is NOT in this list. SanitizeHostname trims it to the
	// perfectly good name "vulos" — recovering the owner's intent from a
	// whitespace-polluted value is the point of sanitising, and
	// TestSanitizeHostnameTakesFirstLabel pins that. What must never happen is
	// the RAW value reaching a name, which the loop body below checks.
	for _, bad := range []string{"", "   ", "\n", "-nope", "nope-", "has space", "under_score", "ünïcode", strings.Repeat("a", 64), "vulos\x00", "vulos bad"} {
		ns := NewNameSet(id, bad)
		if ns.Hostname != fallback {
			t.Errorf("NewNameSet(id, %q).Hostname = %q, want the per-instance fallback %q", bad, ns.Hostname, fallback)
		}
		for _, n := range append(append([]string{}, ns.MDNS...), ns.DNSNames...) {
			if strings.ContainsAny(n, " \t\r\n\x00_") {
				t.Errorf("NewNameSet(id, %q) produced the malformed name %q", bad, n)
			}
		}
	}
}

// TestSanitizeHostnameTakesFirstLabel: /etc/hostname sometimes holds an FQDN.
func TestSanitizeHostnameTakesFirstLabel(t *testing.T) {
	cases := map[string]string{
		"vulos":                 "vulos",
		"VULOS":                 "vulos",
		"  Study  ":             "study",
		"study.local":           "study",
		"study.home.arpa":       "study",
		"vulos\n":               "vulos",
		"box-1.lan":             "box-1",
		"":                      "",
		".local":                "",
		"-bad":                  "",
		"a_b":                   "",
		strings.Repeat("a", 64): "",
	}
	for in, want := range cases {
		if got := SanitizeHostname(in); got != want {
			t.Errorf("SanitizeHostname(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestShortIDIsStableAndLabelSafe: the per-box suffix must be deterministic
// (the cert SAN and the mDNS record are derived independently and must match)
// and always a legal DNS label fragment.
func TestShortIDIsStableAndLabelSafe(t *testing.T) {
	id := "01HZZZZZZZZZZZZZZZZZK3N7Q2"
	a, b := ShortID(id), ShortID(id)
	if a != b {
		t.Fatalf("ShortID is not deterministic: %q vs %q", a, b)
	}
	if len(a) != shortIDLen {
		t.Fatalf("ShortID(%q) = %q, want %d chars", id, a, shortIDLen)
	}
	for _, in := range []string{"", "x", "01HZZZ", strings.ToLower(id), "with-hyphens-and.dots"} {
		got := ShortID(in)
		if len(got) != shortIDLen {
			t.Errorf("ShortID(%q) = %q, want %d chars", in, got, shortIDLen)
		}
		if !ValidHostnameLabel(GenericHostname + "-" + got) {
			t.Errorf("ShortID(%q) = %q makes an invalid label", in, got)
		}
	}
}

// TestDNSNamesCoverTheNoMDNSFallbacks: the router-served short forms and the
// box.<id> name must all be in the certificate, because they are the paths a
// client that cannot do mDNS (Android's browser) has to use.
func TestDNSNamesCoverTheNoMDNSFallbacks(t *testing.T) {
	id := "01HZZZZZZZZZZZZZZZZZK3N7Q2"
	ns := NewNameSet(id, "study")
	for _, want := range []string{
		"study", "study.local", "study.lan", "study.home.arpa",
		"vulos", "vulos.local",
		BoxHostname(id),
	} {
		if !contains(ns.DNSNames, want) {
			t.Errorf("DNSNames %v is missing %q", ns.DNSNames, want)
		}
	}
	// No duplicates: a duplicate SAN is a derivation bug.
	seen := map[string]bool{}
	for _, n := range ns.DNSNames {
		if seen[n] {
			t.Errorf("DNSNames contains %q twice: %v", n, ns.DNSNames)
		}
		seen[n] = true
	}
}

// TestNewNameSetIsPureAndOrdered: two calls with the same inputs must produce
// byte-identical output, because the certificate re-mint check compares the SAN
// list for equality — an unstable derivation would re-mint on every handshake.
func TestNewNameSetIsPureAndOrdered(t *testing.T) {
	for i := 0; i < 20; i++ {
		a := NewNameSet("01HZZZZZZZZZZZZZZZZZK3N7Q2", "study")
		b := NewNameSet("01HZZZZZZZZZZZZZZZZZK3N7Q2", "study")
		if !equalStrings(a.DNSNames, b.DNSNames) {
			t.Fatalf("DNSNames is not deterministic:\n%v\n%v", a.DNSNames, b.DNSNames)
		}
		if !equalStrings(a.MDNS, b.MDNS) {
			t.Fatalf("MDNS is not deterministic:\n%v\n%v", a.MDNS, b.MDNS)
		}
	}
}

// TestServiceSetHostnameRederivesNames pins the live rename: the name set the
// box reports must change immediately, not on the next reboot.
//
// The "reports success but nothing changed" no-op is the failure mode this
// project keeps finding, so a rename that leaves Names() untouched is red.
func TestServiceSetHostnameRederivesNames(t *testing.T) {
	id := "01HZZZZZZZZZZZZZZZZZK3N7Q2"
	svc, err := New(Config{
		InstanceID:  id,
		CertSource:  NewSelfSignedCertSource(nil, nil),
		Handler:     nopHandler{},
		HTTPSAddr:   "127.0.0.1:0",
		DNSAddr:     "127.0.0.1:0",
		LANIP:       net.IPv4(127, 0, 0, 1),
		DisableMDNS: true,
		DisableDNS:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	before := svc.Names()
	if before.Hostname != DefaultHostname(id) {
		t.Fatalf("initial Hostname = %q, want the per-instance default %q", before.Hostname, DefaultHostname(id))
	}

	after, err := svc.SetHostname("Study")
	if err != nil {
		t.Fatalf("SetHostname: %v", err)
	}
	if after.Hostname != "study" {
		t.Fatalf("after rename Hostname = %q, want %q", after.Hostname, "study")
	}
	if svc.Names().Hostname != "study" {
		t.Fatalf("Names() still reports %q after a rename to study — the rename was a silent no-op", svc.Names().Hostname)
	}
	if !contains(svc.Names().MDNS, "study.local") {
		t.Fatalf("after rename MDNS = %v, missing study.local", svc.Names().MDNS)
	}
	if !contains(svc.Names().DNSNames, "study.local") {
		t.Fatalf("after rename the certificate SANs %v do not carry study.local — renaming the box would break TLS", svc.Names().DNSNames)
	}
	// The collision-free name must survive a rename.
	if !contains(svc.Names().MDNS, DefaultHostname(id)+".local") {
		t.Fatalf("rename dropped the per-instance name from %v", svc.Names().MDNS)
	}

	if _, err := svc.SetHostname("not a hostname"); err == nil {
		t.Fatal("SetHostname accepted an invalid label")
	}
	if svc.Names().Hostname != "study" {
		t.Fatalf("a rejected rename still mutated the name set to %q", svc.Names().Hostname)
	}
}

// TestMDNSAdvertiserDropsAClaimedName is the conflict guard.
//
// pion/mdns claims every LocalName unconditionally, which is what made two
// boxes both answer for vulos.local. The advertiser now probes first; a name
// somebody else answers must NOT end up in the claim.
func TestMDNSAdvertiserDropsAClaimedName(t *testing.T) {
	orig := probeConflict
	t.Cleanup(func() { probeConflict = orig })

	probeConflict = func(name string, _ net.IP) (bool, string) {
		return name == "vulos.local", "192.168.1.9"
	}

	m, err := newMDNSAdvertiser(net.IPv4(192, 168, 1, 42), []string{"vulos-k3n7q2.local", "vulos.local"})
	if err != nil {
		t.Skipf("mDNS multicast unavailable in this environment: %v", err)
	}
	defer m.Close()

	got := m.Names()
	if contains(got, "vulos.local") {
		t.Fatalf("claimed %v — vulos.local was already answered on the link and must not be claimed a second time", got)
	}
	if !contains(got, "vulos-k3n7q2.local") {
		t.Fatalf("claimed %v — the uncontested per-instance name must still be claimed", got)
	}
}

// TestMDNSAdvertiserFailsWhenEveryNameIsTaken: if literally every name is
// claimed elsewhere we must report it, not silently advertise nothing.
func TestMDNSAdvertiserFailsWhenEveryNameIsTaken(t *testing.T) {
	orig := probeConflict
	t.Cleanup(func() { probeConflict = orig })
	probeConflict = func(string, net.IP) (bool, string) { return true, "192.168.1.9" }

	if _, err := newMDNSAdvertiser(net.IPv4(192, 168, 1, 42), []string{"a.local", "b.local"}); err == nil {
		t.Fatal("expected an error when every requested name is already claimed")
	}
}

// TestIsSelfAddrDoesNotYieldToOurselves: the box's own avahi-daemon (started by
// cmd/init on the bare-metal image) answers for the same host at the same
// address. Treating that as a foreign claimant would make the box abandon its
// own name.
func TestIsSelfAddrDoesNotYieldToOurselves(t *testing.T) {
	self := net.IPv4(192, 168, 1, 42)
	if !isSelfAddr(mustAddr("192.168.1.42"), self) {
		t.Error("our own LAN IP was treated as a foreign claimant")
	}
	if !isSelfAddr(mustAddr("127.0.0.1"), self) {
		t.Error("loopback was treated as a foreign claimant")
	}
	if isSelfAddr(mustAddr("192.168.1.99"), self) {
		t.Error("a different host's address was treated as ours — a real conflict would be ignored")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

type nopHandler struct{}

func (nopHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}
