package security

import (
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// openTestStore creates an in-memory (SQLite) security store for testing.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	s, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
