// fleet.go — fleet HTTP routes for the Vulos control-plane (moved verbatim from
// vulos-cloud backend/cp/cmd/server/routes_fleet.go into the operational route
// set).
//
// Routes registered:
//
//	POST   /api/fleet/org                              — create org (session)
//	GET    /api/fleet/org/{id}                         — get org (session, member)
//	POST   /api/fleet/org/{id}/members                 — add member (session, admin)
//	GET    /api/fleet/org/{id}/members                 — list members (session, member)
//	PUT    /api/fleet/org/{id}/members/{account_id}    — change member role (session, admin)
//	GET    /api/fleet/org/{id}/devices                 — list devices (session, member)
//	GET    /api/fleet/org/{id}/version-distribution    — version→count map (session, member)
//	GET    /api/fleet/org/{id}/seats                   — seat count (session, admin)
//	GET    /api/fleet/org/{id}/invites                 — list invites (session, admin)
//	GET    /api/fleet/devices                          — list by account_id (session)
//	GET    /api/fleet/device/{ulid}                    — get device (session, owner/admin)
//	PUT    /api/fleet/device/{ulid}                    — update device (session, admin)
//	GET    /api/fleet/device/{ulid}/heartbeats         — recent heartbeats (session, admin)
//	POST   /api/fleet/decommission                     — decommission device (session, admin)
//	POST   /api/fleet/heartbeat                        — device heartbeat (HMAC or unsigned dev)
//	POST   /api/fleet/invite                           — create invite (session, admin)
//	POST   /api/fleet/invite/{id}/revoke               — revoke invite (session, admin)
package cproutes

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/routing"
)

// RegisterFleet wires fleet endpoints into mux.
// authStore is used for session validation.
// ownerResolver optionally cross-checks device ULID ownership via the routing
// store (FLEET-03); pass nil to skip ownership resolution (dev / test only).
func RegisterFleet(mux *http.ServeMux, st fleet.Store, authStore *auth.Store, ownerResolver fleet.OwnershipResolver) {
	fh := &fleetHandlers{st: st, auth: authStore, ownerResolver: ownerResolver}

	// fa wraps a handler with RequireFleetAuth: fleet_admin accounts must have
	// TOTP enabled before they can access any session-authenticated fleet route.
	fa := func(h http.HandlerFunc) http.Handler {
		return authStore.RequireFleetAuth(http.HandlerFunc(h))
	}

	// Session-authenticated routes — wrapped with fleet-admin TOTP enforcement.
	mux.Handle("POST /api/fleet/org", fa(fh.createOrg))
	mux.Handle("GET /api/fleet/org/{id}", fa(fh.getOrg))
	mux.Handle("POST /api/fleet/org/{id}/members", fa(fh.addMember))
	mux.Handle("GET /api/fleet/org/{id}/members", fa(fh.listMembers))
	mux.Handle("PUT /api/fleet/org/{id}/members/{account_id}", fa(fh.setMemberRole))
	mux.Handle("GET /api/fleet/org/{id}/devices", fa(fh.listOrgDevices))
	mux.Handle("GET /api/fleet/org/{id}/version-distribution", fa(fh.versionDistribution))
	mux.Handle("GET /api/fleet/org/{id}/seats", fa(fh.seats))
	mux.Handle("GET /api/fleet/org/{id}/invites", fa(fh.listInvites))
	mux.Handle("GET /api/fleet/devices", fa(fh.listDevicesByAccount))
	mux.Handle("GET /api/fleet/device/{ulid}", fa(fh.getDevice))
	mux.Handle("PUT /api/fleet/device/{ulid}", fa(fh.updateDevice))
	mux.Handle("GET /api/fleet/device/{ulid}/heartbeats", fa(fh.recentHeartbeats))
	mux.Handle("POST /api/fleet/decommission", fa(fh.decommission))
	mux.Handle("POST /api/fleet/invite", fa(fh.createInvite))
	mux.Handle("POST /api/fleet/invite/{id}/revoke", fa(fh.revokeInvite))

	// Device heartbeat — HMAC-authenticated, no session required; not wrapped.
	mux.HandleFunc("POST /api/fleet/heartbeat", fh.heartbeat)
}

// fleetHandlers holds dependencies for fleet HTTP handlers.
type fleetHandlers struct {
	st            fleet.Store
	auth          *auth.Store
	ownerResolver fleet.OwnershipResolver // optional; nil disables routing-side ownership checks
}

// --------------------------------------------------------------------------
// Error mapping
// --------------------------------------------------------------------------

func fleetErrStatus(err error) int {
	switch {
	case errors.Is(err, fleet.ErrInvalidRole):
		return http.StatusBadRequest // 400
	case errors.Is(err, fleet.ErrNotMember),
		errors.Is(err, fleet.ErrNotAdmin):
		return http.StatusForbidden // 403
	case errors.Is(err, fleet.ErrDeviceNotFound),
		errors.Is(err, fleet.ErrOrgNotFound),
		errors.Is(err, fleet.ErrInviteNotFound):
		return http.StatusNotFound // 404
	default:
		return http.StatusInternalServerError // 500
	}
}

// --------------------------------------------------------------------------
// Auth helpers
// --------------------------------------------------------------------------

// requireSession validates the session cookie and returns the user.
// Writes 401 and returns nil on failure.
func (fh *fleetHandlers) requireSession(w http.ResponseWriter, r *http.Request) *auth.User {
	return fh.auth.RequireSession(r.Context(), w, r)
}

// requireMember checks that accountID is a member of orgID.
// Returns the role string on success, or writes 403 + returns "" on failure.
func (fh *fleetHandlers) requireMember(w http.ResponseWriter, orgID, accountID string, r *http.Request) string {
	role, err := fh.st.MemberRole(r.Context(), orgID, accountID)
	if err != nil {
		if errors.Is(err, fleet.ErrNotMember) {
			httpx.Err(w, http.StatusForbidden, fleet.ErrNotMember.Error())
			return ""
		}
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return ""
	}
	return role
}

// requireAdmin checks that accountID is an admin or owner of orgID.
// Returns true on success; writes 403 on failure.
func (fh *fleetHandlers) requireAdmin(w http.ResponseWriter, orgID, accountID string, r *http.Request) bool {
	role := fh.requireMember(w, orgID, accountID, r)
	if role == "" {
		return false // 403 already written
	}
	if role != "owner" && role != "admin" {
		httpx.Err(w, http.StatusForbidden, fleet.ErrNotAdmin.Error())
		return false
	}
	return true
}

// --------------------------------------------------------------------------
// Org handlers
// --------------------------------------------------------------------------

func (fh *fleetHandlers) createOrg(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		httpx.Err(w, http.StatusBadRequest, "name is required")
		return
	}

	org, err := fh.st.CreateOrg(r.Context(), req.Name, u.ID)
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	httpx.JSON(w, orgJSON(org))
}

func (fh *fleetHandlers) getOrg(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	orgID := r.PathValue("id")
	org, err := fh.st.GetOrg(r.Context(), orgID)
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}

	// Must be a member to see the org.
	if role := fh.requireMember(w, orgID, u.ID, r); role == "" {
		return
	}

	httpx.JSON(w, orgJSON(org))
}

// --------------------------------------------------------------------------
// Member handlers
// --------------------------------------------------------------------------

func (fh *fleetHandlers) addMember(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	orgID := r.PathValue("id")
	if !fh.requireAdmin(w, orgID, u.ID, r) {
		return
	}

	var req struct {
		AccountID string `json:"account_id"`
		Role      string `json:"role"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.AccountID == "" {
		httpx.Err(w, http.StatusBadRequest, "account_id is required")
		return
	}

	if err := fh.st.AddMember(r.Context(), orgID, req.AccountID, req.Role); err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}

	w.WriteHeader(http.StatusCreated)
	httpx.JSON(w, map[string]string{"org_id": orgID, "account_id": req.AccountID, "role": req.Role})
}

func (fh *fleetHandlers) listMembers(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	orgID := r.PathValue("id")
	if role := fh.requireMember(w, orgID, u.ID, r); role == "" {
		return
	}

	members, err := fh.st.Members(r.Context(), orgID)
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}

	out := make([]map[string]any, len(members))
	for i, m := range members {
		out[i] = map[string]any{
			"org_id":     m.OrgID,
			"account_id": m.AccountID,
			"role":       m.Role,
			"joined_at":  m.JoinedAt.Format("2006-01-02T15:04:05Z"),
		}
	}
	httpx.JSON(w, out)
}

// --------------------------------------------------------------------------
// Device list handlers
// --------------------------------------------------------------------------

func (fh *fleetHandlers) listOrgDevices(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	orgID := r.PathValue("id")
	if role := fh.requireMember(w, orgID, u.ID, r); role == "" {
		return
	}

	// PAGINATE-01: bounded page size + opaque next-cursor (stable ORDER BY in the
	// store). The limit is clamped to [1, maxPageSize]; ?cursor= round-trips the
	// next offset so a client need not track offsets.
	page := parsePageParams(r)
	devs, err := fh.st.ListDevices(r.Context(), orgID, page.Limit, page.Offset)
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}
	items := devicesJSON(devs)
	httpx.JSON(w, newPagedResponse(page, items, len(items)))
}

func (fh *fleetHandlers) listDevicesByAccount(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	accountID := r.URL.Query().Get("account_id")
	if accountID == "" {
		httpx.Err(w, http.StatusBadRequest, "account_id query param required")
		return
	}
	// Must match the authenticated user.
	if accountID != u.ID {
		httpx.Err(w, http.StatusForbidden, "account_id must match authenticated user")
		return
	}

	// PAGINATE-01: bounded page size + opaque next-cursor.
	page := parsePageParams(r)
	devs, err := fh.st.ListByAccount(r.Context(), accountID, page.Limit, page.Offset)
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}
	items := devicesJSON(devs)
	httpx.JSON(w, newPagedResponse(page, items, len(items)))
}

// --------------------------------------------------------------------------
// Per-device handlers
// --------------------------------------------------------------------------

func (fh *fleetHandlers) getDevice(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	rawULID := r.PathValue("ulid")
	canon, err := routing.CanonULID(rawULID)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid ULID")
		return
	}

	d, err := fh.st.GetDevice(r.Context(), canon)
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}

	// Must own the device or be an org admin.
	if !fh.canAccessDevice(w, r, u.ID, d) {
		return
	}

	httpx.JSON(w, deviceJSON(d))
}

func (fh *fleetHandlers) updateDevice(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	rawULID := r.PathValue("ulid")
	canon, err := routing.CanonULID(rawULID)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid ULID")
		return
	}

	d, err := fh.st.GetDevice(r.Context(), canon)
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}

	// Must be org admin/owner or device owner.
	if !fh.canAdminDevice(w, r, u.ID, d) {
		return
	}

	var req struct {
		Name    *string `json:"name"`
		Group   *string `json:"group"`
		Channel *string `json:"channel"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}

	if req.Name != nil {
		if err := fh.st.SetName(r.Context(), canon, *req.Name); err != nil {
			httpx.Err(w, fleetErrStatus(err), err.Error())
			return
		}
	}
	if req.Group != nil {
		if err := fh.st.SetGroup(r.Context(), canon, *req.Group); err != nil {
			httpx.Err(w, fleetErrStatus(err), err.Error())
			return
		}
	}
	if req.Channel != nil {
		if err := fh.st.SetChannel(r.Context(), canon, *req.Channel); err != nil {
			httpx.Err(w, fleetErrStatus(err), err.Error())
			return
		}
	}

	// Re-read updated device.
	updated, err := fh.st.GetDevice(r.Context(), canon)
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}
	httpx.JSON(w, deviceJSON(updated))
}

func (fh *fleetHandlers) recentHeartbeats(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	rawULID := r.PathValue("ulid")
	canon, err := routing.CanonULID(rawULID)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid ULID")
		return
	}

	d, err := fh.st.GetDevice(r.Context(), canon)
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}
	if !fh.canAdminDevice(w, r, u.ID, d) {
		return
	}

	n := queryInt(r, "n", 20)
	hbs, err := fh.st.RecentHeartbeats(r.Context(), canon, n)
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}

	out := make([]map[string]any, len(hbs))
	for i, hb := range hbs {
		out[i] = map[string]any{
			"ulid":             hb.ULID,
			"version":          hb.Version,
			"health":           hb.Health,
			"sync_lag_seconds": hb.SyncLagSec,
			"uptime_seconds":   hb.UptimeSec,
			"crash_count":      hb.CrashCount,
			"received_at":      hb.ReceivedAt.Format("2006-01-02T15:04:05Z"),
		}
	}
	httpx.JSON(w, out)
}

func (fh *fleetHandlers) decommission(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	var req struct {
		ULID string `json:"ulid"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}

	canon, err := routing.CanonULID(req.ULID)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid ULID")
		return
	}

	d, err := fh.st.GetDevice(r.Context(), canon)
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}
	if !fh.canAdminDevice(w, r, u.ID, d) {
		return
	}

	if err := fh.st.Decommission(r.Context(), canon); err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --------------------------------------------------------------------------
// Heartbeat handler (device auth via HMAC or env-disabled check)
// --------------------------------------------------------------------------

func (fh *fleetHandlers) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ULID           string `json:"ulid"`
		Version        string `json:"version"`
		Health         string `json:"health"`
		SyncLagSeconds int    `json:"sync_lag_seconds"`
		UptimeSeconds  int    `json:"uptime_seconds"`
		CrashCount     int    `json:"crash_count"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}

	if req.ULID == "" {
		httpx.Err(w, http.StatusBadRequest, "ulid is required")
		return
	}

	canon, err := routing.CanonULID(req.ULID)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid ULID")
		return
	}

	// HMAC verification.
	requireSig := os.Getenv("FLEET_REQUIRE_DEVICE_SIG") != "0"
	if requireSig {
		// RISK-SEC-01: source the device HMAC secret via the SecretsProvider
		// (env default; KMS-ready).
		secret := secretOrEnv(r.Context(), "DEVICE_SHARED_SECRET")
		if secret == "" {
			// DEVICE_SHARED_SECRET is unset and FLEET_REQUIRE_DEVICE_SIG=1:
			// we cannot verify the device signature — reject as misconfiguration.
			httpx.Err(w, http.StatusServiceUnavailable, "heartbeat verification not configured")
			return
		}
		sig := r.Header.Get("X-Fleet-Sig")
		if !verifyFleetSig(canon, secret, sig) {
			httpx.Err(w, http.StatusUnauthorized, "invalid device signature")
			return
		}
	}

	// Validate health value.
	switch req.Health {
	case "healthy", "degraded", "unknown", "":
	default:
		httpx.Err(w, http.StatusBadRequest, "health must be healthy|degraded|unknown")
		return
	}
	if req.Health == "" {
		req.Health = "unknown"
	}

	d, err := fh.st.InsertHeartbeat(r.Context(), fleet.Heartbeat{
		ULID:       canon,
		Version:    req.Version,
		Health:     req.Health,
		SyncLagSec: req.SyncLagSeconds,
		UptimeSec:  req.UptimeSeconds,
		CrashCount: req.CrashCount,
	})
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}
	httpx.JSON(w, deviceJSON(d))
}

// verifyFleetSig checks X-Fleet-Sig: HMAC-SHA256(secret, ulid) in hex.
func verifyFleetSig(ulid, secret, sig string) bool {
	if sig == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ulid))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(expected))
}

// --------------------------------------------------------------------------
// Org analytics handlers
// --------------------------------------------------------------------------

func (fh *fleetHandlers) versionDistribution(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	orgID := r.PathValue("id")
	if role := fh.requireMember(w, orgID, u.ID, r); role == "" {
		return
	}

	dist, err := fleet.VersionDistributionFromStore(r.Context(), fh.st, orgID)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, dist)
}

func (fh *fleetHandlers) seats(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	orgID := r.PathValue("id")
	if !fh.requireAdmin(w, orgID, u.ID, r) {
		return
	}

	count, err := fh.st.SeatCount(r.Context(), orgID)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, map[string]int{"count": count})
}

// --------------------------------------------------------------------------
// Role-change handler
// --------------------------------------------------------------------------

func (fh *fleetHandlers) setMemberRole(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	orgID := r.PathValue("id")
	accountID := r.PathValue("account_id")

	if !fh.requireAdmin(w, orgID, u.ID, r) {
		return
	}

	var req struct {
		Role string `json:"role"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.Role == "" {
		httpx.Err(w, http.StatusBadRequest, "role is required")
		return
	}

	if err := fh.st.SetRole(r.Context(), orgID, accountID, req.Role); err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}

	httpx.JSON(w, map[string]string{"org_id": orgID, "account_id": accountID, "role": req.Role})
}

// --------------------------------------------------------------------------
// Invite handler
// --------------------------------------------------------------------------

func (fh *fleetHandlers) createInvite(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	var req struct {
		OrgID string `json:"org_id"`
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.OrgID == "" || req.Email == "" || req.Role == "" {
		httpx.Err(w, http.StatusBadRequest, "org_id, email, role are required")
		return
	}
	if !fh.requireAdmin(w, req.OrgID, u.ID, r) {
		return
	}

	inv, err := fh.st.CreateInvite(r.Context(), req.OrgID, req.Email, req.Role)
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}

	w.WriteHeader(http.StatusCreated)
	httpx.JSON(w, inviteJSON(inv))
}

func (fh *fleetHandlers) revokeInvite(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	inviteID := r.PathValue("id")
	inv, err := fh.st.GetInvite(r.Context(), inviteID)
	if err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}

	// Caller must be admin/owner of the invite's org.
	if !fh.requireAdmin(w, inv.OrgID, u.ID, r) {
		return
	}

	if err := fh.st.RevokeInvite(r.Context(), inviteID); err != nil {
		httpx.Err(w, fleetErrStatus(err), err.Error())
		return
	}

	inv.State = "revoked"
	httpx.JSON(w, inviteJSON(inv))
}

func (fh *fleetHandlers) listInvites(w http.ResponseWriter, r *http.Request) {
	u := fh.requireSession(w, r)
	if u == nil {
		return
	}

	orgID := r.PathValue("id")
	if !fh.requireAdmin(w, orgID, u.ID, r) {
		return
	}

	invites, err := fh.st.ListInvites(r.Context(), orgID)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]map[string]any, len(invites))
	for i, inv := range invites {
		out[i] = inviteJSON(inv)
	}
	httpx.JSON(w, out)
}

// --------------------------------------------------------------------------
// Access control helpers
// --------------------------------------------------------------------------

// canAccessDevice returns true if the user owns the device or is an org admin.
func (fh *fleetHandlers) canAccessDevice(w http.ResponseWriter, r *http.Request, accountID string, d fleet.Device) bool {
	if d.AccountID == accountID {
		return true
	}
	if d.OrgID != "" {
		role, err := fh.st.MemberRole(r.Context(), d.OrgID, accountID)
		if err == nil && (role == "admin" || role == "owner") {
			return true
		}
	}
	httpx.Err(w, http.StatusForbidden, "access denied")
	return false
}

// canAdminDevice returns true if the user is admin/owner of the device's org,
// or is the sole account owner (no org).
func (fh *fleetHandlers) canAdminDevice(w http.ResponseWriter, r *http.Request, accountID string, d fleet.Device) bool {
	if d.OrgID != "" {
		role, err := fh.st.MemberRole(r.Context(), d.OrgID, accountID)
		if err == nil && (role == "admin" || role == "owner") {
			return true
		}
		httpx.Err(w, http.StatusForbidden, fleet.ErrNotAdmin.Error())
		return false
	}
	// No org: device owner is the only admin.
	if d.AccountID == accountID {
		return true
	}
	httpx.Err(w, http.StatusForbidden, "access denied")
	return false
}

// --------------------------------------------------------------------------
// JSON serialisation helpers
// --------------------------------------------------------------------------

func orgJSON(o fleet.Org) map[string]any {
	return map[string]any{
		"id":            o.ID,
		"name":          o.Name,
		"owner_account": o.OwnerAccount,
		"created_at":    o.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func deviceJSON(d fleet.Device) map[string]any {
	var lhb any
	if d.LastHeartbeat != nil {
		lhb = d.LastHeartbeat.Format("2006-01-02T15:04:05Z")
	}
	return map[string]any{
		"ulid":           d.ULID,
		"org_id":         d.OrgID,
		"account_id":     d.AccountID,
		"name":           d.Name,
		"group_label":    d.GroupLabel,
		"version":        d.Version,
		"channel":        d.Channel,
		"health":         d.Health,
		"status":         fleet.DeriveStatus(d.LastHeartbeat, time.Now().UTC()),
		"last_heartbeat": lhb,
		"crash_count":    d.CrashCount,
		"decommissioned": d.Decommissioned,
		"created_at":     d.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at":     d.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func inviteJSON(inv fleet.Invite) map[string]any {
	return map[string]any{
		"id":         inv.ID,
		"org_id":     inv.OrgID,
		"email":      inv.Email,
		"role":       inv.Role,
		"state":      inv.State,
		"created_at": inv.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func devicesJSON(devs []fleet.Device) []map[string]any {
	out := make([]map[string]any, len(devs))
	for i, d := range devs {
		out[i] = deviceJSON(d)
	}
	return out
}

// --------------------------------------------------------------------------
// Utility
// --------------------------------------------------------------------------

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}
