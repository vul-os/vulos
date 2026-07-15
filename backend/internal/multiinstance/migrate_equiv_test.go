package multiinstance

// migrate_equiv_test.go — SCHEMA-EQUIVALENCE PROOF for the multi-instance
// registry migration fold.
//
// The registry schema was folded from the incremental 0001–0006 SQL chain PLUS
// the Go-side additive-column shims (ensureObservationEpochColumns,
// ensureInstanceKeyRotationColumns, ensureInstanceRegionColumn — the only
// ALTER TABLE ... ADD COLUMN paths in the store) into a single CREATE-only
// 0001_initial.sql.
//
// This test reproduces the EXACT legacy application — the 0001–0006 SQL files
// (preserved verbatim under testdata/legacy/) followed by the three shims in the
// precise order the pre-fold migrate() ran them — and asserts the resulting
// effective schema fingerprints identically to the current folded baseline.

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

// applyLegacyRegistry reproduces the pre-fold migrate(): apply 0001–0006 in
// order, then run the three additive-column shims in their historical order.
func applyLegacyRegistry(t *testing.T, db *sql.DB) {
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

	// Shim order matches the historical registry.migrate():
	//   1. ensureObservationEpochColumns  (epoch, observer_pubkey, idx)
	//   2. ensureInstanceKeyRotationColumns (prev_ed25519_public_key, prev_key_expires_at, revoked)
	//   3. ensureInstanceRegionColumn       (region)
	shims := []string{
		`ALTER TABLE app_uninstall_observations ADD COLUMN epoch INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE app_uninstall_observations ADD COLUMN observer_pubkey TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_app_uninstall_obs_epoch ON app_uninstall_observations(instance_ulid, app_id, epoch)`,
		`ALTER TABLE instances ADD COLUMN prev_ed25519_public_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE instances ADD COLUMN prev_key_expires_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE instances ADD COLUMN revoked INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE instances ADD COLUMN region TEXT NOT NULL DEFAULT 'eu'`,
	}
	for _, s := range shims {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec legacy shim %q: %v", s, err)
		}
	}
}

// TestMigrationFold_SchemaEquivalent proves the folded 0001_initial.sql produces
// a byte-identical effective schema to the original 0001–0006 chain + shims.
func TestMigrationFold_SchemaEquivalent(t *testing.T) {
	oldDB := equivOpenDB(t, "old")
	applyLegacyRegistry(t, oldDB)
	oldFP, err := dbmigrate.SchemaFingerprint(oldDB)
	if err != nil {
		t.Fatalf("fingerprint old: %v", err)
	}

	newDB := equivOpenDB(t, "new")
	if err := dbmigrate.Apply(newDB, migrationsFS, "migrations"); err != nil {
		t.Fatalf("apply folded baseline: %v", err)
	}
	newFP, err := dbmigrate.SchemaFingerprint(newDB)
	if err != nil {
		t.Fatalf("fingerprint new: %v", err)
	}

	if oldFP != newFP {
		t.Errorf("registry folded schema differs from the original 0001–0006 chain + shims:\n--- legacy ---\n%s\n--- folded ---\n%s", oldFP, newFP)
	}
}
