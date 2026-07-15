package migrate

import (
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"
)

// openTestDB opens a fresh temp-file SQLite DB (WAL, single writer) mirroring the
// production DSN. A temp file (not :memory:) is used so multiple connections in a
// single test see the same database.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func fsWith(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, body := range files {
		m["migrations/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

// TestApply_AppliesAllInOrderAndRecords verifies migrations run in ascending
// filename order and every one is recorded exactly once.
func TestApply_AppliesAllInOrderAndRecords(t *testing.T) {
	db := openTestDB(t)
	fsys := fsWith(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id TEXT PRIMARY KEY);`,
		"0002_b.sql": `CREATE TABLE b (id TEXT PRIMARY KEY);`,
		"0003_c.sql": `CREATE INDEX idx_a ON a(id);`,
	})
	if err := Apply(db, fsys, "migrations"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	recs, err := Applied(db)
	if err != nil {
		t.Fatalf("Applied: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("want 3 applied, got %d", len(recs))
	}
	want := []string{"0001_a.sql", "0002_b.sql", "0003_c.sql"}
	for i, r := range recs {
		if r.Version != want[i] {
			t.Errorf("recs[%d].Version = %q, want %q", i, r.Version, want[i])
		}
		if r.Checksum == "" || r.AppliedAt == "" {
			t.Errorf("recs[%d] missing checksum/applied_at: %+v", i, r)
		}
	}
}

// TestApply_Idempotent verifies a second Apply is a no-op (nothing re-run, no
// duplicate bookkeeping rows).
func TestApply_Idempotent(t *testing.T) {
	db := openTestDB(t)
	fsys := fsWith(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id TEXT PRIMARY KEY);`,
	})
	if err := Apply(db, fsys, "migrations"); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if err := Apply(db, fsys, "migrations"); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	recs, _ := Applied(db)
	if len(recs) != 1 {
		t.Fatalf("want 1 applied after two runs, got %d", len(recs))
	}
}

// TestApply_NewMigrationAppliedOnUpgrade simulates a forward upgrade: after the
// baseline is applied, adding a new file applies ONLY the new one.
func TestApply_NewMigrationAppliedOnUpgrade(t *testing.T) {
	db := openTestDB(t)
	base := map[string]string{"0001_a.sql": `CREATE TABLE a (id TEXT PRIMARY KEY);`}
	if err := Apply(db, fsWith(base), "migrations"); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	upgraded := map[string]string{
		"0001_a.sql": `CREATE TABLE a (id TEXT PRIMARY KEY);`,
		"0002_b.sql": `CREATE TABLE b (id TEXT PRIMARY KEY);`,
	}
	if err := Apply(db, fsWith(upgraded), "migrations"); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	recs, _ := Applied(db)
	if len(recs) != 2 {
		t.Fatalf("want 2 applied after upgrade, got %d", len(recs))
	}
	// b must now exist.
	var n string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='b'`).Scan(&n); err != nil {
		t.Fatalf("table b not created by upgrade: %v", err)
	}
}

// TestApply_ChecksumTamperFailsClosed verifies that editing an already-applied
// migration file is rejected (never silently re-applied or ignored).
func TestApply_ChecksumTamperFailsClosed(t *testing.T) {
	db := openTestDB(t)
	if err := Apply(db, fsWith(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id TEXT PRIMARY KEY);`,
	}), "migrations"); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	// Same version, different body → tamper.
	err := Apply(db, fsWith(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id TEXT PRIMARY KEY, extra TEXT);`,
	}), "migrations")
	if err == nil {
		t.Fatal("expected error when an applied migration's checksum changes, got nil")
	}
}

// TestApply_FailClosedNoPartialApply verifies a migration that errors part-way
// leaves NO trace: not recorded, and its earlier statements rolled back.
func TestApply_FailClosedNoPartialApply(t *testing.T) {
	db := openTestDB(t)
	// The migration creates a table then issues invalid SQL. The whole file must
	// roll back atomically.
	err := Apply(db, fsWith(map[string]string{
		"0001_bad.sql": `CREATE TABLE good (id TEXT PRIMARY KEY);
THIS IS NOT VALID SQL;`,
	}), "migrations")
	if err == nil {
		t.Fatal("expected error from invalid migration, got nil")
	}
	// good must NOT exist (rolled back).
	var n string
	qerr := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='good'`).Scan(&n)
	if qerr == nil {
		t.Fatal("partial apply: table 'good' exists after a failed migration — rollback did not happen")
	}
	// Nothing recorded.
	recs, _ := Applied(db)
	if len(recs) != 0 {
		t.Fatalf("failed migration was recorded as applied: %+v", recs)
	}
}

// TestApply_EmptyDirIsError guards against a mis-wired embed (no migrations
// found) silently succeeding.
func TestApply_EmptyDirIsError(t *testing.T) {
	db := openTestDB(t)
	if err := Apply(db, fstest.MapFS{"migrations/readme.txt": &fstest.MapFile{Data: []byte("x")}}, "migrations"); err == nil {
		t.Fatal("expected error when no *.sql migrations are present, got nil")
	}
}

// TestSchemaFingerprint_StableAcrossFormatting verifies the fingerprint ignores
// comments/whitespace but captures effective schema (so it can prove folds).
func TestSchemaFingerprint_StableAcrossFormatting(t *testing.T) {
	db1 := openTestDB(t)
	db2 := openTestDB(t)
	// Same effective schema, wildly different formatting/comments.
	must := func(db *sql.DB, fsys fstest.MapFS) {
		t.Helper()
		if err := Apply(db, fsys, "migrations"); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
	must(db1, fsWith(map[string]string{
		"0001_x.sql": `CREATE TABLE t (id TEXT PRIMARY KEY, n INTEGER NOT NULL DEFAULT 0);
CREATE INDEX idx_t_n ON t(n);`,
	}))
	must(db2, fsWith(map[string]string{
		"0001_a.sql": `-- a comment
CREATE TABLE IF NOT EXISTS t (
    id   TEXT PRIMARY KEY,     -- inline
    n    INTEGER NOT NULL DEFAULT 0
);`,
		"0002_b.sql": `CREATE INDEX IF NOT EXISTS idx_t_n ON t(n);`,
	}))
	f1, err := SchemaFingerprint(db1)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := SchemaFingerprint(db2)
	if err != nil {
		t.Fatal(err)
	}
	if f1 != f2 {
		t.Errorf("fingerprints differ for equivalent schemas:\n--- db1 ---\n%s\n--- db2 ---\n%s", f1, f2)
	}
}

// TestSchemaFingerprint_DetectsDifference is the negative control: a genuinely
// different schema must fingerprint differently (so the equivalence proof has
// teeth).
func TestSchemaFingerprint_DetectsDifference(t *testing.T) {
	db1 := openTestDB(t)
	db2 := openTestDB(t)
	_ = Apply(db1, fsWith(map[string]string{"0001.sql": `CREATE TABLE t (id TEXT PRIMARY KEY);`}), "migrations")
	_ = Apply(db2, fsWith(map[string]string{"0001.sql": `CREATE TABLE t (id TEXT PRIMARY KEY, extra TEXT);`}), "migrations")
	f1, _ := SchemaFingerprint(db1)
	f2, _ := SchemaFingerprint(db2)
	if f1 == f2 {
		t.Fatal("fingerprints matched for different schemas — the check has no teeth")
	}
}
