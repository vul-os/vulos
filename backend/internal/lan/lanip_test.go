package lan

import (
	"net"
	"testing"
)

// ─── The address the box tells the LAN it lives at ───────────────────────────
//
// detectLANIP is not a local convenience. Its result is
//
//	published to the WHOLE LAN   mdns.go, cfg.LocalAddress — one address for the
//	                             whole process, answered on every interface
//	put in the certificate       cmd/server/lan_pairing.go, certIPs()
//	bound by the HTTPS listener  lan.go, lanBindAddr
//
// so a wrong value tells every client on the link to go somewhere it cannot
// reach, hands it a certificate that does not name that address, and binds the
// listener where nobody is looking. Three failures from one number.
//
// On 2026-08-17 a booted box was measured answering `avahi-resolve -n
// vulos.local` with 169.254.23.36 — an address on ONE APPLICATION's veth.
// That came from avahi (fixed in scripts/vulos-lan-ifaces.sh), but the same
// address could reach the LAN through THIS function by two other routes, and
// nothing asserted otherwise. These are those routes.

// TestPickLANIPNeverReturnsAnAppAddress is the table. Every case is a real
// interface layout, and the two marked MEASURED are the ones that were observed
// on hardware rather than imagined.
func TestPickLANIPNeverReturnsAnAppAddress(t *testing.T) {
	lo := ifaceAddr{"lo", net.ParseIP("127.0.0.1")}

	cases := []struct {
		name   string
		dialed net.IP
		addrs  []ifaceAddr
		want   string
		why    string
	}{
		{
			name:   "ordinary box: routing probe wins",
			dialed: net.ParseIP("10.0.2.15"),
			addrs:  []ifaceAddr{lo, {"eth0", net.ParseIP("10.0.2.15")}},
			want:   "10.0.2.15",
			why:    "the common case must keep working, or the rest of this table proves nothing",
		},
		{
			name:   "MEASURED: dhcpcd put a default route on an app veth",
			dialed: net.ParseIP("169.254.23.36"),
			addrs: []ifaceAddr{
				lo,
				{"eth0", net.ParseIP("10.0.2.15")},
				{"vh_bae456", net.ParseIP("10.200.23.1")},
				{"vh_bae456", net.ParseIP("169.254.23.36")},
			},
			want: "10.0.2.15",
			why: "dhcpcd was logged doing exactly this — 'vh_bae456: adding default route' — " +
				"and while that route is up the kernel hands the probe an app link's link-local",
		},
		{
			name:   "MEASURED: appnet's private range must not win the fallback",
			dialed: nil,
			addrs: []ifaceAddr{
				lo,
				{"vh_bae456", net.ParseIP("10.200.23.1")},
				{"eth0", net.ParseIP("192.168.1.50")},
			},
			want: "192.168.1.50",
			why: "10.200.0.0/16 is private, so the old 'first private IPv4' rule returned it " +
				"whenever interface ordering put an app veth first",
		},
		{
			name:   "app veth listed first, no routing answer",
			dialed: nil,
			addrs: []ifaceAddr{
				{"vh_a1b2c3", net.ParseIP("10.200.7.1")},
				{"vn_a1b2c3", net.ParseIP("10.200.7.2")},
				{"wlan0", net.ParseIP("172.16.4.9")},
			},
			want: "172.16.4.9",
			why:  "interface index order is start-up order; it must not decide the box's identity",
		},
		{
			name:   "the legacy bridge name is an app link too",
			dialed: nil,
			addrs: []ifaceAddr{
				lo,
				{"vulos-br0", net.ParseIP("10.200.1.1")},
				{"end0", net.ParseIP("192.168.8.2")},
			},
			want: "192.168.8.2",
			why:  "appnet's Manager is constructed with this bridge name and would use it again",
		},
		{
			name:   "a plain link-local on the LAN NIC is not an identity",
			dialed: net.ParseIP("169.254.72.86"),
			addrs: []ifaceAddr{
				lo,
				{"eth0", net.ParseIP("169.254.72.86")},
			},
			want: "127.0.0.1",
			why: "a box that got no DHCP lease has no LAN identity to publish; loopback makes " +
				"detectLANIPWaiting say so out loud instead of advertising an unroutable address",
		},
		{
			name:   "nothing but app links: loopback, loudly",
			dialed: net.ParseIP("10.200.23.1"),
			addrs: []ifaceAddr{
				lo,
				{"vh_bae456", net.ParseIP("10.200.23.1")},
			},
			want: "127.0.0.1",
			why: "advertising an app's address to the LAN is a SILENT failure; loopback is a " +
				"visible one, and detectLANIPWaiting logs it",
		},
		{
			name:   "no interfaces at all (CI, isolated container)",
			dialed: nil,
			addrs:  nil,
			want:   "127.0.0.1",
			why:    "the service must still be well-formed with no network",
		},
	}

	// COVERAGE COUNT. Every guard in this suite that carried one survived
	// mutation; every one that lacked one did not. This number is the claim
	// that the table still covers both routes an app address can reach the LAN
	// by (the routing probe and the fallback scan), both app naming forms
	// (vh_/vn_ prefix and the vulos-br0 literal), and the three ways there is
	// no answer. Deleting a case fails HERE.
	const wantCases = 8
	if len(cases) != wantCases {
		t.Fatalf("the table has %d cases, want exactly %d. A case was removed — the "+
			"scenarios are named for the defects they pin, so read which one and put it "+
			"back. Do NOT lower this number.", len(cases), wantCases)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pickLANIP(c.dialed, c.addrs)
			if got == nil || got.String() != c.want {
				t.Errorf("pickLANIP(%v, %v) = %v, want %s\nWhy this case exists: %s",
					c.dialed, c.addrs, got, c.want, c.why)
			}
		})
	}
}

// TestIsAppIfaceCoversAppnetNaming pins the classifier itself, so a table above
// that happened to pass for the wrong reason still fails here.
func TestIsAppIfaceCoversAppnetNaming(t *testing.T) {
	app := []string{"vh_bae456", "vn_bae456", "vh_a1b2c3", "vn_a1b2c3", "vulos-br0"}
	notApp := []string{"eth0", "wlan0", "end0", "enp1s0", "lo", "br0", "vhost0", "vnet0"}

	// COVERAGE COUNT — both sides. A classifier that said "true" to everything
	// would pass the first loop and fail the second, and vice versa; asserting
	// both lists are non-empty and of the expected size is what makes that
	// argument hold.
	if len(app) != 5 || len(notApp) != 8 {
		t.Fatalf("list sizes changed (%d app, %d not-app); this test's argument depends on "+
			"both sides being exercised", len(app), len(notApp))
	}

	for _, n := range app {
		if !isAppIface(n) {
			t.Errorf("isAppIface(%q) = false; appnet creates this name and its address must "+
				"never become the box's identity", n)
		}
	}
	for _, n := range notApp {
		if isAppIface(n) {
			t.Errorf("isAppIface(%q) = true; that is a LAN interface and excluding it would "+
				"leave the box with no address to publish", n)
		}
	}
}

// TestDetectLANIPUsesTheFilter is the wiring assertion: the exported entry
// point must go through pickLANIP, not around it.
//
// Without this, pickLANIP could be perfect and unreachable — the shape of a
// dozen defects already found in this suite, where the tested function was not
// the called one.
func TestDetectLANIPUsesTheFilter(t *testing.T) {
	ip := detectLANIP()
	if ip == nil {
		t.Fatal("detectLANIP returned nil; every caller dereferences it")
	}
	if ip.IsLinkLocalUnicast() {
		t.Errorf("detectLANIP returned the link-local %v. Whatever this machine's "+
			"interfaces look like, that address cannot be published as a box's identity — "+
			"it is in no certificate SAN and routes nowhere off-link.", ip)
	}
	// And on this machine, whatever it is, the result must be one of its own
	// addresses or loopback — never invented.
	if !ip.IsLoopback() {
		found := false
		for _, a := range hostIfaceAddrs() {
			if a.IP.To4() != nil && a.IP.To4().Equal(ip.To4()) {
				found = true
				if isAppIface(a.Iface) {
					t.Errorf("detectLANIP returned %v, which is on app interface %q", ip, a.Iface)
				}
			}
		}
		if !found {
			t.Errorf("detectLANIP returned %v, which is on none of this host's interfaces", ip)
		}
	}
}
