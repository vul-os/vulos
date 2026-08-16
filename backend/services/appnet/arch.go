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
//	FLATPAK   `flatpak install` resolves a ref for an arch its INSTALLATION
//	          supports. A qemu binfmt handler does not add x86_64 to
//	          `flatpak --supported-arches`; the ref does not exist for this
//	          installation and the install fails at resolve time, before any
//	          instruction is executed. Emulation cannot help, because nothing
//	          was ever downloaded to emulate. (It is technically possible to
//	          force a foreign-arch install; §4.4 records why it is not worth
//	          having — the x86_64 Mesa inside the runtime is then emulated
//	          too, so GL falls back to emulated software rasterisation.)
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

// EmulationCanServe reports whether an emulator could make a foreign-arch app
// of this delivery kind actually run.
func EmulationCanServe(k DeliveryKind) bool { return k == DeliveryBinary }

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

type emulatedArchCache struct {
	mu      sync.Mutex
	arches  []string
	updated time.Time
	ttl     time.Duration
}

var emulCache = &emulatedArchCache{ttl: time.Minute}

// EmulatedArches returns the architectures this box can EXECUTE but is not,
// in Debian spelling, sorted. The native arch is never included — it is not
// emulated — so an empty result means "no emulation available here", which is
// the honest default on a box that never installed qemu-user.
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
	native := BoxArch()

	emulCache.mu.Lock()
	defer emulCache.mu.Unlock()
	if emulCache.arches != nil && time.Since(emulCache.updated) < emulCache.ttl {
		return append([]string(nil), emulCache.arches...)
	}

	var found []string
	if v := os.Getenv("VULOS_EMULATED_ARCHES"); v != "" {
		for _, part := range strings.Split(v, ",") {
			if a := NormalizeArch(part); a != "" {
				found = append(found, a)
			}
		}
	} else {
		found = probeBinfmtArches()
	}

	set := map[string]bool{}
	for _, a := range found {
		if a != "" && a != native {
			set[a] = true
		}
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)

	emulCache.arches = out
	emulCache.updated = time.Now()
	return append([]string(nil), out...)
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
func probeBinfmtArches() []string {
	ents, err := os.ReadDir(binfmtMiscDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if name == "register" || name == "status" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(binfmtMiscDir, name))
		if err != nil {
			continue
		}
		if a := archFromBinfmtEntry(string(b)); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// archFromBinfmtEntry parses one binfmt_misc entry file. Split out from the
// directory walk so it can be tested against real /proc content without /proc.
func archFromBinfmtEntry(body string) string {
	var enabled, hasF bool
	var magic, interp string
	offset := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "enabled":
			enabled = true
		case line == "disabled":
			return ""
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
		return ""
	}
	if !hasF {
		// No fix-binary: the kernel resolves the path at exec time, in the
		// namespace of whatever is being executed. Require it to exist here,
		// and note in the caller's docs that this is the weaker case.
		if interp == "" {
			return ""
		}
		if _, err := os.Stat(interp); err != nil {
			return ""
		}
	}
	raw, err := hex.DecodeString(magic)
	if err != nil || offset != 0 || len(raw) < 20 {
		return ""
	}
	if raw[0] != 0x7f || raw[1] != 'E' || raw[2] != 'L' || raw[3] != 'F' {
		return ""
	}
	var machine uint16
	switch raw[5] { // EI_DATA
	case 1:
		machine = uint16(raw[18]) | uint16(raw[19])<<8
	case 2:
		machine = uint16(raw[18])<<8 | uint16(raw[19])
	default:
		return ""
	}
	return elfMachineArch[machine]
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
	AppName   string
	Declared  []string     // RegistryEntry.Arch
	Delivery  DeliveryKind // which resolver decides — see EmulationCanServe
	Policy    EmulationPolicy
	NeedsGPU  bool     // exception E3: emulated GL is not worth offering
	Supported []string // SupportedArches(), native first
	Emulated  []string // EmulatedArches()
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

	// Could an emulator actually serve this? Four independent gates, and the
	// order matters only for which sentence the user reads.
	emulatorPresent := false
	for _, d := range needs {
		for _, e := range req.Emulated {
			if d == e {
				emulatorPresent = true
			}
		}
	}
	switch {
	case req.NeedsGPU:
		// E3. Offering these would be offering a defect: §4.4 measured that an
		// x86_64 Mesa under emulation loses acceleration outright.
		av.State = ArchStateUnavailable
		av.Badge = "Not available on this box"
		av.Detail = name + " ships for " + needsStr + " only and needs graphics acceleration, " +
			"which emulation cannot provide." + elsewhereClause(req, needsStr)
		return av
	case !EmulationCanServe(req.Delivery):
		av.State = ArchStateUnavailable
		av.Badge = "Not available on this box"
		av.Detail = name + " ships for " + needsStr + " only, and this box is " + box +
			". " + deliveryClause(req.Delivery) + elsewhereClause(req, needsStr)
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
			" box through emulation — noticeably slower, and without graphics acceleration."
		return av
	}
	av.Badge = "Needs emulation"
	av.Detail = name + " ships for " + needsStr + " only. This box is " + box +
		" and can run it through emulation — noticeably slower, and without graphics " +
		"acceleration. Turn on emulated apps in Settings to install it."
	return av
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
