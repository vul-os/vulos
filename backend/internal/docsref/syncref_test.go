package docsref

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"vulos/backend/internal/sqlcrdt"
)

// roadmap/SYNC.md's account of WHICH TABLES REPLICATE must match the code.
//
// That document is not decoration. It is where the decision about each table is
// recorded — what syncs, what does not, and the reason — and a reader deciding
// whether to trust this box with their account has nothing else to go on. A
// table that replicates without appearing there is an undocumented data flow.
//
// The drift this was written for was already present. SYNC.md's own two-tier
// table described the hot path as "real, one domain, LAN only — wired for
// reminders" while the prose four lines above it correctly said users, profiles,
// reminders and acctsec_sensitive_actions all replicate. One document, two
// answers, and the wrong one was the summary — the line a hurried reader takes.

const syncDoc = "roadmap/SYNC.md"

func readSyncDoc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, syncDoc))
	if err != nil {
		t.Fatalf("read %s: %v", syncDoc, err)
	}
	body := string(b)
	if len(body) < 2000 {
		t.Fatalf("%s is %d bytes; too short to be the document these checks describe",
			syncDoc, len(body))
	}
	return body
}

// domainRow finds the "| `name` | … |" row for a table in the domain table.
func domainRow(body, table string) (string, bool) {
	re := regexp.MustCompile("(?m)^\\|[^|\n]*`" + regexp.QuoteMeta(table) + "`[^|\n]*\\|([^\n]*)$")
	m := re.FindStringSubmatch(body)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func TestSyncDocListsEveryReplicatedTable(t *testing.T) {
	body := readSyncDoc(t)

	tables := sqlcrdt.ReplicatedTables()
	if len(tables) < 3 {
		t.Fatalf("only %d replicated tables were read from the code; the checks below "+
			"would pass by having almost nothing to check", len(tables))
	}

	for _, rt := range tables {
		name := rt.Spec.Name
		rest, ok := domainRow(body, name)
		if !ok {
			t.Errorf("table %q replicates between boxes and has no row in %s's domain table. "+
				"A data flow off the machine that is not written down is the one nobody "+
				"reviewed", name, syncDoc)
			continue
		}
		// The row must say it syncs. A table listed with "no" while the code
		// replicates it is worse than an absent row: it is a denial.
		verdict := strings.ToLower(strings.SplitN(rest, "|", 2)[0])
		if !strings.Contains(verdict, "yes") {
			t.Errorf("%s says %q does not sync (%q), but sqlcrdt.ReplicatedTables() replicates it",
				syncDoc, name, strings.TrimSpace(verdict))
		}
	}
}

// The reason column is the whole point of the table — an allow-list entry with
// no argument behind it is just a list. Each replicated table must carry a
// substantive justification, not a placeholder.
func TestSyncDocGivesAReasonForEachReplicatedTable(t *testing.T) {
	body := readSyncDoc(t)

	for _, rt := range sqlcrdt.ReplicatedTables() {
		name := rt.Spec.Name
		rest, ok := domainRow(body, name)
		if !ok {
			continue // reported by the test above
		}
		parts := strings.SplitN(rest, "|", 2)
		if len(parts) < 2 {
			t.Errorf("%s's row for %q has no reason column", syncDoc, name)
			continue
		}
		why := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[1]), "|"))
		if len(why) < 80 {
			t.Errorf("%s's reason for replicating %q is %d characters (%q). This column is "+
				"where the decision is defended; a stub means nobody made one",
				syncDoc, name, len(why), why)
		}
	}

	// And the code's own Why must not be empty either — ReplicatedTables carries
	// the same justification next to the declaration, which is what someone
	// reading the code sees instead of the document.
	for _, rt := range sqlcrdt.ReplicatedTables() {
		if len(strings.TrimSpace(rt.Why)) < 80 {
			t.Errorf("sqlcrdt.ReplicatedTables()[%q].Why is %d characters; the declaration "+
				"must carry its own reason, not defer to a document the reader may not open",
				rt.Spec.Name, len(strings.TrimSpace(rt.Why)))
		}
	}
}

// The SUMMARY line must not contradict the detail. This is the exact drift that
// prompted these tests: the two-tier table's "Hot" row named one domain while
// four replicated, and a summary is what most readers actually read.
func TestSyncDocHotTierNamesEveryReplicatedTable(t *testing.T) {
	body := readSyncDoc(t)

	var hot string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "| **Hot** |") {
			hot = line
			break
		}
	}
	if hot == "" {
		t.Fatalf("%s has no \"| **Hot** |\" row; this check has lost its subject", syncDoc)
	}

	for _, rt := range sqlcrdt.ReplicatedTables() {
		name := rt.Spec.Name
		if !strings.Contains(hot, "`"+name+"`") {
			t.Errorf("the Hot tier row in %s does not name %q, which replicates over exactly "+
				"that path. The row reads:\n  %s", syncDoc, name, strings.TrimSpace(hot))
		}
	}
}
