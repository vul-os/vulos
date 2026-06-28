package auth

// migrate.go — exported helper for the `vulos migrate` subcommand.
//
// Migrate opens (or creates) the auth SQLite database at the given path,
// applies every embedded migration (idempotent — all DDL is IF NOT EXISTS),
// and immediately closes the connection. It is safe to run while the server is
// stopped or before it has ever started.
func Migrate(dbPath string) error {
	db, err := openDB(dbPath)
	if err != nil {
		return err
	}
	db.Close()
	return nil
}

// MigrationTables returns the names of the tables created by the auth
// migrations, for use by the migrate status subcommand.
func MigrationTables() []string {
	return []string{"meta", "users", "sessions", "profiles", "recovery_blobs"}
}
