package appnet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFlatpakOverrideFlags_TheDeclarationDecides pins the whole point of the
// bridge: a recipe's `permissions` array must change what the installed app can
// reach. Before this existed, every case below produced the same thing —
// nothing — and the array was decoration.
func TestFlatpakOverrideFlags_TheDeclarationDecides(t *testing.T) {
	cases := []struct {
		name  string
		perms []string
		want  []string
	}{
		{
			// The shape that named the problem: gimp says ["filesystem","gpu"]
			// and Flathub hands it network anyway.
			name:  "no network declared revokes network",
			perms: []string{"filesystem", "gpu"},
			want:  []string{"--disallow=bluetooth", "--unshare=network"},
		},
		{
			// firefox says ["network","gpu"] — no filesystem — while its
			// manifest can carry host. Only host and home are negated, so the
			// app's own xdg-download still works and downloads do not break.
			name:  "no filesystem declared revokes host and home only",
			perms: []string{"network", "gpu"},
			want:  []string{"--disallow=bluetooth", "--nofilesystem=host", "--nofilesystem=home"},
		},
		{
			name:  "declaring both leaves only the bluetooth revocation",
			perms: []string{"network", "filesystem"},
			want:  []string{"--disallow=bluetooth"},
		},
		{
			name:  "declaring everything enforceable removes nothing",
			perms: []string{"network", "filesystem", "bluetooth"},
			want:  nil,
		},
		{
			name:  "an empty array is a real declaration, not a missing one",
			perms: nil,
			want:  []string{"--disallow=bluetooth", "--nofilesystem=host", "--nofilesystem=home", "--unshare=network"},
		},
		{
			name:  "case and whitespace do not smuggle a permission through",
			perms: []string{" Network ", "FILESYSTEM"},
			want:  []string{"--disallow=bluetooth"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FlatpakOverrideFlags(c.perms)
			if strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Errorf("permissions %v\n got: %v\nwant: %v", c.perms, got, c.want)
			}
		})
	}
}

// TestFlatpakOverrideFlags_OnlyEverRemoves. Vulos narrowing a publisher's
// sandbox on the owner's behalf is defensible. Vulos WIDENING it because a
// registry entry listed a string is not — a compromised or careless entry could
// otherwise hand itself the host filesystem that Flathub's manifest withheld.
func TestFlatpakOverrideFlags_OnlyEverRemoves(t *testing.T) {
	all := []string{"network", "filesystem", "camera", "microphone", "bluetooth", "usb", "gpu", "background", "notifications", "storage"}
	for _, subset := range [][]string{nil, all, {"network"}, {"filesystem"}, {"bluetooth"}, {"gpu", "usb", "storage"}} {
		for _, f := range FlatpakOverrideFlags(subset) {
			if !strings.HasPrefix(f, "--unshare=") && !strings.HasPrefix(f, "--nofilesystem=") &&
				!strings.HasPrefix(f, "--nodevice=") && !strings.HasPrefix(f, "--nosocket=") &&
				!strings.HasPrefix(f, "--disallow=") {
				t.Errorf("permissions %v produced %q, which GRANTS rather than revokes", subset, f)
			}
		}
	}
}

// TestFlatpakPermissions_EveryValidPermissionIsAccountedFor is the guard that
// keeps the gap from re-forming quietly. Seven of the ten permission strings
// cannot be expressed as a Flatpak override; that is a stated limit, not an
// omission, and it stays stated. A permission added to ValidPermissions later
// must be either enforced or listed as unenforceable WITH ITS REASON, and this
// test is what forces that choice to be made rather than defaulted into.
func TestFlatpakPermissions_EveryValidPermissionIsAccountedFor(t *testing.T) {
	if len(ValidPermissions) == 0 {
		t.Fatal("ValidPermissions is empty — this test would pass over nothing")
	}
	for _, p := range ValidPermissions {
		_, enforced := enforcedFlatpakPermissions[p]
		reason, named := unenforcedFlatpakPermissions[p]
		switch {
		case enforced && named:
			t.Errorf("%q is listed as both enforced and unenforceable — one of the two is a lie", p)
		case !enforced && !named:
			t.Errorf("%q is neither enforced by the Flatpak bridge nor named as unenforceable. "+
				"It is therefore a permission a recipe can declare that decides nothing, which is the "+
				"defect this bridge exists to close. Add it to enforcedFlatpakPermissions, or to "+
				"unenforcedFlatpakPermissions with the reason it cannot be expressed.", p)
		case named && strings.TrimSpace(reason) == "":
			t.Errorf("%q is named unenforceable with an empty reason — the reason IS the documentation", p)
		}
	}
	// And nothing invented: both maps may only name real permissions.
	valid := map[string]bool{}
	for _, p := range ValidPermissions {
		valid[p] = true
	}
	for _, m := range []map[string]string{unenforcedFlatpakPermissions} {
		for p := range m {
			if !valid[p] {
				t.Errorf("unenforcedFlatpakPermissions names %q, which is not a valid permission", p)
			}
		}
	}
	for p := range enforcedFlatpakPermissions {
		if !valid[p] {
			t.Errorf("enforcedFlatpakPermissions names %q, which is not a valid permission", p)
		}
	}
}

// TestFlatpakInstall_IsGivenTheRecipesPermissions reads the install dispatch.
// The bridge is worthless if the call site passes nothing: FlatpakOverrideFlags
// would then compute a full revocation from an empty array and break every
// Flatpak app, or — with a nil-guard — silently narrow nothing. Collection is
// not execution, so the wiring is asserted rather than assumed.
func TestFlatpakInstall_IsGivenTheRecipesPermissions(t *testing.T) {
	src, err := os.ReadFile("registry.go")
	if err != nil {
		t.Fatalf("read registry.go: %v", err)
	}
	if !strings.Contains(string(src), "FlatpakInstall(ctx, recipe.FlatpakID, recipe.Permissions)") {
		t.Error("the install dispatch does not hand FlatpakInstall the recipe's permissions — " +
			"the declared sandbox would not reach the app")
	}
	fp, err := os.ReadFile("flatpak.go")
	if err != nil {
		t.Fatalf("read flatpak.go: %v", err)
	}
	body := string(fp)
	if !strings.Contains(body, "FlatpakApplyOverrides(ctx, flatpakID, permissions)") {
		t.Error("FlatpakInstall does not apply the overrides it was given")
	}
	// Fail-closed: an app whose narrowing could not be applied must not be left
	// installed. Anchor on the uninstall inside the error branch.
	i := strings.Index(body, "FlatpakApplyOverrides(ctx, flatpakID, permissions)")
	j := strings.Index(body, "InvalidateFlatpakCache()\n\n\t// Ensure user data dirs")
	if i < 0 || j < 0 || i > j {
		t.Fatal("could not locate the override branch — this guard is reading the wrong shape")
	}
	if !strings.Contains(body[i:j], `"uninstall"`) {
		t.Error("FlatpakInstall does not roll back when the declared sandbox cannot be applied — " +
			"it would leave an app the owner was told is restricted running unrestricted")
	}
	if !strings.Contains(body, "FlatpakResetOverrides(ctx, flatpakID)") {
		t.Error("FlatpakUninstall does not drop the override — a stale narrowing would apply to " +
			"whatever is installed under this id next")
	}
}

// TestValidateRecipe_RefusesAPermissionThatIsNotOne is PERMS-01 at the install
// path, where it fails closed for every recipe that will ever be written — not
// only the ones in today's registry.
func TestValidateRecipe_RefusesAPermissionThatIsNotOne(t *testing.T) {
	for _, bad := range []string{"display", "audio", "net", "", "NETWORKING"} {
		if err := rejectUnknownPermissions([]string{"filesystem", bad}); err == nil {
			t.Errorf("accepted %q as a permission — the bridge would revoke the access it was "+
				"meant to declare", bad)
		}
	}
	for _, ok := range [][]string{nil, {"network"}, {"filesystem", "gpu", "microphone"}, {"Network", " gpu "}} {
		if err := rejectUnknownPermissions(ok); err != nil {
			t.Errorf("refused a legitimate permission set %v: %v", ok, err)
		}
	}
	// Wired, not merely written.
	recipe := &VersionRecipe{
		FlatpakID:   "org.example.App",
		Command:     "flatpak run org.example.App",
		Permissions: []string{"display"},
	}
	if err := validateRecipeSecurity(recipe); err == nil || !strings.Contains(err.Error(), "PERMS-01") {
		t.Fatalf("validateRecipeSecurity does not apply the permission-name rule: %v", err)
	}
}

// TestShippedRegistry_DeclaresOnlyRealPermissions. With the bridge live, an
// unrecognised permission string is no longer harmless documentation: it does
// not match any enforced name, so the permission it was meant to declare is
// REVOKED. This is scoped to entries that can actually reach an install:
// registry.json's parked `steam` entry declares "display" and "audio" — found
// by an earlier revision of this test — and it is `_disabled`, so it cannot
// install and cannot reach the bridge. PERMS-01 refuses it the moment anyone
// re-enables it, and the corrected entry is staged in
// registry.d/apt-retired.json for the registry's single writer to merge.
func TestShippedRegistry_DeclaresOnlyRealPermissions(t *testing.T) {
	// testdata/registry.json is a symlink to the real file. The path matters:
	// `go test` only tracks files INSIDE the module when deciding whether a
	// cached result is stale, so a gate reading ../../../registry.json prints
	// "ok (cached)" over a registry it never opened. Measured 2026-08-19; see
	// registry_fragments_test.go.
	raw, err := os.ReadFile(filepath.Join("testdata", "registry.json"))
	if err != nil {
		t.Fatalf("cannot read registry.json: %v", err)
	}
	var reg struct {
		Apps map[string]struct {
			Disabled bool `json:"_disabled"`
			Versions map[string]struct {
				Disabled    bool     `json:"_disabled"`
				Permissions []string `json:"permissions"`
				FlatpakID   string   `json:"flatpak_id"`
			} `json:"versions"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatalf("registry.json did not parse: %v", err)
	}
	if len(reg.Apps) < registryTotalFloor {
		t.Fatalf("examined %d entries, floor is %d — a shrinking registry would make this "+
			"gate pass by looking at nothing", len(reg.Apps), registryTotalFloor)
	}
	valid := map[string]bool{}
	for _, p := range ValidPermissions {
		valid[p] = true
	}
	checked := 0
	skipped := 0
	for id, entry := range reg.Apps {
		for version, recipe := range entry.Versions {
			if entry.Disabled || recipe.Disabled {
				// Cannot install, so cannot reach the bridge. PERMS-01 covers it
				// at the install path the day it is re-enabled.
				skipped++
				continue
			}
			for _, p := range recipe.Permissions {
				checked++
				if !valid[p] {
					t.Errorf("%s@%s declares permission %q, which is not in ValidPermissions. "+
						"Under the Flatpak bridge an unrecognised string is not documentation: it "+
						"matches no enforced name, so the access it was meant to declare is revoked.",
						id, version, p)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no permission strings were examined — the gate proved nothing")
	}
	// The skip is bounded too. If _disabled spread across the registry this
	// gate would quietly stop examining the catalogue while still reporting a
	// non-zero count from a handful of entries.
	if skipped > len(reg.Apps)/2 {
		t.Fatalf("%d of %d recipes were skipped as disabled — this gate is no longer looking at "+
			"the shipping catalogue", skipped, len(reg.Apps))
	}
}
