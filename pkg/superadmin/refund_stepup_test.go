package superadmin_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vul-os/vulos-management/pkg/superadmin"
)

// TestRefundHandler_StepUpEnforced proves the M3 fix: when VULOS_ADMIN_REAUTH_TOTP
// is on, the JSON refund endpoint requires the operator's step-up TOTP just like
// the HTML confirm path — a request WITHOUT a valid code is rejected 401 before
// the refund runs (no longer a step-up bypass for money movement).
func TestRefundHandler_StepUpEnforced(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	t.Setenv("VULOS_ADMIN_REAUTH_TOTP", "1")
	t.Setenv("PAYSTACK_SECRET_KEY", "")

	// Create an account and a billing_transactions row it owns, so ownership would
	// otherwise pass — proving step-up is checked FIRST.
	acctID := createUser(t, authStore, "refund-stepup@test.example", "password-secure-99")
	if _, err := saStore.DB().Exec(
		`CREATE TABLE IF NOT EXISTS billing_transactions (
			txn_id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			amount_zar_cents INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'success',
			created_at TEXT NOT NULL DEFAULT ''
		)`,
	); err != nil {
		t.Skipf("cannot create billing_transactions: %v", err)
	}
	if _, err := saStore.DB().Exec(
		`INSERT OR IGNORE INTO billing_transactions (txn_id, account_id, amount_zar_cents, status, created_at)
		 VALUES ('txn_stepup', ?, 100, 'success', '2026-01-01T00:00:00Z')`, acctID,
	); err != nil {
		t.Fatalf("insert billing row: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("POST /api/superadmin/accounts/{id}/refund",
		superadmin.HandleRefund(saStore, authStore, al))

	// No totp supplied.
	req := httptest.NewRequest("POST", "/api/superadmin/accounts/"+acctID+"/refund?txn_id=txn_stepup", nil)
	req = req.WithContext(context.WithValue(req.Context(), superadmin.ExportedCtxAdminAccountID, "actor-id"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (step-up required), got %d: %s", rr.Code, rr.Body.String())
	}
}
