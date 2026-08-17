package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"vulos/backend/internal/lan"
)

// WIRING GUARDS. internal/lan owns the derivation; these pin that cmd/server
// actually USES it. A perfect derivation that main.go ignores is worth nothing,
// and "the library was right, the caller wasn't" is how the original defect
// happened: lan.go had a vulos.local constant AND main.go had a vulos.local
// literal AND lan_pairing.go had a third copy.

// TestLANCertSANsComeFromTheNameSet: the SAN callback the certificate source
// reads must be exactly the derived set, not a re-listed subset.
func TestLANCertSANsComeFromTheNameSet(t *testing.T) {
	const id = "01HZZZZZZZZZZZZZZZZZK3N7Q2"
	ref := newLANServiceRef(id, "study")

	want := lan.NewNameSet(id, "study").DNSNames
	got := ref.certDNSNames()

	if len(got) != len(want) {
		t.Fatalf("certDNSNames() = %v (%d), want the derived set %v (%d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("certDNSNames()[%d] = %q, want %q\ngot:  %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
	// The names the box advertises must be in there. This is the property that
	// avahi's "retrying with vulos-2" rename violated on bare metal.
	for _, n := range lan.NewNameSet(id, "study").MDNS {
		if !hasString(got, n) {
			t.Fatalf("the certificate SANs %v omit the advertised name %q", got, n)
		}
	}
}

// TestLANCertHasIPSANs is the Android/no-mDNS guard.
//
// https://<lan-ip> is the ONLY address that works on a client whose resolver
// does no mDNS. The SAN list used to be built with a nil IP argument, so that
// fallback raised a NAME MISMATCH on top of the unknown-issuer warning: two
// errors where there should be one.
func TestLANCertHasIPSANs(t *testing.T) {
	ref := newLANServiceRef("01HZZZZZZZZZZZZZZZZZK3N7Q2", "study")
	ips := ref.certIPs()
	if len(ips) == 0 {
		t.Fatal("certIPs() is empty — https://<lan-ip> would give a NAME MISMATCH warning on every client that cannot resolve .local")
	}
	var loopback bool
	for _, ip := range ips {
		if ip == nil {
			t.Fatal("certIPs() returned a nil IP")
		}
		if ip.IsLoopback() {
			loopback = true
		}
	}
	if !loopback {
		t.Errorf("certIPs() = %v, want it to include loopback so the box's own kiosk browser is covered too", ips)
	}
}

// TestLANRefFollowsALiveRename: once the service is running, the SAN callback
// must read the SERVICE's name set, so a rename reaches the certificate without
// a restart. Reading the startup config instead would freeze the SANs at boot.
func TestLANRefFollowsALiveRename(t *testing.T) {
	const id = "01HZZZZZZZZZZZZZZZZZK3N7Q2"
	ref := newLANServiceRef(id, "")

	svc, err := lan.New(lan.Config{
		InstanceID:  id,
		CertSource:  lan.NewSelfSignedCertSource(nil, nil),
		Handler:     http.NewServeMux(),
		HTTPSAddr:   "127.0.0.1:0",
		DNSAddr:     "127.0.0.1:0",
		LANIP:       net.IPv4(127, 0, 0, 1),
		DisableMDNS: true,
		DisableDNS:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref.set(svc)

	if _, err := svc.SetHostname("study"); err != nil {
		t.Fatal(err)
	}
	if !hasString(ref.certDNSNames(), "study.local") {
		t.Fatalf("after a live rename the certificate SANs are %v — they do not carry study.local, so the renamed box would serve a certificate that never mentions its own name", ref.certDNSNames())
	}
}

// TestNoHardcodedLANSANLiterals is the anti-regression guard for the root
// cause. The SAN list must be DERIVED. Re-introducing a literal name list next
// to lan.Load*CertSource is exactly how the advertisement and the certificate
// drifted apart, so a `[]string{"vulos.local"` argument in this package is red
// on sight.
func TestNoHardcodedLANSANLiterals(t *testing.T) {
	// []string{...} containing a quoted .local name, i.e. a SAN list typed by
	// hand rather than derived.
	literal := regexp.MustCompile(`\[\]string\{[^}]*"[a-z0-9.-]*\.local"`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for i, line := range strings.Split(string(b), "\n") {
			// Strip line comments: prose ABOUT the old literal (including the
			// comment in main.go explaining why it was removed) is not the
			// defect. Only code counts.
			if j := strings.Index(line, "//"); j >= 0 {
				line = line[:j]
			}
			if literal.MatchString(line) {
				t.Errorf("%s:%d has a hand-written .local SAN list — derive it from lan.NewNameSet instead:\n\t%s", e.Name(), i+1, strings.TrimSpace(line))
			}
		}
	}
	// A guard that scanned nothing is not a guard.
	if checked < 10 {
		t.Fatalf("only scanned %d non-test .go files in cmd/server; the glob is broken", checked)
	}
}

func hasString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestStartupHostnameDoesNotUndoARename pins the Docker-path startup writer.
//
// It used to be the literal "vulos", so every restart wrote "vulos" back over
// an owner's chosen name and the rename silently reverted. It also meant every
// box installed the SAME name, which is the collision that made vulos.local
// resolve to a random box.
func TestStartupHostnameDoesNotUndoARename(t *testing.T) {
	const id = "01HZZZZZZZZZZZZZZZZZK3N7Q2"

	if got := startupBoxHostname(id, "study"); got != "study" {
		t.Fatalf("startupBoxHostname(id, %q) = %q — startup would write %q over the owner's chosen name", "study", got, got)
	}
	if got := startupBoxHostname(id, "  Study  "); got != "study" {
		t.Errorf("startupBoxHostname did not sanitise: %q", got)
	}
	// The measured bare-metal value must be recovered, not propagated.
	if got := startupBoxHostname(id, "vulos\n"); got != "vulos" {
		t.Errorf("startupBoxHostname(id, %q) = %q, want %q", "vulos\n", got, "vulos")
	}

	// With nothing usable configured, the fallback must be PER-INSTANCE. A
	// generic fallback puts every box back on the same name.
	fallback := startupBoxHostname(id, "")
	if fallback == "vulos" {
		t.Fatal("with no configured name the startup hostname falls back to the shared \"vulos\" — every box installs the same name and they collide on the LAN")
	}
	if fallback != lan.DefaultHostname(id) {
		t.Errorf("fallback = %q, want the per-instance default %q", fallback, lan.DefaultHostname(id))
	}
	other := startupBoxHostname("01HAAAAAAAAAAAAAAAAAB4M8R3", "")
	if other == fallback {
		t.Fatalf("two different boxes both fall back to %q", fallback)
	}
	if !lan.ValidHostnameLabel(fallback) {
		t.Errorf("fallback %q is not a valid hostname label", fallback)
	}
}
