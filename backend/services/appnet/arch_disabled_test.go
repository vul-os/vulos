package appnet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// DISABLED-01. `_disabled` refuses an install in validateRecipeSecurity before
// a byte is fetched, on every box and every architecture — but the listing's
// `installable` was the ARCHITECTURE verdict alone, so a withdrawn entry was
// offered with an Install button that could only ever return "this version
// entry is disabled and cannot be installed".

func TestEvaluate_AWithdrawnEntryIsNotOffered(t *testing.T) {
	env := archEnvironment{supported: []string{"arm64"}, trustKey: nil}

	cases := []struct {
		name string
		json string
	}{
		{
			name: "disabled at the entry level, the shape `steam` and `wine` use",
			json: `{"name":"Steam","_disabled":true,"arch":["amd64","arm64"],
			        "versions":{"latest":{"flatpak_id":"com.valvesoftware.Steam","command":"x"}}}`,
		},
		{
			name: "disabled on the recipe only, the shape `excalidraw` uses",
			json: `{"name":"Excalidraw","arch":["amd64","arm64"],
			        "versions":{"0.18.0":{"_disabled":true,"download_url":"https://e.test/x.tgz","command":"x"}}}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var entry RegistryEntry
			if err := json.Unmarshal([]byte(c.json), &entry); err != nil {
				t.Fatalf("fixture did not parse: %v", err)
			}
			av := env.evaluate("app", &entry)
			if av.Installable {
				t.Fatal("a withdrawn entry reports installable — the hub offers an Install button " +
					"for something the installer is built to refuse")
			}
			if av.Detail == "" {
				t.Error("no reason given; an app that vanishes from availability without a sentence " +
					"produces \"why can't I install this?\"")
			}
			// The wording rules this verdict is held to, asserted positively:
			// a negative list is satisfied by a sentence that says nothing.
			if !strings.Contains(av.Detail, "not offered for install") {
				t.Errorf("the reason does not say the entry is withdrawn: %q", av.Detail)
			}
			if !strings.Contains(av.Detail, "no box installs it") {
				t.Errorf("the reason does not say this holds on EVERY box, so the reader is left "+
					"to conclude their machine is the problem: %q", av.Detail)
			}
			if strings.Contains(strings.ToLower(av.Detail), "architecture") {
				t.Errorf("the withdrawal talks about architecture, which does not decide it: %q", av.Detail)
			}
			if av.RequiresEmulation {
				t.Error("a withdrawn entry claims to require emulation — it requires nothing, it is not offered")
			}
		})
	}
}

// TestEvaluate_WithdrawalOutranksTheArchVerdict. The two answers must not
// compete: an entry that is BOTH withdrawn and installable on this arch has to
// read as withdrawn, or the hub shows an Install button with a reason attached.
func TestEvaluate_WithdrawalOutranksTheArchVerdict(t *testing.T) {
	// The enabled fixture has to actually REACH installable, or the comparison
	// below is two falses agreeing for unrelated reasons — the signature hold
	// would otherwise refuse both and the test would pass over an inert flag.
	withInsecureRegistry(t)
	env := archEnvironment{supported: []string{"arm64"}}
	var live, dead RegistryEntry
	body := `"arch":["arm64"],"versions":{"latest":{"flatpak_id":"org.example.App","command":"x"}}`
	if err := json.Unmarshal([]byte(`{"name":"App",`+body+`}`), &live); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"name":"App","_disabled":true,`+body+`}`), &dead); err != nil {
		t.Fatal(err)
	}
	liveAv := env.evaluate("app", &live)
	deadAv := env.evaluate("app", &dead)
	if liveAv.Installable == deadAv.Installable {
		t.Fatalf("_disabled changed nothing: enabled installable=%v, withdrawn installable=%v — "+
			"this test would pass over an inert flag", liveAv.Installable, deadAv.Installable)
	}
	if deadAv.CardBadge == "" {
		t.Error("no card badge on a withdrawn entry, so the catalogue grid shows nothing to " +
			"distinguish it from an app that installs")
	}
}

// TestListEntries_WithdrawnEntriesCarryTheirReason walks the SHIPPED registry.
// A unit test over a fixture proves the branch works; this proves it reaches
// the entries that actually have the flag, which is the half that was missing.
func TestListEntries_WithdrawnEntriesCarryTheirReason(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "registry.json"))
	if err != nil {
		t.Fatalf("cannot read registry.json: %v", err)
	}
	var reg Registry
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatalf("registry.json did not parse: %v", err)
	}
	if len(reg.Apps) < registryTotalFloor {
		t.Fatalf("examined %d entries, floor is %d — a shrinking registry would make this gate "+
			"pass by looking at nothing", len(reg.Apps), registryTotalFloor)
	}
	env := archEnvironment{supported: []string{"arm64"}}
	withdrawn := 0
	for id, entry := range reg.Apps {
		if !entryWithdrawn(entry) {
			continue
		}
		withdrawn++
		av := env.evaluate(id, entry)
		if av.Installable {
			t.Errorf("%s is _disabled but the listing reports it installable", id)
		}
		if !strings.Contains(av.Detail, "not offered for install") {
			t.Errorf("%s is _disabled but its reason does not say so: %q", id, av.Detail)
		}
	}
	// The shipped catalogue carries these; a count of zero would mean the flag
	// is being read from the wrong place and every assertion above was skipped.
	if withdrawn == 0 {
		t.Fatal("no withdrawn entries found in registry.json — entryWithdrawn is reading the " +
			"wrong field and this gate examined nothing")
	}
	t.Logf("%d of %d shipped entries are withdrawn and now say so", withdrawn, len(reg.Apps))
}
