package cpdb

// migrate.go — SQL migration runner for cpdb.
//
// # File naming convention
//
// Migration files live in a package's migrations/ directory and are named:
//
//	NNNN_<description>.sql            dialect-neutral  (applied on both backends)
//	NNNN_<description>.sqlite.sql     SQLite-only
//	NNNN_<description>.postgres.sql   Postgres-only
//
// Rules:
//   - Files are applied in lexicographic (alphabetical) order, so the NNNN_
//     numeric prefix controls sequencing (0001, 0002, …).
//   - A given NNNN_ prefix MUST appear in at most one file per backend.
//     Having both 0001_init.sql and 0001_init.sqlite.sql is an error.
//   - Dialect-neutral files MUST NOT contain SQLite-specific syntax (PRAGMA,
//     AUTOINCREMENT, etc.) — use a .sqlite.sql variant instead.
//   - SQLite PRAGMAs belong in the store's Open function (via cpdb.Open /
//     cpdb.OpenSQLiteDSN), not in migration files.
//
// # Tracking
//
// Applied migrations are recorded in a _schema_migrations table created
// automatically in the active database (or schema on Postgres).  The key is
// the filename (e.g. "0001_init.sql").  Re-running Migrate is idempotent.
//
// # Usage
//
//	//go:embed migrations
//	var migrationsFS embed.FS
//
//	func Open(db *cpdb.DB) (*Store, error) {
//	    sub, _ := fs.Sub(migrationsFS, "migrations")
//	    if err := db.Migrate(sub); err != nil {
//	        return nil, fmt.Errorf("mystore: migrate: %w", err)
//	    }
//	    return &Store{db: db}, nil
//	}

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const createMigrationsTable = `
CREATE TABLE IF NOT EXISTS _schema_migrations (
    filename   TEXT NOT NULL PRIMARY KEY,
    applied_at TEXT NOT NULL,
    checksum   TEXT
)`

// Migrate applies all pending migrations from fsys (root of migrations/ dir).
//
// fsys is typically obtained via fs.Sub(embeddedFS, "migrations").  The FS
// must contain only .sql files at its root level (no sub-directories).
//
// Migration files are applied in alphabetical order.  Each file is applied
// exactly once per database; subsequent Migrate calls skip already-applied
// files.
func (db *DB) Migrate(fsys fs.FS) error {
	// CONCURRENT-BOOT SERIALIZATION (F2): on Postgres, a rolling deploy can boot
	// replicas that both run this check-then-apply with no cross-process lock; the
	// loser fails closed on the _schema_migrations primary key. Take a
	// session-level advisory lock keyed on this schema so replicas of the SAME
	// subsystem serialize their migration apply instead of racing. SQLite
	// (single-writer self-host) needs no lock, so this is a Postgres-only no-op.
	// The lock is always released (defer), and Postgres auto-releases session
	// advisory locks when the backend session ends, so a crash mid-migration
	// cannot wedge the lock permanently.
	if db.backend == BackendPostgres {
		unlock, err := db.acquirePostgresMigrationLock()
		if err != nil {
			return err
		}
		defer unlock()
	}

	// Ensure the tracking table exists.
	if _, err := db.Exec(createMigrationsTable); err != nil {
		return fmt.Errorf("cpdb/migrate: create tracking table: %w", err)
	}
	// Back-compat (item 14): add the checksum column to tracking tables created
	// before it existed. Idempotent: Postgres uses IF NOT EXISTS; on SQLite a
	// duplicate-column error (column already present) is ignored.
	if db.backend == BackendPostgres {
		if _, err := db.Exec(`ALTER TABLE _schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT`); err != nil {
			return fmt.Errorf("cpdb/migrate: add checksum column: %w", err)
		}
	} else {
		// SQLite has no ADD COLUMN IF NOT EXISTS. The only benign error here is
		// "duplicate column name" (the column already exists from a prior run) —
		// ignore that so the add stays idempotent. Any OTHER error (database
		// locked, disk I/O, etc.) must fail closed rather than be masked, so a
		// genuinely broken DB is not silently treated as up-to-date.
		if _, err := db.Exec(`ALTER TABLE _schema_migrations ADD COLUMN checksum TEXT`); err != nil && !isDuplicateColumnErr(err) {
			return fmt.Errorf("cpdb/migrate: add checksum column: %w", err)
		}
	}

	// Read the directory.
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("cpdb/migrate: read dir: %w", err)
	}

	// COLLISION GUARD (WAVE34-6): enforce the documented invariant that a bare
	// NNNN_desc.sql must NOT coexist with a dialect variant of the same stem
	// (NNNN_desc.sqlite.sql / NNNN_desc.postgres.sql). The bare file carries no
	// dialect suffix so it is applied on BOTH backends — alongside the matching
	// dialect file that would be double-applied on that backend. This was
	// documented at the top of the file but never enforced; enforce it now so a
	// future colliding pair fails loudly instead of silently double-applying.
	{
		bare := map[string]bool{}      // stem → bare NNNN_desc.sql present
		dialect := map[string]string{} // stem → the dialect filename present
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".sql") {
				continue
			}
			switch {
			case strings.HasSuffix(name, ".sqlite.sql"):
				dialect[strings.TrimSuffix(name, ".sqlite.sql")] = name
			case strings.HasSuffix(name, ".postgres.sql"):
				dialect[strings.TrimSuffix(name, ".postgres.sql")] = name
			default:
				bare[strings.TrimSuffix(name, ".sql")] = true
			}
		}
		for stem := range bare {
			if d, ok := dialect[stem]; ok {
				return fmt.Errorf("cpdb/migrate: migration collision: both bare %q and dialect %q exist — a bare migration is applied on BOTH backends and would double-apply against %q; keep only the dialect-split pair (.sqlite.sql/.postgres.sql) or only the bare file", stem+".sql", d, d)
			}
		}
	}

	// Collect candidate filenames in sorted order.
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		// Filter by dialect.
		if strings.HasSuffix(name, ".sqlite.sql") && db.backend != BackendSQLite {
			continue
		}
		if strings.HasSuffix(name, ".postgres.sql") && db.backend != BackendPostgres {
			continue
		}
		files = append(files, name)
	}
	sort.Strings(files)

	// Load already-applied filenames.
	applied, err := db.loadApplied()
	if err != nil {
		return err
	}

	// Apply pending migrations in order.
	for _, name := range files {
		sqlBytes, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("cpdb/migrate: read %q: %w", name, err)
		}
		sum := checksumOf(sqlBytes)

		if prev, ok := applied[name]; ok {
			// DRIFT DETECTION (item 14): an already-applied migration whose file
			// content changed is a coherency hazard — the DB no longer matches the
			// source. Reject. A legacy row with no recorded checksum (prev == "")
			// is backfilled rather than rejected (we can't know its original bytes).
			if prev == "" {
				if _, uerr := db.Exec(
					db.Rebind(`UPDATE _schema_migrations SET checksum = ? WHERE filename = ?`),
					sum, name,
				); uerr != nil {
					return fmt.Errorf("cpdb/migrate: backfill checksum %q: %w", name, uerr)
				}
			} else if prev != sum {
				// CONTROLLED RECONCILE (prod clean-baseline roll-out): when the
				// operator explicitly opts in, accept the changed file content as
				// canonical by backfilling the recorded checksum WITHOUT re-running
				// the DDL. This lets an intentional clean-baseline fold (which
				// rewrites already-applied files) deploy against an EXISTING database
				// that cannot be reset (e.g. prod) instead of crash-looping on this
				// guard. The default (env unset) stays strictly fail-closed.
				if reconcileChecksums() {
					log.Printf("[cpdb/migrate] WARNING: reconciling drifted checksum for %q (recorded %s → %s) — VULOS_MIGRATE_RECONCILE_CHECKSUMS is set, so the changed migration is accepted WITHOUT re-running its DDL; verify the live schema still matches the new file", name, prev, sum)
					if _, uerr := db.Exec(
						db.Rebind(`UPDATE _schema_migrations SET checksum = ? WHERE filename = ?`),
						sum, name,
					); uerr != nil {
						return fmt.Errorf("cpdb/migrate: reconcile checksum %q: %w", name, uerr)
					}
				} else {
					return fmt.Errorf("cpdb/migrate: applied migration %q was modified after being applied (checksum %s != recorded %s); never edit an applied migration (set VULOS_MIGRATE_RECONCILE_CHECKSUMS=1 to accept the new content without re-running it)", name, sum, prev)
				}
			}
			continue
		}

		// ATOMICITY (item 14): apply the migration body AND record the tracking row
		// in ONE transaction, so a crash between them cannot leave a migration
		// applied-but-unrecorded (re-run → double-apply) or recorded-but-unapplied.
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("cpdb/migrate: begin tx for %q: %w", name, err)
		}
		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("cpdb/migrate: apply %q: %w", name, err)
		}
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := tx.Exec(
			db.Rebind(`INSERT INTO _schema_migrations (filename, applied_at, checksum) VALUES (?,?,?)`),
			name, now, sum,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("cpdb/migrate: record %q: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("cpdb/migrate: commit %q: %w", name, err)
		}
	}
	return nil
}

// acquirePostgresMigrationLock takes a session-level pg_advisory_lock on a
// dedicated pooled connection, keyed on a stable hash of the current schema, so
// concurrent Postgres boots of the SAME subsystem serialize their check-then-
// apply instead of racing (F2). It returns a release func that unlocks and
// returns the connection to the pool; the func is idempotent (safe to call once
// via defer).
//
// Keying: hashtext(current_schema()) is computed in-DB. current_schema() is the
// first entry of the pinned search_path (the subsystem's schema). Advisory locks
// are database-global, so hashing per-schema lets DIFFERENT subsystems migrate
// in parallel while replicas of the same subsystem serialize. A hash collision
// between two schemas would merely serialize them unnecessarily — harmless.
//
// Deadlock guard: the lock holds ONE connection for the whole migration, so the
// pool must be able to serve migration work on ANOTHER connection. If the pool
// cap is < 2 we skip locking (return a no-op release) rather than risk starving
// ourselves — a sub-2 cap already serializes work, so the racy fallback is no
// worse than the pre-existing behavior.
func (db *DB) acquirePostgresMigrationLock() (func(), error) {
	noop := func() {}
	if cap := db.Stats().MaxOpenConnections; cap > 0 && cap < 2 {
		return noop, nil
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("cpdb/migrate: acquire advisory-lock conn: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext(current_schema()))`); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cpdb/migrate: advisory lock: %w", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			// Best-effort unlock, THEN return the conn to the pool. defer order in
			// Migrate guarantees this runs before the pool would hand the conn out
			// again. If the unlock errors the conn is likely broken; Close discards
			// it and Postgres frees the session lock when that backend session ends.
			_, _ = conn.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext(current_schema()))`)
			_ = conn.Close()
		})
	}, nil
}

// isDuplicateColumnErr reports whether err is the benign "column already exists"
// outcome of an idempotent ADD COLUMN on either backend. SQLite reports
// "duplicate column name: <col>"; Postgres reports "column <col> ... already
// exists" (SQLSTATE 42701 duplicate_column). Used to keep the checksum-column
// backfill idempotent while still failing closed on any other error (locked
// DB, disk I/O). Matched case-insensitively for robustness.
func isDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42701" {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") || // SQLite
		strings.Contains(msg, "already exists") // Postgres
}

// checksumOf returns the hex SHA-256 of a migration file's bytes.
func checksumOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// reconcileChecksums reports whether the operator has opted into reconciling a
// drifted migration checksum (VULOS_MIGRATE_RECONCILE_CHECKSUMS=1|true) instead
// of failing closed. This is the controlled escape hatch for deploying an
// intentional clean-baseline fold — which rewrites already-applied migration
// files — against an EXISTING database that cannot be reset (e.g. production):
// the runner backfills the recorded checksum to the current file WITHOUT
// re-running the DDL. The default (env unset) keeps the strict, fail-closed
// drift guard so an *accidental* edit to an applied migration is still caught.
func reconcileChecksums() bool {
	v := strings.TrimSpace(os.Getenv("VULOS_MIGRATE_RECONCILE_CHECKSUMS"))
	return v == "1" || strings.EqualFold(v, "true")
}

// loadApplied returns filename → recorded checksum (checksum is "" for legacy
// rows written before the checksum column existed).
func (db *DB) loadApplied() (map[string]string, error) {
	rows, err := db.Query(`SELECT filename, COALESCE(checksum, '') FROM _schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("cpdb/migrate: query applied: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var name, sum string
		if err := rows.Scan(&name, &sum); err != nil {
			return nil, err
		}
		out[name] = sum
	}
	return out, rows.Err()
}
