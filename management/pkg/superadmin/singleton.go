// singleton.go — package-level SuperAdminStore singleton.
//
// wireSuperAdmin in cmd/server registers the single store instance via
// RegisterSuperAdminStore. wireSecurityRequireAdmin reads it via
// SuperAdminStore() instead of opening a second handle to auth.db.
//
// Usage pattern:
//
//	// In wireSuperAdmin (cmd/server/wire_superadmin.go):
//	saStore, _ := superadmin.New(authStore.DB())
//	superadmin.RegisterSuperAdminStore(saStore)
//
//	// In wireSecurityRequireAdmin (cmd/server/wire_security.go):
//	saStore := superadmin.SuperAdminStore()
//	if saStore == nil {
//	    // wireSuperAdmin has not run yet — guard required.
//	}
package superadmin

import "sync"

var (
	singletonOnce  sync.Once
	singletonStore *Store
)

// RegisterSuperAdminStore stores the singleton instance. It is idempotent: only
// the first call takes effect (subsequent calls are silently ignored via
// sync.Once). Must be called from wireSuperAdmin before any call to
// SuperAdminStore().
func RegisterSuperAdminStore(s *Store) {
	singletonOnce.Do(func() {
		singletonStore = s
	})
}

// SuperAdminStore returns the singleton *Store registered by
// RegisterSuperAdminStore, or nil if called before registration.
//
// Callers must guard against nil (i.e. wireSecurityRequireAdmin must be called
// after wireSuperAdmin).
func SuperAdminStore() *Store {
	return singletonStore
}

// resetSingletonForTest resets the singleton so tests can register a fresh
// store. Only for use in tests (unexported in non-test builds via build tag).
func resetSingletonForTest() {
	singletonOnce = sync.Once{}
	singletonStore = nil
}
