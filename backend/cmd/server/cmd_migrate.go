package main

// cmd_migrate.go — `vulos migrate` subcommand.
//
// Usage:
//
//	vulos migrate up       — apply all embedded schema migrations to the
//	                         auth.db and files.db SQLite databases (idempotent).
//	vulos migrate status   — report which tables are present in each database.
//
// DB paths follow the same defaults as the server ($HOME/.vulos/db/). Override
// with VULOS_AUTH_DB and VULOS_FILES_DB environment variables.
//
// Cloud / ops usage (out-of-band, after a rolling upgrade or initial provision):
//
//	vulos migrate up
//	# → auth.db:  migrations applied
//	# → files.db: migrations applied

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"vulos/backend/services/auth"
	"vulos/backend/services/files"

	_ "modernc.org/sqlite"
)

// defaultMigrateDBs returns the canonical db paths for auth.db and files.db
// under $HOME/.vulos/db/, overridable by env vars.
func defaultMigrateDBs() (authDB, filesDB string) {
	home, _ := os.UserHomeDir()
	dbDir := filepath.Join(home, ".vulos", "db")
	authDB = os.Getenv("VULOS_AUTH_DB")
	if authDB == "" {
		authDB = filepath.Join(dbDir, "auth.db")
	}
	filesDB = os.Getenv("VULOS_FILES_DB")
	if filesDB == "" {
		filesDB = filepath.Join(dbDir, "files.db")
	}
	return
}

// runMigrateCLI is the entry point for `vulos migrate <up|status>`.
func runMigrateCLI(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	authFlag := fs.String("auth-db", "", "path to auth SQLite DB (default: VULOS_AUTH_DB or ~/.vulos/db/auth.db)")
	filesFlag := fs.String("files-db", "", "path to files SQLite DB (default: VULOS_FILES_DB or ~/.vulos/db/files.db)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	subcmd := "up"
	if fs.NArg() > 0 {
		subcmd = fs.Arg(0)
	}

	authDB, filesDB := defaultMigrateDBs()
	if *authFlag != "" {
		authDB = *authFlag
	}
	if *filesFlag != "" {
		filesDB = *filesFlag
	}

	switch subcmd {
	case "up":
		return runMigrateUp(authDB, filesDB)
	case "status":
		return runMigrateStatus(authDB, filesDB)
	default:
		fmt.Fprintf(os.Stderr, "migrate: unknown subcommand %q (valid: up, status)\n", subcmd)
		return 2
	}
}

func runMigrateUp(authDB, filesDB string) int {
	exitCode := 0

	if err := auth.Migrate(authDB); err != nil {
		fmt.Fprintf(os.Stderr, "migrate up: auth.db (%s): %v\n", authDB, err)
		exitCode = 1
	} else {
		fmt.Printf("migrate up: auth.db  (%s): ok\n", authDB)
	}

	if err := files.Migrate(filesDB); err != nil {
		fmt.Fprintf(os.Stderr, "migrate up: files.db (%s): %v\n", filesDB, err)
		exitCode = 1
	} else {
		fmt.Printf("migrate up: files.db (%s): ok\n", filesDB)
	}

	return exitCode
}

func runMigrateStatus(authDB, filesDB string) int {
	type dbStatus struct {
		label  string
		path   string
		tables []string
	}
	dbs := []dbStatus{
		{label: "auth.db", path: authDB, tables: auth.MigrationTables()},
		{label: "files.db", path: filesDB, tables: files.MigrationTables()},
	}

	exitCode := 0
	for _, d := range dbs {
		fmt.Printf("\n[%s] %s\n", d.label, d.path)
		present, err := sqliteTables(d.path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			exitCode = 1
			continue
		}
		// Build expected set.
		expected := make(map[string]bool, len(d.tables))
		for _, t := range d.tables {
			expected[t] = false
		}
		for _, t := range present {
			if _, known := expected[t]; known {
				expected[t] = true
			}
		}
		// Print sorted.
		keys := make([]string, 0, len(expected))
		for k := range expected {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, t := range keys {
			state := "ok"
			if !expected[t] {
				state = "MISSING"
				exitCode = 1
			}
			fmt.Printf("  %-40s %s\n", t, state)
		}
	}
	return exitCode
}

// sqliteTables returns the names of all user-created tables in the SQLite DB
// at path (reads sqlite_master). Returns nil if the file does not exist yet.
func sqliteTables(path string) ([]string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}
