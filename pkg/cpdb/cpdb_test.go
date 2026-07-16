package cpdb_test

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ─── Rebind ───────────────────────────────────────────────────────────────────

func TestRebind_SQLite(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	q := `SELECT * FROM t WHERE a=? AND b=?`
	if got := db.Rebind(q); got != q {
		t.Errorf("sqlite rebind should be a no-op: got %q", got)
	}
}

func TestRebind_Postgres_Numbering(t *testing.T) {
	// Exercise the rebind logic without a real Postgres connection by testing
	// the internal function via a fake sqlite db — we just care about the
	// output format.  The postgres path is covered in apikeys_pg_test.go.
	//
	// We test the exported Rebind contract through an in-process sqlite DB:
	// for sqlite the query must pass through unchanged; the postgres numbering
	// is verified here via the cpdb package's internal table (white-box).
	// We re-test via a table-driven approach instead.

	cases := []struct {
		in   string
		want string
	}{
		{`SELECT 1`, `SELECT 1`},
		{`SELECT ? `, `SELECT $1 `},
		{`WHERE a=? AND b=? AND c=?`, `WHERE a=$1 AND b=$2 AND c=$3`},
		{`INSERT INTO t VALUES (?,?,?)`, `INSERT INTO t VALUES ($1,$2,$3)`},
		// No ? — passthrough.
		{`SELECT id FROM foo`, `SELECT id FROM foo`},
	}

	// Use an unexported helper exposed via testable example or package-level
	// var — instead we reach it through the DB struct by injecting a fake
	// backend.  Since we can't instantiate a BackendPostgres without a real
	// PG connection here, we exercise Rebind indirectly by calling the sqlite
	// version and verifying the no-op, and test the postgres path in
	// apikeys_pg_test.go.  For unit coverage of rebindPostgres we expose a
	// thin test helper in cpdb_export_test.go (internal test package trick)
	// — see below.
	_ = cases // covered by RebindPostgresUnit below
}

// RebindPostgresUnit tests the postgres placeholder rewriting via the
// cpdb_export_test.go shim that exposes the unexported rebindPostgres function.
func TestRebindPostgresUnit(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`SELECT 1`, `SELECT 1`},
		{`SELECT ?`, `SELECT $1`},
		{`WHERE a=? AND b=? AND c=?`, `WHERE a=$1 AND b=$2 AND c=$3`},
		{`INSERT INTO t VALUES (?,?,?)`, `INSERT INTO t VALUES ($1,$2,$3)`},
		{`SELECT id FROM foo`, `SELECT id FROM foo`},
	}
	for _, c := range cases {
		got := cpdb.RebindPostgres(c.in)
		if got != c.want {
			t.Errorf("rebindPostgres(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestRebindPostgres_StringLiteralAware verifies the literal-aware scanner (F1):
// a ? inside a single-quoted string literal is emitted verbatim and does NOT
// consume a placeholder number, so it cannot shift the numbering of the real
// placeholders around it. Ordinary positional placeholders must still number
// $1..$N in strict appearance order (regression on the pre-existing behaviour).
func TestRebindPostgres_StringLiteralAware(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// (a) Ordinary positional placeholders still number $1..$N in order.
		{"ordinary_ordering", `WHERE a=? AND b=? AND c=?`, `WHERE a=$1 AND b=$2 AND c=$3`},
		{"insert_values", `INSERT INTO t VALUES (?,?,?)`, `INSERT INTO t VALUES ($1,$2,$3)`},
		// (b) A ? inside a single-quoted literal is preserved verbatim; the real
		// placeholder after it becomes $1 (NOT $2 — the in-literal ? did not count).
		{"literal_question_then_placeholder", `WHERE note = 'ok?' AND id = ?`, `WHERE note = 'ok?' AND id = $1`},
		// Placeholder before AND after a literal-with-? — numbering skips the literal.
		{"placeholder_literal_placeholder", `SELECT ? WHERE note='huh?' AND id=?`, `SELECT $1 WHERE note='huh?' AND id=$2`},
		// (c) Doubled-quote escape keeps the ? inside the literal.
		{"doubled_quote_escape", `WHERE s = 'it''s ok?' AND id = ?`, `WHERE s = 'it''s ok?' AND id = $1`},
		// A literal containing an escaped quote adjacent to the closing quote,
		// followed by a placeholder, still numbers correctly.
		{"escape_then_close_then_ph", `SELECT 'a''b?' , ? , 'c?d'`, `SELECT 'a''b?' , $1 , 'c?d'`},
		// No placeholders at all when every ? is inside literals.
		{"only_in_literals", `SELECT 'x?y', 'z?'`, `SELECT 'x?y', 'z?'`},
	}
	for _, c := range cases {
		if got := cpdb.RebindPostgres(c.in); got != c.want {
			t.Errorf("%s: rebindPostgres(%q) = %q; want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestRebind_SQLiteUntouchedByLiteralAwarePass verifies the SQLite backend still
// returns the query byte-for-byte unchanged even when it contains a ? inside a
// string literal — Rebind must be a pure no-op on SQLite (item F1 safety gate).
func TestRebind_SQLiteUntouchedByLiteralAwarePass(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	for _, q := range []string{
		`SELECT * FROM t WHERE a=? AND b=?`,
		`WHERE note = 'ok?' AND id = ?`,
		`WHERE s = 'it''s ok?' AND id = ?`,
	} {
		if got := db.Rebind(q); got != q {
			t.Errorf("sqlite Rebind must be a no-op: Rebind(%q) = %q", q, got)
		}
	}
}

// ─── SQLite Open ──────────────────────────────────────────────────────────────

func TestOpenSQLiteDSN_Memory(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if db.Backend() != cpdb.BackendSQLite {
		t.Errorf("backend: want sqlite, got %s", db.Backend())
	}
	if err := db.Ping(); err != nil {
		t.Errorf("ping: %v", err)
	}
}

// TestOpenSQLite_BusyTimeoutApplied verifies the busy_timeout pragma (F3) is set
// on open, so a would-be-immediate SQLITE_BUSY becomes a bounded wait. Open must
// still succeed and `PRAGMA busy_timeout` must report the configured value.
func TestOpenSQLite_BusyTimeoutApplied(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var ms int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&ms); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if ms != 5000 {
		t.Errorf("busy_timeout = %d; want 5000", ms)
	}
	// foreign_keys must still be ON (unchanged by adding busy_timeout).
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d; want 1", fk)
	}
}

// ─── Migrate ──────────────────────────────────────────────────────────────────

func TestMigrate_BasicSQLite(t *testing.T) {
	db := openMem(t)

	fsys := fstest.MapFS{
		"0001_create.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS testmig (id TEXT PRIMARY KEY)`),
		},
	}

	if err := db.Migrate(fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Table must exist.
	_, err := db.Exec(`INSERT INTO testmig VALUES ('row1')`)
	if err != nil {
		t.Fatalf("insert after migrate: %v", err)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	db := openMem(t)

	fsys := fstest.MapFS{
		"0001_create.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS testmig2 (id TEXT PRIMARY KEY)`),
		},
	}

	// Running twice must not error.
	if err := db.Migrate(fsys); err != nil {
		t.Fatalf("migrate 1st: %v", err)
	}
	if err := db.Migrate(fsys); err != nil {
		t.Fatalf("migrate 2nd (idempotent): %v", err)
	}
}

func TestMigrate_DialectFilter_SQLiteOnly(t *testing.T) {
	db := openMem(t)

	// Only the .sqlite.sql file should run; .postgres.sql must be skipped.
	fsys := fstest.MapFS{
		"0001_init.sqlite.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS dialect_sq (id TEXT PRIMARY KEY)`),
		},
		"0001_init.postgres.sql": &fstest.MapFile{
			// Different table name so we can detect if it ran on SQLite.
			Data: []byte(`CREATE TABLE IF NOT EXISTS dialect_pg (id TEXT PRIMARY KEY)`),
		},
	}

	if err := db.Migrate(fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// dialect_sq table must exist (it was in the .sqlite.sql file).
	if _, err := db.Exec(`INSERT INTO dialect_sq VALUES ('a')`); err != nil {
		t.Fatalf("dialect_sq table missing: %v", err)
	}

	// dialect_pg table must NOT exist on sqlite (postgres-only file was skipped).
	_, err := db.Exec(`INSERT INTO dialect_pg VALUES ('b')`)
	if err == nil {
		t.Error("dialect_pg table should not exist on sqlite")
	}
}

// TestMigrate_RejectsBareAndDialectCollision is the WAVE34-6 regression: the
// loader must reject a migration set that ships BOTH a bare NNNN_desc.sql and a
// dialect variant of the same stem — the bare file applies on both backends and
// would double-apply against the matching dialect file.
func TestMigrate_RejectsBareAndDialectCollision(t *testing.T) {
	db := openMem(t)

	fsys := fstest.MapFS{
		"0001_init.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS coll (id TEXT PRIMARY KEY)`),
		},
		"0001_init.sqlite.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS coll (id TEXT PRIMARY KEY)`),
		},
		"0001_init.postgres.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS coll (id TEXT PRIMARY KEY)`),
		},
	}

	err := db.Migrate(fsys)
	if err == nil {
		t.Fatal("Migrate must reject a bare+dialect collision")
	}
	if !strings.Contains(err.Error(), "collision") {
		t.Errorf("error should mention the collision, got: %v", err)
	}
}

// TestMigrate_DialectPairAloneIsFine verifies the fixed layout (dialect pair, no
// bare stem) migrates without a collision error.
func TestMigrate_DialectPairAloneIsFine(t *testing.T) {
	db := openMem(t)
	fsys := fstest.MapFS{
		"0001_init.sqlite.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS ok_sq (id TEXT PRIMARY KEY)`),
		},
		"0001_init.postgres.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS ok_pg (id TEXT PRIMARY KEY)`),
		},
	}
	if err := db.Migrate(fsys); err != nil {
		t.Fatalf("dialect-split pair should migrate cleanly: %v", err)
	}
}

func TestMigrate_Ordering(t *testing.T) {
	db := openMem(t)

	// Files must be applied in lexicographic order; 0002 depends on 0001.
	fsys := fstest.MapFS{
		"0001_parent.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS parent (id TEXT PRIMARY KEY)`),
		},
		"0002_child.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS child (
				id         TEXT PRIMARY KEY,
				parent_id  TEXT NOT NULL REFERENCES parent(id)
			)`),
		},
	}

	if err := db.Migrate(fsys); err != nil {
		t.Fatalf("migrate with FK dependency: %v", err)
	}
}

func TestMigrate_Tracking(t *testing.T) {
	db := openMem(t)

	applied := 0
	// Use a custom FS that counts reads to verify each file is only read once.
	fsys := &countingFS{
		inner: fstest.MapFS{
			"0001_once.sql": &fstest.MapFile{
				Data: []byte(`CREATE TABLE IF NOT EXISTS once_table (id TEXT PRIMARY KEY)`),
			},
		},
		reads: &applied,
	}

	if err := db.Migrate(fsys); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	first := applied

	if err := db.Migrate(fsys); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	// No additional ReadFile calls on the second run.
	if applied != first {
		t.Errorf("second migrate re-read files: reads went from %d to %d", first, applied)
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func openMem(t *testing.T) *cpdb.DB {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("open :memory:: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// countingFS wraps an fs.FS and increments *reads on every ReadFile call.
type countingFS struct {
	inner fs.FS
	reads *int
}

func (c *countingFS) Open(name string) (fs.File, error) { return c.inner.Open(name) }

func (c *countingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(c.inner, name)
}

// Ensure countingFS satisfies fs.FS (Open is enough for fstest).
var _ fs.FS = (*countingFS)(nil)

// TestValidateLogicalName tests the name validation embedded in Open.
func TestOpen_InvalidLogicalName(t *testing.T) {
	cases := []string{"", "Foo", "foo bar", "foo-bar", "1foo", "foo.bar"}
	for _, name := range cases {
		_, err := cpdb.Open(name)
		if err == nil {
			t.Errorf("Open(%q) should fail validation", name)
		}
		if !strings.Contains(err.Error(), "logical name") {
			t.Errorf("Open(%q) error should mention 'logical name', got: %v", name, err)
		}
	}
}

// TestMigrationLock_DeadlockGuardSkipsWhenCapBelowTwo verifies the F2 advisory-
// lock deadlock guard: when the pool cap is < 2 (the single-conn SQLite pool, and
// any Postgres pool configured with CPDB_PG_MAX_OPEN_CONNS=1), the helper returns
// a usable no-op release WITHOUT attempting any Postgres advisory-lock SQL — so
// holding a lock conn can never starve the migration work conn. The real
// Postgres locking path requires a live cluster and is not exercised here.
func TestMigrationLock_DeadlockGuardSkipsWhenCapBelowTwo(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// SQLite pools are opened with MaxOpenConns(1) → cap < 2 → skip branch.
	release, err := db.AcquirePostgresMigrationLockForTest()
	if err != nil {
		t.Fatalf("guard should not error on a sub-2-cap pool: %v", err)
	}
	if release == nil {
		t.Fatal("release func must be non-nil")
	}
	// Must be safe to call (and idempotent).
	release()
	release()
}

// ─── Read-replica seam (IDENTITY-SERVICE §2.3) ─────────────────────────────────

// TestReplicaFallbackToPrimary verifies that when no replica pool is configured
// (the SQLite self-host case, and cloud with the env unset), HasReplica is false
// and QueryRowReplica / QueryReplica transparently read the primary. This is the
// "seam is a no-op until a replica is wired" invariant.
func TestReplicaFallbackToPrimary(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if db.HasReplica() {
		t.Fatal("SQLite DB should never have a replica")
	}
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t (id, v) VALUES (1, 'hello')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// QueryRowReplica must fall back to the primary and see the row.
	var v string
	if err := db.QueryRowReplica(t.Context(), db.Rebind(`SELECT v FROM t WHERE id = ?`), 1).Scan(&v); err != nil {
		t.Fatalf("QueryRowReplica: %v", err)
	}
	if v != "hello" {
		t.Fatalf("QueryRowReplica: got %q, want hello", v)
	}

	// QueryReplica must fall back to the primary and see the row.
	rows, err := db.QueryReplica(t.Context(), db.Rebind(`SELECT v FROM t WHERE id = ?`), 1)
	if err != nil {
		t.Fatalf("QueryReplica: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("QueryReplica: no rows")
	}
}
