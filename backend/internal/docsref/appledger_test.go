package docsref

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The App Hub install-verification ledger must only contain rows a real run
// could have produced.
//
// # Why this guard exists
//
// roadmap/app-verification-ledger.json is written by scripts/verify-app-recipe.sh
// after a container has actually installed an app through the product's own
// installer (appnet.InstallFromRegistry).  It is also a plain JSON file in a
// repo where a dozen agents are working at once, and a "passed" row is the
// artefact each of them is asked to produce.  That is precisely the shape that
// invites a hand-written green tick.
//
// This test cannot prove a container ran.  What it CAN do is make a fabricated
// row fail the build:
//
//   - a status outside the four the harness emits;
//   - a "passed" row with no assertions, or missing the two that are the whole
//     point (the product's installer ran; the app's command really executed);
//   - a row for an app id that is not in registry.json;
//   - an "untestable-on-arm64" row with no stated reason — the whole value of
//     that status is that it says WHY, and a reasonless one is a green tick in
//     disguise;
//   - a "passed" row that also carries a failure note.
//
// The ledger is optional: before the first sweep it does not exist, and this
// test skips.  That is deliberate — a missing ledger is an honest "nothing has
// been verified yet", and failing the build for it would push someone to
// create a fake one.
func TestAppVerificationLedgerRowsAreHonest(t *testing.T) {
	ledgerPath := filepath.Join(repoRoot, "roadmap", "app-verification-ledger.json")
	data, err := os.ReadFile(ledgerPath)
	if os.IsNotExist(err) {
		t.Skip("no ledger yet — nothing has been install-verified (roadmap/app-verification-ledger.json)")
	}
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}

	var doc struct {
		Rows []struct {
			ID         string   `json:"id"`
			Status     string   `json:"status"`
			Assertions []string `json:"assertions"`
			Note       string   `json:"note"`
			Date       string   `json:"date"`
			Arch       string   `json:"arch"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("ledger is not valid JSON (%s): %v", ledgerPath, err)
	}

	regData, err := os.ReadFile(shippedRegistryPath(t))
	if err != nil {
		t.Fatalf("read registry.json: %v", err)
	}
	var reg struct {
		Apps map[string]json.RawMessage `json:"apps"`
	}
	if err := json.Unmarshal(regData, &reg); err != nil {
		t.Fatalf("parse registry.json: %v", err)
	}

	// The statuses scripts/verify-app-recipe.sh can write.  Anything else was
	// typed by hand.
	valid := map[string]bool{
		"passed":              true,
		"failed":              true,
		"untestable-on-arm64": true,
		"skipped":             true,
		"disabled":            true,
	}
	// A pass claims two things above all: the product's own install path ran,
	// and the binary the launcher would exec is real.
	required := []string{"install-path"}
	execProof := map[string]bool{"command-executes": true, "command-resolves": true}

	seen := map[string]bool{}
	for _, r := range doc.Rows {
		if r.ID == "" {
			t.Errorf("ledger row with no id")
			continue
		}
		if seen[r.ID] {
			t.Errorf("%s: duplicate ledger row — the harness rewrites in place, so two rows means hand editing", r.ID)
		}
		seen[r.ID] = true

		if _, ok := reg.Apps[r.ID]; !ok {
			t.Errorf("%s: ledger row for an app id that is not in registry.json", r.ID)
		}
		if !valid[r.Status] {
			t.Errorf("%s: status %q is not one the harness emits (passed|failed|untestable-on-arm64|skipped|disabled)", r.ID, r.Status)
		}
		if r.Date == "" {
			t.Errorf("%s: ledger row has no date", r.ID)
		}

		switch r.Status {
		case "passed":
			if len(r.Assertions) == 0 {
				t.Errorf("%s: status=passed with no assertions — a pass is the list of what held, not a tick", r.ID)
			}
			for _, want := range required {
				if !containsStr(r.Assertions, want) {
					t.Errorf("%s: status=passed but %q is not among the assertions — the product's installer is what has to have run", r.ID, want)
				}
			}
			proved := false
			for _, a := range r.Assertions {
				if execProof[a] {
					proved = true
				}
			}
			if !proved {
				t.Errorf("%s: status=passed but nothing proves the app's command is real "+
					"(expected command-resolves and/or command-executes)", r.ID)
			}
			if strings.TrimSpace(r.Note) != "" {
				t.Errorf("%s: status=passed but the row carries a failure note %q — a pass with a complaint in it is not a pass", r.ID, r.Note)
			}
		case "untestable-on-arm64", "skipped", "disabled":
			if strings.TrimSpace(r.Note) == "" {
				t.Errorf("%s: status=%s with no note — this status is only worth anything when it says why", r.ID, r.Status)
			}
			if len(r.Assertions) != 0 {
				t.Errorf("%s: status=%s but it carries assertions — nothing was installed, so nothing was asserted", r.ID, r.Status)
			}
		}
	}
}

func containsStr(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
