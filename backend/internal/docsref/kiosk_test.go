package docsref

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The kiosk browser list exists TWICE and the copies must agree.
//
// findKioskBrowser in backend/cmd/init/main.go is the initramfs path's PID 1.
// /usr/local/bin/vulos-kiosk, written by build.sh, is the systemd path's — and
// an installed Vulos boots systemd, so that shell copy is the one a real
// machine with a monitor actually runs.
//
// Duplication was the pragmatic choice: extracting the Go logic would mean
// shipping another binary into the initramfs to serve a five-line search. The
// cost of that choice is drift, and drift here is silent in the worst way — a
// browser added to the Go list but not the script means the OS shows a blank
// screen on exactly the hardware the new entry was added for, while every test
// in the repository stays green.
//
// So the invariant is asserted instead: same browsers, same order.

var (
	// The candidate strings in findKioskBrowser's findBinary calls, bare names
	// only — the absolute /usr/bin/… variants are the same programs.
	goKioskRe = regexp.MustCompile(`findBinary\(\s*((?:"[^"]+",?\s*)+)\)`)
	// `for cand in cog chromium chromium-browser; do`
	shKioskRe = regexp.MustCompile(`for cand in ([a-z0-9 _-]+); do`)
)

func kioskBrowsersFromGo(t *testing.T) []string {
	t.Helper()
	src := readRepoFile(t, "backend/cmd/init/main.go")

	start := strings.Index(src, "func findKioskBrowser()")
	if start < 0 {
		t.Fatal("findKioskBrowser is gone from backend/cmd/init/main.go; this check has lost its subject")
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of findKioskBrowser")
	}
	body := src[start : start+end]

	var out []string
	seen := map[string]bool{}
	for _, m := range goKioskRe.FindAllStringSubmatch(body, -1) {
		for _, raw := range strings.Split(m[1], ",") {
			name := strings.Trim(strings.TrimSpace(raw), `"`)
			// Absolute paths name the same binaries as the bare candidates.
			if name == "" || strings.HasPrefix(name, "/") {
				continue
			}
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no browser candidates parsed out of findKioskBrowser; the matcher is broken " +
			"and would report agreement with an empty list")
	}
	return out
}

func kioskBrowsersFromShell(t *testing.T) []string {
	t.Helper()
	src := readRepoFile(t, "scripts/vulos-kiosk.sh")
	m := shKioskRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("build.sh no longer writes a `for cand in …` browser search into " +
			"/usr/local/bin/vulos-kiosk; the systemd path has no kiosk")
	}
	return strings.Fields(m[1])
}

// unitBlock returns the heredoc body build.sh writes for a named systemd unit,
// so an assertion about one unit cannot be satisfied by a different one.
func unitBlock(t *testing.T, src, unit string) string {
	t.Helper()
	marker := "/etc/systemd/system/" + unit
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("build.sh no longer writes %s", unit)
	}
	rest := src[i:]
	end := strings.Index(rest, "\nEOF\n")
	if end < 0 {
		t.Fatalf("could not find the end of the %s heredoc", unit)
	}
	return rest[:end]
}

func TestKioskBrowsersMatchInit(t *testing.T) {
	goList := kioskBrowsersFromGo(t)
	shList := kioskBrowsersFromShell(t)

	if strings.Join(goList, " ") != strings.Join(shList, " ") {
		t.Errorf("the two kiosk browser lists disagree.\n"+
			"  backend/cmd/init/main.go (initramfs PID 1): %v\n"+
			"  build.sh → /usr/local/bin/vulos-kiosk (systemd): %v\n"+
			"An installed box boots systemd, so the second list is what a machine with a "+
			"monitor actually runs. A browser present in one and not the other shows a "+
			"blank screen on the hardware it was added for, with every test still green.",
			goList, shList)
	}
}

// A display must get the OS, not a login prompt. The two units are mutually
// exclusive on the SAME condition, so exactly one runs on any given box.
func TestKioskAndConsoleAreMutuallyExclusiveOnADisplay(t *testing.T) {
	src := readRepoFile(t, "build.sh")

	kioskUnit := unitBlock(t, src, "vulos-kiosk.service")

	// The kiosk decides FOR ITSELF and says so. A systemd Condition would skip
	// the unit silently — booting a real image, the screen looked identical
	// whether the unit was missing, skipped or crashed, and nothing outside the
	// machine could tell which. The detection therefore has to be somewhere it
	// can log, and the log line is what this asserts.
	if !strings.Contains(readRepoFile(t, "scripts/vulos-kiosk.sh"), "no display found") {
		t.Error("vulos-kiosk no longer reports WHY it declined to start a browser. A box " +
			"that shows no OS and says nothing is indistinguishable from a broken one")
	}
	if !strings.Contains(kioskUnit, "StandardOutput=journal+console") {
		t.Error("vulos-kiosk.service no longer echoes to the console. Its whole purpose is " +
			"to be readable from outside a box that is not showing you a desktop — the one " +
			"situation where reading the journal means logging in to that box first")
	}
	if !strings.Contains(readRepoFile(t, "scripts/vulos-kiosk.sh"), "/sys/class/drm/") {
		t.Error("the display check no longer falls back to the sysfs connector state; a " +
			"DRM node can be absent while a display is attached")
	}
	// SCOPED to the kiosk unit's own block. `Restart=on-failure` appears four
	// times in build.sh, so an unscoped Contains() passed while the kiosk itself
	// said Restart=always — the mutation that proved this check was hollow.
	if !strings.Contains(kioskUnit, "Restart=on-failure") {
		t.Errorf("vulos-kiosk.service does not use Restart=on-failure — a headless box "+
			"exits 0 deliberately and would be restarted forever for deciding correctly. "+
			"Unit block:\n%s", kioskUnit)
	}
	if !strings.Contains(src, "ConditionPathExistsGlob=!/dev/dri/card*") {
		t.Error("vulos-console.service is no longer conditioned on the ABSENCE of a DRM " +
			"device, so a box with a monitor can show the text status screen instead of " +
			"the OS — which is the defect this pair was written to remove")
	}
	if !strings.Contains(src, "systemctl enable vulos-kiosk.service") {
		t.Error("vulos-kiosk.service is written but never enabled, so it never starts")
	}
}

// The compositor ENVIRONMENT is duplicated for the same reason the browser list
// is, and drifts the same way.
//
// backend/cmd/init/main.go's startKiosk documents why each wlroots variable is
// required under QEMU virtio-gpu without working GL: legacy KMS modeset, no GBM
// modifier negotiation, the pixman renderer. Its comment records the symptom of
// getting it wrong — cage starts, opens /dev/dri/card0, and silently paints
// nothing, with no log and no exit.
//
// The systemd path needs the identical set, and a missing one there is invisible
// in exactly that way. This was not hypothetical: the first version of
// /usr/local/bin/vulos-kiosk set none of them and cage died on every start with
// "XDG_RUNTIME_DIR is not set in the environment", restarting every three
// seconds behind a black screen.

// scriptBlock returns the heredoc body build.sh writes to a given path, so an
// assertion about one generated script cannot be satisfied by another file.
func scriptBlock(t *testing.T, src, path, delim string) string {
	t.Helper()
	i := strings.Index(src, path)
	if i < 0 {
		t.Fatalf("build.sh no longer writes %s", path)
	}
	rest := src[i:]
	end := strings.Index(rest, "\n"+delim+"\n")
	if end < 0 {
		t.Fatalf("could not find the %s heredoc terminator for %s", delim, path)
	}
	return rest[:end]
}

func TestKioskEnvMatchesInit(t *testing.T) {
	initSrc := readRepoFile(t, "backend/cmd/init/main.go")

	start := strings.Index(initSrc, "func startKiosk()")
	if start < 0 {
		t.Fatal("startKiosk is gone from backend/cmd/init/main.go")
	}
	end := strings.Index(initSrc[start:], "\n}\n")
	body := initSrc[start : start+end]

	// Every wlroots/seat/runtime variable init sets for the software path.
	wanted := regexp.MustCompile(`"(XDG_RUNTIME_DIR|LIBSEAT_BACKEND|WLR_[A-Z_]+|DBUS_SESSION_BUS_ADDRESS)=`)
	names := map[string]bool{}
	for _, m := range wanted.FindAllStringSubmatch(body, -1) {
		names[m[1]] = true
	}
	if len(names) < 5 {
		t.Fatalf("only %d compositor env names parsed from startKiosk; the matcher is "+
			"broken and would report agreement with almost nothing", len(names))
	}

	// SCOPED to the kiosk script's own heredoc. XDG_RUNTIME_DIR= appears
	// elsewhere in build.sh, so an unscoped search passed while the kiosk script
	// itself set nothing — the mutation that exposed this check as hollow, and
	// the second time the same mistake appeared in this file.
	kioskScript := readRepoFile(t, "scripts/vulos-kiosk.sh")

	var missing []string
	for n := range names {
		if !strings.Contains(kioskScript, n+"=") {
			missing = append(missing, n)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("build.sh's vulos-kiosk sets none of %v, which backend/cmd/init/main.go "+
			"sets for the same job. Under QEMU virtio-gpu a missing one of these makes cage "+
			"open the card and paint nothing, silently — the systemd path would show a black "+
			"screen while every test stayed green", missing)
	}
}

// The image must ship a pre-built font cache.
//
// The OS root is a read-only dm-verity squashfs, so fontconfig cannot write its
// cache at runtime — every kiosk start logged "Fontconfig error: No writable
// cache directories" and then re-scanned every font on the system to lay out
// the first frame, on every boot for the life of the machine. fc-cache during
// the build is the only moment the rootfs is writable.

// withoutShellComments drops whole-line shell comments, so an assertion about a
// command cannot be satisfied by the comment that explains it.
func withoutShellComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestImageShipsAFontCache(t *testing.T) {
	src := readRepoFile(t, "build.sh")
	// COMMENTS STRIPPED FIRST. The prose above the fc-cache call explains why it
	// is there and contains the word — so replacing the actual command with a
	// no-op left this check green. Found by running that mutation, and the third
	// hollow assertion of this exact shape in this file: a check about code
	// satisfied by writing about the code.
	// The INVOCATION, not the word. Stripping comments was not enough: the
	// fallback branch echoes "fc-cache failed …", and that message alone kept
	// this green when the command was replaced by a no-op. An assertion about a
	// command has to name the command being run.
	if !strings.Contains(withoutShellComments(src), `chroot "$ROOTFS" fc-cache`) {
		t.Error("build.sh no longer runs fc-cache. On a read-only root the kiosk cannot " +
			"build a font cache at runtime, so it rebuilds one it cannot save on every " +
			"single boot")
	}
	kiosk := readRepoFile(t, "scripts/vulos-kiosk.sh")
	if !strings.Contains(kiosk, "XDG_CACHE_HOME=") {
		t.Error("vulos-kiosk does not point XDG_CACHE_HOME at a writable path; anything " +
			"fontconfig still tries to cache has nowhere to go on a read-only root")
	}
}

// The kiosk must SET the screen identity, not just parse it.
//
// frontend/src/providers/screenIdentity.ts reads screen/screens/screenIndex out
// of the URL and the shell renders the output name from them. Nothing set those
// parameters when that parser shipped, so readScreenIdentity() returned null on
// every real boot: the feature was tested, rendered in code, and reachable from
// no surface. A green suite over an unreachable feature is a shape this
// repository has shipped before, and the parser's own tests cannot catch it —
// they are honest about parsing and say nothing about whether anything calls it.
//
// This asserts the caller exists. It deliberately does NOT assert screens=1:
// that is the current single-output step, and a multi-output launcher setting
// screens=2 must not have to edit this test to stay honest.
func TestKioskSetsScreenIdentity(t *testing.T) {
	kiosk := readRepoFile(t, "scripts/vulos-kiosk.sh")
	body := withoutShellComments(kiosk)

	for _, param := range []string{"screen=", "screens=", "screenIndex="} {
		if !strings.Contains(body, param) {
			t.Errorf("vulos-kiosk never sets %q in the URL it opens. The shell's screen "+
				"identity parser then returns null on every boot and the feature is reachable "+
				"from no surface — see roadmap/SCREENS.md", param)
		}
	}

	// The name must come from the DRM connector, not be invented. A hardcoded
	// name would satisfy the checks above while telling the shell the wrong
	// output on every machine.
	// Matched on the DERIVATION, not on "/sys/class/drm/". That was the first
	// version of this check and it was HOLLOW: the display-detection code higher
	// up the same script already reads that path, so the assertion passed no
	// matter what the identity code did. Found by mutating the identity loop's
	// glob and watching the test stay green — an assertion satisfied by
	// unrelated code in the same file, which is the defect this suite keeps
	// finding elsewhere and had just reproduced.
	if !strings.Contains(body, `sed 's/^card[0-9]*-//'`) {
		t.Error("vulos-kiosk's screen name is not derived from the DRM connector directory " +
			"name, so the identity it reports is not the output it is actually on")
	}
}

// The URL is the THIRD thing duplicated across the two launchers, and the only
// one that had already drifted before anything pinned it.
//
// The multi-screen work put screen/screens/screenIndex on the URL
// scripts/vulos-kiosk.sh opens. backend/cmd/init/main.go was not touched and
// went on opening a bare http://localhost:8080. The tests above pin the browser
// list and the compositor environment; nothing pinned the address, so the two
// copies diverged silently and the initramfs path simply never set an identity.
//
// It has been harmless, and the caveat is the whole reason this is a guard and
// not a bug report: that path runs cage — one window, one output — and
// isMultiScreen() needs total>1, so screens=1 and a bare URL render the same
// pixels today. What it is not is stable. The moment the initramfs path drives
// more than one output, a bare URL means every window claims to be nowhere.
//
// So the invariant is the same one the browser list gets: same identity, same
// shape, or say so.

// screenIdentityRe matches one COMPLETE identity triple. Partial is the failure
// that matters: readScreenIdentity() in frontend/src/providers/screenIdentity.ts
// returns null unless all three are present, so a copy emitting two of them has
// done work that changes nothing while looking like it works.
//
// The name field is captured rather than matched, because each copy substitutes
// it differently — $screen_name in the shell, %s in the Go format string.
var screenIdentityRe = regexp.MustCompile(`screen=([^&"'\s]+)&screens=([^&"'\s]+)&screenIndex=([^&"'\s;]+)`)

// goFuncBody returns the body of a top-level Go function, so an assertion about
// one function cannot be satisfied by another — or by the doc comment above it,
// which is where the prose about these parameters lives.
func goFuncBody(t *testing.T, src, sig string) string {
	t.Helper()
	start := strings.Index(src, sig)
	if start < 0 {
		t.Fatalf("%s is gone from backend/cmd/init/main.go; this check has lost its subject", sig)
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("could not find the end of %s", sig)
	}
	return src[start : start+end]
}

// identityTriples returns every complete triple in src, and fails if any of the
// three parameter names appears OUTSIDE one — which is how a half-set identity
// would look.
func identityTriples(t *testing.T, what, src string) [][]string {
	t.Helper()
	got := screenIdentityRe.FindAllStringSubmatch(src, -1)
	for _, param := range []string{"screen=", "screens=", "screenIndex="} {
		if n := strings.Count(src, param); n != len(got) {
			t.Errorf("%s mentions %q %d time(s) but forms %d complete screen/screens/screenIndex "+
				"triple(s). readScreenIdentity() refuses a partial identity, so a lone parameter "+
				"is either dead or a lie:\n%s", what, param, n, len(got), src)
		}
	}
	return got
}

func TestKioskURLIdentityMatchesInit(t *testing.T) {
	shell := identityTriples(t, "scripts/vulos-kiosk.sh",
		withoutShellComments(readRepoFile(t, "scripts/vulos-kiosk.sh")))
	if len(shell) == 0 {
		t.Fatal("no screen-identity triple parsed out of scripts/vulos-kiosk.sh; either the " +
			"systemd launcher stopped setting one, or this matcher is broken and would report " +
			"agreement between two empty lists")
	}

	initSrc := readRepoFile(t, "backend/cmd/init/main.go")
	urlFn := goFuncBody(t, initSrc, "func kioskURL()")
	goTriples := identityTriples(t, "kioskURL in backend/cmd/init/main.go", urlFn)
	if len(goTriples) == 0 {
		t.Fatal("kioskURL in backend/cmd/init/main.go sets no screen/screens/screenIndex triple. " +
			"The initramfs path then opens a bare URL while the systemd path names its output — " +
			"the exact drift this check exists for. See roadmap/SCREENS.md")
	}

	// The two copies must agree on the CONSTANTS. The name necessarily differs
	// in spelling, so it is checked for being a substitution rather than a
	// literal — a hardcoded name would satisfy every shape check here while
	// telling the shell the wrong output on every machine.
	for _, tr := range append(append([][]string{}, shell...), goTriples...) {
		if !strings.ContainsAny(tr[1], "$%") {
			t.Errorf("the screen name in %q is the literal %q, not a substitution; it would "+
				"report the same output on every box", tr[0], tr[1])
		}
	}
	want := shell[0]
	for _, tr := range goTriples {
		if tr[2] != want[2] || tr[3] != want[3] {
			t.Errorf("the two kiosk launchers disagree on the screen identity they set.\n"+
				"  scripts/vulos-kiosk.sh (systemd, what an installed box runs): screens=%s screenIndex=%s\n"+
				"  backend/cmd/init/main.go kioskURL (initramfs):                screens=%s screenIndex=%s\n"+
				"Both launch one window on one output, so these must match. A launcher that "+
				"reports a screen count it is about to contradict is worse than one that reports "+
				"nothing.", want[2], want[3], tr[2], tr[3])
		}
	}

	// The identity has to be REACHED, not merely defined. A kioskURL nobody
	// calls is the shape this file already caught once: a feature tested,
	// rendered in code, and reachable from no surface.
	browser := goFuncBody(t, initSrc, "func findKioskBrowser()")
	if !strings.Contains(browser, "kioskURL()") {
		t.Error("findKioskBrowser does not call kioskURL, so whatever kioskURL builds is never " +
			"opened by anything")
	}
	if strings.Contains(browser, "http://localhost:8080") {
		t.Error("findKioskBrowser still hands a browser a hardcoded http://localhost:8080; that " +
			"argument carries no screen identity no matter what kioskURL returns")
	}

	// And derived from the DRM connector, not invented — the same assertion
	// TestKioskSetsScreenIdentity makes about the shell's `sed 's/^card[0-9]*-//'`,
	// against the Go copy of that expression. Scoped to the deriving function:
	// displayConnected reads the same sysfs path higher up the file, and an
	// unscoped search for it is satisfied by that unrelated code — which is how
	// the shell version of this check was found hollow.
	name := goFuncBody(t, initSrc, "func kioskScreenName()")
	if !strings.Contains(name, "drmRoot") {
		t.Error("kioskScreenName no longer reads the DRM connector tree, so the name it reports " +
			"is not the output the box is on")
	}
	// Both halves: that the stripper is APPLIED, and that it strips what the
	// shell's sed strips. Checking only the declaration passes with the call
	// removed; checking only the call passes with the pattern gutted.
	if !strings.Contains(name, "drmCardPrefix") {
		t.Error("kioskScreenName does not strip the card prefix from the connector directory; " +
			"it would report card0-HDMI-A-1 where labwc and screenWindowTitle both say HDMI-A-1, " +
			"so the placement rule matches no window")
	}
	decl := regexp.MustCompile("var drmCardPrefix = regexp\\.MustCompile\\(`([^`]*)`\\)")
	m := decl.FindStringSubmatch(initSrc)
	if m == nil {
		t.Fatal("drmCardPrefix is no longer a literal regexp in backend/cmd/init/main.go; this " +
			"check can no longer see what the initramfs path strips")
	}
	if m[1] != `^card[0-9]*-` {
		t.Errorf("drmCardPrefix is %q, but scripts/vulos-kiosk.sh strips `^card[0-9]*-`; the two "+
			"launchers would name the same output differently", m[1])
	}
}

// Multi-output launch: the pieces must agree, because nothing here can be
// checked at runtime by anything but a person with two monitors.
//
// labwc places a window with a windowRule carrying a MoveToOutput action
// (labwc-config(5), labwc-actions(5) — verified against the manuals). The rule
// matches on the window's WAYLAND APP_ID, which vulos-kiosk-genconfig derives
// from the connector name and hands to cog as --gapplication-app-id. So the same
// connector name has to appear in four places: read from /sys/class/drm, put in
// the URL, folded into the app id that cog runs under and that the rule
// matches, and given to MoveToOutput.
//
// It matched on the TITLE until 2026-08-14, which could never have worked —
// see TestKioskPlacementMatchesOnAppIDNotTitle for what labwc 0.8.3's source
// says about when rules are evaluated.
//
// If any one of those drifts, every browser lands on one monitor and nothing
// logs a reason — the exact silent failure this file exists to prevent.
func TestKioskMultiOutputLaunch(t *testing.T) {
	src := readRepoFile(t, "build.sh")
	kiosk := withoutShellComments(readRepoFile(t, "scripts/vulos-kiosk.sh"))

	// Only on more than one screen. A single-output box must keep the old path,
	// because that is what every real boot uses.
	if !strings.Contains(kiosk, `"$screen_count" -gt 1`) {
		t.Error("the multi-output path is not gated on more than one connected screen, so a " +
			"single-screen box could take it — that box is the one every real boot is")
	}
	// The CONTENT of the generated config is covered by TestKioskGenConfig,
	// which runs the real generator. What build.sh still owns is calling it and
	// shipping it — so that is what is asserted here. These used to check the
	// XML text inline; after the generator moved to scripts/, keeping them here
	// would have meant asserting against a string build.sh no longer contains.
	if !strings.Contains(kiosk, "vulos-kiosk-genconfig") {
		t.Error("vulos-kiosk does not call the config generator, so no labwc rules are written " +
			"and every browser lands on the active output")
	}
	// Name the FILES, not just "install -m 0755". That phrase now appears twice
	// in build.sh — once for the kiosk, once for the generator — so a check for
	// the phrase alone passes with either one deleted. Found by mutation:
	// removing the kiosk install left this green because the generator's install
	// still matched. An assertion satisfied by a different line is the defect
	// this file keeps catching.
	buildSrc := withoutShellComments(src)
	for _, want := range []string{"scripts/vulos-kiosk.sh", "scripts/vulos-kiosk-genconfig.sh"} {
		if !strings.Contains(buildSrc, "install -m 0755") || !strings.Contains(buildSrc, want) {
			t.Errorf("build.sh does not install %s into the image; the kiosk would call or be "+
				"a command that is not there", want)
		}
	}
}

// Run the REAL generator with fake outputs and check what it produces.
//
// scripts/vulos-kiosk-genconfig.sh is a file in the repo that build.sh installs
// into the image, so this executes the same bytes the OS runs. That is the
// reason it was pulled out of build.sh: while it was a heredoc, a test could
// only grep at the text that would later generate the config, never at the
// config itself. Every assertion here would have been satisfiable by a script
// that emitted nothing.
func TestKioskGenConfig(t *testing.T) {
	gen := filepath.Join(repoRoot, "scripts", "vulos-kiosk-genconfig.sh")
	if _, err := os.Stat(gen); err != nil {
		t.Fatalf("generator missing: %v", err)
	}

	out := t.TempDir()
	cmd := exec.Command("sh", gen, out, "http://localhost:8080", "HDMI-A-1", "DP-2")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generator failed: %v\n%s", err, b)
	}

	rc, err := os.ReadFile(filepath.Join(out, "rc.xml"))
	if err != nil {
		t.Fatalf("no rc.xml: %v", err)
	}

	// Well-formed, not merely present. A truncated or unescaped config is the
	// failure that leaves every browser on one monitor with nothing logged.
	parsed, err := parseWindowRules(rc)
	if err != nil {
		t.Fatalf("rc.xml is not well-formed XML: %v\n%s", err, rc)
	}
	if len(parsed) != 2 {
		t.Fatalf("want 2 windowRules for 2 outputs, got %d:\n%s", len(parsed), rc)
	}

	for i, want := range []struct{ identifier, output string }{
		{"org.vulos.kiosk.out-HDMI-A-1", "HDMI-A-1"},
		{"org.vulos.kiosk.out-DP-2", "DP-2"},
	} {
		got := parsed[i]
		// The identifier must be EXACTLY the app id cog is launched with — see
		// TestKioskAppIDsAgreeAndAreValid, which checks the two sides against
		// each other rather than against a literal. Pinned here as well because
		// a literal catches a change that moves BOTH sides at once.
		if got.Identifier != want.identifier {
			t.Errorf("rule %d identifier = %q, want %q — labwc matches this against the "+
				"Wayland app_id, so it must be exactly what session.sh passes to cog as "+
				"--gapplication-app-id or the rule matches no window",
				i, got.Identifier, want.identifier)
		}
		if got.Action.Name != "MoveToOutput" {
			t.Errorf("rule %d action = %q, want MoveToOutput (FocusOutput only changes focus)",
				i, got.Action.Name)
		}
		if got.Action.Output != want.output {
			t.Errorf("rule %d output = %q, want %q", i, got.Action.Output, want.output)
		}
	}

	sess, err := os.ReadFile(filepath.Join(out, "session.sh"))
	if err != nil {
		t.Fatalf("no session.sh: %v", err)
	}
	for _, want := range []string{
		"screen=HDMI-A-1&screens=2&screenIndex=1",
		"screen=DP-2&screens=2&screenIndex=2",
	} {
		if !strings.Contains(string(sess), want) {
			t.Errorf("session.sh does not give a browser %q; the shell cannot then tell the "+
				"instances apart:\n%s", want, sess)
		}
	}

	// One output must be REFUSED rather than given a pointless rule set.
	one := exec.Command("sh", gen, t.TempDir(), "http://localhost:8080", "HDMI-A-1")
	if err := one.Run(); err == nil {
		t.Error("the generator accepted a single output; it should refuse, since a one-screen " +
			"box needs no placement rules and writing them claims something it cannot honour")
	}
}

// ── The app id is the placement mechanism ────────────────────────────────────
//
// Everything below exists because the previous mechanism — matching the rule on
// the window TITLE — could never have worked, and every test in this file was
// green while it did not. There are TWO independent reasons, each sufficient
// alone, both read out of the source of the exact versions in the image.
//
//  1. labwc 0.8.3 evaluates rules ONCE, at first map:
//
//     enum window_rule_event { LAB_WINDOW_RULE_EVENT_ON_FIRST_MAP = 0 };
//
//     one member; and window_rules_apply() is called from exactly one place,
//     src/view-impl-common.c inside view_impl_map(), behind
//     `if (!view->been_mapped)`. A later title change re-runs nothing.
//
//  2. cog 0.18.4 never puts the document title on the Wayland toplevel at all.
//     platform/wayland/cog-platform-wl.c has exactly one
//     xdg_toplevel_set_title() call in the whole file, at window creation, with
//     COG_DEFAULT_APPNAME — the literal "Cog". So the title labwc sees for a cog
//     window is "Cog", forever, on every screen. No page-side change could ever
//     have fixed this; there was nothing on the page end of the wire.
//
// Two two-display QEMU boots confirmed it: both windows on output 1.
//
// `identifier` is compared against the Wayland app_id (src/view.c
// view_matches_query -> view_get_string_prop(view, "app_id"); src/xdg.c returns
// xdg_toplevel->app_id), which cog sets at toplevel creation on the statement
// immediately before wl_surface_commit() — the same commit that maps the
// surface — so it is present when the rules run.

// windowRule is the shape of one generated placement rule.
//
// Title is captured deliberately even though nothing should set it: labwc ANDs
// its criteria (view_matches_query returns false on the first mismatch), so a
// rule carrying BOTH identifier and title matches nothing at first map, which
// is the original bug wearing a fix's clothes.
type windowRule struct {
	Identifier string `xml:"identifier,attr"`
	Title      string `xml:"title,attr"`
	MatchOnce  string `xml:"matchOnce,attr"`
	Action     struct {
		Name   string `xml:"name,attr"`
		Output string `xml:"output,attr"`
	} `xml:"action"`
}

func parseWindowRules(rc []byte) ([]windowRule, error) {
	var parsed struct {
		Rules []windowRule `xml:"windowRules>windowRule"`
	}
	if err := xml.Unmarshal(rc, &parsed); err != nil {
		return nil, err
	}
	return parsed.Rules, nil
}

// sessionAppIDs returns the --gapplication-app-id values session.sh launches cog
// with, in order.
var sessionAppIDRe = regexp.MustCompile(`--gapplication-app-id="([^"]*)"`)

func sessionAppIDs(sess string) []string {
	var out []string
	for _, m := range sessionAppIDRe.FindAllStringSubmatch(sess, -1) {
		out = append(out, m[1])
	}
	return out
}

// glibAppIDIsValid models g_application_id_is_valid().
//
// It is a MODEL, and a model of something whose disagreement silently restores
// the very bug this replaced. MEASURED, because the obvious guess is wrong: cog
// does NOT refuse to start on an invalid id. GLib's
// g_application_set_application_id is a g_return_if_fail, so the setter becomes
// a no-op, one GLib-GIO-CRITICAL line is printed, and cog carries on under
// COG_DEFAULT_APPID — "com.igalia.Cog", identical for every instance, matching
// no rule, every window on one output.
//
// So the rules encoded here were measured against the real function rather than
// read off a doc page —
// GLib 2.84.4, which is what debian trixie ships and trixie is the image's own
// suite (build.sh: SUITE="trixie"). The measurements are replayed as a table in
// TestGlibAppIDModelMatchesMeasuredGlib, so this function cannot quietly drift
// away from the thing it stands in for.
func glibAppIDIsValid(id string) bool {
	if id == "" || len(id) > 255 {
		return false
	}
	if !strings.Contains(id, ".") {
		return false
	}
	if strings.HasPrefix(id, ".") || strings.HasSuffix(id, ".") {
		return false
	}
	for _, el := range strings.Split(id, ".") {
		if el == "" {
			return false
		}
		if el[0] >= '0' && el[0] <= '9' {
			return false
		}
		for i := 0; i < len(el); i++ {
			c := el[i]
			switch {
			case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z',
				c >= '0' && c <= '9', c == '_', c == '-':
			default:
				return false
			}
		}
	}
	return true
}

// The measured answers. Every line here was printed by a C program calling
// g_application_id_is_valid() directly, in a debian:trixie container:
//
//	docker run --rm -v "$PWD:/work" debian:trixie sh /work/glib-only.sh
//
// If the model above stops reproducing them, the model is wrong — not the
// table.
func TestGlibAppIDModelMatchesMeasuredGlib(t *testing.T) {
	for _, tc := range []struct {
		id    string
		valid bool
	}{
		{"org.vulos.kiosk.DP-1", true},
		{"org.vulos.kiosk.DP_1", true},
		{"org.vulos.kiosk.eDP-1", true},
		{"org.vulos.kiosk.HDMI-A-1", true},
		{"org.vulos.kiosk.DP-2-1", true},
		{"org.vulos.kiosk.Virtual-2", true},
		{"org.vulos.kiosk.out-9PinDIN-1", true},
		{"org.vulos.kiosk.out_9PinDIN_1", true},
		// THE trap. 9PinDIN is a real connector type in the kernel's table
		// (drm_connector.c), so a mapping that puts the raw connector name in
		// the last element produces an id GLib rejects, which drops cog back
		// to the shared default and un-does the placement entirely.
		{"org.vulos.kiosk.9PinDIN-1", false},
		{"nodot", false},
		{".org.vulos.x", false},
		{"org.vulos.x.", false},
		{"org.vulos..x", false},
		{"org.vulos.1x", false},
		// fnmatch metacharacters. labwc matches identifier with
		// fnmatch(pattern, app_id, FNM_CASEFOLD) (src/common/match.c), so this
		// is what makes a valid app id necessarily a LITERAL pattern.
		{"org.vulos.a*b", false},
		{"org.vulos.a?b", false},
		{"org.vulos.a[b]", false},
		{"org.vulos.a b", false},
		{"org.vulos.a/b", false},
	} {
		if got := glibAppIDIsValid(tc.id); got != tc.valid {
			t.Errorf("glibAppIDIsValid(%q) = %v, but GLib 2.84.4 measured %v. The model has "+
				"drifted from g_application_id_is_valid, and it is the model that is wrong. An "+
				"id GLib rejects is not a loud failure: the setter is a g_return_if_fail, so cog "+
				"keeps com.igalia.Cog, no rule matches, and every window lands on one output — "+
				"exactly the defect this replaced.", tc.id, got, tc.valid)
		}
	}
}

// kioskConnectorSpread is the real range of DRM connector names, not just the
// one QEMU happens to produce. The type names come from the kernel's
// drm_connector_enum_list (drivers/gpu/drm/drm_connector.c).
var kioskConnectorSpread = []string{
	"Virtual-1", "Virtual-2", "DP-1", "DP-2", "DP-2-1",
	"HDMI-A-1", "HDMI-A-2", "HDMI-B-1", "eDP-1",
	"DVI-I-1", "DVI-D-1", "DVI-A-1", "9PinDIN-1",
	"SVIDEO-1", "Composite-1", "Component-1", "LVDS-1",
	"TV-1", "DSI-1", "DPI-1", "Writeback-1", "SPI-1",
	"USB-1", "VGA-1", "Unknown-1",
}

// genKioskConfig runs the REAL generator over the given connectors and returns
// what it wrote.
func genKioskConfig(t *testing.T, connectors ...string) ([]windowRule, string) {
	t.Helper()
	gen := filepath.Join(repoRoot, "scripts", "vulos-kiosk-genconfig.sh")
	out := t.TempDir()

	args := append([]string{gen, out, "http://localhost:8080"}, connectors...)
	if b, err := exec.Command("sh", args...).CombinedOutput(); err != nil {
		t.Fatalf("generator failed for %v: %v\n%s", connectors, err, b)
	}

	rc, err := os.ReadFile(filepath.Join(out, "rc.xml"))
	if err != nil {
		t.Fatalf("no rc.xml: %v", err)
	}
	rules, err := parseWindowRules(rc)
	if err != nil {
		t.Fatalf("rc.xml is not well-formed XML: %v\n%s", err, rc)
	}
	sess, err := os.ReadFile(filepath.Join(out, "session.sh"))
	if err != nil {
		t.Fatalf("no session.sh: %v", err)
	}
	return rules, string(sess)
}

// THE assertion this change turns on: the id the rule matches and the id the
// browser runs under are the same string, one per connector, and GLib accepts
// every one of them.
//
// These two values live in two generated files and are the whole mechanism. A
// rule built from the raw connector name while cog got a sanitised one — or the
// reverse — places nothing and logs nothing, which is exactly the failure the
// title-matching version produced for two full QEMU boots.
func TestKioskAppIDsAgreeAndAreValid(t *testing.T) {
	rules, sess := genKioskConfig(t, kioskConnectorSpread...)
	ids := sessionAppIDs(sess)

	if len(rules) != len(kioskConnectorSpread) {
		t.Fatalf("got %d windowRules for %d connectors", len(rules), len(kioskConnectorSpread))
	}
	if len(ids) != len(kioskConnectorSpread) {
		t.Fatalf("session.sh passes --gapplication-app-id %d time(s) for %d connectors. Every cog "+
			"instance needs its own id: without one it keeps cog's default com.igalia.Cog, "+
			"which is identical for every instance, and the rule that names it matches all of "+
			"them or none.\n%s", len(ids), len(kioskConnectorSpread), sess)
	}

	seen := map[string]string{}
	for i, conn := range kioskConnectorSpread {
		rule, id := rules[i], ids[i]

		if rule.Identifier != id {
			t.Errorf("connector %q: the rule matches identifier %q but cog is launched with "+
				"--gapplication-app-id=%q. labwc compares identifier against the Wayland app_id, so "+
				"these two strings ARE the placement mechanism — a mismatch places nothing and "+
				"logs nothing.", conn, rule.Identifier, id)
		}
		// Paired with the RIGHT output. Two agreeing lists that are rotated
		// against each other would satisfy the check above and send every window
		// to the wrong monitor.
		if rule.Action.Output != conn {
			t.Errorf("rule %d matches %q but moves the window to output %q, not %q",
				i, rule.Identifier, rule.Action.Output, conn)
		}
		if !glibAppIDIsValid(id) {
			t.Errorf("connector %q produces app id %q, which g_application_id_is_valid rejects. "+
				"GLib's setter is a g_return_if_fail, so cog does not fail loudly: it keeps "+
				"COG_DEFAULT_APPID (com.igalia.Cog) for every instance, this rule matches no "+
				"window, and all of them land on one output.", conn, id)
		}
		// The connector must still be RECOVERABLE from the id, or the four
		// places the name lives have stopped being the same name.
		if !strings.Contains(strings.ToLower(id), strings.ToLower(conn)) {
			t.Errorf("connector %q is not visible in its app id %q; the id is no longer derived "+
				"from the connector name in a way anyone reading a journal can follow", conn, id)
		}
		// Case-INSENSITIVELY unique, because labwc's match is
		// fnmatch(..., FNM_CASEFOLD) — two ids differing only in case would
		// match each other's rule.
		key := strings.ToLower(id)
		if prev, dup := seen[key]; dup {
			t.Errorf("connectors %q and %q both produce app id %q (compared case-insensitively, "+
				"as labwc compares it). Both browsers would land on one screen.", prev, conn, id)
		}
		seen[key] = conn
	}
}

// The rule must match on the app id and on NOTHING ELSE.
//
// labwc ANDs its criteria — view_matches_query returns false the moment one
// fails — and the only title a cog window ever has is the literal "Cog". So a
// rule carrying identifier AND title matches no window at all, which is the
// original defect restored by someone being careful.
func TestKioskPlacementMatchesOnAppIDNotTitle(t *testing.T) {
	rules, _ := genKioskConfig(t, "HDMI-A-1", "DP-2")
	for i, r := range rules {
		if r.Identifier == "" {
			t.Errorf("rule %d carries no identifier, so it matches every window or none", i)
		}
		if r.Title != "" {
			t.Errorf("rule %d carries title=%q as well as identifier=%q. labwc ANDs its "+
				"criteria, and cog's Wayland platform sets the toplevel title exactly once, to "+
				"the literal \"Cog\" (one xdg_toplevel_set_title call in "+
				"platform/wayland/cog-platform-wl.c, with COG_DEFAULT_APPNAME). It never "+
				"propagates the document title. So any title criterion makes the rule match "+
				"nothing — the exact defect two QEMU boots recorded.",
				i, r.Title, r.Identifier)
		}
		// A valid app id cannot contain one (measured: `*`, `?`, `[` are all
		// rejected by GLib), so this failing means the id stopped being a valid
		// app id in a way glibAppIDIsValid did not catch.
		if strings.ContainsAny(r.Identifier, "*?[\\") {
			t.Errorf("rule %d identifier %q contains an fnmatch metacharacter; labwc matches it "+
				"with fnmatch(pattern, app_id, FNM_CASEFOLD), so the rule would match windows "+
				"it was not written for", i, r.Identifier)
		}
	}
}

// Two connectors that would share an app id must be REFUSED, not silently
// stacked on one monitor.
//
// The mapping is injective over the names the kernel produces. It is not
// injective over arbitrary input, and the difference between those two
// statements is a dark monitor, so the generator checks rather than assumes.
func TestKioskGenConfigRefusesCollidingAppIDs(t *testing.T) {
	gen := filepath.Join(repoRoot, "scripts", "vulos-kiosk-genconfig.sh")
	for _, pair := range [][2]string{
		{"DP-1", "DP.1"}, // both fold to out-DP-1
		{"eDP-1", "EDP-1"},
	} {
		cmd := exec.Command("sh", gen, t.TempDir(), "http://localhost:8080", pair[0], pair[1])
		b, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("the generator accepted %q and %q, which produce the same app id. Both "+
				"browsers would match the first rule and land on one screen, with nothing "+
				"logged.\n%s", pair[0], pair[1], b)
			continue
		}
		if !strings.Contains(string(b), "collide") {
			t.Errorf("the generator refused %q + %q but did not say why:\n%s", pair[0], pair[1], b)
		}
	}
}

// The kiosk must not report a screen count it is about to contradict.
//
// The identity line runs before the multi-output branch, so a hardcoded
// "(1 of 1)" claimed one screen and then started two browsers on a two-monitor
// box. The journal is the only window into a machine that is showing you
// nothing, and one that disagrees with what the machine did is worse than
// silence.
func TestKioskLogsRealScreenCount(t *testing.T) {
	kiosk := withoutShellComments(readRepoFile(t, "scripts/vulos-kiosk.sh"))
	if strings.Contains(kiosk, "(1 of 1)") {
		t.Error("vulos-kiosk logs a hardcoded \"(1 of 1)\" screen count; on a multi-output box " +
			"it then launches several browsers, so the journal contradicts the machine")
	}
	if !strings.Contains(kiosk, "$screen_count connected") {
		t.Error("the screen identity line does not report the counted number of screens")
	}
}

// The readiness probe must not be built from a URL that carries a query string.
//
// vulos-kiosk waits for $BASE_URL/api/setup/status before launching a browser,
// because starting it first shows an error page that never retries. Once the
// screen identity was appended to $URL, probing "$URL/api/setup/status" put the
// path AFTER the query — an address that can never answer. Every box with a
// display then waited the full sixty seconds and launched anyway, so the guard
// was silently doing nothing and costing a minute of boot.
//
// Caught by tracing the real script under sh -x, not by reading it: the broken
// URL looks entirely reasonable in the source.
func TestKioskProbesTheBareURL(t *testing.T) {
	kiosk := withoutShellComments(readRepoFile(t, "scripts/vulos-kiosk.sh"))
	if strings.Contains(kiosk, `"$URL/api/`) {
		t.Error(`vulos-kiosk probes "$URL/api/..." — $URL carries the screen identity query ` +
			"string, so the path lands after the query and the probe can never succeed")
	}
	if !strings.Contains(kiosk, `"$BASE_URL/api/setup/status"`) {
		t.Error("vulos-kiosk no longer probes $BASE_URL/api/setup/status before launching a " +
			"browser; without it the browser can open before the server answers and shows an " +
			"error page that never retries")
	}
}

// A headless box must EXIT ZERO, and this runs the script to find out.
//
// roadmap/SCREENS.md listed "multi-screen must not turn the headless path into
// an error path" as unresolved, and every check on it so far was a grep. A grep
// cannot see the failure mode that matters: vulos-kiosk.service is
// Restart=on-failure, so a non-zero exit on a box with no display is not a bad
// log line, it is systemd restarting the kiosk every three seconds forever, on
// exactly the machines that have no screen to show the reason on.
//
// It is easy to reintroduce without touching the exit statement. `set -eu` is
// in force, so any command in the detection path that fails last in its list
// terminates the script with ITS status — the multi-output work added a second
// connector loop above this point, and a future one could add a third.
//
// So: run the real file with no render nodes and no connected connectors, and
// assert both the status and the reason. VULOS_DRI_ROOT/VULOS_DRM_ROOT exist
// for this; a real boot sets neither.
func TestKioskHeadlessExitsZero(t *testing.T) {
	empty := t.TempDir()
	cmd := exec.Command("sh", filepath.Join(repoRoot, "scripts", "vulos-kiosk.sh"))
	cmd.Env = append(os.Environ(),
		"VULOS_DRI_ROOT="+empty,
		"VULOS_DRM_ROOT="+empty,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("vulos-kiosk exited non-zero on a headless box: %v\n%s\n\n"+
			"vulos-kiosk.service is Restart=on-failure, so this is not a cosmetic "+
			"defect: systemd restarts the unit every RestartSec forever on a machine "+
			"with no display, and no display means nothing to read the reason on.", err, out)
	}
	if !strings.Contains(string(out), "no display found") {
		t.Errorf("vulos-kiosk exited 0 without saying why. A box that shows no OS and "+
			"explains nothing is indistinguishable from a broken one.\nOutput:\n%s", out)
	}
	// It must DECLINE, not launch. An exit 0 reached by some other route — a
	// browser that started and quit, say — would satisfy the two checks above
	// while meaning the opposite.
	if strings.Contains(string(out), "display present") {
		t.Errorf("vulos-kiosk claims a display is present when given empty DRM roots; the "+
			"headless detection is looking somewhere other than where it was told.\n%s", out)
	}
}

// ── Memory warning on a multi-output box ─────────────────────────────────────
//
// roadmap/SCREENS-COST.md measured one kiosk browser per screen at ~322MB PSS
// for the first and ~150-170MB for each one after it, and decided three screens
// IS supported on the advertised 2GB floor. What it asked for on a low-memory
// multi-output box was therefore a warning, explicitly not a gate — and the
// difference between those two is invisible to a grep. `echo …` and
// `echo …; exit 1` differ by four characters, both contain the warning text,
// and only one of them still shows you a desktop.
//
// So these run the real file. The fake box is assembled out of the seams the
// script already has for exactly this (VULOS_DRM_ROOT, VULOS_DRI_ROOT, and now
// VULOS_MEMINFO) plus a PATH of stubs standing in for labwc/cog/cage, so that
// "did it launch" is an observable event — the stub prints when it is exec'd —
// rather than something inferred from the source.
//
// The most important assertion in here is the boring one:
// TestKioskLowMemoryMultiScreenStillLaunches. A warning that grew into a
// refusal would leave a user's second and third monitors dark because a
// launcher decided it knew better about hardware it has not watched run out of
// memory, on a box whose only diagnostic surface is the monitors it just
// declined to use.

const (
	// The wording the warning is recognised by. Deliberately a substring of the
	// real line rather than the whole of it, so rephrasing the sentence does not
	// turn every test below red — but specific enough that nothing else in the
	// script's output matches it.
	//
	// If it ever stops matching, TestKioskWarnsOnLowMemoryMultiScreen fails
	// FIRST and loudly. That is what stops the three negative tests below from
	// quietly passing against a marker that matches nothing, which is the shape
	// of hollow guard this package keeps catching.
	kioskLowMemWarning = "multi-screen is recommended at 4GB+"
	// What a stub prints when the script execs it. Seeing this is the proof the
	// launcher launched.
	kioskStubExec = "kiosk-stub-exec"
)

// meminfo2GB is the shape a real 2GB box reports: MemTotal is memory the kernel
// can hand out, always some way below what is on the board.
const meminfo2GB = "MemTotal:        2035892 kB\nMemFree:          131072 kB\nBuffers:            8192 kB\n"

// meminfo8GB is comfortably over the threshold — an ordinary box.
const meminfo8GB = "MemTotal:        8037892 kB\nMemFree:         4131072 kB\n"

// kioskStubNames are the programs the launcher looks for on PATH. Standing in
// for all of them is what lets the real script be run off a real box.
var kioskStubNames = []string{"labwc", "cog", "cage", "vulos-kiosk-genconfig", "curl"}

// kioskStubBin is a directory of symlinks — one per name above — pointing at
// THIS TEST BINARY, which re-execs as the named stub (see TestMain).
//
// That indirection is a measured decision, not cleverness. The obvious harness
// writes five little `#!/bin/sh` files into a temp dir, and on macOS the FIRST
// execution of any freshly written executable blocks ~10-20 seconds while the
// system inspects it — all wall time, ~0 user and ~0 sys, so it does not even
// look like work. Measured here: 21.4s for the first labwc launch and 11.6s for
// the first cage launch, against 0.1-0.2s for every run after them, and the toll
// is paid again on every `go test` because the temp dir is new each time. This
// file's tests took 36s that way and take under a second now.
//
// A symlink to the already-running test binary is exempt because that binary has
// already been through it — measured at 21 MILLIseconds cold. Symlinking to
// /bin/echo is equally cheap but loses the program's name, and "which launcher
// did it exec" is the question these tests exist to ask.
var kioskStubBin string

func TestMain(m *testing.M) {
	// Running AS a stub: the script exec'd one of the symlinks. Announce the name
	// and the arguments — that line is the only proof anywhere that the launcher
	// launched, as opposed to a source file that looks like it would.
	for _, name := range kioskStubNames {
		if filepath.Base(os.Args[0]) == name {
			fmt.Printf("%s %s %s\n", kioskStubExec, name, strings.Join(os.Args[1:], " "))
			os.Exit(0)
		}
	}

	self, err := os.Executable()
	if err != nil {
		panic("kiosk stub setup: " + err.Error())
	}
	dir, err := os.MkdirTemp("", "vulos-kiosk-stubs")
	if err != nil {
		panic("kiosk stub setup: " + err.Error())
	}
	for _, name := range kioskStubNames {
		if err := os.Symlink(self, filepath.Join(dir, name)); err != nil {
			panic("kiosk stub setup: " + err.Error())
		}
	}
	kioskStubBin = dir

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type kioskBox struct {
	// Connector names as the compositor sees them, e.g. "HDMI-A-1". Written into
	// the fake DRM tree as card0-<name>/status = connected.
	screens []string
	// Contents of the fake /proc/meminfo. Ignored when noMeminfoFile is set.
	meminfo string
	// Point VULOS_MEMINFO at a path that does not exist, i.e. the box whose
	// memory size is unknown.
	noMeminfoFile bool
}

// runKiosk runs scripts/vulos-kiosk.sh — the same bytes build.sh installs into
// the image — against a fabricated box, and returns everything it said.
func runKiosk(t *testing.T, box kioskBox) (string, error) {
	t.Helper()

	root := t.TempDir()
	drm := filepath.Join(root, "drm")
	dri := filepath.Join(root, "dri")
	xdg := filepath.Join(root, "run")
	for _, d := range []string{drm, dri, xdg} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("harness setup: %v", err)
		}
	}

	for _, name := range box.screens {
		dir := filepath.Join(drm, "card0-"+name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("harness setup: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte("connected\n"), 0o644); err != nil {
			t.Fatalf("harness setup: %v", err)
		}
	}
	// A render node, so the headless branch (TestKioskHeadlessExitsZero's
	// subject) is not the one taken here.
	if err := os.WriteFile(filepath.Join(dri, "card0"), nil, 0o644); err != nil {
		t.Fatalf("harness setup: %v", err)
	}

	// The stubs (TestMain builds them). labwc/cog/cage announce themselves so an
	// exec is observable; vulos-kiosk-genconfig stands in for the generator,
	// whose real output is TestKioskGenConfig's subject and not this one's — it
	// would otherwise write to /run/vulos-kiosk, a path a test has no business
	// creating.
	//
	// curl is stubbed too, and not because it is missing: the script waits up to
	// sixty seconds for $BASE_URL/api/setup/status before launching anything, so
	// without this every test here would either take a minute or need a real
	// listening socket. What is probed is TestKioskProbesTheBareURL's subject.
	// The stub also means these tests open no socket at all.
	bin := kioskStubBin

	meminfoPath := filepath.Join(root, "meminfo")
	if box.noMeminfoFile {
		meminfoPath = filepath.Join(root, "no-such-meminfo")
	} else if err := os.WriteFile(meminfoPath, []byte(box.meminfo), 0o644); err != nil {
		t.Fatalf("harness setup: %v", err)
	}

	cmd := exec.Command("sh", filepath.Join(repoRoot, "scripts", "vulos-kiosk.sh"))
	// Later entries win in os/exec, so these override anything inherited.
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"VULOS_DRM_ROOT="+drm,
		"VULOS_DRI_ROOT="+dri,
		// ALWAYS set, even for the "memory is unknown" case, which points it at a
		// path that does not exist rather than leaving it unset. Unset, a Linux CI
		// machine would read its OWN /proc/meminfo and the result would depend on
		// how much RAM the runner happens to have.
		"VULOS_MEMINFO="+meminfoPath,
		"XDG_RUNTIME_DIR="+xdg,
		// Never dialled: curl is stubbed. Port 1 so a regression that removed the
		// stub fails fast and visibly rather than hanging.
		"VULOS_KIOSK_URL=http://127.0.0.1:1",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// launched reports whether the script reached a compositor, and which one.
func launched(out, what string) bool {
	return strings.Contains(out, kioskStubExec+" "+what+" ")
}

// THE assertion. A low-memory multi-output box must still light up every screen.
func TestKioskLowMemoryMultiScreenStillLaunches(t *testing.T) {
	out, err := runKiosk(t, kioskBox{screens: []string{"HDMI-A-1", "DP-2"}, meminfo: meminfo2GB})
	if err != nil {
		t.Fatalf("vulos-kiosk exited non-zero on a 2GB two-monitor box: %v\n%s\n\n"+
			"The memory check is a WARNING. An exit here is a refusal, and "+
			"vulos-kiosk.service is Restart=on-failure, so it is also a restart loop.", err, out)
	}
	if !launched(out, "labwc") {
		t.Errorf("vulos-kiosk never exec'd labwc on a 2GB two-monitor box, so the second "+
			"monitor stays dark. roadmap/SCREENS-COST.md measured three screens as fitting "+
			"the 2GB floor with ~1GB free — there is no wall here to stop a user at, and a "+
			"launcher that silently drops to one output is indistinguishable from broken "+
			"hardware.\nOutput:\n%s", out)
	}
	// And it must be the MULTI-output launcher, not a quiet fallback to the
	// single-window cage path — which would also "launch", on one screen.
	if launched(out, "cage") {
		t.Errorf("vulos-kiosk fell back to cage (one window, one output) on a two-monitor "+
			"box; that is the refusal this test exists to catch, wearing a launch's "+
			"clothes.\nOutput:\n%s", out)
	}
	if !strings.Contains(out, "2 screens (") {
		t.Errorf("vulos-kiosk did not report launching 2 screens.\nOutput:\n%s", out)
	}
}

// TestKioskSessionIsRunByAnInterpreter pins the fix for the defect that made
// the multi-output kiosk not work AT ALL on a real boot, found 2026-08-14 by
// scripts/smoke-multiscreen-qemu.sh against the v0.1.0 arm64 image:
//
//	[ERROR] [../src/common/spawn.c:120] Failed to execute primary client
//	        /run/vulos-kiosk/session.sh: Permission denied
//
// Debian's initramfs mounts /run NOEXEC and then moves that mount onto the
// real root, so /run is noexec for the whole life of every booted system. The
// generated session script lives there (correctly — the installed root is a
// read-only dm-verity squashfs), and vulos-kiosk-genconfig chmod +x's it,
// which succeeds and buys nothing: execve() on a noexec mount returns EACCES
// whatever the mode bits say. So labwc must be given an INTERPRETER and the
// script as an argument, which reads the file as data.
//
// This asserts on the argv the launcher actually exec'd, not on the source.
// The distinction matters here more than usual: the broken and fixed versions
// differ by nine characters in a string, and every other test in this file
// passed against the broken one — including the ones that check labwc was
// launched at all, because labwc WAS launched. It just could not start a
// browser, and no assertion anywhere looked past the compositor.
func TestKioskSessionIsRunByAnInterpreter(t *testing.T) {
	out, err := runKiosk(t, kioskBox{screens: []string{"HDMI-A-1", "DP-2"}, meminfo: meminfo8GB})
	if err != nil {
		t.Fatalf("vulos-kiosk exited non-zero on a two-monitor box: %v\n%s", err, out)
	}
	// The stub prints its whole argv, so this is the command line the box runs.
	var labwcArgs string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, kioskStubExec+" labwc ") {
			labwcArgs = strings.TrimPrefix(line, kioskStubExec+" labwc ")
		}
	}
	if labwcArgs == "" {
		t.Fatalf("labwc was never exec'd, so there is no session command to check.\nOutput:\n%s", out)
	}
	if !strings.Contains(labwcArgs, "-S /bin/sh ") {
		t.Errorf("labwc's session (-S) is %q.\n\n"+
			"It must hand the generated script to an interpreter — `-S \"/bin/sh <path>\"` — "+
			"rather than naming the script's own path. /run is mounted noexec on every "+
			"real boot (Debian initramfs: `mount -t tmpfs -o \"nodev,noexec,nosuid,...\" "+
			"tmpfs /run`, then `mount -n -o move /run ${rootmnt}/run`), so labwc's execve "+
			"of the script fails with EACCES and BOTH monitors keep the boot splash "+
			"forever. chmod +x does not help and did not help.", labwcArgs)
	}
	// The path must still be there — an interpreter with nothing to run would
	// satisfy the check above while starting no browsers at all.
	if !strings.Contains(labwcArgs, "session.sh") {
		t.Errorf("labwc's session (-S) is %q and names no session script, so no browser "+
			"is ever started.", labwcArgs)
	}
}

func TestKioskWarnsOnLowMemoryMultiScreen(t *testing.T) {
	out, err := runKiosk(t, kioskBox{screens: []string{"HDMI-A-1", "DP-2"}, meminfo: meminfo2GB})
	if err != nil {
		t.Fatalf("vulos-kiosk exited non-zero: %v\n%s", err, out)
	}
	if !strings.Contains(out, kioskLowMemWarning) {
		t.Fatalf("vulos-kiosk said nothing about memory on a 2GB two-monitor box. The "+
			"measurement in roadmap/SCREENS-COST.md is only useful to someone who is told "+
			"it, and a console or journal is the one place a user of a slow box will "+
			"look.\nOutput:\n%s", out)
	}
	// The NUMBERS, not just a mood. "low memory" is not actionable; "1988MB, 2
	// screens" is — it tells a reader which of the two facts to change.
	// 2035892 kB / 1024 = 1988 MB.
	if !strings.Contains(out, "1988MB") {
		t.Errorf("the warning does not name the RAM it actually found (expected 1988MB from "+
			"MemTotal 2035892 kB). A warning that will not say what it measured cannot be "+
			"acted on or disbelieved.\nOutput:\n%s", out)
	}
	if !strings.Contains(out, "2 screens on") {
		t.Errorf("the warning does not name how many screens it is warning about.\nOutput:\n%s", out)
	}
	if !strings.Contains(out, "docs/GETTING-STARTED.md") {
		t.Errorf("the warning does not point at the recommendation it is invoking, so a "+
			"reader cannot find out what to do about it.\nOutput:\n%s", out)
	}
	// Still launched. Stated here too, because a test named "warns" that passed
	// while the box refused would be the worst possible green.
	if !launched(out, "labwc") {
		t.Errorf("the warning appeared but nothing launched.\nOutput:\n%s", out)
	}
}

// An ordinary box must hear nothing. A warning printed on every multi-monitor
// machine is a warning nobody reads by the second week.
func TestKioskDoesNotWarnOnAmpleMemory(t *testing.T) {
	out, err := runKiosk(t, kioskBox{screens: []string{"HDMI-A-1", "DP-2"}, meminfo: meminfo8GB})
	if err != nil {
		t.Fatalf("vulos-kiosk exited non-zero on an 8GB two-monitor box: %v\n%s", err, out)
	}
	// Prove the branch was REACHED before believing the silence. Without this,
	// deleting the multi-output path entirely would pass this test.
	if !launched(out, "labwc") {
		t.Fatalf("the multi-output branch was never reached, so this test's silence proves "+
			"nothing.\nOutput:\n%s", out)
	}
	if strings.Contains(out, kioskLowMemWarning) {
		t.Errorf("vulos-kiosk warned about memory on an 8GB box (MemTotal 8037892 kB, well "+
			"over the ~3GB line). An unconditional warning is noise, and noise is what makes "+
			"the real one on a 2GB box get ignored.\nOutput:\n%s", out)
	}
}

// Unknown memory must be treated like an ordinary box, not a low one — and must
// not break the boot path. Guessing low would put a warning in front of users
// whose machines are fine, on the strength of a number nobody read.
func TestKioskDoesNotWarnWhenMemoryIsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		box  kioskBox
	}{
		{"no meminfo file at all", kioskBox{screens: []string{"HDMI-A-1", "DP-2"}, noMeminfoFile: true}},
		// What a truncated or foreign /proc/meminfo looks like.
		{"empty meminfo", kioskBox{screens: []string{"HDMI-A-1", "DP-2"}, meminfo: ""}},
		{"unparseable meminfo", kioskBox{screens: []string{"HDMI-A-1", "DP-2"},
			meminfo: "MemTotal:        not-a-number kB\n"}},
		{"truncated meminfo", kioskBox{screens: []string{"HDMI-A-1", "DP-2"}, meminfo: "MemTot"}},
		// TWO MemTotal lines — a corrupt or concatenated file. This is the case
		// the all-digits guard in the script actually exists for, and the only one
		// of these five that kills it under mutation: the sed pattern already
		// demands digits, so a garbage VALUE never reaches the comparison, but two
		// matches leave a two-line value that `[ … -lt … ]` answers with
		// "[: integer expression expected" on the console of a healthy box.
		{"two MemTotal lines", kioskBox{screens: []string{"HDMI-A-1", "DP-2"},
			meminfo: "MemTotal:        2035892 kB\nMemTotal:        8037892 kB\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runKiosk(t, tc.box)
			if err != nil {
				t.Fatalf("vulos-kiosk exited non-zero when it could not read MemTotal: %v\n%s\n\n"+
					"An unreadable /proc/meminfo must not be able to stop a box booting to its "+
					"screens; the only thing that depends on it is one printed line.", err, out)
			}
			if !launched(out, "labwc") {
				t.Fatalf("nothing launched when memory was unknown.\nOutput:\n%s", out)
			}
			if strings.Contains(out, kioskLowMemWarning) {
				t.Errorf("vulos-kiosk warned about memory it never successfully read — that is a "+
					"guess presented as a measurement.\nOutput:\n%s", out)
			}
			// And it must fail QUIETLY. Both spellings, because the two shells this
			// script runs under disagree: bash says "integer expression expected",
			// dash says "Illegal number". Either one lands on the console of a box
			// that is working perfectly, from a check whose entire job is to print
			// at most one line.
			for _, complaint := range []string{"integer expression", "Illegal number"} {
				if strings.Contains(out, complaint) {
					t.Errorf("the shell complained (%q) about the value read from a meminfo it "+
						"could not parse. Nothing is wrong with this box; the memory check is "+
						"talking to itself in front of the user.\nOutput:\n%s", complaint, out)
				}
			}
		})
	}
}

// One screen, whatever the RAM, is the case the measurement found comfortable
// (~322MB PSS of a 2048MB budget) and is what every real boot is today.
func TestKioskDoesNotWarnOnASingleScreen(t *testing.T) {
	out, err := runKiosk(t, kioskBox{screens: []string{"HDMI-A-1"}, meminfo: meminfo2GB})
	if err != nil {
		t.Fatalf("vulos-kiosk exited non-zero on a 2GB single-monitor box: %v\n%s", err, out)
	}
	if !launched(out, "cage") {
		t.Fatalf("the single-screen path did not reach cage, so this test's silence proves "+
			"nothing.\nOutput:\n%s", out)
	}
	if strings.Contains(out, kioskLowMemWarning) {
		t.Errorf("vulos-kiosk warned about multi-screen memory on a box with one monitor. "+
			"One browser on 2GB is the measured, comfortable case.\nOutput:\n%s", out)
	}
}

// The compositor environment must be DEFAULTS, not mandates.
//
// vulos-kiosk sets XDG_RUNTIME_DIR, LIBSEAT_BACKEND, the WLR_* flags and
// DBUS_SESSION_BUS_ADDRESS for a real DRM boot. Written as bare assignments
// they were unoverridable, and a harness could not start the script against a
// headless compositor at all — LIBSEAT_BACKEND=builtin wants real DRM.
//
// As ${VAR:-default} a real boot is unchanged (it sets none of them) while a
// test can supply an environment that suits it. This is the seam that let
// scripts/smoke-kiosk-multiscreen.sh execute the real launcher.
func TestKioskEnvIsOverridable(t *testing.T) {
	kiosk := withoutShellComments(readRepoFile(t, "scripts/vulos-kiosk.sh"))
	for _, v := range []string{
		"XDG_RUNTIME_DIR", "LIBSEAT_BACKEND", "WLR_RENDERER",
		"WLR_DRM_NO_ATOMIC", "DBUS_SESSION_BUS_ADDRESS",
	} {
		if !strings.Contains(kiosk, "export "+v+`="${`+v+":-") {
			t.Errorf("%s is exported as a bare assignment rather than ${%s:-default}; the "+
				"script cannot then be run against any environment but real DRM hardware", v, v)
		}
	}
}
