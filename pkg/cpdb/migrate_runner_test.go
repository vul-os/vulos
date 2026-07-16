// migrate_runner_test.go — fail-closed guarantees of the unified migration
// runner (db.Migrate): tamper-evidence, atomicity (no partial apply), legacy
// checksum backfill, and fingerprint stability across re-runs.
//
// These complement the ordering / idempotency / dialect-filter / collision tests
// in cpdb_test.go. Together they pin the runner's contract: one forward-only,
// version-tracked, transactional, fail-closed, tamper-evident apply in filename
// order, recorded in the _schema_migrations ledger.
package cpdb_test

import (
	"strings"
	"testing"
	"testing/fstest"
)

// TestMigrate_ChecksumTamper_FailClosed is the meetalloc-class regression: once a
// migration file has been applied and its checksum recorded, EDITING that file
// (same filename, different bytes) must be rejected on the next Migrate. A
// modified-in-place applied migration is a coherency hazard — the DB no longer
// matches the source — so the runner fails closed instead of silently drifting.
func TestMigrate_ChecksumTamper_FailClosed(t *testing.T) {
	db := openMem(t)

	orig := fstest.MapFS{
		"0001_tamper.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS tamper_t (id TEXT PRIMARY KEY)`),
		},
	}
	if err := db.Migrate(orig); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}

	// Same filename, different content (an in-place edit).
	tampered := fstest.MapFS{
		"0001_tamper.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS tamper_t (id TEXT PRIMARY KEY, extra TEXT)`),
		},
	}
	err := db.Migrate(tampered)
	if err == nil {
		t.Fatal("Migrate must reject an applied migration that was modified in place")
	}
	if !strings.Contains(err.Error(), "modified after being applied") {
		t.Errorf("error should flag the in-place modification, got: %v", err)
	}
}

// TestMigrate_UnchangedReapply_OK is the companion: re-applying the byte-identical
// file (the normal boot path) must be a clean no-op, not a false tamper trip.
func TestMigrate_UnchangedReapply_OK(t *testing.T) {
	db := openMem(t)
	fsys := fstest.MapFS{
		"0001_stable.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS stable_t (id TEXT PRIMARY KEY)`),
		},
	}
	if err := db.Migrate(fsys); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := db.Migrate(fsys); err != nil {
		t.Fatalf("re-apply of identical file must be a no-op, got: %v", err)
	}
}

// TestMigrate_NoPartialApply_FailClosed verifies atomicity: a migration whose
// SECOND statement fails must leave NO trace — neither the first statement's
// table nor a tracking-ledger row. The whole file applies in one transaction, so
// a mid-file error rolls the entire file back and Migrate returns the error.
func TestMigrate_NoPartialApply_FailClosed(t *testing.T) {
	db := openMem(t)

	fsys := fstest.MapFS{
		"0001_halfbad.sql": &fstest.MapFile{
			// First statement is valid; second is a syntax error.
			Data: []byte(`CREATE TABLE IF NOT EXISTS half_a (id TEXT PRIMARY KEY);
CREATE TABLE half_b (id TEXT PRIMARY KEY) THIS IS NOT SQL;`),
		},
	}

	err := db.Migrate(fsys)
	if err == nil {
		t.Fatal("Migrate must fail on a migration with a broken statement")
	}

	// The first statement's table must NOT survive the rollback.
	if _, qerr := db.Exec(`INSERT INTO half_a VALUES ('x')`); qerr == nil {
		t.Error("half_a table exists — the failed migration was applied partially (no rollback)")
	}

	// The filename must NOT be recorded as applied.
	var n int
	row := db.QueryRow(`SELECT COUNT(*) FROM _schema_migrations WHERE filename = '0001_halfbad.sql'`)
	if err := row.Scan(&n); err != nil {
		t.Fatalf("scan ledger: %v", err)
	}
	if n != 0 {
		t.Errorf("failed migration recorded in ledger (%d rows) — should be 0", n)
	}
}

// TestMigrate_LegacyChecksumBackfill verifies the back-compat path: an applied
// migration whose ledger row predates the checksum column (checksum IS NULL) is
// BACKFILLED with the current checksum rather than rejected — we cannot know its
// original bytes, so we adopt the current file as canonical. A subsequent edit is
// then caught by the tamper guard.
func TestMigrate_LegacyChecksumBackfill(t *testing.T) {
	db := openMem(t)

	fsys := fstest.MapFS{
		"0001_legacy.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS legacy_t (id TEXT PRIMARY KEY)`),
		},
	}
	if err := db.Migrate(fsys); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Simulate a legacy row: blank out the recorded checksum.
	if _, err := db.Exec(`UPDATE _schema_migrations SET checksum = NULL WHERE filename = '0001_legacy.sql'`); err != nil {
		t.Fatalf("null out checksum: %v", err)
	}

	// Re-migrating the SAME bytes must backfill (not reject) and leave a non-empty checksum.
	if err := db.Migrate(fsys); err != nil {
		t.Fatalf("backfill migrate must not error, got: %v", err)
	}
	var sum string
	if err := db.QueryRow(`SELECT COALESCE(checksum,'') FROM _schema_migrations WHERE filename = '0001_legacy.sql'`).Scan(&sum); err != nil {
		t.Fatalf("scan checksum: %v", err)
	}
	if sum == "" {
		t.Error("checksum was not backfilled for the legacy row")
	}
}

// TestMigrate_FingerprintStable_AcrossReruns verifies determinism: applying the
// same file set to two independent fresh DBs yields the identical schema, and a
// second Migrate against an already-migrated DB does not mutate it. This is the
// property the fold-equivalence proof leans on.
func TestMigrate_FingerprintStable_AcrossReruns(t *testing.T) {
	fsys := fstest.MapFS{
		"0002_child.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS fp_child (id TEXT PRIMARY KEY, p TEXT NOT NULL)`),
		},
		"0001_parent.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS fp_parent (id TEXT PRIMARY KEY)`),
		},
	}

	fp := func() string {
		db := openMem(t)
		if err := db.Migrate(fsys); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		// Second apply must be a no-op that does not change the schema.
		if err := db.Migrate(fsys); err != nil {
			t.Fatalf("re-migrate: %v", err)
		}
		return sqliteFingerprint(t, db)
	}

	a, b := fp(), fp()
	if a != b {
		t.Errorf("schema fingerprint not stable across independent applies:\n a=%q\n b=%q", a, b)
	}
	if !strings.Contains(a, "fp_parent") || !strings.Contains(a, "fp_child") {
		t.Errorf("fingerprint missing expected tables: %q", a)
	}
}
