// Package migrate is the single, forward-only SQL migration runner shared by
// every SQLite-backed subsystem in the OS (auth, files, multiinstance, cgroups,
// llmuxclient, store).
//
// # Model
//
// Migrations are plain *.sql files embedded next to the code that owns them, in
// a "migrations" directory, named "NNNN_short_name.sql" (zero-padded, ascending).
// The baseline schema is 0001_*.sql. To evolve the schema you add a NEW file with
// the next number — you never edit an already-applied file. That is the whole
// upgrade model: forward-only, one file per change, applied in filename order.
//
// # Guarantees
//
//   - Forward-only & ordered: files are applied in ascending lexicographic
//     filename order. Numbering must be zero-padded so the order is stable.
//   - Exactly-once & version-tracked: every applied file is recorded (by name +
//     content checksum) in the schema_migrations table, so a file already applied
//     is skipped on the next boot. This is what makes a non-idempotent future
//     migration (a real ALTER) safe.
//   - Transactional & fail-closed: each migration runs inside its own
//     transaction together with the bookkeeping insert. A failure rolls the whole
//     migration back and returns an error — there is never a partial apply, and
//     the boot aborts rather than running on a half-migrated database.
//   - Tamper-evident: if a file that was already applied no longer matches its
//     recorded checksum, Apply refuses. Editing applied history is a foot-gun
//     (self-host and managed boxes would silently diverge); the runner forces you
//     to add a new migration instead.
//
// Self-host and managed boxes run the identical path: the same embedded files,
// the same runner, the same bookkeeping table. There is no separate "prod"
// migration story.
package migrate

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// BookkeepingTable is the name of the table in which applied migrations are
// recorded. It is intentionally distinct from any product table so a schema dump
// can filter it out.
const BookkeepingTable = "schema_migrations"

// Record is one applied migration.
type Record struct {
	Version   string // the migration filename, e.g. "0001_initial.sql"
	Checksum  string // hex(sha256(file contents)) captured at apply time
	AppliedAt string // RFC3339Nano UTC timestamp of application
}

// file is an on-disk migration awaiting application.
type file struct {
	name     string
	body     []byte
	checksum string
}

// Apply runs every "*.sql" migration found directly in dir within fsys, in
// ascending filename order, exactly once. See the package doc for the full
// contract. It is safe to call on every boot.
func Apply(db *sql.DB, fsys fs.FS, dir string) error {
	files, err := load(fsys, dir)
	if err != nil {
		return err
	}
	if err := ensureBookkeeping(db); err != nil {
		return err
	}
	applied, err := appliedChecksums(db)
	if err != nil {
		return err
	}
	for _, f := range files {
		prev, seen := applied[f.name]
		if seen {
			// Already applied: verify the file has not been edited since.
			if prev != f.checksum {
				return fmt.Errorf(
					"migrate: %s was already applied with checksum %s but the file now hashes to %s — "+
						"never edit an applied migration; add a new NNNN_*.sql file instead",
					f.name, prev, f.checksum)
			}
			continue
		}
		if err := applyOne(db, f); err != nil {
			return fmt.Errorf("migrate: apply %s: %w", f.name, err)
		}
	}
	return nil
}

// Applied returns the migrations recorded as applied, in version order. It
// returns an empty slice (not an error) if the bookkeeping table does not exist
// yet — i.e. nothing has ever been migrated.
func Applied(db *sql.DB) ([]Record, error) {
	if !bookkeepingExists(db) {
		return nil, nil
	}
	rows, err := db.Query(`SELECT version, checksum, applied_at FROM ` + BookkeepingTable + ` ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.Version, &r.Checksum, &r.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SchemaFingerprint returns a stable, formatting-independent description of the
// database's user schema: for every table (except the bookkeeping table and
// SQLite internals) its columns (name, type, not-null, default, pk position) and
// every index (name, uniqueness, columns). It deliberately ignores SQL text,
// comments and whitespace, so two databases with the same effective schema
// produce byte-identical fingerprints regardless of how the DDL was written.
//
// It is the primitive behind the migration-fold equivalence proof: a folded
// baseline and the original incremental chain must fingerprint identically. It
// is also handy for an operator to confirm a box's schema matches the release.
func SchemaFingerprint(db *sql.DB) (string, error) {
	tables, err := userTables(db)
	if err != nil {
		return "", err
	}
	sort.Strings(tables)
	var b strings.Builder
	for _, t := range tables {
		b.WriteString("TABLE " + t + "\n")
		cols, err := tableColumns(db, t)
		if err != nil {
			return "", err
		}
		for _, c := range cols {
			b.WriteString("  COL " + c + "\n")
		}
		idxs, err := tableIndexes(db, t)
		if err != nil {
			return "", err
		}
		for _, ix := range idxs {
			b.WriteString("  IDX " + ix + "\n")
		}
	}
	return b.String(), nil
}

func userTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name != ?`,
		BookkeepingTable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func tableColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(`SELECT cid, name, type, "notnull", dflt_value, pk FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, typ        string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%d %s %s notnull=%d default=%q pk=%d",
			cid, name, typ, notnull, dflt.String, pk))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func tableIndexes(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(`SELECT name, "unique", origin FROM pragma_index_list(?)`, table)
	if err != nil {
		return nil, err
	}
	type idx struct {
		name           string
		unique, origin string
	}
	var idxs []idx
	for rows.Next() {
		var name, origin string
		var uniq int
		if err := rows.Scan(&name, &uniq, &origin); err != nil {
			rows.Close()
			return nil, err
		}
		idxs = append(idxs, idx{name: name, unique: fmt.Sprintf("%d", uniq), origin: origin})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var out []string
	for _, ix := range idxs {
		cols, err := indexColumns(db, ix.name)
		if err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s unique=%s origin=%s cols=%s", ix.name, ix.unique, ix.origin, cols))
	}
	sort.Strings(out)
	return out, nil
}

func indexColumns(db *sql.DB, index string) (string, error) {
	rows, err := db.Query(`SELECT seqno, name FROM pragma_index_info(?)`, index)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var seqno int
		var name sql.NullString
		if err := rows.Scan(&seqno, &name); err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%d:%s", seqno, name.String))
	}
	return strings.Join(parts, ","), rows.Err()
}

// load reads and sorts the *.sql migrations in dir.
func load(fsys fs.FS, dir string) ([]file, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: read dir %q: %w", dir, err)
	}
	var files []file
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(body)
		files = append(files, file{name: e.Name(), body: body, checksum: hex.EncodeToString(sum[:])})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("migrate: no *.sql migrations found in %q", dir)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

// ensureBookkeeping creates the schema_migrations table if it is absent.
func ensureBookkeeping(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS ` + BookkeepingTable + ` (
        version    TEXT PRIMARY KEY,
        checksum   TEXT NOT NULL,
        applied_at TEXT NOT NULL
    )`)
	if err != nil {
		return fmt.Errorf("migrate: create %s: %w", BookkeepingTable, err)
	}
	return nil
}

func bookkeepingExists(db *sql.DB) bool {
	var name string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, BookkeepingTable,
	).Scan(&name)
	return err == nil
}

// appliedChecksums returns version→checksum for every recorded migration.
func appliedChecksums(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT version, checksum FROM ` + BookkeepingTable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var v, c string
		if err := rows.Scan(&v, &c); err != nil {
			return nil, err
		}
		out[v] = c
	}
	return out, rows.Err()
}

// applyOne runs a single migration and records it, atomically. Any failure rolls
// the transaction back so the migration is neither half-applied nor recorded.
func applyOne(db *sql.DB, f file) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.Exec(string(f.body)); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO `+BookkeepingTable+` (version, checksum, applied_at) VALUES (?, ?, ?)`,
		f.name, f.checksum, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
