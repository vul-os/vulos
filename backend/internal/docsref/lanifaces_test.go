package docsref

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ─── Does the box's own name resolve to the box? ─────────────────────────────
//
// THE DEFECT, measured 2026-08-17 on a booted arm64 box with an app running:
//
//	avahi-resolve -n vulos.local  ->  169.254.23.36
//	avahi-resolve -a 10.0.2.15    ->  vulos.local
//
// The box was 10.0.2.15. 169.254.23.36 was an IPv4LL address dhcpcd had put on
// `vh_bae456` — the host side of ONE APPLICATION's veth pair, created by
// backend/services/appnet/namespace.go. So the box answered for its own name
// with an address that belongs to an application's private point-to-point link
// and appears in no certificate's SAN list. internal/lan/names.go is the single
// derivation feeding both the mDNS advertisement and the certificate SANs
// precisely so those two can never disagree — but avahi-daemon is a THIRD
// publisher, installed by the image and started by systemd, that derives its
// answer from neither.
//
// The image configured neither daemon: no /etc/dhcpcd.conf stanza and no
// /etc/avahi/avahi-daemon.conf change, so both ran on Debian defaults, and
// Debian's dhcpcd.service is literally "DHCP Client Daemon on all interfaces".
//
// THIS FILE IS THE STATIC HALF. scripts/smoke-lan-name.sh (LANNAME-01) is the
// behavioural half: it runs the real daemons against an appnet-shaped veth in a
// container and asks avahi what the box's name resolves to. That gate proves
// the CONFIG works; these tests prove the config is REACHED — that build.sh
// installs it on both paths, and that the one list of "what is an app link"
// still describes the interfaces appnet actually creates.
//
// Neither half boots a Vulos image. See roadmap/LAN-NAME-RESOLUTION.md.

const lanIfacesScript = "scripts/vulos-lan-ifaces.sh"

// appIfaceGlobs returns APP_IFACE_GLOBS as the shipping script itself reports
// it — by RUNNING the script, not by re-parsing its source. A test that parsed
// the assignment would agree with a script whose --list-app-globs had stopped
// working, which is the exact shape of the "guard that checks nothing" this
// suite keeps finding.
func appIfaceGlobs(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(repoRoot, lanIfacesScript)
	out, err := exec.Command("sh", path, "--list-app-globs").Output()
	if err != nil {
		t.Fatalf("%s --list-app-globs: %v", lanIfacesScript, err)
	}
	globs := strings.Fields(string(out))
	if len(globs) == 0 {
		t.Fatalf("%s reported an EMPTY app-interface glob list; every assertion below "+
			"would be vacuously satisfied by it", lanIfacesScript)
	}
	return globs
}

// globMatches is the same matching rule the script's `case` uses, restricted to
// the trailing-`*` and exact forms the glob list actually contains.
func globMatches(glob, name string) bool {
	if strings.HasSuffix(glob, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(glob, "*"))
	}
	return glob == name
}

// TestAppIfaceGlobsCoverAppnet pins the shell script's idea of an app link to
// the names appnet actually creates.
//
// WHY: the glob list and the veth names are two declarations of one fact, in
// two languages, in two directories. Renaming appnet's veths — a one-character
// edit in a file this test does not own — would silently re-open the defect,
// because dhcpcd would resume managing the new names and avahi would resume
// publishing them as the box's identity, and NOTHING else in the tree looks at
// either daemon.
func TestAppIfaceGlobsCoverAppnet(t *testing.T) {
	src := readRepoFile(t, "backend/services/appnet/namespace.go")

	// The two veth names appnet builds, e.g.
	//   VethHost: fmt.Sprintf("vh_%s", shortID),
	//   VethNS:   fmt.Sprintf("vn_%s", shortID),
	re := regexp.MustCompile(`Veth(?:Host|NS):\s*fmt\.Sprintf\("([^"%]*)%s"`)
	ms := re.FindAllStringSubmatch(src, -1)

	// COVERAGE COUNT. appnet creates exactly two veth interface names, and both
	// briefly exist in the host's namespace (VethNS is created host-side and
	// then moved, namespace.go's "move veth to ns" step). If this regex ever
	// matches a different number, the parse has drifted and the assertions
	// below are checking fewer things than this test's name claims.
	const wantVethNames = 2
	if len(ms) != wantVethNames {
		t.Fatalf("parsed %d veth name constructions out of appnet/namespace.go, want exactly %d.\n"+
			"The matcher has drifted from the source it pins, so it can no longer tell\n"+
			"whether %s still covers the interfaces appnet creates. Fix the matcher —\n"+
			"do NOT lower this count.", len(ms), wantVethNames, lanIfacesScript)
	}

	globs := appIfaceGlobs(t)
	for _, m := range ms {
		prefix := m[1] // "vh_" / "vn_"
		// A concrete name of the shape appnet would produce.
		sample := prefix + "a1b2c3"
		covered := false
		for _, g := range globs {
			if globMatches(g, sample) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("appnet creates interfaces named %q (e.g. %s) and %s's "+
				"APP_IFACE_GLOBS %v does not cover them.\n"+
				"dhcpcd will DHCP on that link — inside an untrusted app namespace, and it\n"+
				"accepts a default route from whatever answers — and avahi will publish its\n"+
				"address as the box's own name. Add the pattern; do not narrow this test.",
				prefix+"*", sample, lanIfacesScript, globs)
		}
	}
}

// TestAppIfaceGlobsMatchGo pins the SHELL list of app interfaces to the GO list.
//
// Two declarations of one fact, in two languages, for two consumers that both
// have to be right: the shell half configures dhcpcd and avahi before this
// process exists, and the Go half (internal/lan's isAppIface) decides which
// address pion advertises to the LAN and which goes into the certificate's IP
// SAN. Sharing one definition is impossible — the shell half runs as an
// ExecStartPre with no Go available — so they are pinned instead of trusted.
//
// A box where these disagree is a box where avahi says one thing and the
// certificate says another, which is precisely the class of defect
// internal/lan/names.go was created to end.
func TestAppIfaceGlobsMatchGo(t *testing.T) {
	shell := appIfaceGlobs(t)

	src := readRepoFile(t, "backend/internal/lan/lan.go")
	prefixes := goStringList(t, src, "appIfacePrefixes")
	names := goStringList(t, src, "appIfaceNames")

	// The Go halves, rendered in the shell's glob vocabulary.
	var fromGo []string
	for _, p := range prefixes {
		fromGo = append(fromGo, p+"*")
	}
	fromGo = append(fromGo, names...)

	// COVERAGE COUNT. Both sides must be non-trivial: an empty Go list would
	// make every comparison below vacuous, and an empty shell list is already
	// rejected in appIfaceGlobs.
	if len(fromGo) < 2 {
		t.Fatalf("parsed only %d app-interface patterns out of internal/lan/lan.go (%v); "+
			"the parse has drifted and this comparison proves nothing", len(fromGo), fromGo)
	}

	shellSet := map[string]bool{}
	for _, g := range shell {
		shellSet[g] = true
	}
	goSet := map[string]bool{}
	for _, g := range fromGo {
		goSet[g] = true
	}
	for _, g := range fromGo {
		if !shellSet[g] {
			t.Errorf("internal/lan treats %q as an app interface and %s does not.\n"+
				"dhcpcd and avahi would keep using that link while the advertiser and the "+
				"certificate ignore it.", g, lanIfacesScript)
		}
	}
	for _, g := range shell {
		if !goSet[g] {
			t.Errorf("%s treats %q as an app interface and internal/lan does not.\n"+
				"pion would advertise its address to the LAN, and certIPs would put it in the "+
				"certificate, while avahi correctly refuses to.", lanIfacesScript, g)
		}
	}
}

// goStringList extracts the elements of a `name = []string{...}` declaration.
func goStringList(t *testing.T, src, name string) []string {
	t.Helper()
	re := regexp.MustCompile(name + `\s*=\s*\[\]string\{([^}]*)\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("internal/lan/lan.go no longer declares %s as a []string literal; "+
			"this check has lost its subject", name)
	}
	var out []string
	for _, raw := range strings.Split(m[1], ",") {
		v := strings.Trim(strings.TrimSpace(raw), `"`)
		if v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		t.Fatalf("parsed %s as EMPTY; every comparison against it would be vacuous", name)
	}
	return out
}

// TestLANIfacesReachesBothBuildPaths asserts build.sh actually installs and
// wires the script — on the image path AND the ssh-deploy path.
//
// WHY BOTH: build.sh maintains two independent package lists that install
// avahi-daemon and dhcpcd5 (PACKAGE-SET: deploy at ~line 324 and the rootfs
// list at ~line 775), so both kinds of box had this defect. A fix applied to
// one of them is the "collection ≠ execution" failure this repo has hit before:
// the sweep looked complete and half the fleet was untouched.
func TestLANIfacesReachesBothBuildPaths(t *testing.T) {
	build := readRepoFile(t, "build.sh")

	// COVERAGE COUNT — over CODE, not prose. The first version of this counted
	// every occurrence in the file and got 11, because the comments explaining
	// the fix name the script too. A count that moves when someone edits a
	// comment is not a coverage assertion, it is a tripwire on the wrong thing.
	// So: comment lines are dropped first, and what remains is the wiring.
	//
	// Nine executable references, five on the deploy path and four on the image
	// path, each one a step that has to exist for the fix to reach a box:
	//   deploy  scp source, scp destination, chmod, ExecStartPre, --dhcpcd run
	//   image   install source, install destination, --dhcpcd run, ExecStartPre
	// The number is the claim that BOTH paths are wired; a fix that reached
	// only one of them drops it.
	const wantRefs = 9
	refs := 0
	for _, line := range strings.Split(build, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		refs += strings.Count(line, "vulos-lan-ifaces")
	}
	if refs != wantRefs {
		t.Fatalf("build.sh has %d executable references to vulos-lan-ifaces, want exactly %d.\n"+
			"Either a build path lost its LAN-identity config, or one was added and this\n"+
			"count was not updated to say which. Read the diff — do not just change the\n"+
			"number to match.", refs, wantRefs)
	}

	// Each half of the fix, on each path.
	checks := []struct {
		what string
		want string
		why  string
	}{
		{
			what: "image path installs the script",
			want: `"$ROOTFS/usr/local/bin/vulos-lan-ifaces"`,
			why:  "a booted image has no way to compute its own LAN interface set",
		},
		{
			what: "image path applies the dhcpcd deny stanza",
			want: `VULOS_DHCPCD_CONF="$ROOTFS/etc/dhcpcd.conf"`,
			why:  "dhcpcd would keep IPv4LL-addressing app veths and DHCPing inside app namespaces",
		},
		{
			what: "image path installs the avahi ExecStartPre drop-in",
			want: "avahi-daemon.service.d/vulos-lan-only.conf",
			why:  "avahi would keep publishing app-link addresses as the box's own name",
		},
		{
			what: "deploy path copies the script",
			want: `"$DEPLOY_HOST:/usr/local/bin/vulos-lan-ifaces"`,
			why:  "a deployed box installs the same avahi + dhcpcd and had the same defect",
		},
		{
			what: "deploy path applies the dhcpcd deny stanza",
			want: "/usr/local/bin/vulos-lan-ifaces --dhcpcd",
			why:  "same reason as the image path",
		},
	}
	for _, c := range checks {
		if !strings.Contains(build, c.want) {
			t.Errorf("build.sh: %s — expected to find %q.\nWithout it: %s.", c.what, c.want, c.why)
		}
	}

	// The avahi hook must be an ExecStartPre, not a one-shot at install time.
	// Which interfaces are LAN interfaces is only knowable on a booted box, and
	// app veths appear and vanish as apps start; a list computed once at image
	// build would be computed on the BUILD HOST, which is a developer Mac.
	const hook = "ExecStartPre=/usr/local/bin/vulos-lan-ifaces --avahi"
	if n := strings.Count(build, hook); n != 2 {
		t.Errorf("build.sh has %d %q lines, want 2 (image drop-in + deploy drop-in).\n"+
			"An allow-list computed anywhere other than at avahi start is computed on the\n"+
			"wrong machine or at the wrong time.", n, hook)
	}
}

// TestLANIfacesFailsOpen pins the deliberate fail-open: a box whose interfaces
// this script cannot classify must keep an UNRESTRICTED avahi, not an empty
// allow-list.
//
// WHY THIS IS A TEST AND NOT A COMMENT: the natural "hardening" edit — always
// write allow-interfaces= — turns an unfamiliar NIC naming scheme into a box
// with no mDNS name at all. That is a worse failure than the one being fixed,
// it would only appear on hardware nobody here owns, and it is exactly the
// fail-closed reflex this codebase applies correctly elsewhere and must not
// apply here.
func TestLANIfacesFailsOpen(t *testing.T) {
	dir := t.TempDir()

	// A sysfs with nothing but loopback and app links: no LAN interface at all.
	sysnet := filepath.Join(dir, "net")
	for _, n := range []string{"lo", "vh_bae456", "vn_bae456"} {
		mustMkdirAll(t, filepath.Join(sysnet, n))
	}

	conf := filepath.Join(dir, "avahi-daemon.conf")
	writeRepoTemp(t, conf, "[server]\nuse-ipv4=yes\nuse-ipv6=yes\n")

	runLANIfaces(t, sysnet, conf, "--avahi")

	got := readFileString(t, conf)
	if strings.Contains(got, "allow-interfaces=") {
		t.Fatalf("with no identifiable LAN interface the script wrote an allow-interfaces "+
			"line:\n%s\nAn empty or bogus allow-list leaves the box with NO mDNS name. "+
			"Fail open here.", got)
	}

	// And the positive case, so this test cannot pass by the script being inert.
	mustMkdirAll(t, filepath.Join(sysnet, "eth0"))
	runLANIfaces(t, sysnet, conf, "--avahi")
	got = readFileString(t, conf)
	if !strings.Contains(got, "allow-interfaces=eth0") {
		t.Fatalf("with eth0 present the script did not restrict avahi to it:\n%s\n"+
			"If this half never fires, the fail-open assertion above proves nothing.", got)
	}
	// The app links must not be in it. Parse the VALUE — a substring search over
	// the whole file reports "lo" as present because it occurs inside the word
	// "allow-interfaces", which made the first version of this loop fail on a
	// correct config and would equally have passed a wrong one.
	allowed := map[string]bool{}
	for _, line := range strings.Split(got, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "allow-interfaces="); ok {
			for _, n := range strings.Split(v, ",") {
				allowed[strings.TrimSpace(n)] = true
			}
		}
	}
	if len(allowed) != 1 || !allowed["eth0"] {
		t.Fatalf("parsed allow-interfaces as %v, want exactly {eth0}; the parse is wrong "+
			"so the exclusions below prove nothing:\n%s", allowed, got)
	}
	for _, bad := range []string{"vh_bae456", "vn_bae456", "lo"} {
		if allowed[bad] {
			t.Errorf("allow-interfaces includes %q; that is how the box came to publish an "+
				"app link's address as its own name:\n%s", bad, got)
		}
	}
}

func runLANIfaces(t *testing.T, sysnet, avahiConf string, args ...string) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(repoRoot, lanIfacesScript)}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"VULOS_SYSNET_ROOT="+sysnet,
		"VULOS_AVAHI_CONF="+avahiConf,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", lanIfacesScript, args, err, out)
	}
}

func mustMkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func writeRepoTemp(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func readFileString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}
