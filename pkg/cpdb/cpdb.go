// Package cpdb is the database backend seam for vulos.cloud/cp.
//
// # Backend selection
//
// Call [Open] with a logical subsystem name (e.g. "apikeys", "auth").
//
//   - If DATABASE_URL or VULOS_DATABASE_URL is set → opens Postgres via
//     jackc/pgx/v5/stdlib.  A Postgres schema named logicalName is created if
//     absent, and every pooled connection's search_path is set to
//     `logicalName, public` so unqualified table names resolve to the right
//     schema without code changes.
//
//   - Otherwise → opens modernc.org/sqlite (pure-Go, no CGO) at
//     <VULOS_DB_DIR>/<logicalName>.db with WAL mode and foreign_keys=ON.
//     VULOS_DB_DIR defaults to "." when unset.
//
// This maps cleanly from today's "auth.db, billing.db, …" file-per-subsystem
// model: each subsystem becomes its own Postgres schema on a shared cluster.
//
// # Dialect helpers
//
// [DB.Rebind] rewrites ? placeholders to $1, $2, … for Postgres; it is a
// no-op for SQLite.  Call it on every query string before executing:
//
//	rows, err := db.QueryContext(ctx, db.Rebind(`SELECT id FROM foo WHERE x=?`), x)
//
// [DB.Backend] returns [BackendSQLite] or [BackendPostgres] so callers can
// guard SQLite-only statements (e.g. PRAGMA) at runtime.
//
// # Migrations
//
// [DB.Migrate] runs any pending SQL files from an fs.FS (typically an
// embed.FS sub-rooted at the package's migrations/ directory).  See migrate.go
// for file naming conventions and tracking semantics.
//
// # Example (store Open)
//
//	func Open(db *cpdb.DB) (*Store, error) {
//	    subFS, _ := fs.Sub(migrationsFS, "migrations")
//	    if err := db.Migrate(subFS); err != nil {
//	        return nil, err
//	    }
//	    return &Store{db: db}, nil
//	}
package cpdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	_ "modernc.org/sqlite"
)

// IsUniqueViolation reports whether err is a unique-constraint violation on
// EITHER backend. It is the portable replacement for the SQLite-only
// `strings.Contains(err, "UNIQUE")` sniff, which silently fails on Postgres
// (whose message is "duplicate key value violates unique constraint", SQLSTATE
// 23505). Centralised here so every store handles both dialects identically.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// Postgres: structured SQLSTATE 23505 (unique_violation).
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return true
	}
	// Fallback string match covers SQLite ("UNIQUE constraint failed") and any
	// Postgres error surfaced as a plain string ("duplicate key value violates
	// unique constraint").
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key")
}

// Backend identifies the active database engine.
type Backend string

const (
	// BackendSQLite is modernc.org/sqlite (pure-Go, no CGO, default for self-host).
	BackendSQLite Backend = "sqlite"
	// BackendPostgres is Postgres via jackc/pgx/v5/stdlib (cloud / Neon).
	BackendPostgres Backend = "postgres"
)

// DB wraps *sql.DB and carries the dialect for the active backend.
// Always use [DB.Rebind] on query strings rather than writing ?-style queries
// directly, so they work on both backends.
//
// # Read-replica seam (IDENTITY-SERVICE §2.3)
//
// When a per-schema replica URL is configured (e.g. AUTH_DATABASE_REPLICA_URL
// for the "auth" schema, or generically CPDB_REPLICA_URL_<SCHEMA>), DB also
// holds a SECOND, read-only pool (replica). Only login-critical READS opt in via
// [DB.QueryRowReplica] / [DB.QueryReplica]; every other query keeps using the
// embedded primary *sql.DB, so nothing else changes. When no replica is
// configured (self-host SQLite always, or cloud with the env unset) the replica
// accessors transparently fall back to the primary — so the seam is a no-op
// until a replica is wired.
type DB struct {
	*sql.DB
	backend Backend

	// replica is an optional read-only pool. nil ⇒ QueryRowReplica/QueryReplica
	// fall back to the primary (*sql.DB above). Never used for writes.
	replica *sql.DB
}

// HasReplica reports whether a distinct read-replica pool is configured. When
// false, the *Replica accessors read the primary.
func (db *DB) HasReplica() bool { return db.replica != nil }

// AttachReplicaForTest sets the read-replica pool to an arbitrary *sql.DB. It
// exists ONLY so tests in other packages (e.g. internal/auth) can simulate a
// STALE replica — one that still returns rows the primary has since revoked — to
// exercise the confirm-on-valid revocation-bypass guard (IDENTITY-SERVICE
// §2.4). It is not used by production wiring, which sets the replica from a URL
// in Open. Passing nil detaches the replica.
func (db *DB) AttachReplicaForTest(replica *sql.DB) { db.replica = replica }

// Backend returns the active backend for this DB.
func (db *DB) Backend() Backend { return db.backend }

// Rebind rewrites ? positional placeholders to $1, $2, … for Postgres.
// For SQLite it returns query unchanged.
//
// Usage:
//
//	q := db.Rebind(`SELECT * FROM t WHERE id=? AND name=?`)
//	// sqlite:   "SELECT * FROM t WHERE id=? AND name=?"
//	// postgres: "SELECT * FROM t WHERE id=$1 AND name=$2"
func (db *DB) Rebind(query string) string {
	if db.backend == BackendSQLite {
		return query
	}
	return rebindPostgres(query)
}

// QueryRowReplica runs a single-row read against the read-replica pool when one
// is configured, else against the primary. Intended ONLY for login-critical
// reads (verify-credential lookups, session validation) that tolerate bounded
// replica lag — see IDENTITY-SERVICE §2.3/§2.4. The caller is responsible for
// the read-your-writes / revocation confirm-on-miss policy (§2.4): on a negative
// result that could be replica lag (e.g. a session token that should exist), the
// caller should re-confirm against the primary via the ordinary QueryRowContext
// before treating it as authoritative.
//
// Writes MUST NOT use this method — the replica is read-only.
func (db *DB) QueryRowReplica(ctx context.Context, query string, args ...any) *sql.Row {
	if db.replica != nil {
		return db.replica.QueryRowContext(ctx, query, args...)
	}
	return db.DB.QueryRowContext(ctx, query, args...)
}

// QueryReplica runs a multi-row read against the read-replica pool when one is
// configured, else against the primary. Same contract as [DB.QueryRowReplica].
func (db *DB) QueryReplica(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if db.replica != nil {
		return db.replica.QueryContext(ctx, query, args...)
	}
	return db.DB.QueryContext(ctx, query, args...)
}

// Close closes the primary pool and, if present, the read-replica pool.
func (db *DB) Close() error {
	err := db.DB.Close()
	if db.replica != nil {
		if rerr := db.replica.Close(); rerr != nil && err == nil {
			err = rerr
		}
	}
	return err
}

// rebindPostgres replaces each positional ? placeholder in q with $N
// (1-indexed, in appearance order) for Postgres.
//
// The scanner is single-quoted-string-literal aware: a ? that appears INSIDE a
// single-quoted SQL string literal is emitted verbatim and does NOT consume a
// placeholder number, so a literal such as `note = 'ok?'` can never shift the
// numbering of the real placeholders around it. The SQL escaped-quote
// convention is honoured: a doubled single-quote inside a literal is an escaped
// quote that keeps the scanner inside the string (so a value containing an
// apostrophe, written in SQL as two single-quotes, stays one literal).
// Because no existing query carries a ? inside a literal, this pass leaves every
// current query byte-identical; it only makes a future in-literal ? safe.
//
// LIMITATION — jsonb ? operators: Postgres also spells three operators with a
// question mark OUTSIDE any literal — `?`, `?|`, and `?&` (jsonb key-exists /
// any / all). This function treats every OUTSIDE-literal ? as a positional
// placeholder and will renumber it, so those jsonb operators cannot be written
// literally in a query passed through Rebind. Parsing operator context to
// distinguish them is error-prone and no current query uses them, so it is
// deliberately not attempted here. If a store ever needs a jsonb ?-operator,
// write that query against the Postgres backend without Rebind (or use the
// function-form jsonb_exists / jsonb_exists_any / jsonb_exists_all).
func rebindPostgres(q string) string {
	var b strings.Builder
	b.Grow(len(q) + 10)
	n := 1
	inLiteral := false // inside a single-quoted '...' string literal
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'':
			if inLiteral && i+1 < len(q) && q[i+1] == '\'' {
				// Doubled '' — an escaped quote INSIDE the literal. Emit both and
				// stay inside the literal.
				b.WriteByte(c)
				b.WriteByte(q[i+1])
				i++
				continue
			}
			// Toggle literal state on a lone quote.
			inLiteral = !inLiteral
			b.WriteByte(c)
		case c == '?' && !inLiteral:
			b.WriteString(fmt.Sprintf("$%d", n))
			n++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Open opens the database for the named logical subsystem.
//
// Backend selection:
//   - DATABASE_URL or VULOS_DATABASE_URL set → Postgres (pgx/v5/stdlib).
//     Creates schema logicalName if absent; sets search_path for every
//     pooled connection.
//   - Neither set → SQLite at <VULOS_DB_DIR>/<logicalName>.db
//     (WAL, foreign_keys=ON, MaxOpenConns=1).
//
// IsCloudBackend reports whether the control plane is running against Postgres —
// i.e. DATABASE_URL (or VULOS_DATABASE_URL) is set. This is the canonical
// cloud-vs-self-host signal: the managed cloud always runs Postgres, and a
// self-hosted CP runs SQLite. Subsystems that only make sense in the paid,
// multi-tenant cloud (the pricing catalog and the regions console, which charge
// nobody on a self-hosted box) gate on this rather than inventing their own flag.
func IsCloudBackend() bool {
	return os.Getenv("DATABASE_URL") != "" || os.Getenv("VULOS_DATABASE_URL") != ""
}

// logicalName must be a simple lowercase identifier (letters, digits,
// underscores) — it becomes a Postgres schema name and a SQLite filename.
func Open(logicalName string) (*DB, error) {
	if err := validateLogicalName(logicalName); err != nil {
		return nil, err
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = os.Getenv("VULOS_DATABASE_URL")
	}
	if url != "" {
		return openPostgres(logicalName, url)
	}
	return openSQLiteAuto(logicalName)
}

// OpenSQLiteDSN opens a SQLite DB at an explicit DSN.  Use this in wire
// helpers that already know the DSN path, and in tests (":memory:" works).
// WAL mode and foreign_keys=ON are applied automatically.
func OpenSQLiteDSN(dsn string) (*DB, error) {
	return openSQLiteRaw(dsn)
}

// ─── SQLite ───────────────────────────────────────────────────────────────────

func openSQLiteAuto(logicalName string) (*DB, error) {
	dbDir := os.Getenv("VULOS_DB_DIR")
	if dbDir == "" {
		dbDir = "."
	}
	// Ensure the directory exists before opening — SQLite returns error 14
	// ("unable to open database file") when the parent dir is missing. This
	// makes opens robust against an unset/stale VULOS_DB_DIR (e.g. a prior
	// test's cleaned-up t.TempDir()).
	if dbDir != "." {
		if err := os.MkdirAll(dbDir, 0o700); err != nil {
			return nil, fmt.Errorf("cpdb: create db dir %q: %w", dbDir, err)
		}
	}
	dsn := "file:" + filepath.Join(dbDir, logicalName+".db") + "?_pragma=journal_mode(WAL)"
	return openSQLiteRaw(dsn)
}

func openSQLiteRaw(dsn string) (*DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("cpdb: sqlite open %q: %w", dsn, err)
	}
	// SQLite WAL requires single writer — MaxOpenConns(1) serialises writes.
	db.SetMaxOpenConns(1)
	// busy_timeout makes a would-be-immediate SQLITE_BUSY into a bounded wait: a
	// second opener/writer (e.g. a concurrent process on the same self-host file)
	// blocks up to 5s for the lock instead of erroring out at once. WAL +
	// MaxOpenConns(1) + foreign_keys are unchanged.
	if _, err := db.Exec("PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cpdb: sqlite pragma: %w", err)
	}
	return &DB{DB: db, backend: BackendSQLite}, nil
}

// ─── Postgres ─────────────────────────────────────────────────────────────────

func openPostgres(logicalName, url string) (*DB, error) {
	// Phase 1: create the schema using the URL with no custom search_path, so
	// the CREATE SCHEMA statement works even before the schema exists.
	adminDB, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("cpdb: postgres open: %w", err)
	}
	_, err = adminDB.Exec(`CREATE SCHEMA IF NOT EXISTS ` + pgQuoteIdent(logicalName))
	_ = adminDB.Close()
	if err != nil {
		return nil, fmt.Errorf("cpdb: create schema %q: %w", logicalName, err)
	}

	// Phase 2: open the main pool with search_path pinned to logicalName so
	// every connection resolves unqualified table names to the right schema.
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("cpdb: parse postgres config: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = make(map[string]string)
	}
	// search_path = logicalName, public  (public keeps pg_catalog functions accessible)
	cfg.RuntimeParams["search_path"] = pgQuoteIdent(logicalName) + ",public"
	// NEON-COMPAT: the bare `search_path` startup parameter above is silently
	// DROPPED by Neon's connection proxy (it forwards only a whitelist of startup
	// params, not arbitrary GUCs), so every subsystem would fall back to the
	// default `"$user",public` and create ALL its tables in `public` — collapsing
	// schema-per-subsystem isolation and colliding same-named tables (e.g. auth vs
	// mobilepush `device_tokens`). The `options` startup param IS forwarded by Neon
	// (and honoured identically by stock Postgres), so ALSO pin search_path there.
	cfg.RuntimeParams["options"] = "-c search_path=" + pgQuoteIdent(logicalName) + ",public"

	sqlDB := stdlib.OpenDB(*cfg)

	// CONNECTION-POOL CAPS (item 12; NEON-CEIL-01): ~57 subsystem schemas each
	// open an INDEPENDENT pool against the same (Neon) cluster. Without caps,
	// Go's default of unlimited MaxOpenConns lets them collectively exhaust the
	// server's connection limit. Apply small, configurable per-pool caps so the
	// aggregate stays bounded.
	//
	// The aggregate ceiling is (#schemas × CPDB_PG_MAX_OPEN_CONNS). At the old
	// default of 4 that is ~57×4 ≈ 228 conns — well over Neon's ~100 direct
	// limit. Default to 2 (≈114 worst-case, and realistically far lower since
	// most schemas are idle) AND use Neon's POOLED connection string (PgBouncer,
	// ~10k client conns) in DATABASE_URL — see DEPLOY-CHECKLIST.md. Bump
	// CPDB_PG_MAX_OPEN_CONNS only for a hot schema on a pooled endpoint.
	sqlDB.SetMaxOpenConns(pgEnvInt("CPDB_PG_MAX_OPEN_CONNS", 2))
	sqlDB.SetMaxIdleConns(pgEnvInt("CPDB_PG_MAX_IDLE_CONNS", 1))
	sqlDB.SetConnMaxLifetime(time.Duration(pgEnvInt("CPDB_PG_CONN_MAX_LIFETIME_SECONDS", 300)) * time.Second)
	sqlDB.SetConnMaxIdleTime(time.Duration(pgEnvInt("CPDB_PG_CONN_MAX_IDLE_SECONDS", 60)) * time.Second)

	db := &DB{DB: sqlDB, backend: BackendPostgres}

	// ── Read-replica pool (IDENTITY-SERVICE §2.3) ──────────────────────────────
	// Optional, additive, backward-compatible. When a replica URL is configured
	// for this schema, open a SECOND read-only pool pinned to the same schema
	// search_path. Login-critical reads route here via QueryRowReplica /
	// QueryReplica; everything else keeps using the primary. Unset ⇒ no replica,
	// and the *Replica accessors fall back to the primary.
	if replicaURL := replicaURLFor(logicalName); replicaURL != "" {
		rcfg, err := pgx.ParseConfig(replicaURL)
		if err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("cpdb: parse replica config for %q: %w", logicalName, err)
		}
		if rcfg.RuntimeParams == nil {
			rcfg.RuntimeParams = make(map[string]string)
		}
		rcfg.RuntimeParams["search_path"] = pgQuoteIdent(logicalName) + ",public"
		rcfg.RuntimeParams["options"] = "-c search_path=" + pgQuoteIdent(logicalName) + ",public" // NEON-COMPAT (see primary pool)
		replicaDB := stdlib.OpenDB(*rcfg)
		replicaDB.SetMaxOpenConns(pgEnvInt("CPDB_PG_MAX_OPEN_CONNS", 2))
		replicaDB.SetMaxIdleConns(pgEnvInt("CPDB_PG_MAX_IDLE_CONNS", 1))
		replicaDB.SetConnMaxLifetime(time.Duration(pgEnvInt("CPDB_PG_CONN_MAX_LIFETIME_SECONDS", 300)) * time.Second)
		replicaDB.SetConnMaxIdleTime(time.Duration(pgEnvInt("CPDB_PG_CONN_MAX_IDLE_SECONDS", 60)) * time.Second)
		db.replica = replicaDB
	}

	return db, nil
}

// replicaURLFor returns the configured read-replica connection URL for the given
// logical schema, or "" when none is set (⇒ reads served from primary).
//
// Precedence:
//  1. CPDB_REPLICA_URL_<SCHEMA> (generic, per-schema; SCHEMA upper-cased)
//  2. AUTH_DATABASE_REPLICA_URL (the named seam for the login-critical "auth"
//     schema, per IDENTITY-SERVICE §2.3 — read-your-writes/revocation rules live
//     in the auth read path, not here)
//
// Only applies to Postgres/cloud; SQLite self-host never sets these, so the
// replica stays nil and reads hit the single local file (no-op).
func replicaURLFor(logicalName string) string {
	if v := os.Getenv("CPDB_REPLICA_URL_" + strings.ToUpper(logicalName)); v != "" {
		return v
	}
	if logicalName == "auth" {
		if v := os.Getenv("AUTH_DATABASE_REPLICA_URL"); v != "" {
			return v
		}
	}
	return ""
}

// pgEnvInt reads a non-negative integer from env var name, returning def when
// unset, empty, unparseable, or negative.
func pgEnvInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// pgQuoteIdent returns a double-quoted Postgres identifier, safe for use in
// CREATE SCHEMA / SET search_path statements.  Double-quotes inside the name
// are escaped by doubling them.
func pgQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// ─── Validation ───────────────────────────────────────────────────────────────

var validLogicalName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func validateLogicalName(name string) error {
	if !validLogicalName.MatchString(name) {
		return fmt.Errorf("cpdb: logical name %q must match [a-z][a-z0-9_]*", name)
	}
	return nil
}
