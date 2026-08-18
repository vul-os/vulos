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

	av1 := EvaluateArch(signedReq(pkg))
	av2 := EvaluateArch(signedReq(flat))
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
	if av := EvaluateArch(signedReq(qemuOnly)); av.State != ArchStateUnavailable {
		t.Errorf("a GPU-bound app was offered on a box whose only emulator cannot obtain a GL "+
			"visual at all: %+v", av)
	}

	withBox64 := base
	withBox64.EmulatorBindsHostLibraries = true
	av := EvaluateArch(signedReq(withBox64))
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
	if av := EvaluateArch(signedReq(native)); av.State != ArchStateNative || !av.Installable {
		t.Errorf("a natively supported app is no longer native: %+v", av)
	}

	optedOut := ArchRequest{
		AppName: "Code", Declared: []string{"amd64"}, Supported: []string{"arm64"},
		Emulated: []string{"amd64"}, Delivery: DeliveryBinary, Policy: EmulationNever,
	}
	if av := EvaluateArch(signedReq(optedOut)); av.State != ArchStateUnavailable {
		t.Errorf("an entry whose emulation policy is `never` was offered: %+v", av)
	}

	noEmulator := ArchRequest{
		AppName: "Code", Declared: []string{"amd64"}, Supported: []string{"arm64"},
		Emulated: nil, Delivery: DeliveryBinary, Policy: EmulationOptIn,
	}
	if av := EvaluateArch(signedReq(noEmulator)); av.State != ArchStateUnavailable {
		t.Errorf("an app was marked emulatable on a box with no emulator: %+v", av)
	}

	// The badge is never the bare word "Unavailable", for every refusal above.
	for _, req := range []ArchRequest{optedOut, noEmulator} {
		if EvaluateArch(signedReq(req)).Badge == "Unavailable" {
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
		av := EvaluateArch(signedReq(req))
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

// ── item 7: the rungs the App Hub actually renders ──────────────────────────

// TestEvaluateArch_RungFourWasAConstantNothingProduced.
//
// ArchStateOtherInstance has been in the const block since the file was written
// and `elsewhereClause` mentioned a sibling instance in PROSE while every
// refusal arm still set State = unavailable. So a UI switching on `state` — which
// is the field the wire type exists for — could not tell rung 4 from rung 5, and
// the one string that carried the difference was the one nobody parses.
func TestEvaluateArch_RungFourWasAConstantNothingProduced(t *testing.T) {
	base := ArchRequest{
		AppName:   "Lutris",
		Declared:  []string{"amd64"},
		Supported: []string{"arm64"},
		Delivery:  DeliveryFlatpak,
	}

	alone := EvaluateArch(signedReq(base))
	if alone.State != ArchStateUnavailable {
		t.Fatalf("with no sibling instance the app must be rung 5, got %+v", alone)
	}

	withSibling := base
	withSibling.OtherInstance = "studio-box"
	av := EvaluateArch(signedReq(withSibling))
	if av.State != ArchStateOtherInstance {
		t.Fatalf("a sibling instance runs this app and the state is still %q. Rung 4 is a "+
			"state, not a sentence — a hub switching on `state` renders %q identically to an "+
			"app nothing in the fleet can run: %+v", av.State, av.State, av)
	}
	if !strings.Contains(av.Detail, "studio-box") || !strings.Contains(av.CardBadge, "studio-box") {
		t.Errorf("rung 4 does not NAME the instance. Naming it is the whole difference between "+
			"a fleet that is one OS and an app that vanished: badge=%q detail=%q",
			av.CardBadge, av.Detail)
	}
	if av.Installable {
		t.Error("rung 4 reported installable — the app runs on ANOTHER box; offering an install " +
			"here is the thing rung 5 refuses, wearing a friendlier badge")
	}
	// Rung 3 outranks rung 4: an app that runs here, slowly and labelled, beats
	// one the user has to walk to another machine for.
	emulatable := withSibling
	emulatable.Delivery = DeliveryBinary
	emulatable.Emulated = []string{"amd64"}
	emulatable.Policy = EmulationOptIn
	if av := EvaluateArch(signedReq(emulatable)); av.State != ArchStateEmulated {
		t.Errorf("an app this box can emulate was sent to the sibling instead — §1 applies the "+
			"rungs in order and 3 is above 4: %+v", av)
	}
}

// TestEvaluateArch_NoEmulatorAndNotClearedReadDifferently. These two used to be
// one switch arm saying "with no emulation available for it", which is a claim
// about the BOX and is FALSE on a box that has box64 installed and an entry that
// simply has not been cleared for emulation. It sends the reader looking for an
// emulator they already have.
func TestEvaluateArch_NoEmulatorAndNotClearedReadDifferently(t *testing.T) {
	base := ArchRequest{
		AppName: "Bottles", Declared: []string{"amd64"}, Supported: []string{"arm64"},
		Delivery: DeliveryBinary,
	}

	noEmulator := base
	noEmulator.Policy = EmulationOptIn // cleared, but nothing here to run it

	notCleared := base
	notCleared.Emulated = []string{"amd64"} // box64 IS here
	notCleared.Policy = EmulationNever

	a, b := EvaluateArch(signedReq(noEmulator)), EvaluateArch(signedReq(notCleared))
	for _, av := range []ArchAvailability{a, b} {
		if av.State != ArchStateUnavailable {
			t.Fatalf("expected rung 5, got %+v", av)
		}
	}
	if a.Detail == b.Detail {
		t.Fatalf("both refusals produce the SAME sentence:\n  %s\n"+
			"One box has no emulator; the other has one and the entry is not cleared to use it.", a.Detail)
	}
	if strings.Contains(b.Detail, "no emulator") || strings.Contains(b.Detail, "no emulation") {
		t.Errorf("a box WITH a working emulator is told it has none: %s", b.Detail)
	}
}

// TestEvaluateArch_CardBadgeIsShortAndNamesTheFact.
//
// The card chip and the panel heading are different budgets, and the reason is
// measured: AppHub.tsx records that an app needing `ppc64el` squeezed the card
// body to 74px at every width from 768 to 1600 when the status sat in the
// `flex: none` action column. The chip therefore says the actionable fact and
// the panel says the §1 badge — and BOTH come from here, so a card and its own
// panel cannot disagree.
func TestEvaluateArch_CardBadgeIsShortAndNamesTheFact(t *testing.T) {
	unavailable := EvaluateArch(signedReq(ArchRequest{
		AppName: "Lutris", Declared: []string{"x86_64"}, Supported: []string{"arm64"},
		Delivery: DeliveryFlatpak,
	}))
	if unavailable.CardBadge != "Needs amd64" {
		t.Errorf("card badge = %q, want \"Needs amd64\" — the chip has to name the fact the "+
			"user can act on, and %q is the panel heading", unavailable.CardBadge, unavailable.Badge)
	}
	if unavailable.Badge != "Not available on this box" {
		t.Errorf("panel badge = %q, want the §1 wording", unavailable.Badge)
	}

	twoArches := EvaluateArch(signedReq(ArchRequest{
		AppName: "Lutris", Declared: []string{"x86_64", "amd64", "i686"}, Supported: []string{"arm64"},
		Delivery: DeliveryFlatpak,
	}))
	if twoArches.CardBadge != "Needs amd64/i386" {
		t.Errorf("card badge = %q — x86_64 and amd64 are ONE requirement and must not render "+
			"as two", twoArches.CardBadge)
	}
	if len(twoArches.Needs) != 2 || twoArches.Needs[0] != "amd64" || twoArches.Needs[1] != "i386" {
		t.Errorf("needs = %v, want [amd64 i386] — the App Hub renders this list and folds no "+
			"spellings of its own", twoArches.Needs)
	}

	native := EvaluateArch(signedReq(ArchRequest{
		AppName: "Gitea", Declared: []string{"arm64"}, Supported: []string{"arm64"},
	}))
	if native.CardBadge != "" || native.Badge != "" {
		t.Errorf("a native app carries badges %q/%q — rung 1 is \"Install button, no badge\", "+
			"and a badge on every app that simply works is noise on every card in the catalogue",
			native.Badge, native.CardBadge)
	}
}

// unmeasuredClaims are the phrases NO user-facing string may contain, each with
// the measurement that is missing.
//
// This is the §9.1/§9.2 discipline made general. The two existing tests above
// check the specific verdicts they are about; this list is applied to EVERY
// string of EVERY state, so copy added later cannot slip a claim in through a
// path those two do not visit.
var unmeasuredClaims = []struct{ phrase, why string }{
	{"cannot run", "§6 Q3 got `bwrap: Operation not permitted`, a CONTAINER PRIVILEGE limit that " +
		"would have stopped a NATIVE app in the same container. Whether a foreign-arch Flatpak runs " +
		"is recorded untestable-on-arm64-mac and nobody has measured it."},
	{"will not run", "same: nobody has measured whether it runs."},
	{"would not run", "same: nobody has measured whether it runs."},
	{"fails to run", "same: nobody has measured whether it runs."},
	{"hardware acceleration", "§5.3 ran in a container with NO GPU, where the host stack is llvmpipe. " +
		"What was proved is which stack box64 bound, not that it reaches hardware."},
	{"gpu-accelerated", "same: unproven on a GPU-less machine."},
	{"full graphics acceleration", "same: unproven on a GPU-less machine."},
	{"in settings", "there is no Settings screen in this build (frontend/src/builtin/ has no " +
		"settings app). Naming a control that does not exist is as unmeasured as naming a benchmark " +
		"nobody ran."},
	{"x86_64", "Debian spelling only — mixing amd64 and x86_64 in one paragraph makes one box look " +
		"like two."},
	{"aarch64", "Debian spelling only."},
}

// TestEvaluateArch_NoUnmeasuredClaimReachesTheUser sweeps every rung and every
// string on the answer.
func TestEvaluateArch_NoUnmeasuredClaimReachesTheUser(t *testing.T) {
	arm := []string{"arm64"}
	cases := []struct {
		name string
		req  ArchRequest
	}{
		{"native", ArchRequest{AppName: "Gitea", Declared: []string{"arm64"}, Supported: arm}},
		{"flatpak, no build here", ArchRequest{AppName: "Steam", Declared: []string{"x86_64"},
			Supported: arm, Delivery: DeliveryFlatpak}},
		{"package, dpkg refuses", ArchRequest{AppName: "Steam", Declared: []string{"x86_64"},
			Supported: arm, Delivery: DeliveryPackage}},
		{"flatpak that could install and is declined", ArchRequest{AppName: "Blender",
			Declared: []string{"amd64"}, Supported: arm, Delivery: DeliveryFlatpak,
			Emulated: []string{"amd64"}, Policy: EmulationOptIn}},
		{"unclassified recipe", ArchRequest{AppName: "Mystery", Declared: []string{"amd64"},
			Supported: arm}},
		{"declares nothing usable", ArchRequest{AppName: "Broken", Declared: []string{""},
			Supported: arm, Delivery: DeliveryBinary}},
		{"gpu app, qemu only", ArchRequest{AppName: "Blender", Declared: []string{"amd64"},
			Supported: arm, Delivery: DeliveryBinary, Emulated: []string{"amd64"},
			Policy: EmulationOptIn, NeedsGPU: true}},
		{"no emulator installed", ArchRequest{AppName: "Bottles", Declared: []string{"amd64"},
			Supported: arm, Delivery: DeliveryBinary, Policy: EmulationOptIn}},
		{"emulator here, entry not cleared", ArchRequest{AppName: "Bottles", Declared: []string{"amd64"},
			Supported: arm, Delivery: DeliveryBinary, Emulated: []string{"amd64"}}},
		{"emulated, box opted out", ArchRequest{AppName: "Heroic", Declared: []string{"amd64"},
			Supported: arm, Delivery: DeliveryBinary, Emulated: []string{"amd64"}, Policy: EmulationOptIn}},
		{"emulated, box opted in, box64", ArchRequest{AppName: "Heroic", Declared: []string{"amd64"},
			Supported: arm, Delivery: DeliveryBinary, Emulated: []string{"amd64"}, Policy: EmulationOptIn,
			EmulationEnabled: true, EmulatorBindsHostLibraries: true}},
		{"emulated, box opted in, qemu only", ArchRequest{AppName: "Heroic", Declared: []string{"amd64"},
			Supported: arm, Delivery: DeliveryBinary, Emulated: []string{"amd64"}, Policy: EmulationOptIn,
			EmulationEnabled: true}},
		{"sibling instance has it", ArchRequest{AppName: "Steam", Declared: []string{"amd64"},
			Supported: arm, Delivery: DeliveryFlatpak, OtherInstance: "desk-box"}},
	}

	seenStates := map[string]bool{}
	for _, c := range cases {
		av := EvaluateArch(signedReq(c.req))
		seenStates[av.State] = true
		for _, field := range []struct{ what, s string }{
			{"badge", av.Badge}, {"card_badge", av.CardBadge}, {"detail", av.Detail},
		} {
			low := strings.ToLower(field.s)
			for _, banned := range unmeasuredClaims {
				if strings.Contains(low, banned.phrase) {
					t.Errorf("%s: %s says %q.\n  %s\n  full text: %s",
						c.name, field.what, banned.phrase, banned.why, field.s)
				}
			}
		}
		if av.Badge == "Unavailable" || av.CardBadge == "Unavailable" {
			t.Errorf("%s: the bare word \"Unavailable\" — it reads as broken rather than as a "+
				"fact about this piece of hardware", c.name)
		}
		if av.State != ArchStateNative && av.Detail == "" {
			t.Errorf("%s: state %q with no sentence at all — the user is told the app is not "+
				"offered and never told why", c.name, av.State)
		}
	}

	// COVERAGE. Without this the sweep passes by never producing a rung: every
	// guard in this suite that lacked a coverage-count assertion has at some
	// point gone green over an empty examination.
	for _, want := range []string{ArchStateNative, ArchStateEmulated, ArchStateOtherInstance, ArchStateUnavailable} {
		if !seenStates[want] {
			t.Fatalf("the sweep never produced state %q, so nothing it asserts has been applied "+
				"to that rung's copy. seen: %v", want, seenStates)
		}
	}
	if len(cases) < 13 {
		t.Fatalf("the case table has shrunk to %d — it is the coverage, and a sweep over "+
			"fewer inputs asserts less while reporting the same PASS", len(cases))
	}
}

// TestEmulationOptedIn_DefaultsOffAndIsTheServersOwn. The box-level half of E2.
// An app that is present and crawls reads as a Vulos defect rather than as a
// hardware limit, so the default is off — and the override is the SERVER's
// environment, never anything a browser could send.
func TestEmulationOptedIn_DefaultsOffAndIsTheServersOwn(t *testing.T) {
	SetEmulationOptedIn(false)
	defer SetEmulationOptedIn(false)
	if EmulationOptedIn() {
		t.Error("emulated apps are ON by default — rung 3 is opt-in and labelled, which is the " +
			"whole reason it is not rung 2")
	}
	SetEmulationOptedIn(true)
	if !EmulationOptedIn() {
		t.Error("the box owner's opt-in did not take effect, so rung 3 is unreachable by design " +
			"rather than by policy")
	}
	SetEmulationOptedIn(false)

	t.Setenv("VULOS_EMULATION_OPTIN", "1")
	if !EmulationOptedIn() {
		t.Error("VULOS_EMULATION_OPTIN=1 did not opt the box in — without a server-side seam the " +
			"emulated rung cannot be exercised on any developer machine")
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

// signedReq marks a request's entry as carrying a publisher signature this box
// verified.
//
// EVERY case in this file is about ARCHITECTURE, and EvaluateArch asks the
// signature FIRST and short-circuits — deliberately, because that is the order
// InstallFromRegistry's own gates run in. ArchRequest.Signature is fail-closed
// on its zero value, so a case that did not say this would come back "awaiting
// publisher signature" and would assert nothing whatever about the rung it is
// named for. Written as a wrapper rather than a field on each literal so the
// reason is stated once and cannot be half-applied: a case that forgets it does
// not quietly pass, it stops producing its rung and the coverage assertion in
// TestEvaluateArch_NoUnmeasuredClaimReachesTheUser fires.
func signedReq(r ArchRequest) ArchRequest {
	r.Signature = SignatureSigned
	return r
}

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
