// migratefold_test.go — durable schema-equivalence proof for the migration fold.
//
// THE FOLD: several subsystems' incremental migration chains (a folded 0001
// baseline plus later 0002..000N ADD-TABLE steps) were collapsed into a single
// CREATE-only 0001 baseline per dialect, and the intermediate files deleted.
// Because vulos.cloud is EU single-region and UNDEPLOYED (one Neon Postgres, no
// production data), migrations should carry no incremental churn — each
// subsystem's chain collapses to one clean baseline.
//
// THE PROOF (mirrors the OS repo's TestMigrationFold_SchemaEquivalent): for every
// folded subsystem we apply the ORIGINAL pre-fold chain (snapshotted verbatim in
// testdata/legacy/<subsys>/) to a fresh database, and separately apply the
// current FOLDED baseline (the live migrations/ dir), then assert the two
// resulting schemas are byte-identical via a canonical fingerprint. If a fold
// ever changes semantics (adds/drops a column, renames, changes a type), this
// test fails.
//
// Runs on SQLite always (the dev/test workflow relies on throwaway SQLite). The
// Postgres path — the production/Neon target — runs additionally when
// VULOS_TEST_POSTGRES is set to a throwaway DSN.
package cpdb_test

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// foldedSubsystems is the set of subsystems whose migration chain was folded.
// Each has a legacy snapshot in testdata/legacy/<name>/ and a live folded
// baseline in ../<name>/migrations/.
// NOTE: the "billing" subsystem's migrations are part of the commercial
// wrapper module (vulos-cloud), so its fold-equivalence proof lives there
// (internal/billing/migratefold_test.go), not here.
var foldedSubsystems = []string{
	"servingpool",
	"relayusage",
	"orgadmin",
}

// TestMigrationFold_SchemaEquivalent proves each folded baseline is schema-
// equivalent to its original chain, per dialect.
func TestMigrationFold_SchemaEquivalent(t *testing.T) {
	for _, sub := range foldedSubsystems {
		sub := sub
		t.Run("sqlite/"+sub, func(t *testing.T) {
			legacyFP := applyAndFingerprintSQLite(t, "testdata/legacy/"+sub)
			foldedFP := applyAndFingerprintSQLite(t, "../"+sub+"/migrations")
			assertFingerprintEqual(t, sub, "sqlite", legacyFP, foldedFP)
		})
	}

	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Log("VULOS_TEST_POSTGRES not set — skipping the Postgres (production/Neon) equivalence leg")
		return
	}
	for _, sub := range foldedSubsystems {
		sub := sub
		t.Run("postgres/"+sub, func(t *testing.T) {
			// Same schema name for both legs so pg_indexes' schema-qualified
			// indexdef text matches; the schema is dropped between legs.
			schema := "foldtest_" + sub
			legacyFP := applyAndFingerprintPostgres(t, dsn, schema, "testdata/legacy/"+sub)
			foldedFP := applyAndFingerprintPostgres(t, dsn, schema, "../"+sub+"/migrations")
			assertFingerprintEqual(t, sub, "postgres", legacyFP, foldedFP)
		})
	}
}

func assertFingerprintEqual(t *testing.T, sub, dialect, legacy, folded string) {
	t.Helper()
	if legacy == folded {
		return
	}
	// Emit a readable diff of the first mismatching line.
	ll := strings.Split(legacy, "\n")
	fl := strings.Split(folded, "\n")
	max := len(ll)
	if len(fl) > max {
		max = len(fl)
	}
	for i := 0; i < max; i++ {
		var a, b string
		if i < len(ll) {
			a = ll[i]
		}
		if i < len(fl) {
			b = fl[i]
		}
		if a != b {
			t.Errorf("%s/%s: schema fingerprint DIVERGES at line %d:\n  legacy: %q\n  folded: %q",
				sub, dialect, i+1, a, b)
			return
		}
	}
	t.Errorf("%s/%s: schema fingerprint differs (length %d vs %d)", sub, dialect, len(legacy), len(folded))
}

// applyAndFingerprintSQLite opens a fresh in-memory SQLite DB, applies every
// migration in dir (dialect-filtered by the runner), and returns the canonical
// schema fingerprint.
func applyAndFingerprintSQLite(t *testing.T, dir string) string {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(os.DirFS(dir)); err != nil {
		t.Fatalf("migrate %s (sqlite): %v", dir, err)
	}
	return sqliteFingerprint(t, db)
}

// applyAndFingerprintPostgres opens the throwaway Postgres schema (dropped first
// so the leg starts clean), applies every migration in dir, fingerprints, then
// drops the schema again.
func applyAndFingerprintPostgres(t *testing.T, dsn, schema, dir string) string {
	t.Helper()
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open(schema)
	if err != nil {
		t.Fatalf("cpdb.Open(%q): %v", schema, err)
	}
	// Start clean: drop and recreate the schema, then re-pin nothing (search_path
	// already targets this schema from Open).
	if _, err := db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema)); err != nil {
		_ = db.Close()
		t.Fatalf("drop schema %q: %v", schema, err)
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE SCHEMA %q`, schema)); err != nil {
		_ = db.Close()
		t.Fatalf("create schema %q: %v", schema, err)
	}
	if err := db.Migrate(os.DirFS(dir)); err != nil {
		_ = db.Close()
		t.Fatalf("migrate %s (postgres): %v", dir, err)
	}
	fp := postgresFingerprint(t, db, schema)
	if _, err := db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema)); err != nil {
		t.Logf("cleanup drop schema %q: %v", schema, err)
	}
	_ = db.Close()
	return fp
}

// sqliteFingerprint dumps sqlite_master (tables + indexes, excluding internal
// objects and the migration ledger) as a canonical, order-stable string. The
// stored `sql` text is SQLite's verbatim record of each CREATE — this is a
// BYTE-level schema fingerprint.
func sqliteFingerprint(t *testing.T, db *cpdb.DB) string {
	t.Helper()
	rows, err := db.Query(`
		SELECT type, name, COALESCE(sql, '')
		  FROM sqlite_master
		 WHERE name NOT LIKE 'sqlite_%'
		   AND name <> '_schema_migrations'
		 ORDER BY type, name`)
	if err != nil {
		t.Fatalf("sqlite_master query: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var typ, name, sql string
		if err := rows.Scan(&typ, &name, &sql); err != nil {
			t.Fatalf("scan: %v", err)
		}
		fmt.Fprintf(&b, "%s\t%s\n%s\n---\n", typ, name, normWS(sql))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return b.String()
}

// postgresFingerprint builds a canonical fingerprint from the live catalog:
// every column (name, position, type, nullability, default) plus every index
// definition (which covers primary keys and unique constraints, since Postgres
// backs both with indexes). Schema-qualified names in indexdef match across legs
// because both legs use the same schema name.
func postgresFingerprint(t *testing.T, db *cpdb.DB, schema string) string {
	t.Helper()
	var b strings.Builder

	colRows, err := db.Query(`
		SELECT table_name, ordinal_position, column_name, data_type,
		       is_nullable, COALESCE(column_default, ''),
		       COALESCE(character_maximum_length, -1),
		       COALESCE(numeric_precision, -1), COALESCE(numeric_scale, -1)
		  FROM information_schema.columns
		 WHERE table_schema = $1
		   AND table_name <> '_schema_migrations'
		 ORDER BY table_name, ordinal_position`, schema)
	if err != nil {
		t.Fatalf("columns query: %v", err)
	}
	defer colRows.Close()
	b.WriteString("=== COLUMNS ===\n")
	for colRows.Next() {
		var tbl, colName, dtype, nullable, def string
		var pos, charMax, numPrec, numScale int64
		if err := colRows.Scan(&tbl, &pos, &colName, &dtype, &nullable, &def, &charMax, &numPrec, &numScale); err != nil {
			t.Fatalf("scan col: %v", err)
		}
		fmt.Fprintf(&b, "%s.%d %s | %s | null=%s | def=%s | len=%d prec=%d scale=%d\n",
			tbl, pos, colName, dtype, nullable, normWS(def), charMax, numPrec, numScale)
	}
	if err := colRows.Err(); err != nil {
		t.Fatalf("col rows: %v", err)
	}

	idxRows, err := db.Query(`
		SELECT tablename, indexname, indexdef
		  FROM pg_indexes
		 WHERE schemaname = $1
		   AND tablename <> '_schema_migrations'
		 ORDER BY tablename, indexname`, schema)
	if err != nil {
		t.Fatalf("indexes query: %v", err)
	}
	defer idxRows.Close()
	var idxLines []string
	for idxRows.Next() {
		var tbl, name, def string
		if err := idxRows.Scan(&tbl, &name, &def); err != nil {
			t.Fatalf("scan idx: %v", err)
		}
		idxLines = append(idxLines, normWS(def))
	}
	if err := idxRows.Err(); err != nil {
		t.Fatalf("idx rows: %v", err)
	}
	sort.Strings(idxLines)
	b.WriteString("=== INDEXES ===\n")
	b.WriteString(strings.Join(idxLines, "\n"))
	return b.String()
}

// normWS collapses runs of whitespace to a single space so purely-cosmetic
// formatting (indentation, line breaks, inline comments stripped by the catalog)
// never masks a real schema difference. For SQLite this makes the fingerprint a
// robust normalized-text comparison rather than a brittle byte-for-byte one.
func normWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
