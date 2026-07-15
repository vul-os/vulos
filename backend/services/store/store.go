// Package store provides the Vulos SQLite database layer with optional
// cr-sqlite CRDT extension support for multi-node sync.
//
// Opening a database:
//
//	db, err := store.Open("")          // defaults to ~/.vulos/db/vulos.db
//	db, err := store.Open("/tmp/test") // explicit path
//
// The opener:
//  1. Creates the database file and parent directories.
//  2. Attempts to load the cr-sqlite loadable extension from well-known paths.
//     If the extension is absent, the DB opens normally and a clear warning is
//     logged — the application still works, it just cannot sync CRDTs.
//  3. Runs the embedded migration script (idempotent, safe every boot).
//  4. If cr-sqlite loaded successfully, registers all CRR tables.
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	dbmigrate "vulos/backend/internal/migrate"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGo — D23)
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// crrTables lists tables that should be registered as conflict-free replicated
// relations (CRRs) when the cr-sqlite extension is available.
var crrTables = []string{
	"users",
	"sessions",
	"profiles",
	"settings",
	"installed_apps",
}

// candidateExtPaths returns OS-specific locations to look for the cr-sqlite
// shared library, in order of preference.
func candidateExtPaths() []string {
	var lib string
	switch runtime.GOOS {
	case "darwin":
		lib = "crsqlite.dylib"
	case "windows":
		lib = "crsqlite.dll"
	default: // linux and others
		lib = "crsqlite.so"
	}

	home, _ := os.UserHomeDir()

	return []string{
		// Explicit override via env var (highest priority)
		os.Getenv("CRSQLITE_PATH"),

		// Standard Vulos installation directory
		filepath.Join(home, ".vulos", "lib", lib),

		// System-wide locations
		filepath.Join("/usr/local/lib", lib),
		filepath.Join("/usr/lib", lib),
		filepath.Join("/opt/homebrew/lib", lib), // macOS Homebrew arm64

		// Same directory as the running executable
		func() string {
			exe, err := os.Executable()
			if err != nil {
				return ""
			}
			return filepath.Join(filepath.Dir(exe), lib)
		}(),

		// Current working directory (useful in tests)
		lib,
	}
}

// DB wraps *sql.DB and tracks whether cr-sqlite is loaded.
type DB struct {
	*sql.DB
	CRSQLiteLoaded bool   // true when the extension loaded successfully
	CRSQLitePath   string // path of the extension that was loaded, if any
}

// defaultDBPath returns the default database file path: ~/.vulos/db/vulos.db.
func defaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("store: cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".vulos", "db", "vulos.db"), nil
}

// Open opens (or creates) the Vulos SQLite database.
//
// dbPath may be empty, in which case ~/.vulos/db/vulos.db is used.
// It may also be a directory path; in that case vulos.db is appended.
//
// The function applies migrations and, if cr-sqlite is available, marks the
// CRR tables. It never returns an error solely because cr-sqlite is absent —
// that condition is logged and reflected in DB.CRSQLiteLoaded.
func Open(dbPath string) (*DB, error) {
	// ── Resolve path ──────────────────────────────────────────────────────────
	if dbPath == "" {
		var err error
		dbPath, err = defaultDBPath()
		if err != nil {
			return nil, err
		}
	}

	info, err := os.Stat(dbPath)
	if err == nil && info.IsDir() {
		dbPath = filepath.Join(dbPath, "vulos.db")
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return nil, fmt.Errorf("store: cannot create database directory: %w", err)
	}

	// ── Open SQLite (pure-Go modernc driver — no CGo, D23) ───────────────────
	// _pragma= style DSN parameters are required by modernc.org/sqlite.
	// WAL mode for better concurrent reads; busy timeout guards writers.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)",
		dbPath,
	)
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: sql.Open: %w", err)
	}
	// SQLite is not safe for concurrent writers on the same connection.
	raw.SetMaxOpenConns(1)

	if err := raw.Ping(); err != nil {
		raw.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}

	db := &DB{DB: raw}

	// ── Load cr-sqlite extension ───────────────────────────────────────────────
	if extPath, loadErr := loadCRSQLite(raw); loadErr != nil {
		// Log a clear, actionable warning but continue.
		log.Printf("store: WARNING: cr-sqlite extension not loaded — "+
			"multi-node CRDT sync will be unavailable.\n"+
			"  To enable: install the cr-sqlite shared library and set CRSQLITE_PATH.\n"+
			"  Error: %v", loadErr)
	} else {
		db.CRSQLiteLoaded = true
		db.CRSQLitePath = extPath
		log.Printf("store: cr-sqlite loaded from %s", extPath)
	}

	// ── Apply migrations ───────────────────────────────────────────────────────
	if err := migrate(raw); err != nil {
		raw.Close()
		return nil, fmt.Errorf("store: migration: %w", err)
	}

	// ── Register CRR tables ────────────────────────────────────────────────────
	if db.CRSQLiteLoaded {
		if err := registerCRRs(raw); err != nil {
			// Non-fatal: warn and continue.
			log.Printf("store: WARNING: could not register CRR tables: %v", err)
		}
	}

	return db, nil
}

// loadCRSQLite attempts to load the cr-sqlite extension from the first
// candidate path that exists. It returns the path on success or an error
// describing all candidates tried.
func loadCRSQLite(db *sql.DB) (string, error) {
	candidates := candidateExtPaths()

	var tried []string
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			tried = append(tried, p+" (not found)")
			continue
		}

		// Attempt to load the cr-sqlite extension via SQLite's load_extension.
		// modernc.org/sqlite supports load_extension when the shared library
		// is a valid SQLite extension; the init symbol is sqlite3_crsqlite_init.
		// This call will fail gracefully (logged above) on platforms where
		// cr-sqlite is unavailable — CRDT sync is simply disabled, not fatal.
		_, err := db.Exec(`SELECT load_extension(?, 'sqlite3_crsqlite_init')`, p)
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s (load failed: %v)", p, err))
			continue
		}
		return p, nil
	}

	if len(tried) == 0 {
		return "", errors.New("no candidate paths for cr-sqlite extension")
	}
	return "", fmt.Errorf("cr-sqlite not found; tried:\n  %s", strings.Join(tried, "\n  "))
}

// registerCRRs calls crsql_as_crr for each table in crrTables.
// Tables that do not yet exist are skipped.
func registerCRRs(db *sql.DB) error {
	for _, table := range crrTables {
		if _, err := db.Exec(`SELECT crsql_as_crr(?)`, table); err != nil {
			// If the table doesn't exist yet the extension returns an error;
			// treat that gracefully.
			log.Printf("store: crsql_as_crr(%q): %v", table, err)
		}
	}
	return nil
}

// ── Migration runner ──────────────────────────────────────────────────────────

// migrate applies the embedded schema via the shared forward-only runner
// (version-tracked in schema_migrations, transactional, fail-closed).
func migrate(db *sql.DB) error {
	return dbmigrate.Apply(db, migrationsFS, "migrations")
}
