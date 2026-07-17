// admins.go — admin-team management: an existing admin can grant another
// account admin access (promote it) and revoke it, from the console, with no DB
// surgery. Self-contained in this OSS repo so it works identically for a
// self-hoster running vulos-management and for the Vulos team on cloud.
//
// SECURITY MODEL
//   - Every handler here is mounted behind RequireSuperAdmin (main session + IP
//     allowlist + a WebAuthn-backed admin-session cookie), so the caller is
//     already a hardware-key-authenticated admin.
//   - Mutations additionally honour the operator TOTP step-up
//     (VULOS_ADMIN_REAUTH_TOTP=1) exactly like the refund path, so promoting or
//     demoting a teammate can require a fresh TOTP code.
//   - Every grant/revoke is written to the hash-chained audit log.
//   - The last remaining admin can never be revoked (never lock everyone out).
package superadmin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/auth"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────────

var (
	// ErrAdminAccountNotFound is returned when a grant targets an email that has
	// no Vulos account (an account must exist before it can be promoted).
	ErrAdminAccountNotFound = errors.New("superadmin: no account found for that email")
	// ErrLastAdmin is returned when a revoke would remove the last remaining
	// active admin (which would lock everyone out of the console).
	ErrLastAdmin = errors.New("superadmin: cannot revoke the last remaining admin")
	// ErrNotAnAdmin is returned when a revoke targets an account that is not an
	// active admin.
	ErrNotAnAdmin = errors.New("superadmin: account is not an active admin")
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// AdminRow is one active admin, as shown on the console's Admins page.
type AdminRow struct {
	AccountID       string `json:"account_id"`
	Email           string `json:"email"`
	PromotedAt      string `json:"promoted_at"`
	PromotedBy      string `json:"promoted_by,omitempty"`       // promoter account_id ("" for bootstrap)
	PromotedByEmail string `json:"promoted_by_email,omitempty"` // promoter email, resolved for display
	Bootstrap       bool   `json:"bootstrap"`                   // true when promoted_by IS NULL (seeded/bootstrapped)
}

// ─────────────────────────────────────────────────────────────────────────────
// Store methods
// ─────────────────────────────────────────────────────────────────────────────

// ListAdmins returns every active (not revoked) admin, oldest promotion first,
// with the promoter's email resolved for display.
func (s *Store) ListAdmins(ctx context.Context) ([]AdminRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT sa.account_id, u.email, sa.promoted_at, COALESCE(sa.promoted_by, ''), COALESCE(pu.email, '')
		   FROM superadmins sa
		   JOIN users u  ON u.id  = sa.account_id
		   LEFT JOIN users pu ON pu.id = sa.promoted_by
		  WHERE sa.revoked_at IS NULL
		  ORDER BY sa.promoted_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("superadmin: list admins: %w", err)
	}
	defer rows.Close()

	var out []AdminRow
	for rows.Next() {
		var a AdminRow
		if err := rows.Scan(&a.AccountID, &a.Email, &a.PromotedAt, &a.PromotedBy, &a.PromotedByEmail); err != nil {
			return nil, fmt.Errorf("superadmin: scan admin: %w", err)
		}
		a.Bootstrap = a.PromotedBy == ""
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("superadmin: list admins rows: %w", err)
	}
	if out == nil {
		out = []AdminRow{}
	}
	return out, nil
}

// CountActiveAdmins returns how many active (not revoked) admins exist.
func (s *Store) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM superadmins WHERE revoked_at IS NULL`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("superadmin: count active admins: %w", err)
	}
	return n, nil
}

// GrantAdminByEmail promotes an EXISTING account (looked up by email) to admin.
// If the account was previously revoked, its row is re-activated (revoked_at
// cleared, promoted_at/promoted_by refreshed). Idempotent for an already-active
// admin. Returns the resulting AdminRow.
func (s *Store) GrantAdminByEmail(ctx context.Context, email, actorAccountID, actorEmail string, al *auditlog.Logger) (AdminRow, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return AdminRow{}, fmt.Errorf("superadmin: grant: email required")
	}

	var accountID string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id FROM users WHERE email = ?`), email).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminRow{}, ErrAdminAccountNotFound
	}
	if err != nil {
		return AdminRow{}, fmt.Errorf("superadmin: grant lookup: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO superadmins (account_id, promoted_at, promoted_by, revoked_at)
		 VALUES (?, ?, ?, NULL)
		 ON CONFLICT (account_id) DO UPDATE SET
		     promoted_at = excluded.promoted_at,
		     promoted_by = excluded.promoted_by,
		     revoked_at  = NULL`),
		accountID, now, actorAccountID,
	)
	s.mu.Unlock()
	if err != nil {
		return AdminRow{}, fmt.Errorf("superadmin: grant insert: %w", err)
	}

	auditAction(ctx, al, actorEmail, "admin.grant", accountID,
		map[string]string{"email": email, "granted_by": actorAccountID})

	return AdminRow{
		AccountID:       accountID,
		Email:           email,
		PromotedAt:      now,
		PromotedBy:      actorAccountID,
		PromotedByEmail: actorEmail,
		Bootstrap:       actorAccountID == "",
	}, nil
}

// RevokeAdmin removes admin access from targetAccountID. It fail-closes on the
// last-admin guard (never lock everyone out) and, on success, also deletes the
// target's live admin sessions so the revocation takes effect immediately rather
// than lingering until the session's idle timeout.
func (s *Store) RevokeAdmin(ctx context.Context, targetAccountID, actorAccountID, actorEmail string, al *auditlog.Logger) error {
	targetAccountID = strings.TrimSpace(targetAccountID)
	if targetAccountID == "" {
		return fmt.Errorf("superadmin: revoke: account id required")
	}

	// The target must currently be an active admin.
	active, err := s.IsSuperAdmin(ctx, targetAccountID)
	if err != nil {
		return err
	}
	if !active {
		return ErrNotAnAdmin
	}

	// Last-admin guard: refuse if this is the only active admin left.
	count, err := s.CountActiveAdmins(ctx)
	if err != nil {
		return err
	}
	if count <= 1 {
		return ErrLastAdmin
	}

	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE superadmins SET revoked_at = ? WHERE account_id = ? AND revoked_at IS NULL`),
		now, targetAccountID,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("superadmin: revoke: %w", err)
	}

	// Immediately terminate the revoked admin's live admin sessions.
	s.mu.Lock()
	_, _ = s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM admin_sessions WHERE account_id = ?`), targetAccountID)
	s.mu.Unlock()

	auditAction(ctx, al, actorEmail, "admin.revoke", targetAccountID,
		map[string]string{"revoked_by": actorAccountID})
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP handlers
// ─────────────────────────────────────────────────────────────────────────────

// HandleListAdmins handles GET /api/superadmin/admins — the current admin team.
func HandleListAdmins(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		admins, err := store.ListAdmins(r.Context())
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"admins": admins, "count": len(admins)})
	}
}

// HandleGrantAdmin handles POST /api/superadmin/admins — promote an existing
// account (by email) to admin. Body: {"email":"…","totp":"…"}. The optional TOTP
// satisfies the operator step-up when VULOS_ADMIN_REAUTH_TOTP=1.
func HandleGrantAdmin(store *Store, authStore *auth.Store, al *auditlog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email string `json:"email"`
			TOTP  string `json:"totp"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&body)
		email := strings.ToLower(strings.TrimSpace(body.Email))
		if email == "" {
			jsonError(w, "email required", http.StatusBadRequest)
			return
		}

		actor := AdminAccountIDFromCtx(r.Context())
		actorEmail := store.lookupEmail(r.Context(), actor)

		if !verifyStepUpTOTP(r.Context(), authStore, actor, strings.TrimSpace(body.TOTP)) {
			auditAction(r.Context(), al, actorEmail, "admin.grant.stepup_failed", email, nil)
			jsonError(w, "step-up verification failed", http.StatusUnauthorized)
			return
		}

		row, err := store.GrantAdminByEmail(r.Context(), email, actor, actorEmail, al)
		if err != nil {
			if errors.Is(err, ErrAdminAccountNotFound) {
				jsonError(w, "no account found for that email — the person must sign up first", http.StatusUnprocessableEntity)
				return
			}
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(row)
	}
}

// HandleRevokeAdmin handles DELETE /api/superadmin/admins/{id} — remove admin
// access. The optional TOTP (query ?totp= or JSON body {"totp":"…"}) satisfies
// the operator step-up when VULOS_ADMIN_REAUTH_TOTP=1.
func HandleRevokeAdmin(store *Store, authStore *auth.Store, al *auditlog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target := r.PathValue("id")
		actor := AdminAccountIDFromCtx(r.Context())
		actorEmail := store.lookupEmail(r.Context(), actor)

		if !verifyStepUpTOTP(r.Context(), authStore, actor, refundTOTPCode(r)) {
			auditAction(r.Context(), al, actorEmail, "admin.revoke.stepup_failed", target, nil)
			jsonError(w, "step-up verification failed", http.StatusUnauthorized)
			return
		}

		err := store.RevokeAdmin(r.Context(), target, actor, actorEmail, al)
		switch {
		case errors.Is(err, ErrLastAdmin):
			jsonError(w, "cannot revoke the last remaining admin", http.StatusConflict)
			return
		case errors.Is(err, ErrNotAnAdmin):
			jsonError(w, "that account is not an admin", http.StatusNotFound)
			return
		case err != nil:
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// lookupEmail resolves an account_id to its email for audit-log actor labelling.
// Best-effort: returns "" if the row is missing.
func (s *Store) lookupEmail(ctx context.Context, accountID string) string {
	if accountID == "" {
		return ""
	}
	var email string
	_ = s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT email FROM users WHERE id = ?`), accountID).Scan(&email)
	return email
}
