package orgadmin

// rbac_test.go — RBAC-ORG-01: role/permission matrix + route-level enforcement.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCan_PermissionMatrix asserts the (role, action) decisions: owner can do
// everything; admin can do non-destructive member + ops; billing-admin only
// billing; member nothing.
func TestCan_PermissionMatrix(t *testing.T) {
	type row struct {
		role Role
		act  Action
		want bool
	}
	rows := []row{
		// Owner: everything.
		{RoleOwner, ActionMemberInvite, true},
		{RoleOwner, ActionMemberRemove, true},
		{RoleOwner, ActionMemberRole, true},
		{RoleOwner, ActionBackupModeSet, true},
		{RoleOwner, ActionBillingManage, true},
		// Admin: invite/name/backup yes; destructive + billing no.
		{RoleAdmin, ActionMemberInvite, true},
		{RoleAdmin, ActionMemberName, true},
		{RoleAdmin, ActionBackupModeSet, true},
		{RoleAdmin, ActionMemberRemove, false},
		{RoleAdmin, ActionMemberRole, false},
		{RoleAdmin, ActionBillingManage, false},
		// Billing-admin: billing only.
		{RoleBillingAdmin, ActionBillingManage, true},
		{RoleBillingAdmin, ActionMemberInvite, false},
		{RoleBillingAdmin, ActionBackupModeSet, false},
		{RoleBillingAdmin, ActionMemberRemove, false},
		// Member: nothing.
		{RoleMember, ActionMemberInvite, false},
		{RoleMember, ActionMemberRemove, false},
		{RoleMember, ActionBackupModeSet, false},
		{RoleMember, ActionBillingManage, false},
		// Unknown role → least privilege (deny).
		{Role("garbage"), ActionMemberInvite, false},
	}
	for _, r := range rows {
		if got := Can(r.role, r.act); got != r.want {
			t.Errorf("Can(%q, %q) = %v, want %v", r.role, r.act, got, r.want)
		}
	}
}

func TestParseRole_BillingAdminAliases(t *testing.T) {
	for _, s := range []string{"billing-admin", "Billing-Admin", "billing_admin", "BILLINGADMIN"} {
		if got := ParseRole(s); got != RoleBillingAdmin {
			t.Errorf("ParseRole(%q) = %q, want billing-admin", s, got)
		}
	}
	if got := ParseRole("nonsense"); got != RoleMember {
		t.Errorf("ParseRole(nonsense) = %q, want member (least privilege)", got)
	}
}

// fixedRoleResolver returns a canned role for the caller regardless of tenant.
type fixedRoleResolver struct {
	role string
	err  error
}

func (f fixedRoleResolver) RoleInOrg(_ context.Context, _, _ string) (string, error) {
	return f.role, f.err
}

// callerGate is a CallerGate test double: a fixed (tenant, caller) pair.
type callerGate struct {
	tenant, caller string
}

func (g callerGate) TenantFromSession(w http.ResponseWriter, _ *http.Request) (string, bool) {
	if g.tenant == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return "", false
	}
	return g.tenant, true
}

func (g callerGate) CallerFromSession(w http.ResponseWriter, _ *http.Request) (string, string, bool) {
	if g.tenant == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return "", "", false
	}
	return g.tenant, g.caller, true
}

// newRBACRouter builds a router whose service resolves the caller's role from
// the supplied resolver and uses a CallerGate fixing (tenant, caller).
func newRBACRouter(role string) http.Handler {
	svc := NewService(
		&tenantScopedMembers{owner: "org-1", members: []Member{{ID: "org-1", Role: "owner"}}},
		nil, nil, nil, nil,
		&memBackupStore{rows: map[string]BackupModeRequest{}},
		nil,
	)
	svc.Roles = fixedRoleResolver{role: role}
	mux := http.NewServeMux()
	RegisterRoutes(mux, svc, callerGate{tenant: "org-1", caller: "caller-1"})
	return mux
}

// memBackupStore is a minimal in-memory BackupModeStore for the RBAC route test.
type memBackupStore struct{ rows map[string]BackupModeRequest }

func (m *memBackupStore) GetBackupMode(_ context.Context, tenant string) (BackupModeResponse, error) {
	r := m.rows[tenant]
	mode := r.Mode
	if mode == "" {
		mode = BackupModeCentral
	}
	return BackupModeResponse{Mode: mode, PerLocation: []PerLocationMode{}}, nil
}
func (m *memBackupStore) SetBackupMode(_ context.Context, tenant string, req BackupModeRequest) error {
	m.rows[tenant] = req
	return nil
}
func (m *memBackupStore) Close() error { return nil }

func doReq(t *testing.T, h http.Handler, method, path string, body any) int {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestRBAC_MemberDeniedOwnerOnlyAction: a member calling an owner-only
// destructive action (remove member) gets 403; the owner gets through.
func TestRBAC_MemberDeniedOwnerOnlyAction(t *testing.T) {
	member := newRBACRouter("member")
	if code := doReq(t, member, http.MethodDelete, "/api/org/members/victim", nil); code != http.StatusForbidden {
		t.Errorf("member removing a member: want 403, got %d", code)
	}

	owner := newRBACRouter("owner")
	if code := doReq(t, owner, http.MethodDelete, "/api/org/members/victim", nil); code == http.StatusForbidden {
		t.Errorf("owner removing a member: must NOT be 403, got %d", code)
	}
}

// TestRBAC_AdminDeniedDestructiveAndBilling: an admin may invite + set backup
// mode but is denied the owner-only destructive actions and billing.
func TestRBAC_AdminScopes(t *testing.T) {
	admin := newRBACRouter("admin")

	// Allowed: invite.
	if code := doReq(t, admin, http.MethodPost, "/api/org/invite", InviteRequest{Email: "a@b.com", Role: "member"}); code == http.StatusForbidden {
		t.Errorf("admin invite: must NOT be 403, got %d", code)
	}
	// Allowed: backup-mode.
	if code := doReq(t, admin, http.MethodPut, "/api/org/backup-mode", BackupModeRequest{Mode: BackupModeLocal}); code == http.StatusForbidden {
		t.Errorf("admin backup-mode: must NOT be 403, got %d", code)
	}
	// Denied: destructive role change (owner-only).
	if code := doReq(t, admin, http.MethodPatch, "/api/org/members/x/role", RoleChangeRequest{Role: "admin"}); code != http.StatusForbidden {
		t.Errorf("admin role-change: want 403 (owner-only), got %d", code)
	}
	// Denied: destructive remove (owner-only).
	if code := doReq(t, admin, http.MethodDelete, "/api/org/members/x", nil); code != http.StatusForbidden {
		t.Errorf("admin remove: want 403 (owner-only), got %d", code)
	}
}

// TestRBAC_BillingAdminDeniedMemberMgmt: a billing-admin cannot invite/remove
// members nor toggle backup mode.
func TestRBAC_BillingAdminDeniedMemberMgmt(t *testing.T) {
	ba := newRBACRouter("billing-admin")
	if code := doReq(t, ba, http.MethodPost, "/api/org/invite", InviteRequest{Email: "a@b.com", Role: "member"}); code != http.StatusForbidden {
		t.Errorf("billing-admin invite: want 403, got %d", code)
	}
	if code := doReq(t, ba, http.MethodPut, "/api/org/backup-mode", BackupModeRequest{Mode: BackupModeLocal}); code != http.StatusForbidden {
		t.Errorf("billing-admin backup-mode: want 403, got %d", code)
	}
}

// TestRBAC_ReadsNotGated: reads (members list) are allowed for a plain member.
func TestRBAC_ReadsNotGated(t *testing.T) {
	member := newRBACRouter("member")
	if code := doReq(t, member, http.MethodGet, "/api/org/members", nil); code != http.StatusOK {
		t.Errorf("member listing members (read): want 200, got %d", code)
	}
}
