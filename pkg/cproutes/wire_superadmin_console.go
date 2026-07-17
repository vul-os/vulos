// wire_superadmin_console.go — brings the OPERATOR (super-admin) console into
// the self-hosted management binary.
//
// The operator console (pkg/superadmin) is server-rendered HTML today and, until
// now, was mounted only by the commercial cloud composition root (vulos-cloud's
// wire_superadmin.go). A self-hoster therefore had no operator console at all.
// This file mounts the OPERATIONAL subset — login + dashboard/accounts/audit/
// security/reserved-handles/maintenance/analytics/orgs/relay/incidents/
// migrations — plus a JSON admin API the new React admin section consumes.
//
// OPT-IN. Mounting a super-admin surface changes the deny posture of the admin
// routes (a real store makes the gate authenticate rather than deny-all), so it
// is wired ONLY when the operator opts in via VULOS_ENABLE_SUPERADMIN=1 or by
// setting VULOS_BOOTSTRAP_SUPERADMIN. With neither set (the zero-config
// self-host default), nothing here runs and the admin surface stays mounted-but-
// deny-all exactly as before — so the self-host smoke test's 403 contract holds.
//
// NOT COMMERCIAL. Everything here reads operational stores (auth accounts, the
// hash-chained audit log, the security telemetry store). The commercial console
// (pricing / regions / fleet-billing / billing-reconciliation) is NOT part of
// this subset — it lives in the cloud module and is injected there.
//
// The WebAuthn admin-key ENROLMENT endpoints (register begin/finish) are mounted
// here under a bootstrap gate (RequireSuperAdminEnroll) so a fresh self-host
// operator can register their first admin passkey through this binary.
//
// REMAINING (reported, not done this pass): the React admin does not yet cover
// orgs (management has no org-listing provider — that data source lives in the
// cloud module).
package cproutes

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/security"
	"github.com/vul-os/vulos-management/pkg/superadmin"
)

// superAdminConsoleEnabled reports whether the operator opted the super-admin
// console in. Default (both unset) = disabled → the admin surface stays
// deny-all, preserving the zero-config self-host posture.
func superAdminConsoleEnabled() bool {
	if v := strings.TrimSpace(os.Getenv("VULOS_ENABLE_SUPERADMIN")); v == "1" || strings.EqualFold(v, "true") {
		return true
	}
	return strings.TrimSpace(os.Getenv("VULOS_BOOTSTRAP_SUPERADMIN")) != ""
}

// superAdminConsoleDeps carries the collaborators the console needs. All are
// operational; none is commercial.
type superAdminConsoleDeps struct {
	AuthStore *auth.Store
	AuthDB    *cpdb.DB
	Audit     *auditlog.Logger
	Security  *security.Store // may be nil → the security JSON endpoint returns empty sections
}

// wireSuperAdminConsole opens the super-admin store (sharing the auth DB),
// bootstraps the first operator from VULOS_BOOTSTRAP_SUPERADMIN, mounts the Go
// operator pages + the JSON admin API, and returns the store closers. Never
// fails the caller: a store that cannot open logs and skips mounting.
func wireSuperAdminConsole(mux *http.ServeMux, deps superAdminConsoleDeps) []func() {
	if deps.AuthDB == nil {
		log.Println("[superadmin] no auth DB handle — operator console not mounted")
		return nil
	}
	saStore, err := superadmin.New(deps.AuthDB)
	if err != nil {
		log.Printf("[superadmin] WARNING: could not open store (%v) — operator console not mounted", err)
		return nil
	}
	// Register the singleton so any later WireSecurityRequireAdmin reader shares
	// this handle. Idempotent (sync.Once) — harmless if already set.
	superadmin.RegisterSuperAdminStore(saStore)
	if bErr := saStore.Bootstrap(context.Background()); bErr != nil {
		log.Printf("[superadmin] bootstrap: %v", bErr)
	}

	pages := superadmin.NewPages(saStore, deps.Audit, deps.AuthStore)
	pages.SetMigrationManifest(superadmin.LoadMigrationManifestFromEnv())

	requireAdmin := superadmin.RequireSuperAdmin(saStore, deps.AuthStore, deps.Audit)
	requireEnroll := superadmin.RequireSuperAdminEnroll(saStore, deps.AuthStore, deps.Audit)
	csrf := superadmin.CSRFProtect(deps.Audit)

	// Wrappers: SecurityHeaders is OUTERMOST so the strict CSP + hardening
	// headers are present on every response (including denials).
	pub := func(h http.HandlerFunc) http.Handler { return superadmin.SecurityHeaders(h) }
	pubCSRF := func(h http.HandlerFunc) http.Handler {
		return superadmin.SecurityHeaders(csrf(http.HandlerFunc(h)))
	}
	admin := func(h http.HandlerFunc) http.Handler {
		return superadmin.SecurityHeaders(requireAdmin(http.HandlerFunc(h)))
	}
	adminCSRF := func(h http.HandlerFunc) http.Handler {
		return superadmin.SecurityHeaders(requireAdmin(csrf(http.HandlerFunc(h))))
	}
	// apiAdmin wraps a JSON handler. CSRF-exempt by design: not form-triggered,
	// and gated on the SameSite=Strict admin session cookie.
	apiAdmin := func(h http.Handler) http.Handler {
		return superadmin.SecurityHeaders(requireAdmin(h))
	}
	// apiEnroll wraps the first-passkey enrolment JSON handlers. Gated on main
	// session + super-admin status only (a first-time operator has no admin
	// session yet); the handlers themselves enforce bootstrap-only. CSRF-safe:
	// a cross-site caller cannot read the begin challenge (same-origin JSON) nor
	// forge a valid authenticator attestation for finish.
	apiEnroll := func(h http.Handler) http.Handler {
		return superadmin.SecurityHeaders(requireEnroll(h))
	}

	// ── Static assets (public; referenced by the strict-CSP pages) ────────────
	mux.Handle("GET /superadmin/admin.css", pub(pages.AdminCSS()))
	mux.Handle("GET /superadmin/login.js", pub(pages.LoginJS()))
	mux.Handle("GET /superadmin/account_detail.js", pub(pages.AccountDetailJS()))

	// ── Login (public; IP-allowlist guard is inside the middleware) ───────────
	mux.Handle("GET /superadmin/login", pub(pages.LoginGet))
	mux.Handle("POST /superadmin/login", pubCSRF(pages.LoginPost))
	mux.Handle("POST /superadmin/login/webauthn-finish", pubCSRF(pages.LoginWebAuthnFinish))
	mux.Handle("GET /superadmin/logout", pub(pages.Logout))

	// ── First-passkey enrolment (bootstrap; main-session + super-admin gated) ─
	mux.Handle("POST /api/superadmin/webauthn/register/begin", apiEnroll(superadmin.HandleAdminWebAuthnRegisterBegin(saStore, deps.Audit)))
	mux.Handle("POST /api/superadmin/webauthn/register/finish", apiEnroll(superadmin.HandleAdminWebAuthnRegisterFinish(saStore, deps.Audit)))

	// ── Operator HTML pages (kept working alongside the React admin) ──────────
	mux.Handle("GET /superadmin/", admin(pages.Dashboard))
	mux.Handle("GET /superadmin/accounts", admin(pages.AccountsList))
	mux.Handle("GET /superadmin/accounts/{id}", admin(pages.AccountDetail))
	mux.Handle("GET /superadmin/accounts/{id}/confirm", admin(pages.AccountActionConfirm))
	mux.Handle("POST /superadmin/accounts/{id}/action", adminCSRF(pages.AccountActionExecute))
	mux.Handle("GET /superadmin/reserved-handles", admin(pages.ReservedHandles))
	mux.Handle("POST /superadmin/reserved-handles", adminCSRF(pages.ReservedHandleAdd))
	mux.Handle("GET /superadmin/reserved-handles/confirm-delete", admin(pages.ReservedHandleDeleteConfirm))
	mux.Handle("POST /superadmin/reserved-handles/delete", adminCSRF(pages.ReservedHandleDelete))
	mux.Handle("GET /superadmin/auditlog", admin(pages.AuditLog))
	mux.Handle("GET /superadmin/maintenance", admin(pages.Maintenance))
	mux.Handle("POST /superadmin/maintenance/verify-auditlog", adminCSRF(pages.VerifyAuditLog))
	mux.Handle("POST /superadmin/maintenance/rotation-check", adminCSRF(pages.RotationCheck))
	mux.Handle("POST /superadmin/maintenance/subprocessor-changelog", adminCSRF(pages.SubprocessorChangelog))
	mux.Handle("GET /superadmin/analytics", admin(pages.Analytics))
	mux.Handle("GET /superadmin/orgs", admin(pages.OrgsList))
	mux.Handle("GET /superadmin/migrations", admin(pages.MigrationsStatus))
	mux.Handle("GET /superadmin/relay", admin(pages.Relay))
	mux.Handle("GET /superadmin/incidents", admin(pages.Incidents))
	log.Println("[superadmin] operator HTML pages mounted (self-host, opt-in)")

	// ── JSON API (consumed by the React /console/admin section) ───────────────
	// Existing handlers (already in pkg/superadmin):
	mux.Handle("GET /api/superadmin/accounts", apiAdmin(superadmin.HandleListAccounts(saStore)))
	mux.Handle("GET /api/superadmin/accounts/{id}", apiAdmin(superadmin.HandleGetAccount(saStore)))
	mux.Handle("POST /api/superadmin/accounts/{id}/suspend", apiAdmin(superadmin.HandleSuspendAccount(saStore, deps.Audit)))
	mux.Handle("POST /api/superadmin/accounts/{id}/unsuspend", apiAdmin(superadmin.HandleUnsuspendAccount(saStore, deps.Audit)))
	mux.Handle("POST /api/superadmin/accounts/{id}/2fa-reset", apiAdmin(superadmin.HandleReset2FA(saStore, deps.Audit)))
	mux.Handle("GET /api/superadmin/reserved-handles", apiAdmin(superadmin.HandleListReservedHandles(saStore)))
	mux.Handle("POST /api/superadmin/reserved-handles", apiAdmin(superadmin.HandleAddReservedHandle(saStore, deps.Audit)))
	mux.Handle("DELETE /api/superadmin/reserved-handles/{handle}", apiAdmin(superadmin.HandleDeleteReservedHandle(saStore, deps.Audit)))

	// Admin-team management: an existing admin can grant/revoke admin access for
	// other accounts (self-host + Vulos team), no DB surgery. Mutations honour the
	// operator TOTP step-up and are audit-logged; the last admin can't be revoked.
	mux.Handle("GET /api/superadmin/admins", apiAdmin(superadmin.HandleListAdmins(saStore)))
	mux.Handle("POST /api/superadmin/admins", apiAdmin(superadmin.HandleGrantAdmin(saStore, deps.AuthStore, deps.Audit)))
	mux.Handle("DELETE /api/superadmin/admins/{id}", apiAdmin(superadmin.HandleRevokeAdmin(saStore, deps.AuthStore, deps.Audit)))

	// New JSON endpoints mirroring the HTML-only pages' data:
	mux.Handle("GET /api/superadmin/dashboard", apiAdmin(handleAdminDashboard(saStore, deps.Audit)))
	mux.Handle("GET /api/superadmin/audit", apiAdmin(handleAdminAudit(deps.Audit)))
	mux.Handle("GET /api/superadmin/security", apiAdmin(handleAdminSecurity(deps.Security)))
	// Whoami — lets the React admin shell learn the operator's identity + confirm
	// the admin session is live (200 = authorised; the gate returns 401/403 else).
	mux.Handle("GET /api/superadmin/whoami", apiAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"account_id": superadmin.AdminAccountIDFromCtx(r.Context()), "ok": true})
	})))
	log.Println("[superadmin] JSON admin API mounted (/api/superadmin/*)")

	return []func(){}
}

// ── JSON handlers (mirror the operator template data) ─────────────────────────

// handleAdminDashboard mirrors superadmin.dashboardData: fleet-account +
// super-admin counts and the most recent audit rows.
func handleAdminDashboard(store *superadmin.Store, al *auditlog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		saCount, acctCount := store.GetAdminStats(store.DB())
		var recent []auditlog.OrgEntry
		if al != nil {
			recent, _ = al.Query(r.Context(), auditlog.QueryOptions{Limit: 12})
		}
		if recent == nil {
			recent = []auditlog.OrgEntry{}
		}
		writeJSON(w, map[string]any{
			"superadmin_count": saCount,
			"account_count":    acctCount,
			"recent_audit":     recent,
		})
	}
}

// handleAdminAudit mirrors superadmin.auditLogData: filterable, bounded,
// newest-first platform audit rows.
func handleAdminAudit(al *auditlog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
		var rows []auditlog.OrgEntry
		if al != nil {
			rows, _ = al.Query(r.Context(), auditlog.QueryOptions{
				Actor:    strings.TrimSpace(q.Get("actor")),
				Action:   strings.TrimSpace(q.Get("action")),
				AfterSeq: after,
				Limit:    limit,
			})
		}
		if rows == nil {
			rows = []auditlog.OrgEntry{}
		}
		writeJSON(w, map[string]any{
			"rows":   rows,
			"actor":  q.Get("actor"),
			"action": q.Get("action"),
			"count":  len(rows),
		})
	}
}

// handleAdminSecurity mirrors the security dashboard: the recent security
// telemetry the operator reviews. A nil store returns empty sections (the
// self-host default before the security store is opened).
func handleAdminSecurity(sec *security.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const n = 50
		out := map[string]any{
			"waf": []any{}, "bots": []any{}, "stepups": []any{},
			"ato": []any{}, "honeypot": []any{}, "egress": []any{}, "ct": []any{},
		}
		if sec != nil {
			ctx := r.Context()
			if v, err := sec.RecentWAFEvents(ctx, n); err == nil && v != nil {
				out["waf"] = v
			}
			if v, err := sec.RecentBotEvents(ctx, n); err == nil && v != nil {
				out["bots"] = v
			}
			if v, err := sec.RecentStepUpEvents(ctx, n); err == nil && v != nil {
				out["stepups"] = v
			}
			if v, err := sec.PendingATOEvents(ctx, n); err == nil && v != nil {
				out["ato"] = v
			}
			if v, err := sec.RecentHoneypotHits(ctx, n); err == nil && v != nil {
				out["honeypot"] = v
			}
			if v, err := sec.RecentEgressAnomalies(ctx, n); err == nil && v != nil {
				out["egress"] = v
			}
			if v, err := sec.RecentCTCerts(ctx, n); err == nil && v != nil {
				out["ct"] = v
			}
		}
		writeJSON(w, out)
	}
}

// writeJSON encodes v as an application/json response.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
