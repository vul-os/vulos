package superadmin

import (
	"context"
	"fmt"
	"net/http"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/auth"
)

// ctxKey is an unexported type for context keys in this package.
type ctxKey int

const (
	ctxAdminAccountID ctxKey = iota
)

// AdminAccountIDFromCtx returns the super-admin account ID stored by
// RequireSuperAdmin, or "" if not present.
func AdminAccountIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxAdminAccountID).(string)
	return v
}

// ExportedCtxAdminAccountID exports the context key for test injection.
// Tests can set the admin account ID in context via:
//
//	ctx = context.WithValue(ctx, superadmin.ExportedCtxAdminAccountID, accountID)
var ExportedCtxAdminAccountID = ctxAdminAccountID

// RequireSuperAdmin is the three-layer admin access middleware:
//  1. Valid main session cookie (via auth.Store).
//  2. IP in allowlist (parsed once at startup via ParseAllowlist).
//  3. Valid admin session cookie (post-WebAuthn step).
//
// Every request (allowed or denied) is recorded in the audit log.
func RequireSuperAdmin(
	store *Store,
	authStore *auth.Store,
	al *auditlog.Logger,
) func(http.Handler) http.Handler {
	allowlist := ParseAllowlist()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := remoteIP(r)
			route := r.Method + " " + r.URL.Path

			// 1. IP allowlist.
			if !IPAllowed(allowlist, ip) {
				auditAction(r.Context(), al, "unknown", "admin.denied.ip",
					route, map[string]string{"ip": ip})
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			// 2. Main session (auth package writes 401 on failure).
			mainUser := authStore.RequireSession(r.Context(), w, r)
			if mainUser == nil {
				auditAction(r.Context(), al, "unknown", "admin.denied.no_main_session",
					route, map[string]string{"ip": ip})
				return
			}

			// 3. Super-admin check.
			isSA, err := store.IsSuperAdmin(r.Context(), mainUser.ID)
			if err != nil || !isSA {
				auditAction(r.Context(), al, mainUser.Email, "admin.denied.not_superadmin",
					route, map[string]string{"ip": ip})
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			// 4. Admin session cookie (post-WebAuthn).
			adminToken := AdminSessionFromRequest(r)
			adminAccountID, sessErr := store.LookupAdminSession(r.Context(), adminToken)
			if sessErr != nil || adminAccountID != mainUser.ID {
				auditAction(r.Context(), al, mainUser.Email, "admin.denied.no_admin_session",
					route, map[string]string{"ip": ip})
				http.Error(w, "admin session required — complete WebAuthn step at /superadmin/login",
					http.StatusUnauthorized)
				return
			}

			// Inject account ID into context and audit.
			ctx := context.WithValue(r.Context(), ctxAdminAccountID, adminAccountID)
			auditAction(ctx, al, mainUser.Email, "admin.request",
				route, map[string]string{"ip": ip, "method": r.Method, "path": r.URL.Path})

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// auditAction records an audit entry (fire-and-forget; errors are swallowed).
func auditAction(ctx context.Context, al *auditlog.Logger, actor, action, target string, meta map[string]string) {
	if al == nil {
		return
	}
	_ = al.Record(ctx, actor, action, target, meta)
}

// jsonError writes a JSON error response.
func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
