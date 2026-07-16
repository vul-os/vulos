package superadmin

import (
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// TestSuperAdminStoreNilBeforeRegister verifies that SuperAdminStore returns nil
// before RegisterSuperAdminStore has been called.
func TestSuperAdminStoreNilBeforeRegister(t *testing.T) {
	// Reset state so this test is independent.
	resetSingletonForTest()
	t.Cleanup(resetSingletonForTest)

	if got := SuperAdminStore(); got != nil {
		t.Errorf("want nil before registration, got %v", got)
	}
}

// TestSuperAdminStoreSingletonAfterRegister verifies that SuperAdminStore returns
// the same instance that was registered, and that a second RegisterSuperAdminStore
// call is ignored (sync.Once semantics).
func TestSuperAdminStoreSingletonAfterRegister(t *testing.T) {
	resetSingletonForTest()
	t.Cleanup(resetSingletonForTest)

	// Build a minimal Store using a shared in-memory DB.
	db, err := cpdb.OpenSQLiteDSN("file::memory:?mode=memory&cache=shared&singleton_test=1")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	s1 := &Store{db: db}
	RegisterSuperAdminStore(s1)

	got := SuperAdminStore()
	if got != s1 {
		t.Errorf("want instance s1, got different pointer")
	}

	// Second registration should be ignored.
	s2 := &Store{db: db}
	RegisterSuperAdminStore(s2)

	if SuperAdminStore() != s1 {
		t.Errorf("want original s1 after second register, got different pointer")
	}
}
