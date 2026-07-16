// wire_superadmin_stub.go — RequireSuperAdminStub, the fail-closed superadmin
// gate used by wire_ddos.go and any route file not yet migrated to the real
// RequireSuperAdmin middleware.
//
// The real middleware is built into the package-level saRequireAdmin var by
// initSuperAdminSecurity (security.go). This stub delegates to it when wired and
// DENIES otherwise, so a superadmin route is never accidentally exposed before
// the middleware exists.
package cproutes

import "net/http"

// RequireSuperAdminStub gates a handler behind the superadmin middleware.
//
// Deprecated: use the real RequireSuperAdmin middleware via saRequireAdmin.
func RequireSuperAdminStub(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Delegate to the real middleware if it has been wired.
		if mw := saRequireAdmin; mw != nil {
			mw(next).ServeHTTP(w, r)
			return
		}
		// SEC-M8: fall back to DENY-ALL rather than pass-through. If the real
		// middleware has not been wired yet (early startup or mis-wired), deny
		// every request so superadmin routes are never accidentally exposed.
		http.Error(w, `{"error":"superadmin not available"}`, http.StatusForbidden)
	}
}
