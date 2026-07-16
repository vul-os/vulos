package orgadmin

// rbac.go — RBAC-ORG-01: a real role/permission model for the org-admin plane.
//
// Before this, every authenticated member of a tenant could perform every
// /api/org/* mutation (the SessionGate only resolved the tenant; the caller was
// implicitly treated as owner). This file adds a role → permission matrix and
// the seams routes.go uses to (a) resolve the CALLER's role within the tenant
// and (b) gate each mutating action on a permission check.
//
// Roles (most → least privileged):
//
//	owner         — full control, INCLUDING the destructive actions (remove a
//	                member, change a member's role) and billing.
//	admin         — non-destructive member + ops management: invite members, edit
//	                display names, toggle backup-mode. NOT destructive (remove /
//	                role-change are owner-only) and NOT billing.
//	billing-admin — billing only (and read). Cannot manage members or settings.
//	member        — read-only. No mutations.
//
// "Owner-only for destructive; billing-admin for billing" — the destructive
// member mutations (remove, role-change) are reserved to the owner; billing is
// reserved to owner + billing-admin. The matrix is intentionally explicit (no
// inheritance magic) so the authz decision for any (role, action) pair is
// auditable at a glance.

import (
	"context"
	"net/http"
)

// Role is a normalised org role. Use ParseRole to normalise client input.
type Role string

const (
	RoleOwner        Role = "owner"
	RoleAdmin        Role = "admin"
	RoleBillingAdmin Role = "billing-admin"
	RoleMember       Role = "member"
)

// Action is a gated org-admin mutation. Reads are not gated (any member of the
// tenant may read their own org's dashboards) — only mutations are.
type Action string

const (
	ActionMemberInvite  Action = "member.invite"
	ActionMemberRemove  Action = "member.remove"      // destructive
	ActionMemberRole    Action = "member.role"        // destructive (privilege change)
	ActionMemberName    Action = "member.name"        // cosmetic
	ActionBackupModeSet Action = "backup_mode.set"    // operational
	ActionProductStatus Action = "product.status.set" // operational (enable/disable a suite product for the org)
	ActionBillingManage Action = "billing.manage"     // billing
	ActionAuditView     Action = "audit.view"         // read the org's admin audit trail (admin-gated read)
)

// permissionMatrix is the authoritative (role → allowed actions) table. A role
// absent from the inner set is denied that action. Owner is handled specially
// in Can (owner can do everything) so the table need only enumerate the
// delegated roles.
var permissionMatrix = map[Role]map[Action]bool{
	RoleAdmin: {
		ActionMemberInvite:  true,
		ActionMemberName:    true,
		ActionBackupModeSet: true,
		ActionProductStatus: true,
		ActionAuditView:     true,
		// NOT ActionMemberRemove / ActionMemberRole — destructive member mutations
		// are owner-only. NOT ActionBillingManage — admins do not touch billing.
	},
	RoleBillingAdmin: {
		ActionBillingManage: true,
		ActionAuditView:     true,
		// billing-admin manages billing only; no member/settings mutations — but
		// MAY review the audit trail (they answer for the org's spend and need the
		// same accountability record as the ops admin).
	},
	RoleMember: {
		// read-only: no mutations permitted.
	},
}

// Can reports whether role is permitted to perform action. Owner is permitted
// everything. An unknown/empty role is treated as the least-privileged member
// (deny all mutations) — fail closed.
func Can(role Role, action Action) bool {
	if role == RoleOwner {
		return true
	}
	allowed, ok := permissionMatrix[role]
	if !ok {
		return false
	}
	return allowed[action]
}

// ParseRole normalises a client/role-store string into a Role. Unknown values
// map to RoleMember (least privilege) so an unrecognised role can never gain
// permissions. "billing_admin" and "billingadmin" are accepted aliases for
// "billing-admin".
func ParseRole(s string) Role {
	switch normaliseRole(s) {
	case "owner":
		return RoleOwner
	case "admin":
		return RoleAdmin
	case "billing-admin", "billing_admin", "billingadmin":
		return RoleBillingAdmin
	case "member":
		return RoleMember
	default:
		return RoleMember
	}
}

// validRoleRBAC reports whether s names a real assignable role (used by the
// invite/role-change validation so a caller cannot assign a bogus role).
func validRoleRBAC(s string) bool {
	switch ParseRole(s) {
	case RoleOwner, RoleAdmin, RoleBillingAdmin, RoleMember:
		// ParseRole never returns anything else, but an UNKNOWN input also maps to
		// RoleMember — so we must reject inputs that don't *name* a known role.
		switch normaliseRole(s) {
		case "owner", "admin", "billing-admin", "billing_admin", "billingadmin", "member":
			return true
		}
	}
	return false
}

// ──────────────────────────────────────────────────────────────────────────
// Seams: resolving the caller's identity + role
// ──────────────────────────────────────────────────────────────────────────

// CallerGate is an OPTIONAL extension of SessionGate that also reports the
// caller's own account id (distinct from the tenant id). When the wired gate
// implements it, routes.go can resolve the caller's role within the tenant. A
// gate that implements only SessionGate keeps the legacy behaviour: the caller
// is treated as the tenant owner (so existing single-tenant deployments and the
// 1:1 account==tenant convention are unaffected).
type CallerGate interface {
	SessionGate
	// CallerFromSession resolves the session to (tenantID, callerAccountID). On
	// auth failure it MUST write the auth error and return ("", "", false).
	CallerFromSession(w http.ResponseWriter, r *http.Request) (tenantID, callerAccountID string, ok bool)
}

// RoleResolver resolves the role a caller account holds within a tenant/org.
// Wired in main.go from the fleet store (the membership roster). When nil, the
// service treats the caller as owner (legacy behaviour) so RBAC is additive and
// opt-in via wiring.
type RoleResolver interface {
	// RoleInOrg returns the caller's role within tenantID. A caller who is not a
	// member returns ("", ErrNotFound). The returned string is normalised at the
	// service boundary via ParseRole.
	RoleInOrg(ctx context.Context, tenantID, callerAccountID string) (string, error)
}
