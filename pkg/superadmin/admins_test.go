package superadmin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/superadmin"
)

// grantable creates two signed-up accounts and promotes the first to admin,
// returning (adminID, adminEmail, otherID, otherEmail).
func twoAccountsOneAdmin(t *testing.T) (string, string, string, string, *superadmin.Store) {
	t.Helper()
	authStore, saStore, _ := openTestStores(t)
	adminEmail := "root@test.example"
	otherEmail := "teammate@test.example"
	adminID := createUser(t, authStore, adminEmail, "test-password-123456")
	otherID := createUser(t, authStore, otherEmail, "test-password-123456")
	promoteSuperAdmin(t, authStore, adminID)
	return adminID, adminEmail, otherID, otherEmail, saStore
}

// Test: GrantAdminByEmail promotes an existing account.
func TestGrantAdminByEmail_Promotes(t *testing.T) {
	adminID, adminEmail, otherID, otherEmail, saStore := twoAccountsOneAdmin(t)
	ctx := context.Background()

	row, err := saStore.GrantAdminByEmail(ctx, otherEmail, adminID, adminEmail, nil)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if row.AccountID != otherID || row.PromotedBy != adminID || row.Bootstrap {
		t.Fatalf("unexpected row: %+v", row)
	}
	isSA, _ := saStore.IsSuperAdmin(ctx, otherID)
	if !isSA {
		t.Fatal("expected teammate to be admin after grant")
	}
	admins, err := saStore.ListAdmins(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(admins) != 2 {
		t.Fatalf("expected 2 admins, got %d", len(admins))
	}
	// The bootstrapped admin has no promoter; the granted one records its promoter.
	var found bool
	for _, a := range admins {
		if a.AccountID == otherID {
			found = true
			if a.PromotedByEmail != adminEmail {
				t.Fatalf("expected promoter email %q, got %q", adminEmail, a.PromotedByEmail)
			}
		}
	}
	if !found {
		t.Fatal("granted admin missing from list")
	}
}

// Test: grant is case-insensitive on email and reactivates a revoked admin.
func TestGrantAdminByEmail_ReactivatesAfterRevoke(t *testing.T) {
	adminID, adminEmail, otherID, otherEmail, saStore := twoAccountsOneAdmin(t)
	ctx := context.Background()

	if _, err := saStore.GrantAdminByEmail(ctx, otherEmail, adminID, adminEmail, nil); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := saStore.RevokeAdmin(ctx, otherID, adminID, adminEmail, nil); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if isSA, _ := saStore.IsSuperAdmin(ctx, otherID); isSA {
		t.Fatal("expected teammate revoked")
	}
	// Re-grant with a differently-cased email reactivates the same row.
	if _, err := saStore.GrantAdminByEmail(ctx, strings.ToUpper(otherEmail), adminID, adminEmail, nil); err != nil {
		t.Fatalf("re-grant: %v", err)
	}
	if isSA, _ := saStore.IsSuperAdmin(ctx, otherID); !isSA {
		t.Fatal("expected teammate re-activated")
	}
}

// Test: granting an unknown email is rejected (account must exist first).
func TestGrantAdminByEmail_UnknownEmail(t *testing.T) {
	adminID, adminEmail, _, _, saStore := twoAccountsOneAdmin(t)
	_, err := saStore.GrantAdminByEmail(context.Background(), "nobody@nowhere.example", adminID, adminEmail, nil)
	if !errors.Is(err, superadmin.ErrAdminAccountNotFound) {
		t.Fatalf("expected ErrAdminAccountNotFound, got %v", err)
	}
}

// Test: the last remaining admin can never be revoked (no lockout).
func TestRevokeAdmin_LastAdminGuard(t *testing.T) {
	adminID, adminEmail, _, _, saStore := twoAccountsOneAdmin(t)
	ctx := context.Background()

	err := saStore.RevokeAdmin(ctx, adminID, adminID, adminEmail, nil)
	if !errors.Is(err, superadmin.ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin, got %v", err)
	}
	if isSA, _ := saStore.IsSuperAdmin(ctx, adminID); !isSA {
		t.Fatal("last admin must remain an admin after a blocked revoke")
	}
}

// Test: revoking a non-admin account is rejected.
func TestRevokeAdmin_NotAnAdmin(t *testing.T) {
	adminID, adminEmail, otherID, _, saStore := twoAccountsOneAdmin(t)
	err := saStore.RevokeAdmin(context.Background(), otherID, adminID, adminEmail, nil)
	if !errors.Is(err, superadmin.ErrNotAnAdmin) {
		t.Fatalf("expected ErrNotAnAdmin, got %v", err)
	}
}

// Test: revoke succeeds when more than one admin exists, and it terminates the
// revoked admin's live admin sessions immediately.
func TestRevokeAdmin_WorksAndKillsSessions(t *testing.T) {
	adminID, adminEmail, otherID, otherEmail, saStore := twoAccountsOneAdmin(t)
	ctx := context.Background()

	if _, err := saStore.GrantAdminByEmail(ctx, otherEmail, adminID, adminEmail, nil); err != nil {
		t.Fatalf("grant: %v", err)
	}
	// Give the teammate a live admin session.
	tok, err := saStore.CreateAdminSession(ctx, otherID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	if _, err := saStore.LookupAdminSession(ctx, tok); err != nil {
		t.Fatalf("session should be valid pre-revoke: %v", err)
	}

	if err := saStore.RevokeAdmin(ctx, otherID, adminID, adminEmail, nil); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if isSA, _ := saStore.IsSuperAdmin(ctx, otherID); isSA {
		t.Fatal("teammate should no longer be admin")
	}
	if _, err := saStore.LookupAdminSession(ctx, tok); err == nil {
		t.Fatal("revoked admin's session should have been terminated")
	}
	n, _ := saStore.CountActiveAdmins(ctx)
	if n != 1 {
		t.Fatalf("expected 1 active admin after revoke, got %d", n)
	}
}

// ── Handler-level tests ───────────────────────────────────────────────────────

// Test: the grant handler promotes by email (step-up off by default).
func TestHandleGrantAdmin_HTTP(t *testing.T) {
	adminID, _, otherID, otherEmail, saStore := twoAccountsOneAdmin(t)

	body, _ := json.Marshal(map[string]string{"email": otherEmail})
	req := adminCtx(httptest.NewRequest(http.MethodPost, "/api/superadmin/admins", strings.NewReader(string(body))), adminID)
	rr := httptest.NewRecorder()
	superadmin.HandleGrantAdmin(saStore, nil, nil)(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", rr.Code, rr.Body.String())
	}
	if isSA, _ := saStore.IsSuperAdmin(context.Background(), otherID); !isSA {
		t.Fatal("grant handler did not promote account")
	}
}

// Test: the revoke handler returns 409 for the last-admin guard.
func TestHandleRevokeAdmin_LastAdmin409(t *testing.T) {
	adminID, _, _, _, saStore := twoAccountsOneAdmin(t)

	req := adminCtx(httptest.NewRequest(http.MethodDelete, "/api/superadmin/admins/"+adminID, nil), adminID)
	req.SetPathValue("id", adminID)
	rr := httptest.NewRecorder()
	superadmin.HandleRevokeAdmin(saStore, nil, nil)(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 for last-admin revoke, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// Test: with step-up required and no TOTP supplied, the grant handler rejects.
func TestHandleGrantAdmin_StepUpRequired(t *testing.T) {
	adminID, _, _, otherEmail, saStore := twoAccountsOneAdmin(t)

	os.Setenv("VULOS_ADMIN_REAUTH_TOTP", "1")
	defer os.Unsetenv("VULOS_ADMIN_REAUTH_TOTP")

	body, _ := json.Marshal(map[string]string{"email": otherEmail}) // no totp
	req := adminCtx(httptest.NewRequest(http.MethodPost, "/api/superadmin/admins", strings.NewReader(string(body))), adminID)
	rr := httptest.NewRecorder()
	superadmin.HandleGrantAdmin(saStore, nil, nil)(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when step-up TOTP missing, got %d", rr.Code)
	}
}

// Test: with step-up required and no TOTP supplied, the REVOKE handler rejects —
// and the revoke does NOT take effect. A second admin is promoted first so the
// last-admin guard cannot be the cause of the 401 (proving the step-up gate is
// what fires, and that admin demotion is step-up-protected like promotion).
func TestHandleRevokeAdmin_StepUpRequired(t *testing.T) {
	adminID, adminEmail, otherID, otherEmail, saStore := twoAccountsOneAdmin(t)
	if _, err := saStore.GrantAdminByEmail(context.Background(), otherEmail, adminID, adminEmail, nil); err != nil {
		t.Fatalf("promote second admin: %v", err)
	}

	os.Setenv("VULOS_ADMIN_REAUTH_TOTP", "1")
	defer os.Unsetenv("VULOS_ADMIN_REAUTH_TOTP")

	req := adminCtx(httptest.NewRequest(http.MethodDelete, "/api/superadmin/admins/"+otherID, nil), adminID)
	req.SetPathValue("id", otherID)
	rr := httptest.NewRecorder()
	superadmin.HandleRevokeAdmin(saStore, nil, nil)(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when step-up TOTP missing on revoke, got %d (%s)", rr.Code, rr.Body.String())
	}
	if isSA, _ := saStore.IsSuperAdmin(context.Background(), otherID); !isSA {
		t.Fatal("revoke took effect despite a failed step-up — demotion must be step-up-gated")
	}
}

// ── Handler-level error-mapping branches (authz + fail-closed HTTP surface) ───

// Test: the list handler returns the current admin team as JSON with a count.
func TestHandleListAdmins_HTTP(t *testing.T) {
	adminID, _, _, _, saStore := twoAccountsOneAdmin(t)

	req := adminCtx(httptest.NewRequest(http.MethodGet, "/api/superadmin/admins", nil), adminID)
	rr := httptest.NewRecorder()
	superadmin.HandleListAdmins(saStore)(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("list admins = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Admins []map[string]any `json:"admins"`
		Count  int              `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode list body: %v (%s)", err, rr.Body.String())
	}
	if resp.Count != 1 || len(resp.Admins) != 1 {
		t.Fatalf("list count=%d admins=%d, want 1/1", resp.Count, len(resp.Admins))
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (admin data must not be cached)", got)
	}
}

// Test: the grant handler rejects an empty email with 400 (never reaches the
// store lookup).
func TestHandleGrantAdmin_EmptyEmail400(t *testing.T) {
	adminID, _, _, _, saStore := twoAccountsOneAdmin(t)

	req := adminCtx(httptest.NewRequest(http.MethodPost, "/api/superadmin/admins", strings.NewReader(`{"email":"  "}`)), adminID)
	rr := httptest.NewRecorder()
	superadmin.HandleGrantAdmin(saStore, nil, nil)(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("grant empty email = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
}

// Test: granting an email that maps to no account is 422 (the person must sign
// up first) — the caller cannot promote a non-existent account.
func TestHandleGrantAdmin_UnknownEmail422(t *testing.T) {
	adminID, _, _, _, saStore := twoAccountsOneAdmin(t)

	body, _ := json.Marshal(map[string]string{"email": "ghost@nowhere.example"})
	req := adminCtx(httptest.NewRequest(http.MethodPost, "/api/superadmin/admins", strings.NewReader(string(body))), adminID)
	rr := httptest.NewRecorder()
	superadmin.HandleGrantAdmin(saStore, nil, nil)(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("grant unknown email = %d, want 422 (%s)", rr.Code, rr.Body.String())
	}
}

// Test: revoking an account that is not an admin is 404 — a no-op cannot be
// confused with success, and the last-admin guard is not the cause here.
func TestHandleRevokeAdmin_NotAnAdmin404(t *testing.T) {
	adminID, _, otherID, _, saStore := twoAccountsOneAdmin(t)

	// otherID is a plain account (never promoted). Revoking it must be 404.
	req := adminCtx(httptest.NewRequest(http.MethodDelete, "/api/superadmin/admins/"+otherID, nil), adminID)
	req.SetPathValue("id", otherID)
	rr := httptest.NewRecorder()
	superadmin.HandleRevokeAdmin(saStore, nil, nil)(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("revoke non-admin = %d, want 404 (%s)", rr.Code, rr.Body.String())
	}
}
