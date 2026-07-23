// routes_invite_accept.go — invite→member acceptance (ONBOARD-ACCEPT-01).
//
// This closes the genuinely-absent onboarding step: there was no endpoint that
// promoted a pending fleet invite to a real org member. Member rows were only
// ever created by CreateOrg (owner auto-assign) or the direct AddMember admin
// route — an invited user had no self-service way to join.
//
// Route registered:
//
//	POST /api/org/invite/accept   — authenticated invitee accepts an invite
//
// Request body: { "invite_id": "<id>" }  (the invite id IS the bearer token —
// a 26-char opaque ULID; v1 keeps the token == id rather than minting a second
// secret, matching how revoke already addresses invites by id).
//
// Validation (all must hold, else the named error status):
//   - session present                          → 401 (RequireSession)
//   - invite exists                             → 404
//   - invite.email == session user's email      → 403 (wrong invitee)
//   - invite.state == "pending"                 → 409 (already accepted/revoked)
//   - invite not past its TTL (fleet.InviteExpiry) → 410 (expired)
//
// On success it: AddMember(orgID, accountID, invite.Role) →
// MemberNamer.SetDisplayName(invite.DisplayName) → MarkInviteAccepted →
// audit "org.member.accept". The display-name application fulfils
// invite_name.go's documented requirement ("when the membership row is later
// created from the invite, the stored name is applied via SetDisplayName") —
// this route is that invite-accept→member flow.
package cproutes

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/obs"
)

// RegisterInviteAcceptRoutes wires POST /api/org/invite/accept into mux.
// st must satisfy fleet.InviteAcceptor + fleet.MemberNamer (SQLStore/MemStore
// both do); when either assertion fails the route degrades to 501 so a
// misconfiguration is loud rather than silently dropping the membership.
func RegisterInviteAcceptRoutes(mux *http.ServeMux, st fleet.Store, authStore *auth.Store) {
	mux.Handle("POST /api/org/invite/accept", authStore.RequireFleetAuth(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			u := authStore.RequireSession(r.Context(), w, r)
			if u == nil {
				return // 401 already written
			}

			var req struct {
				InviteID string `json:"invite_id"`
				Token    string `json:"token"` // alias accepted for token-style callers
			}
			if !httpx.DecodeJSON(w, r, &req) {
				return
			}
			inviteID := strings.TrimSpace(req.InviteID)
			if inviteID == "" {
				inviteID = strings.TrimSpace(req.Token)
			}
			if inviteID == "" {
				httpx.Err(w, http.StatusBadRequest, "invite_id (or token) is required")
				return
			}

			inv, err := st.GetInvite(r.Context(), inviteID)
			if err != nil {
				httpx.Err(w, inviteAcceptStatus(err), err.Error())
				return
			}

			// The invite must be addressed to the authenticated user's email.
			// Case-insensitive compare — emails are not case-sensitive in the
			// local-part-insensitive practice the rest of auth follows.
			if !strings.EqualFold(strings.TrimSpace(inv.Email), strings.TrimSpace(u.Email)) {
				obs.AuthFailures.Inc()
				httpx.Err(w, http.StatusForbidden, "invite is addressed to a different email")
				return
			}

			// State + expiry gates (before any write).
			if inv.State != "pending" {
				httpx.Err(w, http.StatusConflict, fleet.ErrInviteNotPending.Error())
				return
			}
			if fleet.IsInviteExpired(inv, time.Now().UTC()) {
				httpx.Err(w, http.StatusGone, fleet.ErrInviteExpired.Error())
				return
			}

			// Create the membership row with the invited role.
			if err := st.AddMember(r.Context(), inv.OrgID, u.ID, inv.Role); err != nil {
				httpx.Err(w, inviteAcceptStatus(err), err.Error())
				return
			}

			// Apply the stored display name (the invite_name.go promise). Empty
			// name is a no-op (the adapter falls back to email). A SetDisplayName
			// failure is non-fatal: the member exists; the name can be set later.
			if inv.DisplayName != "" {
				if namer, ok := st.(fleet.MemberNamer); ok {
					_ = namer.SetDisplayName(r.Context(), inv.OrgID, u.ID, inv.DisplayName)
				}
			}

			// Flip invite → accepted. If a concurrent accept already won, the
			// membership row is still correct (AddMember is an upsert) so we treat
			// ErrInviteNotPending as success-equivalent for idempotency.
			if acc, ok := st.(fleet.InviteAcceptor); ok {
				if err := acc.MarkInviteAccepted(r.Context(), inviteID); err != nil &&
					!errors.Is(err, fleet.ErrInviteNotPending) {
					httpx.Err(w, inviteAcceptStatus(err), err.Error())
					return
				}
			}

			// Org-scoped so the accepted membership surfaces in the target org's
			// audit trail (ORGADMIN-AUDIT-01). The actor is the accepting user.
			auditRecordOrg(r.Context(), inv.OrgID, u.Email, "org.member.accept", "tenant:"+inv.OrgID,
				map[string]string{
					"invite_id":  inviteID,
					"account_id": u.ID,
					"role":       inv.Role,
				})

			w.WriteHeader(http.StatusCreated)
			httpx.JSON(w, map[string]any{
				"org_id":       inv.OrgID,
				"account_id":   u.ID,
				"role":         inv.Role,
				"display_name": inv.DisplayName,
				"state":        "accepted",
			})
		})))
}

// inviteAcceptStatus maps fleet errors to HTTP status codes for the accept flow.
func inviteAcceptStatus(err error) int {
	switch {
	case errors.Is(err, fleet.ErrInviteNotFound),
		errors.Is(err, fleet.ErrOrgNotFound):
		return http.StatusNotFound
	case errors.Is(err, fleet.ErrInviteNotPending):
		return http.StatusConflict
	case errors.Is(err, fleet.ErrInviteExpired):
		return http.StatusGone
	case errors.Is(err, fleet.ErrInvalidRole):
		return http.StatusBadRequest
	case errors.Is(err, fleet.ErrNotMember), errors.Is(err, fleet.ErrNotAdmin):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}
