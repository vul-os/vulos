// actions.go — server-rendered, CSRF-protected, step-up confirm flow for
// state-changing super-admin actions.
//
// Every destructive or fleet-affecting action (suspend, force-password-reset,
// 2FA reset, refund, reserved-handle delete) is a two-step flow that works
// under the strict CSP with NO inline JS:
//
//  1. GET  /superadmin/.../confirm  — renders a confirmation page describing
//     exactly what will happen, with a CSRF-protected POST form (and, when
//     enabled, an operator TOTP re-entry field).
//  2. POST /superadmin/...          — verifies CSRF (+ optional TOTP), performs
//     the action via the Store, and redirects back with a ?flash=/?err= banner.
//
// The non-destructive unsuspend is a single CSRF-protected POST (no confirm
// page) but flows through the same execute handler.
package superadmin

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/vul-os/vulos-management/pkg/auth"
)

// confirmKV is a hidden form field carried through the confirm POST.
type confirmKV struct{ Name, Value string }

// confirmData drives the shared confirm.html.tmpl template.
type confirmData struct {
	Heading      string
	Lines        []string
	Warn         string
	ActionURL    string
	Hidden       []confirmKV
	CSRFToken    string
	CancelURL    string
	ConfirmLabel string
	Danger       bool
	RequireTOTP  bool
}

// stepUpTOTPRequired reports whether the operator must re-enter a TOTP code on
// destructive confirm pages. Off by default (so the two-step confirm itself is
// the step-up); set VULOS_ADMIN_REAUTH_TOTP=1 to require it in production.
func stepUpTOTPRequired() bool {
	return os.Getenv("VULOS_ADMIN_REAUTH_TOTP") == "1"
}

// redirectFlash redirects to dest with a success (?flash=) banner.
func redirectFlash(w http.ResponseWriter, r *http.Request, dest, msg string) {
	http.Redirect(w, r, dest+"?flash="+url.QueryEscape(msg), http.StatusSeeOther)
}

// redirectErr redirects to dest with an error (?err=) banner.
func redirectErr(w http.ResponseWriter, r *http.Request, dest, msg string) {
	http.Redirect(w, r, dest+"?err="+url.QueryEscape(msg), http.StatusSeeOther)
}

// verifyStepUpTOTP validates an operator-supplied TOTP code against their own
// enrolled secret. Returns true when step-up is not required.
func (p *Pages) verifyStepUpTOTP(r *http.Request, code string) bool {
	if !stepUpTOTPRequired() {
		return true
	}
	accountID := AdminAccountIDFromCtx(r.Context())
	if accountID == "" || code == "" {
		return false
	}
	encSecret, enabled, err := p.auth.LoadTOTPSecret(r.Context(), accountID)
	if err != nil || !enabled || encSecret == nil {
		return false
	}
	kek, err := auth.LoadTOTPKEK()
	if err != nil {
		return false
	}
	plain, err := auth.DecryptTOTPSecret(encSecret, kek)
	if err != nil {
		return false
	}
	return auth.VerifyTOTPAndRecord(plain, strings.TrimSpace(code), accountID)
}

// ─── Account actions ─────────────────────────────────────────────────────────

// AccountActionConfirm renders the confirm page for a destructive account
// action: GET /superadmin/accounts/{id}/confirm?action=...
func (p *Pages) AccountActionConfirm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	action := r.URL.Query().Get("action")
	detail, err := p.s.GetAccountDetail(r.Context(), id)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	back := "/superadmin/accounts/" + id

	d := confirmData{
		ActionURL:    back + "/action",
		CancelURL:    back,
		CSRFToken:    p.csrf(w, r),
		ConfirmLabel: "Confirm",
		Danger:       true,
		RequireTOTP:  stepUpTOTPRequired(),
		Hidden:       []confirmKV{{Name: "action", Value: action}},
	}

	switch action {
	case "suspend":
		d.Heading = "Suspend account"
		d.Lines = []string{
			"Account: " + detail.Email,
			"This blocks the user from logging in and gates ALL products immediately.",
		}
		d.Warn = "The user will lose access until unsuspended."
		d.ConfirmLabel = "Suspend account"
		d.Hidden = append(d.Hidden, confirmKV{Name: "reason", Value: "admin-initiated"})
	case "force-password-reset":
		d.Heading = "Force password reset"
		d.Lines = []string{
			"Account: " + detail.Email,
			"A password-reset message will be delivered to the user's Vulos inbox.",
		}
		d.Warn = "The current password remains valid until the user completes the reset."
	case "2fa-reset":
		d.Heading = "Reset two-factor authentication"
		d.Lines = []string{
			"Account: " + detail.Email,
			"This clears the user's TOTP secret. They must re-enroll a new authenticator.",
		}
		d.Warn = "Until re-enrolled, the account is protected by password only."
		d.ConfirmLabel = "Reset 2FA"
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	auditFromRequest(r, p.al, p.actorEmail(r), "admin.action.confirm_view", id,
		map[string]string{"action": action})
	p.r.render(w, "Confirm — "+d.Heading, "confirm", d)
}

// AccountActionExecute performs a confirmed account action:
// POST /superadmin/accounts/{id}/action  (CSRF-verified upstream).
func (p *Pages) AccountActionExecute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = r.ParseForm()
	action := r.PostFormValue("action")
	back := "/superadmin/accounts/" + id
	actor := p.actorEmail(r)

	// Step-up TOTP (no-op unless VULOS_ADMIN_REAUTH_TOTP=1). Not required for the
	// non-destructive unsuspend.
	if action != "unsuspend" && !p.verifyStepUpTOTP(r, r.PostFormValue("totp")) {
		auditFromRequest(r, p.al, actor, "admin.action.stepup_failed", id,
			map[string]string{"action": action})
		redirectErr(w, r, back, "Step-up verification failed.")
		return
	}

	var err error
	var okMsg string
	switch action {
	case "suspend":
		reason := r.PostFormValue("reason")
		if reason == "" {
			reason = "admin-initiated"
		}
		err = p.s.SuspendAccount(r.Context(), id, reason, actor, p.al)
		okMsg = "Account suspended."
	case "unsuspend":
		err = p.s.UnsuspendAccount(r.Context(), id, actor, p.al)
		okMsg = "Account unsuspended."
	case "force-password-reset":
		if p.inbox == nil {
			redirectErr(w, r, back, "Inbox sender not configured.")
			return
		}
		err = p.s.ForcePasswordReset(r.Context(), id, actor, p.auth, p.inbox, p.al)
		okMsg = "Password-reset message delivered to the user's inbox."
	case "2fa-reset":
		err = p.s.Reset2FA(r.Context(), id, actor, p.al)
		okMsg = "2FA reset — the user must re-enroll."
	default:
		redirectErr(w, r, back, "Unknown action.")
		return
	}
	if err != nil {
		redirectErr(w, r, back, "Action failed.")
		return
	}
	redirectFlash(w, r, back, okMsg)
}

// ─── Refund ──────────────────────────────────────────────────────────────────

// RefundConfirm renders the refund confirm page:
// GET /superadmin/accounts/{id}/refund/confirm?txn_id=...
func (p *Pages) RefundConfirm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	txnID := r.URL.Query().Get("txn_id")
	if txnID == "" {
		http.Error(w, "txn_id required", http.StatusBadRequest)
		return
	}
	detail, err := p.s.GetAccountDetail(r.Context(), id)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	back := "/superadmin/accounts/" + id
	d := confirmData{
		Heading: "Refund transaction",
		Lines: []string{
			"Account: " + detail.Email,
			"Transaction: " + txnID,
			"This refunds the transaction via the payment provider and CANNOT be undone.",
		},
		Warn:         "Refunds are irreversible.",
		ActionURL:    back + "/refund",
		CancelURL:    back,
		CSRFToken:    p.csrf(w, r),
		ConfirmLabel: "Issue refund",
		Danger:       true,
		RequireTOTP:  stepUpTOTPRequired(),
		Hidden:       []confirmKV{{Name: "txn_id", Value: txnID}},
	}
	auditFromRequest(r, p.al, p.actorEmail(r), "admin.action.confirm_view", id,
		map[string]string{"action": "refund", "txn_id": txnID})
	p.r.render(w, "Confirm — Refund", "confirm", d)
}

// RefundExecute performs a confirmed refund:
// POST /superadmin/accounts/{id}/refund  (CSRF-verified upstream).
func (p *Pages) RefundExecute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = r.ParseForm()
	txnID := r.PostFormValue("txn_id")
	back := "/superadmin/accounts/" + id
	actor := p.actorEmail(r)

	if txnID == "" {
		redirectErr(w, r, back, "txn_id required.")
		return
	}
	if !p.verifyStepUpTOTP(r, r.PostFormValue("totp")) {
		auditFromRequest(r, p.al, actor, "admin.action.stepup_failed", id,
			map[string]string{"action": "refund", "txn_id": txnID})
		redirectErr(w, r, back, "Step-up verification failed.")
		return
	}

	// Ownership check: txn must belong to this account.
	var ownerCount int
	checkErr := p.s.db.QueryRowContext(r.Context(),
		p.s.db.Rebind(`SELECT COUNT(*) FROM billing_transactions WHERE txn_id = ? AND account_id = ?`),
		txnID, id).Scan(&ownerCount)
	if checkErr != nil || ownerCount == 0 {
		auditFromRequest(r, p.al, actor, "admin.billing.refund.denied", id,
			map[string]string{"txn_id": txnID, "reason": "txn_id does not belong to account"})
		redirectErr(w, r, back, "Transaction not found for this account.")
		return
	}

	if _, err := RefundProcessor(r.Context(), txnID); err != nil {
		auditFromRequest(r, p.al, actor, "admin.billing.refund.failed", id,
			map[string]string{"txn_id": txnID, "error": err.Error()})
		redirectErr(w, r, back, "Refund failed at the payment provider.")
		return
	}
	auditFromRequest(r, p.al, actor, "admin.billing.refund", id,
		map[string]string{"txn_id": txnID})
	redirectFlash(w, r, back, "Refund submitted to the payment provider.")
}

// ─── Reserved handles ────────────────────────────────────────────────────────

// ReservedHandleAdd handles POST /superadmin/reserved-handles (CSRF-verified).
func (p *Pages) ReservedHandleAdd(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	handle := strings.TrimSpace(r.PostFormValue("handle"))
	reason := strings.TrimSpace(r.PostFormValue("reason"))
	back := "/superadmin/reserved-handles"
	if handle == "" || reason == "" {
		redirectErr(w, r, back, "Handle and reason are required.")
		return
	}
	if err := p.s.AddReservedHandle(handle, reason, AdminAccountIDFromCtx(r.Context()), p.al); err != nil {
		redirectErr(w, r, back, "Could not reserve handle (already reserved?).")
		return
	}
	redirectFlash(w, r, back, "Handle reserved.")
}

// ReservedHandleDeleteConfirm renders the delete confirm page:
// GET /superadmin/reserved-handles/confirm-delete?handle=...
func (p *Pages) ReservedHandleDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	handle := r.URL.Query().Get("handle")
	if handle == "" {
		http.Error(w, "handle required", http.StatusBadRequest)
		return
	}
	d := confirmData{
		Heading: "Delete reserved handle",
		Lines: []string{
			"Handle: " + handle,
			"Deleting this reservation makes the handle claimable by any user again.",
		},
		Warn:         "Another user may immediately register this handle.",
		ActionURL:    "/superadmin/reserved-handles/delete",
		CancelURL:    "/superadmin/reserved-handles",
		CSRFToken:    p.csrf(w, r),
		ConfirmLabel: "Delete reservation",
		Danger:       true,
		RequireTOTP:  stepUpTOTPRequired(),
		Hidden:       []confirmKV{{Name: "handle", Value: handle}},
	}
	auditFromRequest(r, p.al, p.actorEmail(r), "admin.action.confirm_view", handle,
		map[string]string{"action": "reserved-handle-delete"})
	p.r.render(w, "Confirm — Delete reserved handle", "confirm", d)
}

// ReservedHandleDelete handles POST /superadmin/reserved-handles/delete (CSRF).
func (p *Pages) ReservedHandleDelete(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	handle := strings.TrimSpace(r.PostFormValue("handle"))
	back := "/superadmin/reserved-handles"
	actor := p.actorEmail(r)
	if handle == "" {
		redirectErr(w, r, back, "handle required.")
		return
	}
	if !p.verifyStepUpTOTP(r, r.PostFormValue("totp")) {
		auditFromRequest(r, p.al, actor, "admin.action.stepup_failed", handle,
			map[string]string{"action": "reserved-handle-delete"})
		redirectErr(w, r, back, "Step-up verification failed.")
		return
	}
	if err := p.s.DeleteReservedHandle(handle, AdminAccountIDFromCtx(r.Context()), p.al); err != nil {
		redirectErr(w, r, back, "Could not delete handle (system handle or not found).")
		return
	}
	redirectFlash(w, r, back, "Reserved handle deleted.")
}
