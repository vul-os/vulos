// Package backup provides automated, idempotent backup of the control-plane's
// pure-Go SQLite state plus per-account data manifests, and a restore-drill
// harness that proves recovery works (and measures RTO/RPO) against an
// isolated STAGING directory.
//
// This is the code half of the durability guarantee — "you can never lose your
// @vulos.org account". The drill (RISKS-HUMAN-TEAM.md §5) is what turns that
// claim from *asserted* into *exercised*. tools/restore-drill.sh is the
// one-command operator entrypoint that invokes the Go harness in this package.
//
// # Backend awareness
//
// The backup package detects the active database backend from the environment
// variables DATABASE_URL / VULOS_DATABASE_URL, using the same selection logic
// as cpdb.Open:
//
//   - SQLite (neither env var set): current behaviour — VACUUM INTO each .db
//     file in DataDir, produce a manifest + per-DB checksums/row-counts.
//   - Postgres (either env var set): NOT YET IMPLEMENTED. Run() and
//     RestoreDrill() return ErrBackupUnsupportedOnPostgres so callers can
//     handle the absence explicitly rather than silently ignoring it.
//
// DEFERRED: pg_dump --format=custom <DATABASE_URL> → BackupRoot and a matching
// pg_restore-based restore drill for the Postgres path. Until that is
// implemented, operators should use their managed-Postgres provider's
// point-in-time backup facilities (e.g. Neon branch snapshots, Fly managed
// Postgres PITR).
//
// INVARIANT: pure-Go modernc.org/sqlite ONLY for the SQLite path — never CGO.
// Snapshots are taken with `VACUUM INTO`, which produces a consistent,
// fully-checkpointed copy of a live (WAL-mode) database without needing the C
// SQLite online-backup API.
package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrBackupUnsupportedOnPostgres is returned by Run and RestoreDrill when
// DATABASE_URL / VULOS_DATABASE_URL is set (Postgres backend). The
// file-based backup mechanism (VACUUM INTO) is not applicable to Postgres;
// pg_dump/pg_restore integration is a deferred item.
//
// Callers that receive this error should log it and skip rather than treating
// it as a fatal failure — the backing infrastructure (e.g. Neon PITR,
// Fly managed Postgres backup) handles durability on the Postgres path.
var ErrBackupUnsupportedOnPostgres = errors.New(
	"backup: pg_dump-based backup not yet implemented for Postgres backend " +
		"(DATABASE_URL / VULOS_DATABASE_URL is set); use your Postgres " +
		"provider's PITR until this is implemented",
)

// postgresBackendActive returns true when DATABASE_URL or VULOS_DATABASE_URL
// is set, which means cpdb.Open would select the Postgres backend. The backup
// package replicates this check without importing cpdb to keep the dependency
// tree clean.
func postgresBackendActive() bool {
	return os.Getenv("DATABASE_URL") != "" || os.Getenv("VULOS_DATABASE_URL") != ""
}

// ManifestVersion is bumped when the on-disk manifest schema changes so a
// restore can refuse an incompatible artifact rather than silently misbehave.
const ManifestVersion = 1

// manifestFile is the well-known name of the backup manifest within a snapshot.
const manifestFile = "manifest.json"

// Config controls the Backupper.
type Config struct {
	// DataDir is the live control-plane data directory containing the *.db
	// SQLite files (e.g. /data/cp on the Fly persistent volume, ~/.vulos/cp in dev).
	DataDir string
	// BackupRoot is where timestamped snapshot directories are written.
	// Each Run() creates BackupRoot/<RFC3339-utc-stamp>/.
	BackupRoot string
	// Logf, if set, receives human-readable progress lines. Defaults to a
	// no-op so the package is silent in tests.
	Logf func(format string, args ...any)
}

// TableStat captures verifiable per-table state for a single database.
type TableStat struct {
	Name      string `json:"name"`
	RowCount  int64  `json:"row_count"`
	SchemaSQL string `json:"-"` // not serialised verbatim; folded into SchemaHash
}

// DBEntry describes one backed-up SQLite database within a snapshot.
type DBEntry struct {
	// File is the snapshot-relative filename of the copied DB (e.g. "auth.db").
	File string `json:"file"`
	// SizeBytes is the size of the copied DB file.
	SizeBytes int64 `json:"size_bytes"`
	// SHA256 is the hex checksum of the copied DB file bytes.
	SHA256 string `json:"sha256"`
	// SchemaHash is a hex SHA-256 over the sorted CREATE statements — lets a
	// restore detect schema drift independent of row data.
	SchemaHash string `json:"schema_hash"`
	// Tables is the per-table row count (sorted by name).
	Tables []TableStat `json:"tables"`
}

// AccountManifest is a per-account data manifest entry. The control plane does
// not itself store mailbox blobs (those live in object storage / on the box),
// so this records the account identity + a content fingerprint that a restore
// can use to confirm the right account state was recovered.
type AccountManifest struct {
	AccountID   string `json:"account_id"`
	Email       string `json:"email"`
	Fingerprint string `json:"fingerprint"` // hex SHA-256 of canonical account row
}

// Manifest is the JSON document written at the root of every snapshot. It is
// the source of truth the restore-drill verifies against.
type Manifest struct {
	Version    int               `json:"version"`
	CreatedAt  time.Time         `json:"created_at"`  // snapshot completion time (UTC)
	DataDir    string            `json:"data_dir"`    // source data dir (informational)
	DBs        []DBEntry         `json:"dbs"`         // sorted by File
	Accounts   []AccountManifest `json:"accounts"`    // sorted by AccountID
	DurationMS int64             `json:"duration_ms"` // wall time the backup took
}

// Backupper performs idempotent snapshots of the control-plane data dir.
type Backupper struct {
	cfg Config
}

// New constructs a Backupper. It does not touch the filesystem.
func New(cfg Config) *Backupper {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Backupper{cfg: cfg}
}

// listDBs returns the absolute paths of *.db files in DataDir, sorted. WAL/SHM
// sidecar files are intentionally excluded: VACUUM INTO produces a fully
// checkpointed standalone copy, so only the main .db file is needed.
func (b *Backupper) listDBs() ([]string, error) {
	entries, err := os.ReadDir(b.cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("backup: read data dir: %w", err)
	}
	var dbs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".db") {
			dbs = append(dbs, filepath.Join(b.cfg.DataDir, name))
		}
	}
	sort.Strings(dbs)
	return dbs, nil
}

// Run performs one snapshot and returns the snapshot directory and its
// manifest. It is idempotent: each invocation writes a fresh timestamped
// directory and never mutates the source data dir. Re-running is always safe.
//
// When DATABASE_URL / VULOS_DATABASE_URL is set (Postgres backend), Run
// returns ErrBackupUnsupportedOnPostgres immediately without touching the
// filesystem. This is an explicit, typed signal — not a silent no-op — so
// callers can handle the Postgres path deliberately.
func (b *Backupper) Run(ctx context.Context) (snapshotDir string, m *Manifest, err error) {
	if postgresBackendActive() {
		b.cfg.Logf("[backup] WARNING: Postgres backend detected (DATABASE_URL / VULOS_DATABASE_URL is set) — " +
			"SQLite file-based backup is not applicable. pg_dump-based backup is a deferred item. " +
			"Your Postgres provider's PITR facility handles durability on this backend. " +
			"Set CP_BACKUP_DISABLE=1 to suppress this message.")
		return "", nil, ErrBackupUnsupportedOnPostgres
	}
	start := time.Now()
	if b.cfg.DataDir == "" {
		return "", nil, fmt.Errorf("backup: DataDir is empty")
	}
	if b.cfg.BackupRoot == "" {
		return "", nil, fmt.Errorf("backup: BackupRoot is empty")
	}

	stamp := start.UTC().Format("20060102T150405Z")
	snapshotDir = filepath.Join(b.cfg.BackupRoot, stamp)
	// Idempotent on the rare same-second collision: dedupe with a suffix.
	for i := 1; ; i++ {
		if _, statErr := os.Stat(snapshotDir); os.IsNotExist(statErr) {
			break
		}
		snapshotDir = filepath.Join(b.cfg.BackupRoot, fmt.Sprintf("%s-%d", stamp, i))
	}
	if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
		return "", nil, fmt.Errorf("backup: mkdir snapshot: %w", err)
	}

	dbs, err := b.listDBs()
	if err != nil {
		return "", nil, err
	}

	man := &Manifest{
		Version:   ManifestVersion,
		CreatedAt: start.UTC(),
		DataDir:   b.cfg.DataDir,
	}

	for _, srcPath := range dbs {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		default:
		}
		entry, accts, err := b.snapshotDB(ctx, srcPath, snapshotDir)
		if err != nil {
			return "", nil, fmt.Errorf("backup: snapshot %s: %w", filepath.Base(srcPath), err)
		}
		man.DBs = append(man.DBs, entry)
		man.Accounts = append(man.Accounts, accts...)
		b.cfg.Logf("[backup] snapshotted %s (%d bytes, %d tables)", entry.File, entry.SizeBytes, len(entry.Tables))
	}

	sort.Slice(man.DBs, func(i, j int) bool { return man.DBs[i].File < man.DBs[j].File })
	sort.Slice(man.Accounts, func(i, j int) bool { return man.Accounts[i].AccountID < man.Accounts[j].AccountID })

	man.DurationMS = time.Since(start).Milliseconds()

	if err := writeManifest(snapshotDir, man); err != nil {
		return "", nil, err
	}
	b.cfg.Logf("[backup] snapshot complete: %s (%d dbs, %d accounts, %dms)",
		snapshotDir, len(man.DBs), len(man.Accounts), man.DurationMS)
	return snapshotDir, man, nil
}

// snapshotDB copies one live SQLite DB into snapshotDir via VACUUM INTO and
// records its checksum, schema hash, and per-table row counts. If the DB looks
// like an account store (has an "accounts" table with id/email columns) it also
// emits per-account manifest entries.
func (b *Backupper) snapshotDB(ctx context.Context, srcPath, snapshotDir string) (DBEntry, []AccountManifest, error) {
	base := filepath.Base(srcPath)
	dst := filepath.Join(snapshotDir, base)

	// Open the live DB read-only with WAL; VACUUM INTO checkpoints + copies.
	dsn := "file:" + srcPath + "?_pragma=journal_mode(WAL)&mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return DBEntry{}, nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	// VACUUM INTO requires write capability on the *target*, not the source;
	// modernc honours mode=ro on the source while still copying out.
	if _, err := db.ExecContext(ctx, "VACUUM INTO '"+escapeSQLLiteral(dst)+"'"); err != nil {
		return DBEntry{}, nil, fmt.Errorf("vacuum into: %w", err)
	}

	sum, size, err := fileSHA256(dst)
	if err != nil {
		return DBEntry{}, nil, err
	}

	tables, schemaHash, err := inspectSchema(ctx, db)
	if err != nil {
		return DBEntry{}, nil, err
	}

	entry := DBEntry{
		File:       base,
		SizeBytes:  size,
		SHA256:     sum,
		SchemaHash: schemaHash,
		Tables:     tables,
	}

	accts, _ := extractAccounts(ctx, db) // best-effort; absence is fine
	return entry, accts, nil
}

// inspectSchema returns per-table row counts (sorted) and a stable schema hash.
func inspectSchema(ctx context.Context, db *sql.DB) ([]TableStat, string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name, sql FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var stats []TableStat
	var schemaParts []string
	for rows.Next() {
		var name string
		var ddl sql.NullString
		if err := rows.Scan(&name, &ddl); err != nil {
			return nil, "", err
		}
		stats = append(stats, TableStat{Name: name, SchemaSQL: ddl.String})
		schemaParts = append(schemaParts, name+":"+ddl.String)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	// Count rows per table in a second pass (can't nest queries on a 1-conn DB).
	for i := range stats {
		var n int64
		// Table names come from sqlite_master, so quoting is sufficient.
		q := `SELECT COUNT(*) FROM "` + strings.ReplaceAll(stats[i].Name, `"`, `""`) + `"`
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			return nil, "", fmt.Errorf("count %s: %w", stats[i].Name, err)
		}
		stats[i].RowCount = n
	}

	sort.Strings(schemaParts)
	h := sha256.Sum256([]byte(strings.Join(schemaParts, "\n")))
	return stats, hex.EncodeToString(h[:]), nil
}

// extractAccounts emits per-account manifest entries if the DB has an
// "accounts" table with id + email columns. It is best-effort: any error (no
// such table, different shape) yields an empty slice and nil error.
func extractAccounts(ctx context.Context, db *sql.DB) ([]AccountManifest, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(email,'') FROM accounts ORDER BY id`)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	var out []AccountManifest
	for rows.Next() {
		var id, email string
		if err := rows.Scan(&id, &email); err != nil {
			return out, nil
		}
		fp := sha256.Sum256([]byte(id + "\x00" + email))
		out = append(out, AccountManifest{
			AccountID:   id,
			Email:       email,
			Fingerprint: hex.EncodeToString(fp[:]),
		})
	}
	return out, nil
}

// writeManifest serialises the manifest to <dir>/manifest.json (pretty).
func writeManifest(dir string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("backup: marshal manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFile), data, 0o600); err != nil {
		return fmt.Errorf("backup: write manifest: %w", err)
	}
	return nil
}

// ReadManifest loads and validates a snapshot manifest from a snapshot dir.
func ReadManifest(snapshotDir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(snapshotDir, manifestFile))
	if err != nil {
		return nil, fmt.Errorf("backup: read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("backup: parse manifest: %w", err)
	}
	if m.Version != ManifestVersion {
		return nil, fmt.Errorf("backup: manifest version %d unsupported (want %d)", m.Version, ManifestVersion)
	}
	return &m, nil
}

// LatestSnapshot returns the most recent snapshot directory under BackupRoot,
// or "" if there are none.
func LatestSnapshot(backupRoot string) (string, error) {
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			if _, statErr := os.Stat(filepath.Join(backupRoot, e.Name(), manifestFile)); statErr == nil {
				dirs = append(dirs, e.Name())
			}
		}
	}
	if len(dirs) == 0 {
		return "", nil
	}
	sort.Strings(dirs) // timestamp prefix sorts chronologically
	return filepath.Join(backupRoot, dirs[len(dirs)-1]), nil
}

// fileSHA256 returns the hex SHA-256 and byte size of a file.
func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// escapeSQLLiteral escapes single quotes for use inside a single-quoted SQL
// string literal (the VACUUM INTO target path).
func escapeSQLLiteral(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
