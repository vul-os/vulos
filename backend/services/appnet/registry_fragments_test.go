package appnet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// registry.d/*.json fragments are the ONLY way an entry reaches registry.json:
// that file has a single writer, and everyone else stages a fragment for it to
// merge. Nothing checked those fragments. They were validated, if at all, after
// the merge — by which point the bad entry is in the signed artefact and the
// person who wrote it has moved on.
//
// This gate runs the SAME validation an install runs, before the merge. It is
// deliberately not a second, gentler implementation: validateRecipeSecurity is
// called directly, so a rule added there cannot be one this gate never learns.

type fragmentDoc struct {
	Apps map[string]*RegistryEntry `json:"apps"`
}

// loadFragments reads every registry.d fragment, tolerating the two shapes in
// use: {"apps": {...}} and a bare map of entries.
func loadFragments(t *testing.T) map[string]map[string]*RegistryEntry {
	t.Helper()
	// READ THROUGH testdata/, WHICH IS A SYMLINK, AND THAT IS LOAD-BEARING.
	//
	// `go test` caches a package's result and decides staleness from the files
	// the test opened — but ONLY files inside the module. registry.d/ sits at
	// the repo root and this module is backend/, so a gate opening
	// ../../../registry.d is invisible to the cache: edit a fragment, run
	// `go test ./services/appnet/`, and it prints "ok (cached)" over data it
	// never read.
	//
	// MEASURED 2026-08-19, all three cases: a file inside the package
	// invalidates the cache; ../../../registry.d does not; and the same file
	// reached through testdata/registry.d (in-module path, symlink to the same
	// bytes) DOES. So the read path is the fix, and it costs nothing.
	//
	// Every mutation of this gate was reported "ok (cached)" before this was
	// understood — eight mutations, all apparently survived, all actually
	// unexecuted. A gate that cannot be mutation-tested is a gate nobody can
	// know the strength of.
	dir := filepath.Join("testdata", "registry.d")
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no fragments found in %s (err %v) — this gate would examine nothing", dir, err)
	}
	out := map[string]map[string]*RegistryEntry{}
	for _, path := range names {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc fragmentDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s is not valid JSON: %v", filepath.Base(path), err)
		}
		if doc.Apps == nil {
			var bare map[string]*RegistryEntry
			if err := json.Unmarshal(raw, &bare); err != nil {
				t.Fatalf("%s has neither an `apps` object nor a bare entry map: %v", filepath.Base(path), err)
			}
			doc.Apps = map[string]*RegistryEntry{}
			for k, v := range bare {
				if !strings.HasPrefix(k, "_") && v != nil && v.Versions != nil {
					doc.Apps[k] = v
				}
			}
		}
		out[filepath.Base(path)] = doc.Apps
	}
	return out
}

// TestFragments_PassTheSameValidationAnInstallRuns.
func TestFragments_PassTheSameValidationAnInstallRuns(t *testing.T) {
	frags := loadFragments(t)
	checked := 0
	for name, apps := range frags {
		for id, entry := range apps {
			if entry == nil || len(entry.Versions) == 0 {
				t.Errorf("%s: entry %q has no versions — it can install nothing", name, id)
				continue
			}
			for version, recipe := range entry.Versions {
				if entry.Disabled || recipe.Disabled {
					// A withdrawn recipe is refused by validateRecipeSecurity on
					// its first line, so running it here would assert nothing
					// about the rest of the recipe. DISABLED-01 covers these.
					continue
				}
				checked++
				if err := validateRecipeSecurity(recipe); err != nil {
					t.Errorf("%s: %s@%s would be REFUSED at install: %v", name, id, version, err)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no enabled recipes were validated — the gate proved nothing")
	}
	t.Logf("validated %d enabled recipes across %d fragments", checked, len(frags))
}

// TestFragments_DeclareArchAndValidPermissions covers the two facts that decide
// whether an entry is offered to a box at all, and that validateRecipeSecurity
// does not see because they live on the ENTRY rather than the recipe.
func TestFragments_DeclareArchAndValidPermissions(t *testing.T) {
	frags := loadFragments(t)
	valid := map[string]bool{}
	for _, p := range ValidPermissions {
		valid[p] = true
	}
	entries := 0
	for name, apps := range frags {
		for id, entry := range apps {
			entries++
			if !ArchDeclared(entry.Arch) {
				t.Errorf("%s: %q declares no arch — an undeclared entry is an unverified claim to "+
					"EVERY architecture Vulos ships", name, id)
			}
			for _, a := range entry.Arch {
				if a != "amd64" && a != "arm64" {
					t.Errorf("%s: %q declares arch %q; Vulos publishes amd64 and arm64, and the "+
						"registry uses the Debian spelling (not x86_64/aarch64)", name, id, a)
				}
			}
			for _, recipe := range entry.Versions {
				for _, p := range recipe.Permissions {
					if !valid[p] {
						t.Errorf("%s: %q declares permission %q, which is not one Vulos has. Under the "+
							"Flatpak bridge that REVOKES the access it was meant to declare", name, id, p)
					}
				}
			}
		}
	}
	if entries < 100 {
		t.Fatalf("only %d fragment entries examined; the staged catalogue is larger than that, so "+
			"this gate is reading the wrong files", entries)
	}
}

// TestFragments_AreUnsignedAndUncontested. Two invariants of the staging
// mechanism itself.
//
// UNSIGNED: any byte changed after signing invalidates that entry's signature.
// A fragment carrying an inherited signature merges into a registry where that
// entry verifies as UNTRUSTED — "publisher signature did not verify" — which is
// strictly worse than unsigned, because unsigned reads as "awaiting the
// ceremony" and untrusted reads as tampering.
//
// UNCONTESTED: two fragments defining the same id give the merge a silent
// coin-flip, and the loser's measurements vanish without a diff.
func TestFragments_AreUnsignedAndUncontested(t *testing.T) {
	frags := loadFragments(t)
	owner := map[string]string{}
	for name, apps := range frags {
		for id, entry := range apps {
			if strings.TrimSpace(entry.Signature) != "" {
				t.Errorf("%s: %q carries a signature. Fragments are unsigned by construction; a stale "+
					"signature merges as UNTRUSTED, not as unsigned", name, id)
			}
			if prev, dup := owner[id]; dup {
				t.Errorf("%q is defined by both %s and %s — the merge would silently pick one", id, prev, name)
			}
			owner[id] = name
		}
	}
}

// TestFragments_FlatpakCommandMatchesTheId. A Flatpak recipe's `command` is
// what the launcher execs. If it names a different app than `flatpak_id`, the
// install succeeds, the app is present, and launching it runs the wrong thing
// or nothing — the install-reports-success-and-the-app-is-broken shape.
//
// The bare id is the correct target even when flatpak_id is branch-qualified:
// `flatpak run org.qgis.qgis//stable` is not what the installed app answers to.
func TestFragments_FlatpakCommandMatchesTheId(t *testing.T) {
	frags := loadFragments(t)
	checked := 0
	for name, apps := range frags {
		for id, entry := range apps {
			for version, recipe := range entry.Versions {
				if recipe.FlatpakID == "" {
					continue
				}
				checked++
				bare := strings.SplitN(recipe.FlatpakID, "//", 2)[0]
				want := "flatpak run " + bare
				if recipe.Command != want {
					t.Errorf("%s: %s@%s has flatpak_id %q but command %q; want %q",
						name, id, version, recipe.FlatpakID, recipe.Command, want)
				}
			}
		}
	}
	if checked < 40 {
		t.Fatalf("only %d Flatpak recipes examined — this gate is not reading the staged catalogue", checked)
	}
}

// TestFragments_IdsAreUsableAsAppIds. The registry key becomes a DNS label, a
// directory name and a URL path segment. An id the rest of the system cannot
// carry is found here rather than at install.
func TestFragments_IdsAreUsableAsAppIds(t *testing.T) {
	frags := loadFragments(t)
	var bad []string
	for name, apps := range frags {
		for id := range apps {
			if len(id) == 0 || len(id) > appIDMaxLen {
				bad = append(bad, name+":"+id+" (length)")
				continue
			}
			for _, c := range id {
				if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
					bad = append(bad, name+":"+id+" (character "+string(c)+")")
					break
				}
			}
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("fragment ids that cannot be used as app ids: %v", bad)
	}
}
