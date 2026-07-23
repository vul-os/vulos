// export_test.go — exposes internal helpers for white-box tests.
// This file is compiled only during tests (package cpdb, not cpdb_test).
package cpdb

// RebindPostgres exposes the unexported rebindPostgres for unit testing.
var RebindPostgres = rebindPostgres

// AcquirePostgresMigrationLockForTest exposes acquirePostgresMigrationLock so a
// white-box test can exercise the pool-cap deadlock guard without a live
// Postgres (the guard returns a no-op release before any Postgres SQL runs when
// the pool cap is < 2, which is the case for the single-conn SQLite pool).
func (db *DB) AcquirePostgresMigrationLockForTest() (func(), error) {
	return db.acquirePostgresMigrationLock()
}
