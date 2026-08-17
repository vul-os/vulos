package appnet

// arch.go — ARCH-01: what this box can actually install.
//
// Vulos publishes amd64 AND arm64 images, and a large share of the Flathub
// catalogue is x86_64-only (Steam, Chrome, Spotify, Zoom, VS Code among them).
// Listing those on an arm64 box offers installs that cannot succeed. The store
// has always carried the data to know better — RegistryEntry.Arch — but nothing
// ever compared it to anything, so every entry looked installable everywhere.
//
// ── The spelling ─────────────────────────────────────────────────────────────
//
// Three schemes are in play and mixing them silently matches nothing:
//
//	Debian / dpkg        amd64    arm64
//	Flatpak / uname -m   x86_64   aarch64
//	Go runtime.GOARCH    amd64    arm64
//
// The API speaks DEBIAN spelling. That is not a coin flip: RegistryEntry.Arch
// is already Debian-spelled and registry.json already carries ["amd64"] on 9
// entries, so any other choice would mean rewriting published data. Go's
// GOARCH happens to agree on these two, which is convenient but is NOT the
// reason — a GOARCH the two schemes disagree on (386, arm) still normalises
// through the one table below.
//
// NormalizeArch is the ONLY place a foreign spelling becomes a Vulos one.
// Every comparison in the codebase must run through it.
//
// ── What the box is, vs what the box can run ─────────────────────────────────
//
// The brief asked whether to report execution capability (binfmt/qemu,
// `flatpak --supported-arches`) rather than just identity. The answer is the
// simple one, and here is why it is not a cop-out:
//
//   - The ~120 desktop apps install through FLATPAK (VersionRecipe.FlatpakID).
//    `flatpak install` resolves refs for the arches the installation supports;
//    a qemu-user binfmt handler does not make an x86_64 ref appear in an
//    aarch64 installation. So for the catalogue that motivated this, binfmt is
//    simply not the mechanism that decides, and consulting it would produce a
//    confident wrong answer.
//   - flatpak IS the authority for that path, so when it is present we ask it
//    (`flatpak --supported-arches`) and normalise its x86_64/aarch64 spelling
//    through the same table. When it is absent or fails, we fall back to the
//    box's native arch, which is always true.
//
// Nothing here reads a request header or a user agent. Desktop apps are
// streamed FROM the box, so a user on an arm64 Mac driving an amd64 box must be
// offered amd64 apps. The architecture reported is the server process's own,
// full stop.

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// archAliases maps every foreign spelling to the Vulos (Debian) one.
// The single normalisation point referred to throughout this file.
var archAliases = map[string]string{
	// x86-64
	"x86_64": "amd64",
	"amd64":  "amd64",
	"x64":    "amd64",
	// 64-bit ARM
	"aarch64": "arm64",
	"arm64":   "arm64",
	// 32-bit x86
	"i386":  "i386",
	"i486":  "i386",
	"i586":  "i386",
	"i686":  "i386",
	"386":   "i386",
	"x86":   "i386",
	"amd32": "i386",
	// 32-bit ARM
	"armv7l":  "armhf",
	"armv6l":  "armhf",
	"armhf":   "armhf",
	"arm":     "armhf",
	"armv7hl": "armhf",
	// others that appear in registry data / uname output
	"riscv64": "riscv64",
	"ppc64le": "ppc64el",
	"ppc64el": "ppc64el",
	"s390x":   "s390x",
	"loong64": "loong64",
}

// NormalizeArch converts any known architecture spelling to the Debian one the
// Vulos API and registry.json speak. Unknown values are lower-cased and
// returned unchanged rather than dropped: an unrecognised arch that fails to
// match is a visible "not available", whereas silently mapping it to "" would
// make an entry look universally installable.
func NormalizeArch(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	if v, ok := archAliases[s]; ok {
		return v
	}
	return s
}

// BoxArch returns the architecture of the machine this server process is
// running on, in Debian spelling.
//
// It is derived from runtime.GOARCH — the architecture of THIS BINARY — and
// never from anything a client sends. VULOS_BOX_ARCH overrides it, which exists
// so the store's arch filtering can be tested on any developer machine; it is
// deliberately the server's own environment, not a request input.
func BoxArch() string {
	if v := boxArchOverride(); v != "" {
		return NormalizeArch(v)
	}
	return NormalizeArch(runtime.GOARCH)
}

// supportedArchCache memoises the flatpak query, which shells out.
type supportedArchCache struct {
	mu      sync.Mutex
	arches  []string
	updated time.Time
	ttl     time.Duration
}

var archCache = &supportedArchCache{ttl: time.Minute}

// SupportedArches returns every architecture this box can install apps for, in
// Debian spelling, with the native arch always first.
//
// When flatpak is installed, its own `--supported-arches` is consulted and
// merged in: flatpak is the installer for the desktop catalogue, so it — not
// this process — is the authority on which refs it will resolve. A flatpak that
// is absent, errors, or prints nothing usable leaves the result as the native
// arch alone, which is always true.
func SupportedArches() []string {
	native := BoxArch()

	archCache.mu.Lock()
	defer archCache.mu.Unlock()
	if archCache.arches != nil && time.Since(archCache.updated) < archCache.ttl {
		return append([]string(nil), archCache.arches...)
	}

	set := map[string]bool{native: true}
	for _, a := range flatpakSupportedArches() {
		if a != "" {
			set[a] = true
		}
	}
	out := []string{native}
	var rest []string
	for a := range set {
		if a != native {
			rest = append(rest, a)
		}
	}
	sort.Strings(rest)
	out = append(out, rest...)

	archCache.arches = out
	archCache.updated = time.Now()
	return append([]string(nil), out...)
}

// flatpakSupportedArches asks flatpak what it will install, normalised.
// Returns nil when flatpak is not present or does not answer.
func flatpakSupportedArches() []string {
	path, err := exec.LookPath("flatpak")
	if err != nil {
		return nil
	}
	out, err := exec.Command(path, "--supported-arches").Output()
	if err != nil {
		return nil
	}
	var arches []string
	for _, line := range strings.Split(string(out), "\n") {
		if a := NormalizeArch(line); a != "" {
			arches = append(arches, a)
		}
	}
	return arches
}

// InvalidateArchCache forces the next SupportedArches call to re-query flatpak.
// It also drops the emulated-arch probe: both caches key off BoxArch, so a test
// or a VULOS_BOX_ARCH change that reset only one would leave the other
// answering for a different machine — and the resulting mismatch (a box that is
// arm64 for identity and amd64 for emulation) is unreadable from the outside.
func InvalidateArchCache() {
	archCache.mu.Lock()
	archCache.arches = nil
	archCache.mu.Unlock()
	InvalidateEmulatedArchCache()
}

// ── What an UNSET `arch` means ───────────────────────────────────────────────
//
// It means NOBODY CHECKED. It does not mean "works everywhere", and the two
// are not close: 44 of the 56 entries in registry.json declare no arch, and
// among them are 13 Flatpak entries of which — measured against live Flathub in
// roadmap/ARCH-PLACEMENT.md §3 — a seventh of the catalogue is x86_64-only. An
// undeclared entry is an unverified claim to every architecture Vulos ships.
//
// The one-line fix (return false) has a 44-entry blast radius: it would empty
// most of the store on every box, including the 24 apt entries where dpkg would
// have given a correct answer on its own. Breaking 44 working entries to fix a
// claim is a worse defect than the claim.
//
// So the semantics are a POLICY, not a constant, and the migration is:
//
//  1. Today (default): undeclared is PERMITTED, and `ArchDeclared` lets every
//     caller tell an unverified entry from a verified one. Nothing regresses.
//  2. arch_test.go ratchets the undeclared count: it may fall, never rise. A
//     new entry that does not declare `arch` fails the build. The 44 are a
//     debt with a fixed ceiling instead of a growing one.
//  3. The catalogue merge (ARCH-PLACEMENT §8.1) populates `arch` for the
//     Flatpak entries from the Flathub audit and re-signs them. When the
//     ratchet reaches zero, SetStrictUndeclaredArch(true) becomes the default
//     and this stops being a policy at all.
//
// Strictness never lands before the data does. Flipping it early is precisely
// the "break 44 working entries" move.
var strictUndeclaredArch atomic.Bool

// SetStrictUndeclaredArch makes an entry with no declared architecture refuse
// to install rather than claim all of them. Default false — see above. This is
// the single line that changes when the catalogue merge completes.
func SetStrictUndeclaredArch(v bool) { strictUndeclaredArch.Store(v) }

// StrictUndeclaredArch reports the current policy. VULOS_ARCH_STRICT_UNDECLARED
// forces it on; like VULOS_BOX_ARCH it is read from the SERVER process's own
// environment and can never arrive from a client.
func StrictUndeclaredArch() bool {
	if v := os.Getenv("VULOS_ARCH_STRICT_UNDECLARED"); v == "1" || v == "true" {
		return true
	}
	return strictUndeclaredArch.Load()
}

// ArchDeclared reports whether an entry actually states which architectures it
// supports. Callers that need to distinguish "verified to run here" from
// "nobody checked" must use this rather than reading len(entry.Arch) — the
// meaning of an empty list is this file's to define, not each call site's.
//
// A NON-EMPTY list whose members all normalise to nothing (`[""]`) counts as
// declared and therefore fails closed in ArchSupported. That is malformed data,
// not an absent claim, and the two must not collapse into one another: the
// permissive branch is for entries nobody has audited yet, never for entries
// carrying a broken audit.
func ArchDeclared(declared []string) bool { return len(declared) > 0 }

// ArchSupported reports whether an app declaring `declared` can be installed on
// a box that supports `supported`.
//
// Both sides are normalised here so a registry that spells it x86_64 and a box
// that says amd64 still match; that is the whole failure this function exists
// to prevent. An empty declared list follows the policy documented above.
//
// This answers IDENTITY only — "is this box one of the machines the app is
// built for". It deliberately does not consult binfmt: see EmulatedArches.
func ArchSupported(declared, supported []string) bool {
	if !ArchDeclared(declared) {
		return !StrictUndeclaredArch()
	}
	if len(supported) == 0 {
		return false
	}
	have := make(map[string]bool, len(supported))
	for _, s := range supported {
		if n := NormalizeArch(s); n != "" {
			have[n] = true
		}
	}
	for _, d := range declared {
		if n := NormalizeArch(d); n != "" && have[n] {
			return true
		}
	}
	return false
}

// ArchUnavailableReason explains, in one sentence a UI can show verbatim, why
// an app is not installable here. Empty when it IS installable.
func ArchUnavailableReason(declared, supported []string) string {
	if ArchSupported(declared, supported) {
		return ""
	}
	want := make([]string, 0, len(declared))
	for _, d := range declared {
		if n := NormalizeArch(d); n != "" {
			want = append(want, n)
		}
	}
	if !ArchDeclared(declared) {
		// Only reachable under the strict policy. The wording says what is
		// actually wrong — the entry was never audited — rather than blaming
		// the box, because the fix is a catalogue change, not a hardware one.
		return "this app does not declare which architectures it supports, and unverified entries are refused on this box"
	}
	if len(want) == 0 {
		return "this app declares no architecture this box can install"
	}
	box := "unknown"
	if len(supported) > 0 {
		box = NormalizeArch(supported[0])
	}
	return "requires " + strings.Join(want, " or ") + "; this box is " + box
}

// boxArchOverride reads the server-side test override. Kept as its own function
// so the read is in one place and unmistakably an environment variable of the
// SERVER process — never anything that could arrive from a client.
func boxArchOverride() string {
	return os.Getenv("VULOS_BOX_ARCH")
}

// ═════════════════════════════════════════════════════════════════════════════
// IDENTITY vs EXECUTION CAPABILITY
// ═════════════════════════════════════════════════════════════════════════════
//
// Everything above answers "is this box one of the machines the app is built
// for". That is an IDENTITY question and binfmt has no bearing on it. The
// header of this file argues that, and it is right — for Flatpak. It is the
// WRONG answer applied to a downloaded binary, and applying one rule to both
// was the gap.
//
// The two cases differ in WHO refuses, not in how fast the app would be:
//
//	FLATPAK   A qemu binfmt handler does not add x86_64 to
//	          `flatpak --supported-arches` — that much is unchanged, and it is
//	          why the DEFAULT resolution refuses.
//
//	          THE REST OF WHAT THIS COMMENT USED TO SAY WAS WRONG, and it was
//	          wrong in the confident direction. It said the ref "does not exist
//	          for this installation" and that "nothing was ever downloaded to
//	          emulate". Measured 2026-08-17 on an aarch64 installation
//	          (roadmap/DISTRO-SOURCED-APPS.md §6):
//	          `flatpak remote-info --arch=x86_64 flathub org.blender.Blender`
//	          resolved the x86_64 ref with its commit, and
//	          `flatpak install -y --arch=x86_64 flathub org.gnome.Calculator`
//	          deployed the app and its entire x86_64 GNOME platform — 1.4 GB —
//	          onto the arm64 box. `--arch` is an explicit flag and it is
//	          honoured.
//
//	          The reason not to offer it survives the correction and is a
//	          different reason: bwrap gives the app the RUNTIME's x86_64 /usr,
//	          so box64's native-library substitution cannot reach the host's
//	          drivers, and qemu-user obtained no GL visual at all. That is a
//	          judgement about quality, not about possibility, and the two now
//	          live in two functions — EmulationCanInstall and EmulationRunsWell.
//
//	          NOT PROVEN either way: whether a foreign-arch Flatpak RUNS.
//	          `flatpak run --arch=x86_64` returned `bwrap: Creating new
//	          namespace failed: Operation not permitted`, which is a container
//	          privilege limit that would have stopped a NATIVE app in the same
//	          container. Recorded as untestable-on-arm64-mac. Nothing in this
//	          file may be read as a claim about it.
//
//	PACKAGE   `apt-get install` resolves from the box's dpkg architecture.
//	          Same shape as Flatpak: the resolver refuses first. Multiarch
//	          could change that, and deliberately is not used — pulling an
//	          amd64 package tree onto an arm64 box duplicates every library.
//
//	BINARY    A download_url / artifacts entry hands the box an ELF file and
//	          runs it. Here the ONLY thing standing between the file and a
//	          running process is the kernel, and a registered binfmt_misc
//	          handler genuinely makes an x86_64 ELF execute on an arm64 box.
//	          Refusing it on identity grounds would be a false negative.
//
// So the split is by DELIVERY MECHANISM, and it is a property of the recipe,
// not of the app or the box.

// DeliveryKind is how an app's bits reach the box. It decides whether emulation
// is even in the conversation — see the block comment above.
type DeliveryKind string

const (
	// DeliveryUnknown is the zero value and is treated as NOT emulatable.
	// The safe default is structural: a recipe shape nobody has classified
	// must not silently acquire an emulation path.
	DeliveryUnknown DeliveryKind = ""
	DeliveryFlatpak DeliveryKind = "flatpak"
	DeliveryPackage DeliveryKind = "package"
	DeliveryBinary  DeliveryKind = "binary"
)

// ── EmulationCanServe is SPLIT IN TWO, and the split is the finding ──────────
//
// It used to be one function — `return k == DeliveryBinary` — answering two
// questions at once, and it got one of them wrong:
//
//	EmulationCanInstall   can the bits get here at all?
//	EmulationRunsWell     would the result be worth offering?
//
// The old comment argued Flatpak's exclusion from `--supported-arches`, which
// is correct, and then drew the conclusion that a foreign-arch Flatpak cannot
// be installed, which is not. Measured 2026-08-17
// (roadmap/DISTRO-SOURCED-APPS.md §6), on an aarch64 installation:
//
//	flatpak remote-info --arch=x86_64 flathub org.blender.Blender
//	  → resolves app/org.blender.Blender/x86_64/stable, with its commit
//	flatpak install -y --arch=x86_64 flathub org.gnome.Calculator
//	  → deploys the app AND its whole x86_64 GNOME platform, 1.4 GB, onto the
//	    aarch64 box
//
// So the ref does exist and `--arch` is honoured. Keeping one function would
// have meant either recording that measurement as "cannot install" — false —
// or flipping Flatpak to servable, which is worse, because the reason not to
// offer it survives the correction intact and is a different reason.

// EmulationCanInstall reports whether an emulator-served install of this
// delivery kind could physically complete on a foreign-architecture box.
//
//	binary  : yes — an ELF and a kernel handler, or an emulator invoked by name
//	flatpak : yes — MEASURED, `flatpak install --arch=x86_64` deploys
//	package : no  — dpkg's architecture refuses at resolve time, and multiarch
//	          is deliberately not enabled (it duplicates every library)
//	unknown : no  — the zero value must not acquire a path nobody classified
func EmulationCanInstall(k DeliveryKind) bool {
	return k == DeliveryBinary || k == DeliveryFlatpak
}

// EmulationRunsWell reports whether the result would be worth offering.
//
//	binary  : yes — the payload lands in a private prefix this box controls, and
//	          box64 substitutes the HOST's native libraries there. Measured: an
//	          x86_64 glxinfo under box64 reported this machine's own aarch64
//	          Mesa 25.0.7 / LLVM 19.1.7 and `direct rendering: Yes` — the same
//	          renderer string as the native control.
//	flatpak : NO — bwrap supplies the RUNTIME's x86_64 /usr, so the substitution
//	          box64 depends on cannot reach the host's libraries, and qemu-user
//	          could not obtain a GL visual at all under the same harness.
//
// WHAT IS NOT MEASURED, and must not be read into this: whether a foreign-arch
// Flatpak RUNS. §6 Q3 got `bwrap: Creating new namespace failed: Operation not
// permitted`, which is a CONTAINER PRIVILEGE limit and would have stopped a
// NATIVE aarch64 app in the same container. It is recorded as
// `untestable-on-arm64-mac` — not as an architecture verdict — and it needs a
// real arm64 Linux box. This function returning false is a judgement about
// graphics quality on the evidence above, NOT a claim that the app fails to
// start. If someone later measures that it runs badly, that is more evidence
// for the same answer; if they measure that it does not run at all, then this
// answer was right for a reason nobody had yet established.
func EmulationRunsWell(k DeliveryKind) bool { return k == DeliveryBinary }

// EmulationPolicy declares whether an app may be offered on an instance whose
// architecture it does not natively support (ARCH-PLACEMENT §8.3).
type EmulationPolicy string

const (
	// EmulationNever is the zero value, and that is the whole point: an entry
	// that says nothing must mean "do not pretend this runs here". Matching
	// StoreOnly's reasoning in NODE-CAPABILITY.md, the safe default is
	// structural rather than something each call site has to remember.
	EmulationNever EmulationPolicy = "never"
	EmulationOptIn EmulationPolicy = "opt-in"
)

// ParseEmulationPolicy turns registry data into a policy, defaulting closed.
// An unrecognised value is "never" — a typo must not open the gate.
func ParseEmulationPolicy(s string) EmulationPolicy {
	if EmulationPolicy(strings.ToLower(strings.TrimSpace(s))) == EmulationOptIn {
		return EmulationOptIn
	}
	return EmulationNever
}

// binfmtMiscDir is the kernel's registration table. A variable so tests can
// point it at a fixture; never configurable at runtime.
var binfmtMiscDir = "/proc/sys/fs/binfmt_misc"

// Emulator is one user-space emulator this box can actually run, and the
// architecture it EXECUTES (never the architecture it runs on).
type Emulator struct {
	Name string `json:"name"`
	Arch string `json:"arch"` // Debian spelling
	Via  string `json:"via"`  // "binfmt" (kernel handler) or "binary" (on PATH)
	// BindsHostLibraries reports whether this emulator substitutes the BOX'S
	// OWN native libraries instead of emulating the foreign ones. It is the
	// single property that decides whether an emulated app gets graphics, and
	// it is measured, not assumed:
	//
	//	box64      an x86_64 glxinfo reported this machine's aarch64 Mesa 25.0.7
	//	           / LLVM 19.1.7, `direct rendering: Yes` — identical to native
	//	qemu-user  "Error: couldn't find RGB GLX visual or fbconfig"
	//
	// It is false by default, which is qemu's behaviour and the safe one.
	BindsHostLibraries bool `json:"binds_host_libraries"`
}

// knownEmulators is the second source EmulatedArches consults, and it exists
// because probing /proc/sys/fs/binfmt_misc alone reports NO EMULATION on a box
// with a working, measured, 1.40x emulator installed.
//
// Measured 2026-08-17 on debian:trixie-slim/arm64:
//
//	box64 --version        → exit 0, stdout "[BOX64] Box64 with Dynarec v0.3.4"
//	qemu-x86_64 --version  → exit 0, stdout "qemu-x86_64 version 10.0.11"
//	/proc/sys/fs/binfmt_misc  → the directory exists and is NOT MOUNTED
//
// That last line is the whole point. binfmt_misc is a filesystem someone has
// to mount and a handler someone has to register; box64 is invoked BY NAME,
// which is how every measurement in roadmap/DISTRO-SOURCED-APPS.md §5 was
// taken. A binfmt-only probe answers "no emulation here" for a box that
// emulates fine.
//
// ONE CORRECTION to §9.3, which says box64 "does not register a binfmt handler
// by default": that is true of upstream box64 and NOT of Debian's package,
// which ships /usr/lib/binfmt.d/box64.conf for systemd-binfmt to register —
// e_machine 0x3e at offset 0, interpreter /usr/bin/box64, and no `F` flag. So
// on a systemd box the handler may well be registered and the binfmt probe
// would find it. Neither source subsumes the other, which is why there are two.
var knownEmulators = []Emulator{
	{Name: "box64", Arch: "amd64", BindsHostLibraries: true},
	{Name: "box86", Arch: "i386", BindsHostLibraries: true},
	{Name: "qemu-x86_64", Arch: "amd64"},
	{Name: "qemu-i386", Arch: "i386"},
	{Name: "qemu-aarch64", Arch: "arm64"},
	{Name: "qemu-arm", Arch: "armhf"},
	{Name: "qemu-riscv64", Arch: "riscv64"},
	{Name: "qemu-s390x", Arch: "s390x"},
	{Name: "qemu-ppc64le", Arch: "ppc64el"},
}

// emulatorBinds maps an interpreter's file name to whether it substitutes host
// libraries, so a binfmt-sourced handler carries the same GL answer as a
// PATH-sourced one. Keyed on the interpreter path's base name because that is
// what a binfmt entry gives us.
func emulatorBinds(interpBase string) bool {
	for _, e := range knownEmulators {
		if e.Name == interpBase {
			return e.BindsHostLibraries
		}
	}
	return false
}

// emulatorRuns is the seam. It reports whether the named emulator is present
// and answers `--version` successfully. A variable so tests can exercise both
// answers on a machine that has neither emulator installed — without it the
// whole second source is unreachable in CI, which is the shape that let a
// mutation survive in services/packages this same session.
var emulatorRuns = func(name string) bool {
	path, err := exec.LookPath(name)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// CombinedOutput, not Output: box64 prints its banner and a probe that
	// insisted on non-empty stdout would be measuring the banner's stream
	// rather than whether the emulator works. Exit status is the assertion.
	if _, err := exec.CommandContext(ctx, path, "--version").CombinedOutput(); err != nil {
		return false
	}
	return true
}

// probeEmulatorBinaries returns the emulators installed on this box.
func probeEmulatorBinaries() []Emulator {
	var out []Emulator
	for _, e := range knownEmulators {
		if emulatorRuns(e.Name) {
			e.Via = "binary"
			out = append(out, e)
		}
	}
	return out
}

type emulatedArchCache struct {
	mu      sync.Mutex
	arches  []string
	emuls   []Emulator
	updated time.Time
	ttl     time.Duration
}

var emulCache = &emulatedArchCache{ttl: time.Minute}

// EmulatedArches returns the architectures this box can EXECUTE but is not,
// in Debian spelling, sorted. The native arch is never included — it is not
// emulated — so an empty result means "no emulation available here", which is
// the honest default on a box that never installed an emulator.
//
// TWO SOURCES, and until 2026-08-17 there was only one. Probing
// /proc/sys/fs/binfmt_misc alone reports NO EMULATION on a box with box64
// installed and working: box64 is invoked by name, binfmt_misc is a filesystem
// somebody has to mount (measured: it is NOT mounted in a container), and a
// handler is something somebody has to register. That mattered more than it
// looked, because box64 is the faster emulator by a wide margin and the one
// that keeps graphics — re-measured on a DYNAMIC binary at 1.40x with the host's
// own GL stack, against qemu-user's 2.32x and no GL at all. The earlier
// rejection of box64 tested a STATIC binary, which is the one shape box64
// structurally cannot serve, because its speed comes from not emulating
// libraries at all.
//
// It is deliberately NOT merged into SupportedArches (§8.5). Widening
// SupportedArches would silently reclassify "runs badly" as "runs" for every
// caller at once, including the install gate, and would make a Flatpak entry
// look installable on the strength of a handler that Flatpak never consults.
// Two separate answers is the only shape in which a caller can be made to say
// which one it wants.
//
// VULOS_EMULATED_ARCHES overrides the probe, so the App Hub's emulation states
// can be exercised on a developer machine with no binfmt at all. Like
// VULOS_BOX_ARCH it is the SERVER process's own environment.
func EmulatedArches() []string {
	arches, _ := emulationProbe()
	return arches
}

// EmulatorsAvailable returns the emulators themselves, not just the
// architectures they cover. Callers that have to decide whether an emulated
// app would get graphics need to know WHICH emulator would serve it: box64 and
// qemu-user cover the same architecture and gave opposite GL answers under the
// same harness. Sorted by architecture, then name, so the result is stable.
func EmulatorsAvailable() []Emulator {
	_, emuls := emulationProbe()
	return emuls
}

// EmulationBindsHostLibrariesFor reports whether SOME available emulator for
// `arch` substitutes the host's own native libraries. It is deliberately an
// "any", not an "all": if box64 is installed the box can choose it, and the
// presence of a second, worse emulator alongside it does not take that away.
func EmulationBindsHostLibrariesFor(arch string) bool {
	want := NormalizeArch(arch)
	for _, e := range EmulatorsAvailable() {
		if e.Arch == want && e.BindsHostLibraries {
			return true
		}
	}
	return false
}

// emulationProbe is the single cached probe behind all three accessors. Both
// sources are consulted and merged: a kernel handler and an emulator on PATH
// are independent facts and neither implies the other.
func emulationProbe() ([]string, []Emulator) {
	native := BoxArch()

	emulCache.mu.Lock()
	defer emulCache.mu.Unlock()
	if emulCache.arches != nil && time.Since(emulCache.updated) < emulCache.ttl {
		return append([]string(nil), emulCache.arches...), append([]Emulator(nil), emulCache.emuls...)
	}

	var found []Emulator
	if v := os.Getenv("VULOS_EMULATED_ARCHES"); v != "" {
		// The override names ARCHITECTURES, not emulators, so it cannot state
		// whether host libraries are bound. It says false — the qemu answer,
		// and the one that offers less rather than more.
		for _, part := range strings.Split(v, ",") {
			if a := NormalizeArch(part); a != "" {
				found = append(found, Emulator{Name: "VULOS_EMULATED_ARCHES", Arch: a, Via: "override"})
			}
		}
	} else {
		found = append(found, probeBinfmtArches()...)
		found = append(found, probeEmulatorBinaries()...)
	}

	archSet := map[string]bool{}
	seen := map[string]bool{}
	var emuls []Emulator
	for _, e := range found {
		if e.Arch == "" || e.Arch == native {
			continue
		}
		archSet[e.Arch] = true
		key := e.Arch + "\x00" + e.Name + "\x00" + e.Via
		if seen[key] {
			continue
		}
		seen[key] = true
		emuls = append(emuls, e)
	}
	out := make([]string, 0, len(archSet))
	for a := range archSet {
		out = append(out, a)
	}
	sort.Strings(out)
	sort.Slice(emuls, func(i, j int) bool {
		if emuls[i].Arch != emuls[j].Arch {
			return emuls[i].Arch < emuls[j].Arch
		}
		if emuls[i].Name != emuls[j].Name {
			return emuls[i].Name < emuls[j].Name
		}
		return emuls[i].Via < emuls[j].Via
	})

	emulCache.arches = out
	emulCache.emuls = emuls
	emulCache.updated = time.Now()
	return append([]string(nil), out...), append([]Emulator(nil), emuls...)
}

// elfMachineArch maps ELF e_machine to the Debian arch name. Reading the magic
// is how a handler is identified rather than trusting its NAME: the name is
// chosen by whoever registered it (Debian says "qemu-x86_64", systemd-binfmt
// agrees, Docker Desktop's Rosetta does not), whereas the magic is what the
// kernel actually matches on and cannot be wrong about.
var elfMachineArch = map[uint16]string{
	0x03:  "i386",
	0x28:  "armhf",
	0x3e:  "amd64",
	0xb7:  "arm64",
	0xf3:  "riscv64",
	0x16:  "s390x",
	0x102: "loong64",
}

// probeBinfmtArches reads /proc/sys/fs/binfmt_misc and reports which foreign
// architectures have a usable handler.
//
// "Usable" is three checks, and skipping any of them reports emulation that is
// not there:
//   - the entry says `enabled` (a disabled handler is still a file);
//   - its magic is an ELF header whose e_machine names an architecture;
//   - the interpreter is reachable — either the F (fix-binary) flag is set, in
//     which case the kernel already holds an open fd and the path need not
//     exist anywhere, or the path exists right now.
//
// The F check is not pedantry. It is exactly the difference between a handler
// that works and one that works only outside a sandbox: an app launched into a
// bwrap/Flatpak mount namespace, a chroot, or a container sees the runtime's
// own /usr, and a non-F handler looks the interpreter up THERE and fails.
func probeBinfmtArches() []Emulator {
	ents, err := os.ReadDir(binfmtMiscDir)
	if err != nil {
		return nil
	}
	var out []Emulator
	for _, e := range ents {
		name := e.Name()
		if name == "register" || name == "status" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(binfmtMiscDir, name))
		if err != nil {
			continue
		}
		arch, interp := emulatorFromBinfmtEntry(string(b))
		if arch == "" {
			continue
		}
		base := filepath.Base(interp)
		out = append(out, Emulator{
			Name: base, Arch: arch, Via: "binfmt",
			// A handler and a PATH binary of the same emulator have the same
			// library behaviour; the interpreter path is what says which one
			// this is. Falls back to false for an interpreter nobody has
			// characterised, which is the safe direction.
			BindsHostLibraries: emulatorBinds(base),
		})
	}
	return out
}

// archFromBinfmtEntry reports just the architecture a binfmt entry serves.
func archFromBinfmtEntry(body string) string {
	a, _ := emulatorFromBinfmtEntry(body)
	return a
}

// emulatorFromBinfmtEntry parses one binfmt_misc entry file and returns the
// architecture it serves and the interpreter registered for it. Split out from
// the directory walk so it can be tested against real /proc content without
// /proc — which matters more than it looks, because binfmt_misc is a
// filesystem that has to be MOUNTED and it is not mounted in a container.
func emulatorFromBinfmtEntry(body string) (string, string) {
	var enabled, hasF bool
	var magic, interp string
	offset := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "enabled":
			enabled = true
		case line == "disabled":
			return "", ""
		case strings.HasPrefix(line, "interpreter "):
			interp = strings.TrimSpace(strings.TrimPrefix(line, "interpreter "))
		case strings.HasPrefix(line, "flags:"):
			hasF = strings.Contains(strings.TrimPrefix(line, "flags:"), "F")
		case strings.HasPrefix(line, "offset "):
			offset = atoiSafe(strings.TrimSpace(strings.TrimPrefix(line, "offset ")))
		case strings.HasPrefix(line, "magic "):
			magic = strings.TrimSpace(strings.TrimPrefix(line, "magic "))
		}
	}
	if !enabled {
		return "", ""
	}
	if !hasF {
		// No fix-binary: the kernel resolves the path at exec time, in the
		// namespace of whatever is being executed. Require it to exist here,
		// and note in the caller's docs that this is the weaker case.
		if interp == "" {
			return "", ""
		}
		if _, err := os.Stat(interp); err != nil {
			return "", ""
		}
	}
	raw, err := hex.DecodeString(magic)
	if err != nil || offset != 0 || len(raw) < 20 {
		return "", ""
	}
	if raw[0] != 0x7f || raw[1] != 'E' || raw[2] != 'L' || raw[3] != 'F' {
		return "", ""
	}
	var machine uint16
	switch raw[5] { // EI_DATA
	case 1:
		machine = uint16(raw[18]) | uint16(raw[19])<<8
	case 2:
		machine = uint16(raw[18])<<8 | uint16(raw[19])
	default:
		return "", ""
	}
	return elfMachineArch[machine], interp
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// InvalidateEmulatedArchCache forces the next EmulatedArches call to re-probe.
func InvalidateEmulatedArchCache() {
	emulCache.mu.Lock()
	emulCache.arches = nil
	emulCache.mu.Unlock()
}

// ═════════════════════════════════════════════════════════════════════════════
// THE ONE ANSWER THE APP HUB RENDERS
// ═════════════════════════════════════════════════════════════════════════════
//
// AppHub.tsx currently re-derives availability client-side with a raw
// `app.arch.includes(systemArch)`, which misses x86_64-vs-amd64 and knows
// nothing about emulation. The fix is not a better client-side rule; it is for
// the server to compute the answer AND the sentence, so there is exactly one
// implementation of the policy and the UI cannot drift from it.
//
// The copy below is ARCH-PLACEMENT §7 verbatim, with two rules that are
// enforced by tests rather than by remembering:
//   - Debian spelling only. Mixing amd64 and x86_64 in one paragraph makes one
//     box look like two.
//   - Never the bare word "Unavailable" for a synced app. It reads as broken
//     rather than as a fact about this piece of hardware.

// Arch availability states. Strings, because they cross the API boundary.
const (
	ArchStateNative        = "native"         // installs and runs natively
	ArchStateEmulated      = "emulated"       // can run here, translated
	ArchStateOtherInstance = "other-instance" // not here; runs on a sibling box
	ArchStateUnavailable   = "unavailable"    // no path on this box
)

// ArchRequest is everything the decision needs. Every field is supplied by the
// caller rather than probed here, because the probes have different lifetimes:
// SupportedArches is per-listing, the user's setting is per-request, and
// NeedsGPU is registry data.
type ArchRequest struct {
	AppName  string
	Declared []string     // RegistryEntry.Arch
	Delivery DeliveryKind // which resolver decides — EmulationCanInstall/RunsWell
	Policy   EmulationPolicy
	// NeedsGPU is exception E3, and it is NOT on its own a refusal any more:
	// it is refused only when the emulation available here cannot bind this
	// box's own graphics driver. See EvaluateArch's third switch arm.
	NeedsGPU  bool
	Supported []string // SupportedArches(), native first
	Emulated  []string // EmulatedArches()
	// EmulatorBindsHostLibraries is filled from EmulatorsAvailable() — normally
	// EmulationBindsHostLibrariesFor(the declared arch). The zero value is
	// false, which is qemu-user's measured behaviour and the answer that offers
	// less rather than more; a caller that forgets it gets the conservative
	// verdict rather than an optimistic one.
	EmulatorBindsHostLibraries bool
	// EmulationEnabled is the user's opt-in (exception E2). Emulation is never
	// automatic: an app that is present and crawls reads as a Vulos defect
	// rather than as a hardware limit.
	EmulationEnabled bool
	// OtherInstance names a synced instance where this app IS installed, or "".
	// Naming it is what makes a heterogeneous fleet feel like one OS.
	OtherInstance string
}

// ArchAvailability is the rendered answer.
type ArchAvailability struct {
	State             string `json:"state"`
	Installable       bool   `json:"installable"`
	RequiresEmulation bool   `json:"requires_emulation"`
	Badge             string `json:"badge"`
	Detail            string `json:"detail"`
	BoxArch           string `json:"box_arch"`
	Undeclared        bool   `json:"undeclared"` // nobody audited this entry
}

// EvaluateArch decides what this box does with this app, and says it in one
// sentence the App Hub can show verbatim.
func EvaluateArch(req ArchRequest) ArchAvailability {
	box := "unknown"
	if len(req.Supported) > 0 {
		box = NormalizeArch(req.Supported[0])
	}
	name := req.AppName
	if name == "" {
		name = "This app"
	}
	av := ArchAvailability{BoxArch: box, Undeclared: !ArchDeclared(req.Declared)}

	if ArchSupported(req.Declared, req.Supported) {
		av.State = ArchStateNative
		av.Installable = true
		return av
	}

	needs := declaredList(req.Declared)
	needsStr := strings.Join(needs, " or ")
	if needsStr == "" {
		needsStr = "an architecture this box does not have"
	}

	// Could an emulator actually serve this? FIVE independent gates now, and
	// the first two used to be one. They ask different questions and they
	// deserve different sentences, because they are different facts about the
	// user's box: "nothing exists to download" is not "we chose not to offer
	// what does exist".
	emulatorPresent := false
	for _, d := range needs {
		for _, e := range req.Emulated {
			if d == e {
				emulatorPresent = true
			}
		}
	}
	switch {
	case !EmulationCanInstall(req.Delivery):
		// The resolver refuses before any instruction is executed. Nothing was
		// downloaded, so there is nothing for an emulator to emulate.
		av.State = ArchStateUnavailable
		av.Badge = "Not available on this box"
		av.Detail = name + " ships for " + needsStr + " only, and this box is " + box +
			". " + deliveryClause(req.Delivery) + elsewhereClause(req, needsStr)
		return av

	case !EmulationRunsWell(req.Delivery):
		// The bits CAN get here — measured, for Flatpak — and we decline. Say
		// that, rather than repeating the sentence above, which would be a
		// claim we now know to be false. Note what this does NOT say: that it
		// would fail to run. That case is untested (see EmulationRunsWell).
		av.State = ArchStateUnavailable
		av.Badge = "Not available on this box"
		av.Detail = name + " ships for " + needsStr + " only. This box is " + box +
			" and could install the " + needsStr + " build, but it would bring its own " +
			needsStr + " graphics libraries, which emulation cannot accelerate — so it is " +
			"not offered here." + elsewhereClause(req, needsStr)
		return av

	case req.NeedsGPU && !req.EmulatorBindsHostLibraries:
		// E3, RE-SCOPED. It used to fire for every GPU-bound app regardless of
		// delivery, on the reasoning that "an x86_64 Mesa under emulation loses
		// acceleration". Measured 2026-08-17: that is true of qemu-user, which
		// could not obtain a GL visual at all, and FALSE of box64 in a private
		// prefix, where an x86_64 glxinfo reported this machine's own aarch64
		// Mesa 25.0.7 / LLVM 19.1.7. So the exception belongs to the emulator
		// and the delivery, not to the app.
		av.State = ArchStateUnavailable
		av.Badge = "Not available on this box"
		av.Detail = name + " ships for " + needsStr + " only and needs graphics acceleration, " +
			"which the emulation available on this box cannot provide." + elsewhereClause(req, needsStr)
		return av

	case req.Policy != EmulationOptIn, !emulatorPresent:
		av.State = ArchStateUnavailable
		av.Badge = "Not available on this box"
		av.Detail = name + " ships for " + needsStr + " only and this box is " + box +
			", with no emulation available for it." + elsewhereClause(req, needsStr)
		return av
	}

	av.State = ArchStateEmulated
	av.RequiresEmulation = true
	if req.EmulationEnabled {
		av.Installable = true
		av.Badge = "Runs emulated"
		av.Detail = name + " ships for " + needsStr + " only and will run on this " + box +
			" box through emulation — noticeably slower, " + graphicsClause(req) + "."
		return av
	}
	av.Badge = "Needs emulation"
	av.Detail = name + " ships for " + needsStr + " only. This box is " + box +
		" and can run it through emulation — noticeably slower, " + graphicsClause(req) +
		". Turn on emulated apps in Settings to install it."
	return av
}

// graphicsClause says what actually happens to graphics, which depends on WHICH
// emulator is available and not on the app.
//
// Both halves are measured (roadmap/DISTRO-SOURCED-APPS.md §5.3) and neither
// claims more than was measured: box64 was shown to bind the HOST's driver
// stack — same renderer string, same version, `direct rendering: Yes` as the
// native control — on a machine with NO GPU, where that stack is llvmpipe. What
// that proves is which stack was bound. Whether it then reaches hardware
// acceleration on a box that has a GPU is untested, so the sentence says
// "this box's own graphics driver" and stops there.
func graphicsClause(req ArchRequest) string {
	if req.EmulatorBindsHostLibraries {
		return "though it uses this box's own graphics driver rather than an emulated one"
	}
	return "and without graphics acceleration"
}

// elsewhereClause is the sentence that turns a fleet into one OS: when the app
// works on a sibling instance, say which one and never let it just vanish.
func elsewhereClause(req ArchRequest, needsStr string) string {
	if req.OtherInstance != "" {
		return " It is installed on " + req.OtherInstance + " and stays available there."
	}
	return " It stays available on your " + needsStr + " instances."
}

func deliveryClause(k DeliveryKind) string {
	switch k {
	case DeliveryFlatpak:
		return "Flathub publishes no build for this box, and emulation cannot supply one — " +
			"there is nothing to download."
	case DeliveryPackage:
		return "No package is published for this box's architecture."
	default:
		return "No build is published for this box's architecture."
	}
}

func declaredList(declared []string) []string {
	out := make([]string, 0, len(declared))
	seen := map[string]bool{}
	for _, d := range declared {
		if n := NormalizeArch(d); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// DeliveryKindOf classifies a recipe by which resolver will decide. Kept here
// rather than in registry.go so the identity/capability split has exactly one
// definition, and so the mapping is visible next to the reasoning for it.
//
// flatpakID wins over a download URL: a recipe that carries both installs via
// Flatpak, so Flatpak is the resolver that refuses.
func DeliveryKindOf(flatpakID, downloadURL string, artifacts int, installScript string) DeliveryKind {
	switch {
	case flatpakID != "":
		return DeliveryFlatpak
	case downloadURL != "" || artifacts > 0:
		return DeliveryBinary
	case installScript != "":
		return DeliveryPackage
	default:
		return DeliveryUnknown
	}
}
