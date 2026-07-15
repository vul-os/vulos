package files

// migrate_equiv_test.go — SCHEMA-EQUIVALENCE PROOF for the Files migration fold.
//
// The Files control-plane schema was folded from the incremental 0001–0003
// migration chain into a single 0001_initial.sql (commit "fold 0001–0003
// migrations into one clean initial schema"). This test is the durable,
// re-runnable proof that the fold did not change the schema: it applies the
// ORIGINAL incremental chain (preserved verbatim under testdata/legacy/) to one
// database and the CURRENT folded baseline to another, then asserts the two
// effective schemas fingerprint identically.
//
// If a future edit to 0001_initial.sql ever diverges from the historical shape,
// this test fails — the fold is pinned forever.

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	dbmigrate "vulos/backend/internal/migrate"

	_ "modernc.org/sqlite"
)

func equivOpenDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		t.Fatalf("ping %s: %v", name, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// applyLegacyChain applies every *.sql file in testdata/legacy in ascending
// filename order, exactly the way the pre-fold runner did (raw Exec, in order).
func applyLegacyChain(t *testing.T, db *sql.DB) {
	t.Helper()
	dir := filepath.Join("testdata", "legacy")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read legacy dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		body, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatalf("read %s: %v", n, err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("exec legacy %s: %v", n, err)
		}
	}
}

// TestMigrationFold_SchemaEquivalent proves the folded 0001_initial.sql produces
// a byte-identical effective schema to the original 0001–0003 chain.
func TestMigrationFold_SchemaEquivalent(t *testing.T) {
	// Old: apply the original incremental chain.
	oldDB := equivOpenDB(t, "old")
	applyLegacyChain(t, oldDB)
	oldFP, err := dbmigrate.SchemaFingerprint(oldDB)
	if err != nil {
		t.Fatalf("fingerprint old: %v", err)
	}

	// New: apply the current folded baseline via the shared runner.
	newDB := equivOpenDB(t, "new")
	if err := dbmigrate.Apply(newDB, migrationsFS, "migrations"); err != nil {
		t.Fatalf("apply folded baseline: %v", err)
	}
	newFP, err := dbmigrate.SchemaFingerprint(newDB)
	if err != nil {
		t.Fatalf("fingerprint new: %v", err)
	}

	if oldFP != newFP {
		t.Errorf("Files folded schema differs from the original 0001–0003 chain:\n--- legacy chain ---\n%s\n--- folded baseline ---\n%s", oldFP, newFP)
	}
}
