package appnet

// registry_lossless_test.go — the publisher signature must cover EVERY field on
// disk, not just the ones the Go struct happens to model.
//
// registry.json carries keys the structs do not declare ("_note", "lane",
// "admin_only", a per-recipe "arch"). Before RegistryEntry/VersionRecipe kept an
// Extra map, those keys were dropped on unmarshal — which meant the signature
// was computed over a *subset* of the entry, and an attacker could add or
// rewrite any unmodelled field inside a validly-signed entry without detection.
// These tests pin the fix.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

const entryWithUnmodelledFields = `{
  "name": "Example",
  "vetted": true,
  "type": "web",
  "_disabled": true,
  "admin_only": true,
  "lane": {"needs_gpu": true},
  "_note": "kept for the record",
  "versions": {
    "1.0": {
      "install": "apt-get install -y example",
      "arch": "amd64",
      "_note": "recipe-level note"
    }
  }
}`

// TestRegistryEntry_PreservesUnmodelledFields — load, save, and the unmodelled
// keys are still there, with their values intact.
func TestRegistryEntry_PreservesUnmodelledFields(t *testing.T) {
	var entry RegistryEntry
	if err := json.Unmarshal([]byte(entryWithUnmodelledFields), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// The modelled field must be read, not stashed in Extra.
	if !entry.Disabled {
		t.Error("_disabled did not populate the modelled Disabled field")
	}
	if _, stashed := entry.Extra["_disabled"]; stashed {
		t.Error("_disabled is modelled but was also stashed in Extra")
	}

	out, err := json.Marshal(&entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	for _, key := range []string{"admin_only", "lane", "_note", "_disabled"} {
		if _, ok := got[key]; !ok {
			t.Errorf("entry key %q was dropped on round-trip — it would be outside the signature", key)
		}
	}
	if got["admin_only"] != true {
		t.Errorf("admin_only changed value on round-trip: %v", got["admin_only"])
	}

	recipe := got["versions"].(map[string]any)["1.0"].(map[string]any)
	for _, key := range []string{"arch", "_note"} {
		if _, ok := recipe[key]; !ok {
			t.Errorf("recipe key %q was dropped on round-trip", key)
		}
	}
	if recipe["arch"] != "amd64" {
		t.Errorf("recipe arch changed on round-trip: %v", recipe["arch"])
	}
}

// TestSignature_CoversUnmodelledFields — the actual security claim. Sign an
// entry, then mutate an unmodelled field. Verification must fail.
func TestSignature_CoversUnmodelledFields(t *testing.T) {
	cases := map[string]string{
		"entry_admin_only": `"admin_only": true`,
		"entry_lane":       `"lane": {"needs_gpu": true}`,
		"entry_note":       `"_note": "kept for the record"`,
		"recipe_arch":      `"arch": "amd64"`,
		"recipe_note":      `"_note": "recipe-level note"`,
	}

	for name, needle := range cases {
		t.Run(name, func(t *testing.T) {
			pub, priv := generateTestKey(t)

			var entry RegistryEntry
			if err := json.Unmarshal([]byte(entryWithUnmodelledFields), &entry); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if err := SignEntry(&entry, "example", priv); err != nil {
				t.Fatalf("SignEntry: %v", err)
			}
			if err := VerifyEntrySignature(&entry, "example", pub); err != nil {
				t.Fatalf("freshly signed entry does not verify: %v", err)
			}

			// Tamper with the unmodelled field, keeping the signature intact.
			tampered := strings.Replace(entryWithUnmodelledFields, needle, tamperValue(needle), 1)
			if tampered == entryWithUnmodelledFields {
				t.Fatalf("test bug: %q not found in the fixture", needle)
			}
			var evil RegistryEntry
			if err := json.Unmarshal([]byte(tampered), &evil); err != nil {
				t.Fatalf("unmarshal tampered: %v", err)
			}
			evil.Signature = entry.Signature

			if err := VerifyEntrySignature(&evil, "example", pub); err == nil {
				t.Fatalf("mutating %s did not invalidate the signature — the field is outside the signed bytes", name)
			}
		})
	}
}

// tamperValue rewrites a JSON key/value pair to a different value of the same
// shape, so the tampered fixture stays valid JSON.
func tamperValue(needle string) string {
	key, _, _ := strings.Cut(needle, ":")
	switch {
	case strings.Contains(needle, "true"):
		return key + ": false"
	case strings.Contains(needle, "{"):
		return key + `: {"needs_gpu": false}`
	default:
		return key + `: "ATTACKER"`
	}
}

// TestShippedRegistry_RoundTripsLosslessly — re-serialising the real 55-entry
// registry must not drop a key. If it did, `make sign-registry` would quietly
// delete data from the file it rewrites.
func TestShippedRegistry_RoundTripsLosslessly(t *testing.T) {
	path := filepath.Join(repoRoot, "registry.json")

	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load shipped registry: %v", err)
	}

	out, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var reloaded Registry
	if err := json.Unmarshal(out, &reloaded); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if len(reloaded.Apps) != len(reg.Apps) {
		t.Fatalf("app count changed on round-trip: %d → %d", len(reg.Apps), len(reloaded.Apps))
	}

	// Every unmodelled key, on every entry and recipe, must survive.
	for appID, entry := range reg.Apps {
		got, ok := reloaded.Apps[appID]
		if !ok {
			t.Errorf("entry %q vanished on round-trip", appID)
			continue
		}
		for k := range entry.Extra {
			if _, ok := got.Extra[k]; !ok {
				t.Errorf("entry %q lost unmodelled key %q", appID, k)
			}
		}
		for ver, recipe := range entry.Versions {
			gotRecipe, ok := got.Versions[ver]
			if !ok {
				t.Errorf("entry %q lost version %q", appID, ver)
				continue
			}
			for k := range recipe.Extra {
				if _, ok := gotRecipe.Extra[k]; !ok {
					t.Errorf("entry %q version %q lost unmodelled key %q", appID, ver, k)
				}
			}
		}
	}
}
