package files

// migrate.go — exported helper for the `vulos migrate` subcommand.
//
// Migrate opens (or creates) the Files SQLite database at the given path,
// applies every embedded migration in lexicographic order (idempotent — all
// DDL is IF NOT EXISTS), and immediately closes the connection. It is safe to
// run while the server is stopped or before it has ever started.
func Migrate(dbPath string) error {
	db, err := openDB(dbPath)
	if err != nil {
		return err
	}
	db.Close()
	return nil
}

// MigrationTables returns the names of the tables created by the files
// migrations, for use by the migrate status subcommand.
func MigrationTables() []string {
	return []string{
		"files_nodes",
		"files_acls",
		"files_share_links",
		"files_versions",
		"files_audit",
		"files_external_mounts",
		"files_peer_shares",
		"files_received",
		"files_import_jobs",
		"files_import_items",
	}
}
