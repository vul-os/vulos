package orgadmin_test

import (
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/orgadmin"
)

func openTestStore(t *testing.T) *orgadmin.SQLStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := orgadmin.OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("orgadmin.OpenSQLStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
