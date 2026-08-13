package docsref

import (
	"encoding/xml"
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

// Multi-output launch: the pieces must agree, because nothing here can be
// checked at runtime by anything but a person with two monitors.
//
// labwc places a window with a windowRule carrying a MoveToOutput action
// (labwc-config(5), labwc-actions(5) — verified against the manuals). The rule
// matches on the window TITLE, which the shell builds from the screen=
// parameter it was handed (screenIdentity.ts screenWindowTitle). So the same
// connector name has to appear in four places: read from /sys/class/drm, put in
// the URL, echoed in the title the rule matches, and given to MoveToOutput.
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
	var parsed struct {
		Rules []struct {
			Title  string `xml:"title,attr"`
			Action struct {
				Name   string `xml:"name,attr"`
				Output string `xml:"output,attr"`
			} `xml:"action"`
		} `xml:"windowRules>windowRule"`
	}
	if err := xml.Unmarshal(rc, &parsed); err != nil {
		t.Fatalf("rc.xml is not well-formed XML: %v\n%s", err, rc)
	}
	if len(parsed.Rules) != 2 {
		t.Fatalf("want 2 windowRules for 2 outputs, got %d:\n%s", len(parsed.Rules), rc)
	}

	for i, want := range []struct{ title, output string }{
		{"Vulos — HDMI-A-1", "HDMI-A-1"},
		{"Vulos — DP-2", "DP-2"},
	} {
		got := parsed.Rules[i]
		// The title must be EXACTLY what screenWindowTitle produces, em dash and
		// all. A rule that matches no window places nothing, silently.
		if got.Title != want.title {
			t.Errorf("rule %d title = %q, want %q — this must match screenWindowTitle in "+
				"frontend/src/providers/screenIdentity.ts or the rule matches no window",
				i, got.Title, want.title)
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
