// auth_test_utils_test.go — shared test utilities for opening auth stores.
// Wraps cpdb.OpenSQLiteDSN + auth.OpenAuthStore into a single call so that
// existing test files need only a one-token replacement at each call site.
package cproutes

import (
	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// openAuthStoreForTest opens an auth store from a raw SQLite DSN string.
// It is the test-only equivalent of:
//
//	db, _ := cpdb.OpenSQLiteDSN(dsn)
//	auth.OpenAuthStore(db, secret)
//
// Callers are responsible for closing the returned store (or registering
// t.Cleanup(func() { st.Close() })).
func openAuthStoreForTest(dsn string, secret []byte) (*auth.Store, error) {
	db, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		return nil, err
	}
	st, err := auth.OpenAuthStore(db, secret)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return st, nil
}
