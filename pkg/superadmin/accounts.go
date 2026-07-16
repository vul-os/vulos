package superadmin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/auth"
)

// ─────────────────────────────────────────────────────────────────────────────
// Account types
// ─────────────────────────────────────────────────────────────────────────────

// AccountRow is the paged-list view of an account.
type AccountRow struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Suspended   bool   `json:"suspended"`
	FleetAdmin  bool   `json:"fleet_admin"`
	CreatedAt   string `json:"created_at"`
	LastLoginAt string `json:"last_login_at,omitempty"`
}

// AccountDetail is the full detail view.
type AccountDetail struct {
	AccountRow
	EmailVerified bool     `json:"email_verified"`
	TOTPEnabled   bool     `json:"totp_enabled"`
	Sessions      int      `json:"active_sessions"`
	Transactions  []TxnRow `json:"transactions"`
}

// TxnRow is a billing transaction summary for the detail page.
type TxnRow struct {
	TxnID     string `json:"txn_id"`
	AmountZAR int64  `json:"amount_zar_cents"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Store methods
// ─────────────────────────────────────────────────────────────────────────────

// SearchAccounts returns a paged list of accounts matching q (handle/email/id).
func (s *Store) SearchAccounts(ctx context.Context, q string, limit, offset int) ([]AccountRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	like := "%" + q + "%"
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, email,
		        COALESCE(suspended, 0),
		        COALESCE(fleet_admin, 0),
		        created_at,
		        ''
		 FROM users
		 WHERE id LIKE ? OR email LIKE ?
		 ORDER BY created_at DESC
		 LIMIT ? OFFSET ?`),
		like, like, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("superadmin: search accounts: %w", err)
	}
	defer rows.Close()

	var out []AccountRow
	for rows.Next() {
		var a AccountRow
		var suspended, fleetAdmin int
		if err := rows.Scan(&a.ID, &a.Email, &suspended, &fleetAdmin, &a.CreatedAt, &a.LastLoginAt); err != nil {
			return nil, fmt.Errorf("superadmin: scan account: %w", err)
		}
		a.Suspended = suspended != 0
		a.FleetAdmin = fleetAdmin != 0
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("superadmin: search accounts rows: %w", err)
	}
	if out == nil {
		out = []AccountRow{}
	}
	return out, nil
}

// AccountIDRow is a minimal (id, suspended) account identity used by the
// fleet-wide billing rollup so it can sum usage per account and count
// suspensions without loading full detail rows.
type AccountIDRow struct {
	ID        string
	Suspended bool
}

// AllAccountIDs returns the (id, suspended) of every account. Used by the
// fleet-wide super-admin billing cockpit to iterate accounts. Capped at a
// generous limit so a pathological account table can't hang the dashboard.
func (s *Store) AllAccountIDs(ctx context.Context) ([]AccountIDRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, COALESCE(suspended, 0) FROM users ORDER BY created_at DESC LIMIT 100000`)
	if err != nil {
		return nil, fmt.Errorf("superadmin: all account ids: %w", err)
	}
	defer rows.Close()
	var out []AccountIDRow
	for rows.Next() {
		var a AccountIDRow
		var susp int
		if err := rows.Scan(&a.ID, &susp); err != nil {
			return nil, fmt.Errorf("superadmin: scan account id: %w", err)
		}
		a.Suspended = susp != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAccountDetail returns full account detail for id.
func (s *Store) GetAccountDetail(ctx context.Context, accountID string) (*AccountDetail, error) {
	var d AccountDetail
	var suspended, fleetAdmin, emailVerified, totpEnabled int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id, email,
		        COALESCE(suspended, 0),
		        COALESCE(fleet_admin, 0),
		        COALESCE(email_verified, 0),
		        COALESCE(totp_enabled, 0),
		        created_at
		 FROM users WHERE id = ?`), accountID,
	).Scan(&d.ID, &d.Email, &suspended, &fleetAdmin, &emailVerified, &totpEnabled, &d.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("superadmin: account not found: %s", accountID)
	}
	if err != nil {
		return nil, fmt.Errorf("superadmin: get account detail: %w", err)
	}
	d.Suspended = suspended != 0
	d.FleetAdmin = fleetAdmin != 0
	d.EmailVerified = emailVerified != 0
	d.TOTPEnabled = totpEnabled != 0

	// Active session count.
	_ = s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM sessions
		 WHERE user_id = ? AND revoked = 0 AND partial = 0 AND expires_at > ?`),
		accountID, time.Now().UTC().Format(time.RFC3339),
	).Scan(&d.Sessions)

	// Recent transactions (best-effort; billing table may not exist).
	txnRows, _ := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT txn_id, amount_zar_cents, status, created_at
		 FROM billing_transactions WHERE account_id = ?
		 ORDER BY created_at DESC LIMIT 20`),
		accountID,
	)
	if txnRows != nil {
		defer txnRows.Close()
		for txnRows.Next() {
			var t TxnRow
			if err := txnRows.Scan(&t.TxnID, &t.AmountZAR, &t.Status, &t.CreatedAt); err == nil {
				d.Transactions = append(d.Transactions, t)
			}
		}
	}
	if d.Transactions == nil {
		d.Transactions = []TxnRow{}
	}
	return &d, nil
}

// SuspendAccount sets suspended=1 for accountID.
func (s *Store) SuspendAccount(ctx context.Context, accountID, reason, actorEmail string, al *auditlog.Logger) error {
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET suspended = 1, updated_at = ? WHERE id = ?`),
		time.Now().UTC().Format(time.RFC3339), accountID,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("superadmin: suspend account: %w", err)
	}
	auditAction(ctx, al, actorEmail, "admin.account.suspend", accountID,
		map[string]string{"reason": reason})
	return nil
}

// UnsuspendAccount clears suspended for accountID.
func (s *Store) UnsuspendAccount(ctx context.Context, accountID, actorEmail string, al *auditlog.Logger) error {
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET suspended = 0, updated_at = ? WHERE id = ?`),
		time.Now().UTC().Format(time.RFC3339), accountID,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("superadmin: unsuspend account: %w", err)
	}
	auditAction(ctx, al, actorEmail, "admin.account.unsuspend", accountID, nil)
	return nil
}

// ForcePasswordReset triggers RequestPasswordResetByHandle for the user's
// Vulos account handle. Admin never sees the reset token.
func (s *Store) ForcePasswordReset(ctx context.Context, accountID, actorEmail string,
	authStore *auth.Store, sender auth.InboxSender, al *auditlog.Logger) error {

	var email string
	if err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT email FROM users WHERE id = ?`), accountID,
	).Scan(&email); err != nil {
		return fmt.Errorf("superadmin: force reset: lookup email: %w", err)
	}

	if err := authStore.RequestPasswordResetByHandle(ctx, email, sender); err != nil {
		return fmt.Errorf("superadmin: force reset: %w", err)
	}
	auditAction(ctx, al, actorEmail, "admin.account.force_password_reset", accountID,
		map[string]string{"email": email})
	return nil
}

// Reset2FA clears the TOTP secret for the account (forces re-enrollment).
func (s *Store) Reset2FA(ctx context.Context, accountID, actorEmail string, al *auditlog.Logger) error {
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET totp_secret_enc = NULL, totp_enabled = 0, updated_at = ? WHERE id = ?`),
		time.Now().UTC().Format(time.RFC3339), accountID,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("superadmin: reset 2fa: %w", err)
	}
	auditAction(ctx, al, actorEmail, "admin.account.reset_2fa", accountID, nil)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP handlers — account management
// ─────────────────────────────────────────────────────────────────────────────

// HandleListAccounts handles GET /api/superadmin/accounts
func HandleListAccounts(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if limit == 0 {
			limit = 50
		}
		accounts, err := store.SearchAccounts(r.Context(), q, limit, offset)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accounts": accounts, "limit": limit, "offset": offset,
		})
	}
}

// HandleGetAccount handles GET /api/superadmin/accounts/{id}
func HandleGetAccount(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		detail, err := store.GetAccountDetail(r.Context(), id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(detail)
	}
}

// HandleSuspendAccount handles POST /api/superadmin/accounts/{id}/suspend
func HandleSuspendAccount(store *Store, al *auditlog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		actor := AdminAccountIDFromCtx(r.Context())
		var body struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// Get actor email for audit log.
		var actorEmail string
		_ = store.db.QueryRowContext(r.Context(), store.db.Rebind(`SELECT email FROM users WHERE id = ?`), actor).Scan(&actorEmail)

		if err := store.SuspendAccount(r.Context(), id, body.Reason, actorEmail, al); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleUnsuspendAccount handles POST /api/superadmin/accounts/{id}/unsuspend
func HandleUnsuspendAccount(store *Store, al *auditlog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		actor := AdminAccountIDFromCtx(r.Context())
		var actorEmail string
		_ = store.db.QueryRowContext(r.Context(), store.db.Rebind(`SELECT email FROM users WHERE id = ?`), actor).Scan(&actorEmail)

		if err := store.UnsuspendAccount(r.Context(), id, actorEmail, al); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleForcePasswordReset handles POST /api/superadmin/accounts/{id}/force-password-reset
func HandleForcePasswordReset(store *Store, authStore *auth.Store, sender auth.InboxSender, al *auditlog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		actor := AdminAccountIDFromCtx(r.Context())
		var actorEmail string
		_ = store.db.QueryRowContext(r.Context(), store.db.Rebind(`SELECT email FROM users WHERE id = ?`), actor).Scan(&actorEmail)

		if err := store.ForcePasswordReset(r.Context(), id, actorEmail, authStore, sender, al); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

// HandleReset2FA handles POST /api/superadmin/accounts/{id}/2fa-reset
func HandleReset2FA(store *Store, al *auditlog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		actor := AdminAccountIDFromCtx(r.Context())
		var actorEmail string
		_ = store.db.QueryRowContext(r.Context(), store.db.Rebind(`SELECT email FROM users WHERE id = ?`), actor).Scan(&actorEmail)

		if err := store.Reset2FA(r.Context(), id, actorEmail, al); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleRefund handles POST /api/superadmin/accounts/{id}/refund?txn_id=<x>
// Calls the injected RefundProcessor seam and records the action.
//
// AUTHZ: txn_id is verified to belong to the account identified by {id} before
// calling the processor. This prevents an admin from using one account's URL path
// to trigger a refund against an unrelated transaction reference.
func HandleRefund(store *Store, al *auditlog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		txnID := r.URL.Query().Get("txn_id")
		if txnID == "" {
			jsonError(w, "txn_id required", http.StatusBadRequest)
			return
		}
		actor := AdminAccountIDFromCtx(r.Context())
		var actorEmail string
		_ = store.db.QueryRowContext(r.Context(), store.db.Rebind(`SELECT email FROM users WHERE id = ?`), actor).Scan(&actorEmail)

		// Verify the txn_id belongs to the account in the URL path.
		// billing_transactions may not exist in all environments; if the table is
		// absent we err on the safe side (deny the refund).
		var ownerCount int
		checkErr := store.db.QueryRowContext(r.Context(),
			store.db.Rebind(`SELECT COUNT(*) FROM billing_transactions WHERE txn_id = ? AND account_id = ?`),
			txnID, id,
		).Scan(&ownerCount)
		if checkErr != nil || ownerCount == 0 {
			auditAction(r.Context(), al, actorEmail, "admin.billing.refund.denied", id,
				map[string]string{"txn_id": txnID, "reason": "txn_id does not belong to account"})
			jsonError(w, "transaction not found for this account", http.StatusUnprocessableEntity)
			return
		}

		result, err := RefundProcessor(r.Context(), txnID)
		if err != nil {
			auditAction(r.Context(), al, actorEmail, "admin.billing.refund.failed", id,
				map[string]string{"txn_id": txnID, "error": err.Error()})
			jsonError(w, err.Error(), http.StatusBadGateway)
			return
		}
		auditAction(r.Context(), al, actorEmail, "admin.billing.refund", id,
			map[string]string{"txn_id": txnID})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}
