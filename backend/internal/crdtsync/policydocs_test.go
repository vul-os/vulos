package crdtsync

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The policy table and the document that describes it must agree.
//
// # Why this exists
//
// This has now gone wrong twice in one day, in both directions:
//
//   - `profiles` was refused because its row mixed AIAPIKey and PinHash with
//     Theme and Locale. That blob was split at the storage layer, the reason
//     stopped being true, and the refusal stayed — so the domain users most
//     expect to replicate sat switched off for a condition that no longer held.
//   - Then four domains began replicating and roadmap/SYNC.md still said one,
//     while docs/ARCHITECTURE.md still called profiles unsafe.
//
// Both were found by reading, not by any check. Nothing in the suite compares
// what the code decides with what the documentation claims, so the two drift
// silently and the drift is only ever noticed by someone who happens to look.
//
// # What this does and does not assert
//
// It asserts COVERAGE and VERDICT: every domain the policy knows about appears
// in the document, and a domain the policy syncs is not described there as
// refused (or vice versa). It deliberately does NOT try to check the prose
// reasoning — that would be a brittle string match on English, and the value is
// in catching a domain that silently changed sides, not in policing wording.
//
// A missing document is a hard failure rather than a skip. A skip here would
// reproduce the exact failure mode being closed: a check that quietly stops
// checking.

// syncDocPath is the document this test holds to the policy.
const syncDocPath = "../../../roadmap/SYNC.md"

func readSyncDoc(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(syncDocPath)
	if err != nil {
		t.Fatalf("resolve %s: %v", syncDocPath, err)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("roadmap/SYNC.md is unreadable at %s (%v). It is the document that explains "+
			"which data leaves a box; if it moved, point this test at its new home rather than deleting the check.", abs, err)
	}
	return string(b)
}

func TestEveryPolicyDomainIsDocumented(t *testing.T) {
	doc := readSyncDoc(t)

	for _, d := range Decisions {
		// The doc writes bare table names in backticks ("`profiles`"), not the
		// "sql:" domain prefix, so compare on the table.
		table := strings.TrimPrefix(d.Domain, "sql:")
		if !strings.Contains(doc, "`"+table+"`") {
			t.Errorf("domain %q is in the policy and appears nowhere in roadmap/SYNC.md — "+
				"a decision about what leaves the box that no document records", d.Domain)
		}
	}
}

// A domain's VERDICT must match. This is the half that caught nothing before:
// `profiles` was listed in the document as refused for a whole day after the
// policy started syncing it.
func TestDocumentedVerdictMatchesThePolicy(t *testing.T) {
	doc := readSyncDoc(t)

	// Table rows look like:  | `name` | yes | why |   or   | `a`, `b` | no | why |
	// Capture the row so the verdict column can be read out of it.
	rowRe := regexp.MustCompile(`(?m)^\|([^|]*)\|([^|]*)\|`)
	rows := rowRe.FindAllStringSubmatch(doc, -1)
	if len(rows) < 5 {
		t.Fatalf("found %d table rows in roadmap/SYNC.md — the domain table's shape changed, "+
			"so this check is no longer reading it. Fix the parser rather than deleting the test.", len(rows))
	}

	verdictFor := map[string]string{}
	for _, r := range rows {
		names, verdict := r[1], strings.ToLower(strings.TrimSpace(r[2]))
		for _, m := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(names, -1) {
			verdictFor[m[1]] = verdict
		}
	}

	checked := 0
	for _, d := range Decisions {
		table := strings.TrimPrefix(d.Domain, "sql:")
		verdict, ok := verdictFor[table]
		if !ok {
			continue // coverage is the other test's job; this one only judges rows it found
		}
		checked++

		// "yes" (however emphasised) means it replicates; anything else must not.
		docSays := strings.Contains(verdict, "yes")
		if docSays != d.Sync {
			t.Errorf("roadmap/SYNC.md and the policy disagree about %q: the document says %q, the code says Sync=%v.\n"+
				"One of them changed and the other did not — and when they disagree, the code is what actually leaves the box.",
				d.Domain, strings.TrimSpace(verdict), d.Sync)
		}
	}

	// Without this, a parser that matched no rows would pass silently — the
	// shape of failure this whole file exists to prevent.
	if checked < 5 {
		t.Fatalf("only %d domains were actually compared; the table parser is matching almost nothing "+
			"and this test is passing vacuously", checked)
	}
	t.Logf("compared %d documented domains against the policy", checked)
}

// A domain that syncs must say what it is. This catches the reverse of the
// profiles case: a domain switched ON without anyone writing down why, which is
// how an allow-list quietly becomes a deny-list.
func TestApprovedDomainsCarryAReason(t *testing.T) {
	for _, d := range Decisions {
		if !d.Sync {
			continue
		}
		if len(strings.TrimSpace(d.Reason)) < 40 {
			t.Errorf("%q replicates with a reason of %d characters. Approving a domain is a decision about "+
				"what leaves someone's machine; it needs an argument, not a label.",
				d.Domain, len(strings.TrimSpace(d.Reason)))
		}
	}
}

func TestPolicyHasNoDuplicateDomains(t *testing.T) {
	// Two entries for one domain means DecisionFor returns whichever comes
	// first, and the other is dead text that still reads as authoritative.
	seen := map[string]bool{}
	for _, d := range Decisions {
		if seen[d.Domain] {
			t.Errorf("duplicate policy entry for %q — DecisionFor returns only the first, so the second is "+
				"documentation that looks binding and is not", d.Domain)
		}
		seen[d.Domain] = true
	}
	if len(seen) != len(Decisions) {
		t.Errorf("%d entries, %d distinct domains", len(Decisions), len(seen))
	}
	fmt.Fprintf(os.Stderr, "")
}
