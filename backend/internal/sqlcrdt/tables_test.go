package sqlcrdt

import (
	"database/sql"
	"path/filepath"
	"sort"
	"testing"

	"vulos/backend/internal/crdtsync"
)

// TestPolicyAndWiringAgree is the guard that keeps the two halves of "which
// data syncs" from drifting apart. A domain approved in crdtsync/policy.go but
// never bound to a table here would be a replication that carries nothing; a
// table bound here but not approved there would replicate without a decision.
func TestPolicyAndWiringAgree(t *testing.T) {
	approved := crdtsync.SyncableDomains()
	var wired []string
	for _, rt := range ReplicatedTables() {
		wired = append(wired, rt.Domain)
	}
	sort.Strings(approved)
	sort.Strings(wired)

	if len(approved) != len(wired) {
		t.Fatalf("policy approves %v but wiring binds %v", approved, wired)
	}
	for i := range approved {
		if approved[i] != wired[i] {
			t.Fatalf("policy approves %v but wiring binds %v", approved, wired)
		}
	}
}

// TestDomainNamesMatchTheHelper pins the "sql:<table>" convention that
// crdtsync's constants and this package's Domain() both depend on. If they ever
// disagree, the engine would replicate under one name and the bridge read under
// another, and nothing would converge while everything looked wired.
func TestDomainNamesMatchTheHelper(t *testing.T) {
	for _, rt := range ReplicatedTables() {
		if got := Domain(rt.Spec.Name); got != rt.Domain {
			t.Errorf("table %q: Domain() = %q but wiring declares %q", rt.Spec.Name, got, rt.Domain)
		}
		if _, ok := crdtsync.DecisionFor(rt.Domain); !ok {
			t.Errorf("%s has no recorded policy decision", rt.Domain)
		}
	}
}

// TestReplicatedTablesDeclareColumnsExplicitly enforces the allow-list shape:
// an empty Columns list means "everything except Exclude", which fails open the
// moment somebody adds a column.
func TestReplicatedTablesDeclareColumnsExplicitly(t *testing.T) {
	for _, rt := range ReplicatedTables() {
		if len(rt.Spec.Columns) == 0 {
			t.Errorf("%s: Columns is empty — a column added later would replicate without anyone deciding", rt.Domain)
		}
		if rt.Why == "" {
			t.Errorf("%s: no Why recorded", rt.Domain)
		}
		if rt.DBFile == "" {
			t.Errorf("%s: no DBFile recorded", rt.Domain)
		}
	}
}

// TestRemindersColumnsMatchTheRealSchema is the check that would have caught a
// column-name typo or a schema change: it builds the REAL reminders schema and
// asserts every declared column exists, and that no column of the real table is
// silently left out of the declaration without being noticed.
func TestRemindersColumnsMatchTheRealSchema(t *testing.T) {
	// The schema as services/assistant/reminders_store.go creates it.
	const schema = `CREATE TABLE IF NOT EXISTS reminders (
	    id         TEXT PRIMARY KEY,
	    user_id    TEXT NOT NULL,
	    text       TEXT NOT NULL,
	    remind_at  INTEGER NOT NULL,
	    created_at INTEGER NOT NULL,
	    done       INTEGER NOT NULL DEFAULT 0
	);`
	dir := t.TempDir()
	path := filepath.Join(dir, "reminders.db")
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}

	var rt ReplicatedTable
	for _, c := range ReplicatedTables() {
		if c.Spec.Name == "reminders" {
			rt = c
		}
	}
	if rt.Domain == "" {
		t.Fatal("reminders is not in ReplicatedTables")
	}

	rows, err := db.Query("PRAGMA table_info(reminders)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	real := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		real[name] = true
	}
	for _, c := range rt.Spec.Columns {
		if !real[c] {
			t.Errorf("declared column %q does not exist in the real reminders table", c)
		}
	}
	for c := range real {
		found := false
		for _, d := range rt.Spec.Columns {
			if d == c {
				found = true
			}
		}
		if !found {
			t.Errorf("real column %q is not declared — decide about it explicitly (replicate or Exclude)", c)
		}
	}
}

func TestLiveDBPath(t *testing.T) {
	rt := ReplicatedTable{DBFile: "reminders.db"}
	if got := rt.LiveDBPath("/var/db"); got != "/var/db/reminders.db" {
		t.Errorf("LiveDBPath = %q", got)
	}
	if got := rt.LiveDBPath("/var/db/"); got != "/var/db/reminders.db" {
		t.Errorf("LiveDBPath with trailing slash = %q", got)
	}
	if got := rt.LiveDBPath(""); got != "reminders.db" {
		t.Errorf("LiveDBPath with empty dir = %q", got)
	}
}
