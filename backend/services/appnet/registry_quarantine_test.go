package appnet

// registry_quarantine_test.go — the unverified quarantine registry.
//
// registry.json is the SIGNED set and has no exception path: `make verify-registry`
// and TestAcceptance_ShippedAnchorVerifiesShippedRegistry both require every
// entry in it to be signed by the trusted publisher, full stop. An entry that is
// not fit to be signed — one whose own _note says UNVERIFIED — is not signed
// anyway, and is not quietly excused either: it is moved OUT of the signed file
// into registry-unverified.json, which nothing loads.
//
// These tests assert that arrangement against the files the repo actually ships,
// because the failure mode being guarded against is "the split is documented but
// the android entry crept back in", not "the code can't do set difference".

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// shippedQuarantinePath is the quarantine registry this repo ships.
func shippedQuarantinePath() string {
	return filepath.Join(repoRoot, UnverifiedRegistryFile)
}

// rawShippedApps returns the "apps" object of a shipped registry file, parsed as
// raw JSON so the assertions see the file as it literally is.
func rawShippedApps(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var top struct {
		Unverified *bool                      `json:"_unverified"`
		Apps       map[string]json.RawMessage `json:"apps"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return top.Apps
}

// TestShippedRegistry_HoldsNoUnsignedEntry — the signed set is absolute.
//
// The acceptance suite proves the signatures verify; this proves the weaker but
// more direct property that made the split necessary: not one entry in the file
// we ship is missing a signature, so the gate never needs an excuse.
func TestShippedRegistry_HoldsNoUnsignedEntry(t *testing.T) {
	reg := shippedRegistry(t)
	if len(reg.Apps) == 0 {
		t.Fatal("shipped registry.json has no apps")
	}
	var unsigned []string
	for appID, entry := range reg.Apps {
		if entry.Signature == "" {
			unsigned = append(unsigned, appID)
		}
	}
	sort.Strings(unsigned)
	if len(unsigned) > 0 {
		t.Fatalf("registry.json ships %d UNSIGNED entry/entries %v — either sign them "+
			"(`make sign-registry`) or move them to %s; the signed set has no exception path",
			len(unsigned), unsigned, UnverifiedRegistryFile)
	}
	t.Logf("all %d shipped registry.json entries carry a signature", len(reg.Apps))
}

// TestShippedQuarantine_IsUnsignedDisjointAndNonEmpty is the coverage assertion
// for the split: it fails if the quarantine file is missing, empty, unmarked,
// overlapping with the signed set, or contains anything that looks signed.
//
// An empty or absent quarantine file is NOT quietly treated as "nothing to
// check" here: this test knows the repo currently quarantines android, so a
// vacuous pass is impossible. When android is promoted, this test is what has to
// be deliberately updated — the deletion cannot happen by accident.
func TestShippedQuarantine_IsUnsignedDisjointAndNonEmpty(t *testing.T) {
	qPath := shippedQuarantinePath()
	data, err := os.ReadFile(qPath)
	if err != nil {
		t.Fatalf("read shipped %s: %v (if every entry has been promoted, delete this test "+
			"together with the file — do not leave it passing on an absent file)", UnverifiedRegistryFile, err)
	}

	var top struct {
		Unverified *bool                      `json:"_unverified"`
		Promotion  []string                   `json:"_promotion"`
		Apps       map[string]json.RawMessage `json:"apps"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("parse %s: %v", qPath, err)
	}

	if top.Unverified == nil || !*top.Unverified {
		t.Errorf(`%s must declare "%s": true — that marker is what makes LoadRegistry refuse it `+
			`even if the file is renamed`, qPath, unverifiedMarkerKey)
	}
	if len(top.Promotion) == 0 {
		t.Errorf("%s must carry the _promotion path (what has to happen for an entry to reach "+
			"the signed registry), or the quarantine becomes a place things go to be forgotten", qPath)
	}
	if len(top.Apps) == 0 {
		t.Fatalf("%s is empty — delete the file instead of shipping an empty quarantine", qPath)
	}

	// Nothing in here may carry a signature.
	for appID, raw := range top.Apps {
		var probe struct {
			Signature string `json:"signature"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("parse %s entry %q: %v", qPath, appID, err)
		}
		if probe.Signature != "" {
			t.Errorf("quarantined entry %q carries a signature — it either belongs in registry.json "+
				"(promote it) or the signature is bogus", appID)
		}
	}

	// The two sets must be disjoint.
	signed := shippedRegistry(t)
	for appID := range top.Apps {
		if _, clash := signed.Apps[appID]; clash {
			t.Errorf("app ID %q is in BOTH registry.json and %s — an unverified entry must never "+
				"shadow a signed one", appID, UnverifiedRegistryFile)
		}
	}

	ids := make([]string, 0, len(top.Apps))
	for id := range top.Apps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	t.Logf("quarantined (unsigned, not loaded, not shipped): %s", strings.Join(ids, ", "))
}

// TestShippedQuarantine_AndroidCaveatsTravelVerbatim — moving the entry must not
// launder it. The caveats that made it unverifiable in the first place have to
// arrive intact, or the next person to read the file sees a normal-looking app.
func TestShippedQuarantine_AndroidCaveatsTravelVerbatim(t *testing.T) {
	apps := rawShippedApps(t, shippedQuarantinePath())
	raw, ok := apps["android"]
	if !ok {
		t.Skipf("android is no longer quarantined — if it was promoted, its _note must no longer "+
			"say UNVERIFIED and %s must reflect that", shippedQuarantinePath())
	}
	var probe struct {
		Note string `json:"_note"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("parse quarantined android entry: %v", err)
	}

	// The load-bearing sentences, verbatim from the entry as it stood in
	// registry.json before the move.
	want := []string{
		"UNVERIFIED: authored without access to a real Docker daemon, binder/ashmem kernel modules, " +
			"or an actual redroid image; needs real-hardware validation before being trusted in production.",
		"Registry signature is intentionally blank — this entry is inert until a maintainer runs",
		"HARDWARE/KERNEL-GATED",
		"Location injection is MOCK-LOCATION ONLY",
		"Does NOT unlock banking, tap-to-pay, SafetyNet/Play-Integrity-gated, or attestation-locked apps",
	}
	for _, w := range want {
		if !strings.Contains(probe.Note, w) {
			t.Errorf("the android entry's _note lost a caveat in the move to quarantine.\nmissing: %q", w)
		}
	}
	if len(probe.Note) < 1000 {
		t.Errorf("the android _note is %d bytes — it was 1503 when moved out of registry.json; "+
			"caveats appear to have been trimmed", len(probe.Note))
	}
}

// TestShippedQuarantine_IsNotCopiedIntoTheImage — the quarantine is only
// harmless because it never reaches a box. Nothing that assembles the image or
// the deploy payload may copy it.
//
// This reads the build files rather than editing them: if someone later adds a
// COPY/cp of the quarantine alongside registry.json, this fails and names the
// file, instead of an unsigned entry quietly landing in /opt/vulos.
func TestShippedQuarantine_IsNotCopiedIntoTheImage(t *testing.T) {
	shippers := []string{"Dockerfile", "build.sh", "dev.sh"}
	checked := 0
	for _, name := range shippers {
		path := filepath.Join(repoRoot, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("cannot read %s to check it does not ship the quarantine: %v", name, err)
			continue
		}
		checked++
		// registry.json is a substring of registry-unverified.json's neighbours,
		// so match the quarantine filename itself.
		if strings.Contains(string(data), UnverifiedRegistryFile) {
			t.Errorf("%s references %s — the quarantine must never be copied into an image "+
				"or a deploy payload; only registry.json ships", name, UnverifiedRegistryFile)
		}
		// Coverage: the file must actually ship the signed registry, otherwise a
		// rename would make this check vacuous.
		if !strings.Contains(string(data), "registry.json") {
			t.Errorf("%s no longer mentions registry.json — this check may be looking at the "+
				"wrong file, and would pass without proving anything", name)
		}
	}
	if checked != len(shippers) {
		t.Fatalf("only %d/%d image-assembling files were checked — the rest were unreadable, so "+
			"this test did NOT prove the quarantine stays out of the image", checked, len(shippers))
	}
}

// TestLoadRegistry_RefusesQuarantineByFilename — the shipped file cannot be
// loaded as a trusted registry. This is what stops VULOS_REGISTRY=<quarantine>
// from listing unsigned entries in the App Hub.
func TestLoadRegistry_RefusesQuarantineByFilename(t *testing.T) {
	qPath := shippedQuarantinePath()
	if _, err := os.Stat(qPath); err != nil {
		t.Fatalf("shipped %s missing: %v", UnverifiedRegistryFile, err)
	}
	reg, err := LoadRegistry(qPath)
	if err == nil {
		t.Fatalf("LoadRegistry ACCEPTED %s (%d apps) — the quarantine is loadable as trusted",
			qPath, len(reg.Apps))
	}
	if !errors.Is(err, ErrUnverifiedRegistry) {
		t.Errorf("LoadRegistry rejected %s for the wrong reason: %v", qPath, err)
	}
}

// TestLoadRegistry_RefusesQuarantineMarkerUnderAnyName — renaming the file must
// not launder it. A copy called registry.json that still declares the marker is
// refused just the same.
func TestLoadRegistry_RefusesQuarantineMarkerUnderAnyName(t *testing.T) {
	dir := t.TempDir()
	innocent := filepath.Join(dir, "registry.json")
	body := []byte(`{"_unverified": true, "apps": {"x": {"name": "X", "signature": ""}}}`)
	if err := os.WriteFile(innocent, body, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(innocent); !errors.Is(err, ErrUnverifiedRegistry) {
		t.Fatalf("a quarantine file renamed to registry.json was not refused: err=%v", err)
	}

	// Control: the same file WITHOUT the marker, under a normal name, still loads —
	// proving the refusal keys on the marker and not on something incidental.
	ok := filepath.Join(dir, "sub-registry.json")
	if err := os.WriteFile(ok, []byte(`{"apps": {"x": {"name": "X", "signature": ""}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(ok); err != nil {
		t.Fatalf("an ordinary registry was refused: %v", err)
	}
}

// TestLoadUnverifiedRegistry_OptInIsLoudAndProdRefuses — the one deliberate way
// to read the quarantine. It must refuse prod, refuse a signed-looking entry,
// and refuse files that are not actually quarantined.
func TestLoadUnverifiedRegistry_OptInIsLoudAndProdRefuses(t *testing.T) {
	qPath := shippedQuarantinePath()

	t.Run("refused in prod", func(t *testing.T) {
		t.Setenv("VULOS_ENV", "prod")
		if _, err := LoadUnverifiedRegistry(qPath); !errors.Is(err, ErrUnverifiedRegistry) {
			t.Fatalf("the quarantine was readable under VULOS_ENV=prod: err=%v", err)
		}
	})

	t.Run("refused when VULOS_ENV is unset (defaults to prod)", func(t *testing.T) {
		t.Setenv("VULOS_ENV", "")
		if _, err := LoadUnverifiedRegistry(qPath); err == nil {
			t.Fatal("the quarantine was readable with VULOS_ENV unset — the default must be closed")
		}
	})

	t.Run("explicit opt-in in dev", func(t *testing.T) {
		t.Setenv("VULOS_ENV", "dev")
		reg, err := LoadUnverifiedRegistry(qPath)
		if err != nil {
			t.Fatalf("explicit opt-in in dev failed: %v", err)
		}
		if len(reg.Apps) == 0 {
			t.Fatal("opt-in returned no entries")
		}
		for id, entry := range reg.Apps {
			if entry.Signature != "" {
				t.Errorf("quarantined entry %q came back with a signature", id)
			}
		}
	})

	t.Run("refuses a signed entry", func(t *testing.T) {
		t.Setenv("VULOS_ENV", "dev")
		p := filepath.Join(t.TempDir(), UnverifiedRegistryFile)
		body := []byte(`{"_unverified": true, "apps": {"x": {"name": "X", "signature": "AAAA"}}}`)
		if err := os.WriteFile(p, body, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadUnverifiedRegistry(p); !errors.Is(err, ErrUnverifiedRegistry) {
			t.Fatalf("a signed entry in the quarantine was accepted: err=%v", err)
		}
	})

	t.Run("refuses a file that is not quarantined", func(t *testing.T) {
		t.Setenv("VULOS_ENV", "dev")
		p := filepath.Join(t.TempDir(), "registry.json")
		if err := os.WriteFile(p, []byte(`{"apps": {}}`), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadUnverifiedRegistry(p); err == nil {
			t.Fatal("LoadUnverifiedRegistry accepted an ordinary registry — the opt-in must be narrow")
		}
	})
}
