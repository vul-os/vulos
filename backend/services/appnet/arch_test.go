package appnet

// arch_test.go — ARCH-01 gate.
//
// The requirement: the store must show which apps can actually be installed on
// THIS box. Vulos publishes amd64 and arm64 images and a large share of the
// Flathub catalogue is x86_64-only, so a catalogue that ignores architecture
// offers installs that cannot succeed.
//
// The failure this is most likely to have is silent: a comparison that mixes
// naming schemes (Debian amd64/arm64 vs Flatpak x86_64/aarch64) matches
// nothing, and "matches nothing" looks exactly like "not available here" — a
// wholly empty store with no error anywhere.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestNormalizeArch_CollapsesEverySpelling(t *testing.T) {
	cases := map[string]string{
		// Flatpak / uname -m → Debian
		"x86_64":  "amd64",
		"aarch64": "arm64",
		"armv7l":  "armhf",
		"i686":    "i386",
		// Debian → itself
		"amd64": "amd64",
		"arm64": "arm64",
		// Go GOARCH → Debian
		"386": "i386",
		"arm": "armhf",
		// case and whitespace
		"  X86_64\t": "amd64",
		"AArch64":    "arm64",
		// empty
		"": "",
	}
	for in, want := range cases {
		if got := NormalizeArch(in); got != want {
			t.Errorf("NormalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeArch_KeepsUnknownValues. Mapping an unrecognised arch to "" would
// make it compare equal to "declares nothing", i.e. installable everywhere —
// exactly backwards. An unknown arch must stay unknown so it fails to match.
func TestNormalizeArch_KeepsUnknownValues(t *testing.T) {
	if got := NormalizeArch("sparc64"); got != "sparc64" {
		t.Fatalf("NormalizeArch(sparc64) = %q, want it preserved", got)
	}
	if ArchSupported([]string{"sparc64"}, []string{"amd64"}) {
		t.Fatal("an app for an architecture this box does not have was reported installable")
	}
}

// TestCrossSchemeComparison is the whole point of the normalisation.
// A registry that spells it x86_64 and a box that says amd64 are the SAME
// machine; a comparison that misses that hides real apps from real boxes.
func TestCrossSchemeComparison(t *testing.T) {
	if !ArchSupported([]string{"x86_64"}, []string{"amd64"}) {
		t.Error("x86_64 app on an amd64 box reported NOT installable — the Flatpak " +
			"and Debian spellings of one machine failed to match")
	}
	if !ArchSupported([]string{"amd64"}, []string{"x86_64"}) {
		t.Error("amd64 app on an x86_64 box reported NOT installable")
	}
	if !ArchSupported([]string{"aarch64"}, []string{"arm64"}) {
		t.Error("aarch64 app on an arm64 box reported NOT installable")
	}
	if ArchSupported([]string{"x86_64"}, []string{"arm64"}) {
		t.Error("an x86_64-only app was reported installable on an arm64 box — this " +
			"is the founder-visible bug: offering Steam/Chrome/Zoom on ARM")
	}
}

// TestEmptyArchMeansAny. 46 of the 55 current registry entries declare no arch.
// Treating that as "supports nothing" would empty the store.
func TestEmptyArchMeansAny(t *testing.T) {
	for _, box := range [][]string{{"amd64"}, {"arm64"}, {"riscv64"}} {
		if !ArchSupported(nil, box) {
			t.Errorf("an entry declaring no arch was hidden on a %v box; empty means any", box)
		}
		if r := ArchUnavailableReason(nil, box); r != "" {
			t.Errorf("an installable entry carries a reason it is not: %q", r)
		}
	}
}

// TestNoSupportedArchesIsNotAWildcard. If arch detection ever returns nothing,
// it must fail CLOSED (nothing arch-specific is installable) rather than
// letting every entry through unchecked.
func TestNoSupportedArchesIsNotAWildcard(t *testing.T) {
	if ArchSupported([]string{"amd64"}, nil) {
		t.Fatal("with no known box arches, an amd64-only app was still reported " +
			"installable — arch detection failing open would hand a user an " +
			"install that cannot work")
	}
}

// TestBoxArch_IsTheServersOwn. Desktop apps are streamed FROM the box, so the
// architecture that matters is the server's. This pins that BoxArch comes from
// runtime.GOARCH — the arch of this binary — and not from anything else.
func TestBoxArch_IsTheServersOwn(t *testing.T) {
	t.Setenv("VULOS_BOX_ARCH", "")
	if got, want := BoxArch(), NormalizeArch(runtime.GOARCH); got != want {
		t.Fatalf("BoxArch() = %q, want the server binary's own %q", got, want)
	}
}

// TestBoxArch_OverrideIsServerSide. The override exists so arch filtering can be
// exercised on any developer machine. It must be an environment variable of the
// SERVER process — this test would still pass if it were client-controlled, so
// the guard that matters is the source-level one below.
func TestBoxArch_OverrideIsServerSide(t *testing.T) {
	t.Setenv("VULOS_BOX_ARCH", "x86_64")
	if got := BoxArch(); got != "amd64" {
		t.Fatalf("BoxArch() with override x86_64 = %q, want amd64 (normalised)", got)
	}
}

// TestArchNeverComesFromTheClient reads arch.go itself. A user on an arm64 Mac
// driving an amd64 box must be offered amd64 apps; deriving arch from a user
// agent or a request header would silently invert that for every remote user,
// and no behavioural test can see the difference when the test client happens
// to run on the same machine as the server.
func TestArchNeverComesFromTheClient(t *testing.T) {
	src, err := os.ReadFile("arch.go")
	if err != nil {
		t.Fatalf("read arch.go: %v", err)
	}
	for _, bad := range []string{
		"http.Request", "User-Agent", "userAgent", "r.Header", "Sec-CH-UA",
	} {
		if strings.Contains(string(src), bad) {
			t.Fatalf("arch.go references %q — the box's architecture must never be "+
				"derived from anything the client controls. Desktop apps run ON the "+
				"box; a remote arm64 laptop driving an amd64 box must see amd64.", bad)
		}
	}
}

// TestUnavailableReason_NamesBothSides. A UI shows this string verbatim, so it
// has to say what the app needs AND what the box is. "Not available" alone
// leaves the user with nothing to act on.
func TestUnavailableReason_NamesBothSides(t *testing.T) {
	r := ArchUnavailableReason([]string{"x86_64"}, []string{"arm64"})
	if !strings.Contains(r, "amd64") {
		t.Errorf("reason %q does not name the arch the app requires (normalised)", r)
	}
	if !strings.Contains(r, "arm64") {
		t.Errorf("reason %q does not name what this box is", r)
	}
}

// TestSupportedArches_NativeFirst. ListEntries reports supported[0] as the box's
// arch, so the native arch has to be first — otherwise a flatpak that reports
// several would rename the box.
func TestSupportedArches_NativeFirst(t *testing.T) {
	t.Setenv("VULOS_BOX_ARCH", "arm64")
	InvalidateArchCache()
	defer InvalidateArchCache()
	got := SupportedArches()
	if len(got) == 0 {
		t.Fatal("SupportedArches returned nothing — arch filtering would fail closed " +
			"on every entry and empty the store")
	}
	if got[0] != "arm64" {
		t.Fatalf("SupportedArches()[0] = %q, want the native arm64 first", got[0])
	}
}

// TestListEntries_MarksInstallability walks the REAL registry shipped in the
// repo, on a box pinned to arm64, and checks the amd64-only entries are marked
// not-installable and the rest are.
func TestListEntries_MarksInstallability(t *testing.T) {
	t.Setenv("VULOS_BOX_ARCH", "arm64")
	InvalidateArchCache()
	defer InvalidateArchCache()
	// The subject is architecture; the fixtures carry no publisher signature, and
	// without this every one of them would come back "awaiting publisher
	// signature" and the arch table below would assert nothing. See arm64Box.
	withInsecureRegistry(t)

	reg := &Registry{Apps: map[string]*RegistryEntry{
		"steam":  {Name: "Steam", Type: "desktop", Arch: []string{"amd64"}},
		"gimp":   {Name: "GIMP", Type: "desktop"},
		"chrome": {Name: "Chrome", Type: "desktop", Arch: []string{"x86_64"}},
		"both":   {Name: "Both", Type: "desktop", Arch: []string{"amd64", "arm64"}},
	}}
	entries := reg.ListEntries(t.TempDir())
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4 — the walk examined the wrong thing", len(entries))
	}
	want := map[string]bool{"steam": false, "gimp": true, "chrome": false, "both": true}
	for _, e := range entries {
		if e.Installable != want[e.ID] {
			t.Errorf("%s: installable=%v, want %v (declares %v, box is arm64)",
				e.ID, e.Installable, want[e.ID], e.Arch)
		}
		if e.BoxArch != "arm64" {
			t.Errorf("%s: box_arch=%q, want arm64", e.ID, e.BoxArch)
		}
		if e.Installable && e.InstallableReason != "" {
			t.Errorf("%s: installable but carries reason %q", e.ID, e.InstallableReason)
		}
		if !e.Installable && e.InstallableReason == "" {
			t.Errorf("%s: not installable and gives no reason — the UI has nothing "+
				"to tell the user", e.ID)
		}
	}
}

// TestInstall_RefusesWrongArch. The listing is a suggestion; this is the
// enforcement. Without it an x86_64-only app installed on an arm64 box fails
// deep inside flatpak, or half-succeeds and leaves a manifest for an app that
// can never run.
func TestInstall_RefusesWrongArch(t *testing.T) {
	// The arch check deliberately runs AFTER publisher-signature verification —
	// entry.Arch is registry data and must not be acted on before the entry is
	// verified. So this test has to get past signing to reach the arch gate.
	t.Setenv("VULOS_ENV", "dev")
	t.Setenv("VULOS_REGISTRY_INSECURE", "1")
	t.Setenv("VULOS_BOX_ARCH", "arm64")
	InvalidateArchCache()
	defer InvalidateArchCache()

	reg := &Registry{Apps: map[string]*RegistryEntry{
		"steam": {
			Name: "Steam", Type: "desktop", Arch: []string{"amd64"},
			Versions: map[string]*VersionRecipe{
				"1.0": {FlatpakID: "com.valvesoftware.Steam", Port: 80},
			},
		},
	}}
	err := InstallFromRegistry(t.Context(), reg, "steam", "1.0", t.TempDir())
	if err == nil {
		t.Fatal("installing an amd64-only app on an arm64 box was ALLOWED")
	}
	if !strings.Contains(err.Error(), "arm64") || !strings.Contains(err.Error(), "amd64") {
		t.Fatalf("refusal does not name the mismatch: %v", err)
	}
}

// ── The undeclared-arch ratchet ──────────────────────────────────────────────
//
// arch.go's policy block promises this test exists: "arch_test.go ratchets the
// undeclared count: it may fall, never rise. A new entry that does not declare
// `arch` fails the build." Without it the policy is prose, and the permissive
// branch in ArchSupported has nothing bounding it.
//
// The ceiling is a LITERAL transcribed from a measurement of registry.json on
// 2026-08-17, deliberately NOT computed from the file under test. RATCHETED
// 44 -> 19 when the catalogue migration declared arch on 25 entries; the test
// itself demanded this, refusing to let its own bound go slack. A ratchet
// that derives its own bound from its subject proves only that the file agrees
// with itself — the self-consistency trap that already let a size-limit test
// pass while its constant was raised a thousandfold.
const undeclaredArchCeiling = 6

// registryTotalFloor guards the guard. If registry.json moves, shrinks, or
// fails to parse, a count of zero undeclared entries would read as PERFECT
// COMPLIANCE and this test would go green over an empty examination. Every
// guard in this suite that carried a coverage-count assertion survived
// mutation; every one that lacked one did not.
const registryTotalFloor = 56

func TestRegistry_UndeclaredArchOnlyEverFalls(t *testing.T) {
	path := filepath.Join("..", "..", "..", "registry.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read the registry this gate exists to bound: %v", err)
	}
	var reg struct {
		Apps map[string]struct {
			Arch []string `json:"arch"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatalf("registry.json did not parse: %v", err)
	}
	if len(reg.Apps) < registryTotalFloor {
		t.Fatalf("examined %d entries, floor is %d — a shrinking registry would "+
			"make this gate pass by looking at nothing",
			len(reg.Apps), registryTotalFloor)
	}

	var undeclared []string
	for id, e := range reg.Apps {
		if !ArchDeclared(e.Arch) {
			undeclared = append(undeclared, id)
		}
	}
	sort.Strings(undeclared)

	if len(undeclared) > undeclaredArchCeiling {
		t.Fatalf("%d entries declare no architecture, ceiling is %d.\n"+
			"An undeclared entry is an UNVERIFIED CLAIM TO EVERY ARCHITECTURE "+
			"Vulos ships: ArchSupported permits it, the box downloads it, and an "+
			"amd64 binary that passes its checksum will not exec on arm64.\n"+
			"Declare `arch` on the new entry — measured, not guessed.\nundeclared: %v",
			len(undeclared), undeclaredArchCeiling, undeclared)
	}
	if len(undeclared) < undeclaredArchCeiling {
		t.Fatalf("%d entries now declare no architecture, below the ceiling of %d. "+
			"Good — lower undeclaredArchCeiling to %d so the ratchet keeps its grip. "+
			"A bound that never tightens is not a ratchet.\nundeclared: %v",
			len(undeclared), undeclaredArchCeiling, len(undeclared), undeclared)
	}
}

// TestArchSupported_UndeclaredFollowsPolicyBothWays pins the two halves of the
// migration arch.go describes. Today undeclared is PERMITTED so the 44 keep
// working; when the catalogue merge populates them, strict becomes the default
// and undeclared must REFUSE. Asserting only today's half would let the strict
// branch rot unnoticed until the day it is switched on.
func TestArchSupported_UndeclaredFollowsPolicyBothWays(t *testing.T) {
	box := []string{"arm64"}

	SetStrictUndeclaredArch(false)
	defer SetStrictUndeclaredArch(false)
	if !ArchSupported(nil, box) {
		t.Error("undeclared refused under the permissive default — that empties " +
			"44 working entries to fix a claim")
	}

	SetStrictUndeclaredArch(true)
	if ArchSupported(nil, box) {
		t.Error("undeclared PERMITTED under strict policy — the strict branch is dead")
	}
	// Malformed is not absent, in either mode: [""] carries a broken audit and
	// must fail closed even while undeclared is being tolerated.
	SetStrictUndeclaredArch(false)
	if ArchSupported([]string{""}, box) {
		t.Error("a declaration that normalises to nothing was treated as absent")
	}
}
