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
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
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
func InvalidateArchCache() {
	archCache.mu.Lock()
	archCache.arches = nil
	archCache.mu.Unlock()
}

// ArchSupported reports whether an app declaring `declared` can be installed on
// a box that supports `supported`.
//
// An EMPTY declared list means "any architecture" — that is the documented
// meaning of RegistryEntry.Arch and it covers 46 of the 55 current entries, so
// getting it wrong would hide almost the whole catalogue. Both sides are
// normalised here so a registry that spells it x86_64 and a box that says
// amd64 still match; that is the whole failure this function exists to prevent.
func ArchSupported(declared, supported []string) bool {
	if len(declared) == 0 {
		return true
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
