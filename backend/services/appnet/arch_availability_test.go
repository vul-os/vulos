package appnet

// arch_availability_test.go — the wire that carries EvaluateArch's answer.
//
// ─────────────────────────────────────────────────────────────────────────────
// THE GAP THIS CLOSES
//
// EvaluateArch had tests and still had NO CALLER. `arch_emulation_test.go` says
// so in its own header: "These tests do not make it called. They make it
// CHECKED." Meanwhile the App Hub decided availability in the browser, and the
// first version of that decision — `app.arch.includes(systemArch)` — was a raw
// string comparison in which `x86_64` never matched `amd64`, so most of the
// Flathub catalogue read as incompatible on every box.
//
// So the failure this file guards is not "is EvaluateArch right". It is "is
// EvaluateArch REACHED": does the one answer, computed on the machine that can
// observe binfmt and box64 and the installed flatpak's arches, actually leave
// the box.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// arm64Box pins the listing to an arm64 box with no emulator and no flatpak
// answer, which is the machine this repo's CI runs on and the one where an
// amd64-only entry is interesting.
func arm64Box(t *testing.T) {
	t.Helper()
	withBoxArch(t, "arm64")
	withNoBinfmt(t)
	withEmulatorsOnPath(t)
	SetEmulationOptedIn(false)
	t.Cleanup(func() { SetEmulationOptedIn(false) })
}

// TestListEntries_CarriesTheServersAnswer is the wiring assertion.
func TestListEntries_CarriesTheServersAnswer(t *testing.T) {
	arm64Box(t)

	reg := &Registry{Apps: map[string]*RegistryEntry{
		"gitea": {Name: "Gitea", Type: "service", Arch: []string{"arm64"},
			Versions: map[string]*VersionRecipe{"1.0": {FlatpakID: "io.gitea.Gitea"}}},
		"steam": {Name: "Steam", Type: "desktop", Arch: []string{"x86_64"},
			Versions: map[string]*VersionRecipe{"1.0": {FlatpakID: "com.valvesoftware.Steam"}}},
	}}
	byID := map[string]RegistryListEntry{}
	for _, e := range reg.ListEntries(t.TempDir()) {
		byID[e.ID] = e
	}

	native := byID["gitea"].Availability
	if native.State != ArchStateNative || !native.Installable {
		t.Errorf("gitea: %+v, want the native rung", native)
	}
	if native.BoxArch != "arm64" {
		t.Errorf("gitea: box_arch=%q, want arm64", native.BoxArch)
	}

	steam := byID["steam"].Availability
	if steam.State != ArchStateUnavailable || steam.Installable {
		t.Fatalf("steam: %+v, want rung 5 on an arm64 box", steam)
	}
	if steam.Detail == "" || steam.Badge == "" || steam.CardBadge == "" {
		t.Errorf("steam: the listing carries a verdict with no words for it: %+v — the App Hub "+
			"renders these verbatim and composes nothing of its own", steam)
	}
	// Debian spelling, folded HERE. The entry declares x86_64; a hub that had to
	// fold it would be the client-side derivation this work exists to delete.
	if len(steam.Needs) != 1 || steam.Needs[0] != "amd64" {
		t.Errorf("steam: needs=%v, want [amd64] — the entry says x86_64 and the fold belongs "+
			"on this side of the wire", steam.Needs)
	}
	if steam.CardBadge != "Needs amd64" {
		t.Errorf("steam: card badge %q", steam.CardBadge)
	}

	// The legacy fields are a PROJECTION of the same answer, not a second
	// opinion. Two implementations that agree today are the shape this whole
	// change is removing.
	if byID["steam"].Installable != steam.Installable || byID["gitea"].Installable != native.Installable {
		t.Error("installable disagrees with availability.installable — there are two decisions again")
	}
	if byID["steam"].InstallableReason != steam.Detail {
		t.Errorf("installable_reason %q is not the availability sentence %q — two wordings for "+
			"one fact drift apart the first time one of them is edited",
			byID["steam"].InstallableReason, steam.Detail)
	}
	if byID["gitea"].InstallableReason != "" {
		t.Errorf("an installable app carries a reason it cannot be installed: %q",
			byID["gitea"].InstallableReason)
	}
}

// TestListEntries_AnswerSurvivesJSON. The App Hub reads JSON, not a Go struct.
// A field that does not marshal is a field the hub silently falls back from,
// and the fallback it used to have was the client-side comparison.
func TestListEntries_AnswerSurvivesJSON(t *testing.T) {
	arm64Box(t)
	reg := &Registry{Apps: map[string]*RegistryEntry{
		"steam": {Name: "Steam", Type: "desktop", Arch: []string{"amd64"},
			Versions: map[string]*VersionRecipe{"1.0": {FlatpakID: "com.valvesoftware.Steam"}}},
	}}
	raw, err := json.Marshal(reg.ListEntries(t.TempDir()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d entries", len(out))
	}
	avRaw, ok := out[0]["availability"]
	if !ok {
		t.Fatal("no `availability` key on the wire — the App Hub has nothing to render and " +
			"would have to derive the answer itself, which is the defect")
	}
	var av map[string]any
	if err := json.Unmarshal(avRaw, &av); err != nil {
		t.Fatalf("availability did not decode: %v", err)
	}
	for _, key := range []string{"state", "installable", "badge", "card_badge", "detail", "box_arch", "needs"} {
		if _, ok := av[key]; !ok {
			t.Errorf("availability.%s is missing from the wire shape", key)
		}
	}
	if av["state"] != ArchStateUnavailable {
		t.Errorf("state = %v on an arm64 box for an amd64-only app", av["state"])
	}
}

// ── the per-entry facts EvaluateArch needs ──────────────────────────────────

// TestEntryDeliveryKind_ReadsTheLatestRecipe. Delivery decides whether emulation
// is even in the conversation, and it is a property of the RECIPE.
func TestEntryDeliveryKind_ReadsTheLatestRecipe(t *testing.T) {
	cases := []struct {
		name  string
		entry *RegistryEntry
		want  DeliveryKind
	}{
		{"nil entry", nil, DeliveryUnknown},
		{"no recipes at all", &RegistryEntry{Name: "Empty"}, DeliveryUnknown},
		{"flatpak", &RegistryEntry{Versions: map[string]*VersionRecipe{
			"1.0": {FlatpakID: "org.gimp.GIMP"}}}, DeliveryFlatpak},
		{"per-arch artifacts", &RegistryEntry{Versions: map[string]*VersionRecipe{
			"1.0": {Artifacts: map[string]*Artifact{"amd64": {}, "arm64": {}}}}}, DeliveryBinary},
		{"legacy download_url", &RegistryEntry{Versions: map[string]*VersionRecipe{
			"1.0": {DownloadURL: "https://x/y"}}}, DeliveryBinary},
		{"install shell", &RegistryEntry{Versions: map[string]*VersionRecipe{
			"1.0": {Install: "apt-get install -y blender"}}}, DeliveryPackage},
		// The LATEST recipe decides, because latest is what an install offers.
		{"latest wins over an older shape", &RegistryEntry{Versions: map[string]*VersionRecipe{
			"1.0": {Install: "apt-get install -y thing"},
			"2.0": {FlatpakID: "org.thing.Thing"},
		}}, DeliveryFlatpak},
	}
	for _, c := range cases {
		if got := EntryDeliveryKind(c.entry); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestEntryNeedsGPU_ReadsALaneThatWasDeadData. `lane.needs_gpu` is set on
// shipped entries and, until this wiring, was read by NOTHING — the same shape
// as per-recipe `arch` (APP-RECIPE-STANDARD §1.1): a field that looks like it
// does something and does not.
func TestEntryNeedsGPU_ReadsALaneThatWasDeadData(t *testing.T) {
	cases := []struct {
		name string
		json string
		want bool
	}{
		{"the shipped shape", `{"name":"Blender","lane":{"needs_gpu":true}}`, true},
		{"lane present, gpu false", `{"name":"X","lane":{"needs_gpu":false}}`, false},
		{"lane present, key absent", `{"name":"X","lane":{"other":1}}`, false},
		{"no lane at all", `{"name":"X"}`, false},
		{"lane is not an object", `{"name":"X","lane":"gpu"}`, false},
		{"needs_gpu is a string", `{"name":"X","lane":{"needs_gpu":"yes"}}`, false},
	}
	for _, c := range cases {
		var e RegistryEntry
		if err := json.Unmarshal([]byte(c.json), &e); err != nil {
			t.Fatalf("%s: fixture did not parse: %v", c.name, err)
		}
		if got := EntryNeedsGPU(&e); got != c.want {
			t.Errorf("%s: got %v, want %v (extra=%v)", c.name, got, c.want, e.Extra)
		}
	}
	if EntryNeedsGPU(nil) {
		t.Error("a nil entry needs a GPU")
	}

	// It has to reach EvaluateArch, not merely parse: E3 only fires when the
	// emulation available HERE cannot bind this box's own graphics driver.
	arm64Box(t)
	var gpuEntry RegistryEntry
	if err := json.Unmarshal([]byte(
		`{"name":"Blender","arch":["amd64"],"lane":{"needs_gpu":true},`+
			`"versions":{"1.0":{"artifacts":{"amd64":{}}}}}`), &gpuEntry); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	env := archEnvironment{supported: []string{"arm64"}, emulated: []string{"amd64"}, optedIn: true}
	av := env.evaluate(&gpuEntry)
	if av.State != ArchStateUnavailable {
		t.Fatalf("a GPU-bound app was offered on a box whose only emulator cannot obtain a GL "+
			"visual at all: %+v", av)
	}
	if !strings.Contains(av.Detail, "graphics") {
		t.Errorf("the refusal does not say the GPU is why, so lane.needs_gpu changed the verdict "+
			"and not the explanation: %s", av.Detail)
	}
	// The CONTROL: same entry, same box, lane removed. Without it this test
	// passes on any refusal at all and proves nothing about the lane.
	var plain RegistryEntry
	if err := json.Unmarshal([]byte(
		`{"name":"Blender","arch":["amd64"],"versions":{"1.0":{"artifacts":{"amd64":{}}}}}`), &plain); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if av := env.evaluate(&plain); av.Detail == "" || strings.Contains(av.Detail, "graphics acceleration") {
		t.Errorf("an entry with NO lane got the GPU refusal — the lane is not what decided it: %s",
			av.Detail)
	}
}

// TestEntryEmulationPolicy_DefaultsClosed. A typo must not open the gate, and
// neither must an absent key.
func TestEntryEmulationPolicy_DefaultsClosed(t *testing.T) {
	cases := []struct {
		json string
		want EmulationPolicy
	}{
		{`{"name":"X"}`, EmulationNever},
		{`{"name":"X","emulation_policy":"opt-in"}`, EmulationOptIn},
		{`{"name":"X","emulation_policy":"OPT-IN"}`, EmulationOptIn},
		{`{"name":"X","emulation_policy":"never"}`, EmulationNever},
		{`{"name":"X","emulation_policy":"yes"}`, EmulationNever},
		{`{"name":"X","emulation_policy":true}`, EmulationNever},
		{`{"name":"X","emulation_policy":{"mode":"opt-in"}}`, EmulationNever},
	}
	for _, c := range cases {
		var e RegistryEntry
		if err := json.Unmarshal([]byte(c.json), &e); err != nil {
			t.Fatalf("%s: %v", c.json, err)
		}
		if got := EntryEmulationPolicy(&e); got != c.want {
			t.Errorf("%s: got %q, want %q", c.json, got, c.want)
		}
	}
	if EntryEmulationPolicy(nil) != EmulationNever {
		t.Error("a nil entry opened the emulation gate")
	}
}

// TestEntryEmulationPolicy_ReachesRungThree. The policy has to CHANGE the
// verdict, or it is another field read by nothing.
func TestEntryEmulationPolicy_ReachesRungThree(t *testing.T) {
	arm64Box(t)
	env := archEnvironment{supported: []string{"arm64"}, emulated: []string{"amd64"}, optedIn: true}

	recipe := `"versions":{"1.0":{"artifacts":{"amd64":{}}}}`
	var closed, open RegistryEntry
	if err := json.Unmarshal([]byte(`{"name":"Heroic","arch":["amd64"],`+recipe+`}`), &closed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"name":"Heroic","arch":["amd64"],"emulation_policy":"opt-in",`+recipe+`}`), &open); err != nil {
		t.Fatal(err)
	}

	if av := env.evaluate(&closed); av.State != ArchStateUnavailable {
		t.Errorf("an entry that never opted in was offered emulated: %+v", av)
	}
	av := env.evaluate(&open)
	if av.State != ArchStateEmulated || !av.Installable {
		t.Fatalf("an opted-in entry on an opted-in box with a matching emulator did not reach "+
			"rung 3: %+v", av)
	}
	if av.CardBadge != "Runs emulated" {
		t.Errorf("rung 3 badge = %q — the user has to be TOLD what they are getting, which is "+
			"the whole reason rung 3 is not rung 2", av.CardBadge)
	}
}

// TestArchEnvironment_ResolvedOncePerListing. Two entries of one response must
// not disagree about what machine they are on; that reads as a rendering fault
// and there is no way to tell it from one.
func TestArchEnvironment_ResolvedOncePerListing(t *testing.T) {
	arm64Box(t)
	reg := &Registry{Apps: map[string]*RegistryEntry{}}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		reg.Apps[id] = &RegistryEntry{Name: id, Arch: []string{"amd64"},
			Versions: map[string]*VersionRecipe{"1.0": {FlatpakID: "org.x." + id}}}
	}
	entries := reg.ListEntries(t.TempDir())
	if len(entries) != 5 {
		t.Fatalf("got %d entries", len(entries))
	}
	for _, e := range entries {
		if e.Availability.BoxArch != entries[0].Availability.BoxArch {
			t.Fatalf("%s reports box %q while %s reports %q — one response, two machines",
				e.ID, e.Availability.BoxArch, entries[0].ID, entries[0].Availability.BoxArch)
		}
	}
}

// TestShippedRegistry_EveryEntryGetsAnAnswer walks the REAL catalogue on a
// pinned arm64 box.
//
// It is the honest end of this work: the decision layer is only wired if it
// answers for all 74 shipped entries, including the ones whose recipe shape
// nobody thought about, and none of those answers may carry a claim nobody
// measured.
func TestShippedRegistry_EveryEntryGetsAnAnswer(t *testing.T) {
	arm64Box(t)

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "registry.json"))
	if err != nil {
		t.Fatalf("cannot read the registry this gate exists to walk: %v", err)
	}
	var reg Registry
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatalf("registry.json did not parse: %v", err)
	}
	if len(reg.Apps) < registryTotalFloor {
		t.Fatalf("examined %d entries, floor is %d — a shrinking registry would make this gate "+
			"pass by looking at nothing", len(reg.Apps), registryTotalFloor)
	}

	entries := reg.ListEntries(t.TempDir())
	if len(entries) != len(reg.Apps) {
		t.Fatalf("listed %d of %d entries", len(entries), len(reg.Apps))
	}

	states := map[string]int{}
	for _, e := range entries {
		av := e.Availability
		states[av.State]++
		switch av.State {
		case ArchStateNative, ArchStateEmulated, ArchStateOtherInstance, ArchStateUnavailable:
		default:
			t.Fatalf("%s: state %q is not one of the four rungs", e.ID, av.State)
		}
		if av.BoxArch != "arm64" {
			t.Errorf("%s: box_arch=%q", e.ID, av.BoxArch)
		}
		if av.State != ArchStateNative && av.Detail == "" {
			t.Errorf("%s: refused with no sentence", e.ID)
		}
		for _, s := range []string{av.Badge, av.CardBadge, av.Detail} {
			low := strings.ToLower(s)
			for _, banned := range unmeasuredClaims {
				if strings.Contains(low, banned.phrase) {
					t.Errorf("%s: shipped copy says %q.\n  %s\n  full text: %s",
						e.ID, banned.phrase, banned.why, s)
				}
			}
		}
	}

	// COVERAGE, both ways. All-native would mean the walk never exercised a
	// refusal; all-unavailable would mean it never exercised the native path.
	// Either reads as a green gate over an unexamined half.
	if states[ArchStateNative] == 0 {
		t.Errorf("no entry is native on an arm64 box — the catalogue or the fold is broken: %v", states)
	}
	if states[ArchStateUnavailable] == 0 {
		t.Errorf("every shipped entry is installable on arm64, which contradicts the amd64-only "+
			"declarations the ratchet counts: %v", states)
	}
	t.Logf("shipped registry on an arm64 box: %v", states)
}
