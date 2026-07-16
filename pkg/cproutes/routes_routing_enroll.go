// routes_routing_enroll.go — enrollment HTTP routes for the vulos.cloud
// control-plane routing subsystem.
//
// Routes registered:
//
//	POST /api/enroll        — bind ULID to account with a connection mode
//	POST /api/enroll/direct — bind ULID with mode=direct and a direct IP
//	POST /api/connmode      — update the active connection mode for a ULID
//
// SECURITY (audit C1/C2/H2): every route in this family REQUIRES an authenticated
// session and derives the account_id from that session — it is NEVER taken from
// the request body. /api/connmode additionally enforces ULID ownership before
// mutating routing state. This prevents an attacker from binding an arbitrary
// ULID to their account (and harvesting acme-dns TXT-delegation material),
// poisoning another tenant's direct_ip, or flipping another box's conn mode.
//
// The canonical bare-metal enrollment path is the RFC-8628 device-grant flow in
// routes_enroll.go (POST /enroll/start|poll|approve|deny); these legacy routes
// remain for the session-driven web wizard and are gated behind the same auth.
//
// See backend/cp/ROUTING_API.md for the full HTTP surface + error→status map.
// Mode D (local-only) is never accepted or synthesised by this handler.
//
// Do NOT edit main.go — cloud-harden owns that file and will call
// RegisterEnrollRoutes when wiring the full server.
package cproutes

import (
	"errors"
	"net/http"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/routing"
)

// RegisterEnrollRoutes wires enrollment endpoints into mux.
// st must satisfy routing.Store (may be MemStore or the SQL store).
// authStore gates every route: the account_id is derived from the caller's
// session, never from the request body.
func RegisterEnrollRoutes(mux *http.ServeMux, st routing.Store, authStore *auth.Store) {
	mux.HandleFunc("POST /api/enroll", enrollHandler(st, authStore))
	mux.HandleFunc("POST /api/enroll/direct", enrollDirectHandler(st, authStore))
	mux.HandleFunc("POST /api/connmode", connModeHandler(st, authStore))
}

// enrollRequest is the JSON body for POST /api/enroll.
// NOTE: account_id is intentionally absent — the owning account is always taken
// from the authenticated session, never trusted from the body.
type enrollRequest struct {
	ULID string           `json:"ulid"`
	Mode routing.ConnMode `json:"mode"`
}

// enrollResponse is the JSON body returned by POST /api/enroll.
// It includes acme-dns TXT-delegation material. No TLS private key is ever
// present here — delegation only (see internal/routing/acmedns.go).
type enrollResponse struct {
	ULID      string               `json:"ulid"`
	AccountID string               `json:"account_id"`
	Mode      routing.ConnMode     `json:"mode"`
	AcmeDNS   routing.AcmeDNSCreds `json:"acme_dns"`
	CreatedAt string               `json:"created_at"`
}

// enrollDirectRequest is the JSON body for POST /api/enroll/direct.
// account_id is derived from the session (see enrollRequest).
type enrollDirectRequest struct {
	ULID     string `json:"ulid"`
	DirectIP string `json:"direct_ip"`
}

// connModeRequest is the JSON body for POST /api/connmode.
type connModeRequest struct {
	ULID string           `json:"ulid"`
	Mode routing.ConnMode `json:"mode"`
}

// enrollHandler handles POST /api/enroll.
func enrollHandler(st routing.Store, authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return // RequireSession already wrote 401
		}

		var req enrollRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}

		canon, err := routing.CanonULID(req.ULID)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, err.Error())
			return
		}

		// Mode D is local-only and must never reach the cloud.
		if !routing.ValidConnMode(req.Mode) {
			httpx.Err(w, http.StatusBadRequest, routing.ErrInvalidMode.Error())
			return
		}

		// Ownership: Enroll is idempotent for the same account and returns
		// ErrULIDBoundToOtherAccount (409) when the ULID belongs to someone else,
		// so deriving the account from the session enforces ownership here.
		b, err := st.Enroll(r.Context(), canon, u.ID, req.Mode)
		if err != nil {
			httpx.Err(w, enrollErrStatus(err), err.Error())
			return
		}

		creds, err := routing.GenerateAcmeDNSCreds(canon)
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, "failed to generate acme-dns credentials")
			return
		}

		httpx.JSON(w, enrollResponse{
			ULID:      b.ULID,
			AccountID: b.AccountID,
			Mode:      b.Mode,
			AcmeDNS:   creds,
			CreatedAt: b.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
}

// enrollDirectHandler handles POST /api/enroll/direct.
// Forces mode=direct, then calls SetDirectIP with the supplied IP.
func enrollDirectHandler(st routing.Store, authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}

		var req enrollDirectRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}

		canon, err := routing.CanonULID(req.ULID)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, err.Error())
			return
		}

		// Enroll with mode=direct (mode D is never synthesised). Ownership is
		// enforced by Enroll (409 for a ULID bound to another account).
		b, err := st.Enroll(r.Context(), canon, u.ID, routing.ModeDirect)
		if err != nil {
			httpx.Err(w, enrollErrStatus(err), err.Error())
			return
		}

		// Bind the direct IP (ownership-enforced by the store against u.ID).
		if err := st.SetDirectIP(r.Context(), canon, u.ID, req.DirectIP); err != nil {
			httpx.Err(w, enrollErrStatus(err), err.Error())
			return
		}

		// Re-read so DirectIP is reflected in the response.
		b, err = st.GetBinding(r.Context(), canon)
		if err != nil {
			httpx.Err(w, enrollErrStatus(err), err.Error())
			return
		}

		creds, err := routing.GenerateAcmeDNSCreds(canon)
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, "failed to generate acme-dns credentials")
			return
		}

		httpx.JSON(w, enrollResponse{
			ULID:      b.ULID,
			AccountID: b.AccountID,
			Mode:      b.Mode,
			AcmeDNS:   creds,
			CreatedAt: b.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
}

// connModeHandler handles POST /api/connmode.
// The instance reports its active connection mode. Mode D is rejected.
func connModeHandler(st routing.Store, authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}

		var req connModeRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}

		canon, err := routing.CanonULID(req.ULID)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, err.Error())
			return
		}

		// Mode D (local-only) must never be accepted by the cloud.
		if !routing.ValidConnMode(req.Mode) {
			httpx.Err(w, http.StatusBadRequest, routing.ErrInvalidMode.Error())
			return
		}

		// Ownership: SetConnMode has no ownership check of its own, so verify the
		// caller owns this ULID before mutating its routing state.
		b, err := st.GetBinding(r.Context(), canon)
		if err != nil {
			httpx.Err(w, enrollErrStatus(err), err.Error())
			return
		}
		if b.AccountID != u.ID {
			httpx.Err(w, http.StatusForbidden, routing.ErrNotOwner.Error())
			return
		}

		if err := st.SetConnMode(r.Context(), canon, req.Mode); err != nil {
			httpx.Err(w, enrollErrStatus(err), err.Error())
			return
		}

		b, err = st.GetBinding(r.Context(), canon)
		if err != nil {
			httpx.Err(w, enrollErrStatus(err), err.Error())
			return
		}

		httpx.JSON(w, map[string]any{
			"ulid": b.ULID,
			"mode": b.Mode,
		})
	}
}

// enrollErrStatus maps routing sentinel errors to HTTP status codes per
// ROUTING_API.md.
func enrollErrStatus(err error) int {
	switch {
	case errors.Is(err, routing.ErrInvalidULID),
		errors.Is(err, routing.ErrInvalidName),
		errors.Is(err, routing.ErrInvalidMode):
		return http.StatusBadRequest // 400
	case errors.Is(err, routing.ErrULIDBoundToOtherAccount),
		errors.Is(err, routing.ErrVanityTaken):
		return http.StatusConflict // 409
	case errors.Is(err, routing.ErrNotEnrolled),
		errors.Is(err, routing.ErrNotFound):
		return http.StatusNotFound // 404
	case errors.Is(err, routing.ErrNotOwner):
		return http.StatusForbidden // 403
	case errors.Is(err, routing.ErrNoHealthyPoP):
		return http.StatusServiceUnavailable // 503
	default:
		return http.StatusInternalServerError // 500
	}
}
