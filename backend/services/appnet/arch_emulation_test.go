package appnet

// arch_emulation_test.go — the emulation half of arch.go.
//
// ─────────────────────────────────────────────────────────────────────────────
// WHY THIS FILE EXISTS AT ALL
//
// Before it, `EmulatedArches`, `EmulationCanServe`, `EvaluateArch`,
// `DeliveryKindOf`, `ParseEmulationPolicy`, `probeBinfmtArches` and
// `archFromBinfmtEntry` — roughly 450 lines, the entire second half of arch.go
// and every sentence "THE ONE ANSWER THE APP HUB RENDERS" describes — had
// ZERO tests and ZERO callers. A repo-wide grep for any of those names outside
// arch.go itself returns only prose in roadmap/. The App Hub still re-derives
// availability client-side with `app.arch.includes(systemArch)`, exactly as
// arch.go's own comment says it does.
//
// That is worth writing down rather than quietly fixing: the reason the wrong
// claim about foreign-arch Flatpak survived in a code comment for as long as it
// did is that nothing executed the function underneath it. These tests do not
// make it called. They make it CHECKED, so the next correction has something to
// go red.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── item 5: EmulatedArches must not miss box64 ──────────────────────────────

// TestEmulatedArches_FindsAnEmulatorWithNoBinfmtHandler is the defect.
//
// A box with box64 installed and working reported NO EMULATION, because the
// only probe read /proc/sys/fs/binfmt_misc and box64 is invoked by name.
// Measured 2026-08-17, debian:trixie-slim/arm64: `box64 --version` exits 0 and
// prints "[BOX64] Box64 with Dynarec v0.3.4"; binfmt_misc is not even mounted.
func TestEmulatedArches_FindsAnEmulatorWithNoBinfmtHandler(t *testing.T) {
	withBoxArch(t, "arm64")
	withNoBinfmt(t)
	withEmulatorsOnPath(t, "box64")

	got := EmulatedArches()
	if len(got) != 1 || got[0] != "amd64" {
		t.Fatalf("EmulatedArches() = %v on an arm64 box with a working box64 — want [amd64]. "+
			"A binfmt-only probe answers \"no emulation here\" for a box that emulates fine.", got)
	}
	emuls := EmulatorsAvailable()
	if len(emuls) != 1 || emuls[0].Name != "box64" || emuls[0].Via != "binary" {
		t.Fatalf("EmulatorsAvailable() = %+v, want one box64 found via the PATH", emuls)
	}
	if !emuls[0].BindsHostLibraries {
		t.Error("box64 is recorded as NOT binding host libraries — that is the property the whole " +
			"GL result rests on: an x86_64 glxinfo under box64 reported this machine's own " +
			"aarch64 Mesa, while qemu-user could not get a GL visual at all")
	}
}

// TestEmulatedArches_NoEmulatorIsAnEmptyAnswer is the CONTROL. A probe that
// reported an emulator whenever it was asked would pass the test above and be
// worthless, and the honest default on a box that installed nothing is nothing.
func TestEmulatedArches_NoEmulatorIsAnEmptyAnswer(t *testing.T) {
	withBoxArch(t, "arm64")
	withNoBinfmt(t)
	withEmulatorsOnPath(t) // none

	if got := EmulatedArches(); len(got) != 0 {
		t.Errorf("EmulatedArches() = %v on a box with no emulator at all, want empty", got)
	}
	if got := EmulatorsAvailable(); len(got) != 0 {
		t.Errorf("EmulatorsAvailable() = %+v with nothing installed", got)
	}
}

// TestEmulatedArches_NeverIncludesTheNativeArch: the native architecture is not
// emulated, and reporting it would make every app on the box look translated.
func TestEmulatedArches_NeverIncludesTheNativeArch(t *testing.T) {
	withBoxArch(t, "amd64")
	withNoBinfmt(t)
	withEmulatorsOnPath(t, "box64", "qemu-x86_64", "qemu-aarch64")

	got := EmulatedArches()
	for _, a := range got {
		if a == "amd64" {
			t.Fatalf("EmulatedArches() = %v on an amd64 box — the native arch is not emulated", got)
		}
	}
	if len(got) != 1 || got[0] != "arm64" {
		t.Fatalf("EmulatedArches() = %v, want [arm64] (qemu-aarch64 is the only foreign one here)", got)
	}
}

// TestEmulatedArches_MergesBothSources pins that neither source subsumes the
// other. Debian's box64 package DOES ship /usr/lib/binfmt.d/box64.conf, so on a
// systemd box the handler may be registered; in a container it is not and only
// the PATH probe sees it. Dropping either source loses a real case.
func TestEmulatedArches_MergesBothSources(t *testing.T) {
	withBoxArch(t, "arm64")
	// A real qemu-x86_64 binfmt entry, in /proc's own format.
	dir := t.TempDir()
	writeBinfmt(t, dir, "qemu-x86_64", "enabled\ninterpreter /usr/bin/qemu-x86_64\nflags: OCF\noffset 0\nmagic 7f454c4602010100000000000000000002003e00\n")
	withBinfmtDir(t, dir)
	withEmulatorsOnPath(t, "qemu-aarch64") // a DIFFERENT arch, only on PATH

	got := EmulatedArches()
	if len(got) != 1 || got[0] != "amd64" {
		// qemu-aarch64 serves arm64, which IS this box's native arch, so it is
		// correctly dropped; amd64 must survive from the binfmt side.
		t.Fatalf("EmulatedArches() = %v, want [amd64] from the binfmt source", got)
	}
	emuls := EmulatorsAvailable()
	if len(emuls) != 1 || emuls[0].Via != "binfmt" || emuls[0].Name != "qemu-x86_64" {
		t.Fatalf("EmulatorsAvailable() = %+v, want the binfmt-registered qemu-x86_64", emuls)
	}
	if emuls[0].BindsHostLibraries {
		t.Error("qemu-user is recorded as binding host libraries — it does not, and the measured " +
			"consequence is \"Error: couldn't find RGB GLX visual or fbconfig\"")
	}
}

// TestEmulatorFromBinfmtEntry_ReadsRealEntries feeds the parser the two
// handler definitions Debian actually ships, read out of the packages on
// 2026-08-17, plus the states that must be refused.
func TestEmulatorFromBinfmtEntry_ReadsRealEntries(t *testing.T) {
	// /usr/lib/binfmt.d/box64.conf registers e_machine 0x3e at offset 0 with
	// interpreter /usr/bin/box64 and NO flags — so it is the non-F case, and
	// the interpreter has to exist for the entry to count.
	realBox64 := "enabled\ninterpreter " + mustExistingFile(t) +
		"\nflags: \noffset 0\nmagic 7f454c4602010100000000000000000002003e00\n"
	arch, interp := emulatorFromBinfmtEntry(realBox64)
	if arch != "amd64" {
		t.Errorf("box64's real handler parsed as %q, want amd64", arch)
	}
	if interp == "" {
		t.Error("the interpreter was not reported — without it a handler cannot be identified, " +
			"and the GL answer depends on WHICH emulator it is")
	}

	cases := []struct {
		name string
		body string
		want string
	}{
		{"disabled handler", "disabled\ninterpreter /usr/bin/qemu-x86_64\nflags: F\noffset 0\nmagic 7f454c4602010100000000000000000002003e00\n", ""},
		{"interpreter does not exist, no F flag", "enabled\ninterpreter /nonexistent/qemu-x86_64\nflags: OC\noffset 0\nmagic 7f454c4602010100000000000000000002003e00\n", ""},
		{"F flag, interpreter need not exist", "enabled\ninterpreter /nonexistent/qemu-x86_64\nflags: OCF\noffset 0\nmagic 7f454c4602010100000000000000000002003e00\n", "amd64"},
		{"magic is not an ELF header", "enabled\ninterpreter /bin/sh\nflags: F\noffset 0\nmagic 4d5a9000\n", ""},
		{"non-zero offset", "enabled\ninterpreter /bin/sh\nflags: F\noffset 4\nmagic 7f454c4602010100000000000000000002003e00\n", ""},
		{"aarch64 machine 0xb7", "enabled\ninterpreter /bin/sh\nflags: F\noffset 0\nmagic 7f454c460201010000000000000000000200b700\n", "arm64"},
	}
	for _, c := range cases {
		if got := archFromBinfmtEntry(c.body); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestEmulatedArches_OverrideStillWins keeps the developer seam working, and
// pins that an override which names ARCHITECTURES cannot claim the graphics
// property it has no way to know.
func TestEmulatedArches_OverrideStillWins(t *testing.T) {
	withBoxArch(t, "arm64")
	withNoBinfmt(t)
	withEmulatorsOnPath(t, "box64")
	t.Setenv("VULOS_EMULATED_ARCHES", "x86_64,i686")
	InvalidateEmulatedArchCache()

	got := EmulatedArches()
	if len(got) != 2 || got[0] != "amd64" || got[1] != "i386" {
		t.Fatalf("EmulatedArches() = %v, want [amd64 i386] normalised from the override", got)
	}
	if EmulationBindsHostLibrariesFor("amd64") {
		t.Error("the override claimed host-library binding. It names architectures, not " +
			"emulators, so it cannot know that — and the safe answer is the qemu one.")
	}
}

// ── item 6: the split, and what each half is allowed to say ─────────────────

// TestEmulationCanInstall_FlatpakIsYesAndItWasMeasured is the correction. The
// old single function answered NO for Flatpak on the reasoning that the ref
// does not exist; `flatpak install --arch=x86_64` deployed a 1.4 GB x86_64
// runtime onto an aarch64 box.
func TestEmulationCanInstall_FlatpakIsYesAndItWasMeasured(t *testing.T) {
	if !EmulationCanInstall(DeliveryFlatpak) {
		t.Error("EmulationCanInstall(flatpak) is false. Measured 2026-08-17: `flatpak install " +
			"-y --arch=x86_64 flathub org.gnome.Calculator` installed the app AND its whole " +
			"x86_64 GNOME platform on an aarch64 installation.")
	}
	if !EmulationCanInstall(DeliveryBinary) {
		t.Error("EmulationCanInstall(binary) is false — an ELF plus a handler is the one case " +
			"nobody disputes")
	}
	if EmulationCanInstall(DeliveryPackage) {
		t.Error("EmulationCanInstall(package) is true — dpkg refuses at resolve time and " +
			"multiarch is deliberately not enabled")
	}
	if EmulationCanInstall(DeliveryUnknown) {
		t.Error("DeliveryUnknown — the ZERO VALUE — acquired an install path. A recipe shape " +
			"nobody classified must not get one by default.")
	}
}

// TestEmulationRunsWell_IsNarrowerThanCanInstall is the half that keeps the two
// questions apart. If both functions ever agree on every input, the split has
// collapsed back into the thing it was made from.
func TestEmulationRunsWell_IsNarrowerThanCanInstall(t *testing.T) {
	if !EmulationRunsWell(DeliveryBinary) {
		t.Error("EmulationRunsWell(binary) is false — box64 in a private prefix bound the host's " +
			"own GL stack, measured identical to the native control")
	}
	if EmulationRunsWell(DeliveryFlatpak) {
		t.Error("EmulationRunsWell(flatpak) is true. bwrap supplies the runtime's foreign /usr, " +
			"so the substitution box64 depends on cannot reach the host's libraries.")
	}
	differ := false
	for _, k := range []DeliveryKind{DeliveryUnknown, DeliveryFlatpak, DeliveryPackage, DeliveryBinary} {
		if EmulationCanInstall(k) != EmulationRunsWell(k) {
			differ = true
		}
	}
	if !differ {
		t.Fatal("EmulationCanInstall and EmulationRunsWell agree on every delivery kind — the " +
			"split has collapsed and one function is again answering two questions")
	}
}

// TestEvaluateArch_TheTwoRefusalsReadDifferently is the point of the split from
// the user's side. "Nothing exists to download" and "it exists and we chose not
// to offer it" are different facts, and a UI that shows one sentence for both
// is telling one of the two users something false.
func TestEvaluateArch_TheTwoRefusalsReadDifferently(t *testing.T) {
	base := ArchRequest{
		AppName:   "Blender",
		Declared:  []string{"amd64"},
		Supported: []string{"arm64"},
		Emulated:  []string{"amd64"},
		Policy:    EmulationOptIn,
	}

	pkg := base
	pkg.Delivery = DeliveryPackage
	flat := base
	flat.Delivery = DeliveryFlatpak

	av1 := EvaluateArch(pkg)
	av2 := EvaluateArch(flat)
	for _, av := range []ArchAvailability{av1, av2} {
		if av.State != ArchStateUnavailable || av.Installable {
			t.Fatalf("expected an unavailable verdict, got %+v", av)
		}
	}
	if av1.Detail == av2.Detail {
		t.Fatalf("both refusals produce the SAME sentence:\n  %s\n"+
			"One means no build can reach this box; the other means a build can and does reach "+
			"it and we decline to offer it.", av1.Detail)
	}
	// And the Flatpak sentence must not repeat the claim the measurement broke.
	for _, banned := range []string{"there is nothing to download", "No package is published"} {
		if strings.Contains(av2.Detail, banned) {
			t.Errorf("the Flatpak refusal still says %q, which `flatpak install --arch=x86_64` "+
				"disproved: %s", banned, av2.Detail)
		}
	}
	// Neither may claim the app fails to RUN — that case is untestable-on-arm64-mac.
	for _, av := range []ArchAvailability{av1, av2} {
		low := strings.ToLower(av.Detail)
		if strings.Contains(low, "will not run") || strings.Contains(low, "cannot run") {
			t.Errorf("a refusal claims the app does not run. §6 Q3 got `bwrap: Operation not "+
				"permitted`, a CONTAINER PRIVILEGE limit that would stop a native app too. "+
				"Nobody has measured this: %s", av.Detail)
		}
	}
}

// TestEvaluateArch_NeedsGPUIsScopedToDelivery is §9.2. E3 used to refuse every
// GPU-bound app on the flat claim that emulation loses acceleration. That is
// qemu's measured behaviour and NOT box64's.
func TestEvaluateArch_NeedsGPUIsScopedToDelivery(t *testing.T) {
	base := ArchRequest{
		AppName:          "Blender",
		Declared:         []string{"amd64"},
		Supported:        []string{"arm64"},
		Emulated:         []string{"amd64"},
		Policy:           EmulationOptIn,
		Delivery:         DeliveryBinary,
		NeedsGPU:         true,
		EmulationEnabled: true,
	}

	qemuOnly := base // BindsHostLibraries false — qemu's answer
	if av := EvaluateArch(qemuOnly); av.State != ArchStateUnavailable {
		t.Errorf("a GPU-bound app was offered on a box whose only emulator cannot obtain a GL "+
			"visual at all: %+v", av)
	}

	withBox64 := base
	withBox64.EmulatorBindsHostLibraries = true
	av := EvaluateArch(withBox64)
	if av.State != ArchStateEmulated || !av.Installable {
		t.Fatalf("a GPU-bound app was refused on a box where the emulator binds the HOST's own "+
			"graphics driver — measured: an x86_64 glxinfo under box64 reported this machine's "+
			"aarch64 Mesa 25.0.7, `direct rendering: Yes`, identical to native. Got %+v", av)
	}
	if strings.Contains(av.Detail, "without graphics acceleration") {
		t.Errorf("the sentence still asserts no acceleration for the case that disproved it: %s", av.Detail)
	}
	// It must also not over-claim. Hardware acceleration under box64 is
	// UNMEASURED — §5.3 ran on a container with no GPU, where the host stack is
	// llvmpipe. What was proved is which stack was bound.
	for _, overclaim := range []string{"hardware acceleration", "full graphics acceleration", "GPU-accelerated"} {
		if strings.Contains(strings.ToLower(av.Detail), strings.ToLower(overclaim)) {
			t.Errorf("the sentence promises %q, which nobody has measured — §5.3 ran on a "+
				"container with no GPU: %s", overclaim, av.Detail)
		}
	}
}

// TestEvaluateArch_NativeAndPolicyPathsStillHold is the control on the whole
// switch: the rewrite must not have changed what a native app, an opted-out
// box, or a box with no emulator says.
func TestEvaluateArch_NativeAndPolicyPathsStillHold(t *testing.T) {
	native := ArchRequest{AppName: "Gitea", Declared: []string{"arm64"}, Supported: []string{"arm64"}}
	if av := EvaluateArch(native); av.State != ArchStateNative || !av.Installable {
		t.Errorf("a natively supported app is no longer native: %+v", av)
	}

	optedOut := ArchRequest{
		AppName: "Code", Declared: []string{"amd64"}, Supported: []string{"arm64"},
		Emulated: []string{"amd64"}, Delivery: DeliveryBinary, Policy: EmulationNever,
	}
	if av := EvaluateArch(optedOut); av.State != ArchStateUnavailable {
		t.Errorf("an entry whose emulation policy is `never` was offered: %+v", av)
	}

	noEmulator := ArchRequest{
		AppName: "Code", Declared: []string{"amd64"}, Supported: []string{"arm64"},
		Emulated: nil, Delivery: DeliveryBinary, Policy: EmulationOptIn,
	}
	if av := EvaluateArch(noEmulator); av.State != ArchStateUnavailable {
		t.Errorf("an app was marked emulatable on a box with no emulator: %+v", av)
	}

	// The badge is never the bare word "Unavailable", for every refusal above.
	for _, req := range []ArchRequest{optedOut, noEmulator} {
		if EvaluateArch(req).Badge == "Unavailable" {
			t.Error("badge is the bare word \"Unavailable\" — it reads as broken rather than as a " +
				"fact about this hardware")
		}
	}
}

// TestEvaluateArch_SpellsOneArchitectureScheme — mixing amd64 and x86_64 in one
// paragraph makes one box look like two.
func TestEvaluateArch_SpellsOneArchitectureScheme(t *testing.T) {
	reqs := []ArchRequest{
		{AppName: "A", Declared: []string{"x86_64"}, Supported: []string{"aarch64"}, Delivery: DeliveryFlatpak},
		{AppName: "B", Declared: []string{"x86_64"}, Supported: []string{"aarch64"}, Delivery: DeliveryPackage},
		{AppName: "C", Declared: []string{"x86_64"}, Supported: []string{"aarch64"}, Delivery: DeliveryBinary,
			Emulated: []string{"amd64"}, Policy: EmulationOptIn, EmulationEnabled: true},
		{AppName: "D", Declared: []string{"x86_64"}, Supported: []string{"aarch64"}, Delivery: DeliveryBinary,
			Emulated: []string{"amd64"}, Policy: EmulationOptIn, NeedsGPU: true},
	}
	for _, req := range reqs {
		av := EvaluateArch(req)
		for _, foreign := range []string{"x86_64", "aarch64"} {
			if strings.Contains(av.Detail, foreign) {
				t.Errorf("%s: the sentence uses the %s spelling: %s", req.AppName, foreign, av.Detail)
			}
		}
		if av.Detail == "" {
			t.Errorf("%s: no sentence at all", req.AppName)
		}
	}
}

// TestDeliveryKindOf_ClassifiesEveryShape — DeliveryKind decides whether
// emulation is even in the conversation, and the zero value must be unknown.
func TestDeliveryKindOf_ClassifiesEveryShape(t *testing.T) {
	cases := []struct {
		name                   string
		flatpakID, downloadURL string
		artifacts              int
		installScript          string
		want                   DeliveryKind
	}{
		{"flatpak", "org.gimp.GIMP", "", 0, "", DeliveryFlatpak},
		{"flatpak wins over a download", "org.gimp.GIMP", "https://x/y", 2, "", DeliveryFlatpak},
		{"per-arch artifacts", "", "", 2, "", DeliveryBinary},
		{"legacy single download_url", "", "https://x/y", 0, "", DeliveryBinary},
		{"install shell", "", "", 0, "apt-get install -y blender", DeliveryPackage},
		{"nothing at all", "", "", 0, "", DeliveryUnknown},
	}
	for _, c := range cases {
		if got := DeliveryKindOf(c.flatpakID, c.downloadURL, c.artifacts, c.installScript); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestParseEmulationPolicy_DefaultsClosed — a typo must not open the gate.
func TestParseEmulationPolicy_DefaultsClosed(t *testing.T) {
	if ParseEmulationPolicy("opt-in") != EmulationOptIn {
		t.Error("\"opt-in\" did not parse as opt-in")
	}
	if ParseEmulationPolicy("  OPT-IN  ") != EmulationOptIn {
		t.Error("case and whitespace are not tolerated on the one value that is spelled by hand")
	}
	for _, s := range []string{"", "never", "optin", "opt_in", "yes", "true", "always"} {
		if ParseEmulationPolicy(s) != EmulationNever {
			t.Errorf("%q opened the emulation gate — an unrecognised value must default closed", s)
		}
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func withBoxArch(t *testing.T, arch string) {
	t.Helper()
	t.Setenv("VULOS_BOX_ARCH", arch)
	InvalidateArchCache()
	t.Cleanup(InvalidateArchCache)
}

func withNoBinfmt(t *testing.T) {
	t.Helper()
	orig := binfmtMiscDir
	binfmtMiscDir = filepath.Join(t.TempDir(), "no-binfmt-here")
	t.Cleanup(func() { binfmtMiscDir = orig; InvalidateEmulatedArchCache() })
	InvalidateEmulatedArchCache()
}

func withBinfmtDir(t *testing.T, dir string) {
	t.Helper()
	orig := binfmtMiscDir
	binfmtMiscDir = dir
	t.Cleanup(func() { binfmtMiscDir = orig; InvalidateEmulatedArchCache() })
	InvalidateEmulatedArchCache()
}

// withEmulatorsOnPath replaces the emulator probe. It does NOT stub out
// knownEmulators: the mapping from a binary name to the architecture it
// executes, and to whether it binds host libraries, is the thing under test.
func withEmulatorsOnPath(t *testing.T, names ...string) {
	t.Helper()
	present := map[string]bool{}
	for _, n := range names {
		present[n] = true
	}
	orig := emulatorRuns
	emulatorRuns = func(name string) bool { return present[name] }
	t.Cleanup(func() { emulatorRuns = orig; InvalidateEmulatedArchCache() })
	InvalidateEmulatedArchCache()
}

func writeBinfmt(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatalf("write binfmt fixture: %v", err)
	}
}

// mustExistingFile returns a path that certainly exists, for the non-F binfmt
// case, which requires the interpreter to be resolvable right now.
func mustExistingFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "box64")
	if err := os.WriteFile(p, []byte("x"), 0755); err != nil {
		t.Fatalf("write fake interpreter: %v", err)
	}
	return p
}
