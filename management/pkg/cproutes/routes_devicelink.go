// routes_devicelink.go — WAVE24-RELAY-BILLING: device-code account LINK flow for
// headless / self-host installs (e.g. a wede relay agent), plus the account-bound
// install credential it mints.
//
// Endpoints:
//
//	POST /api/link/device/start   — (install, unauthenticated) → {device_code,
//	                                 user_code, verification_url, interval, expires_in}
//	POST /api/link/device/approve — (session-authed human) {user_code} → binds the
//	                                 pending link to the caller's account
//	POST /api/link/device/poll    — (install) {device_code} → 428 pending until
//	                                 approved, then {install_credential, account_id}
//
// This is a THIN wrapper over the existing device-authorization pattern: it does
// not re-implement per-device crypto. The minted install_credential is an opaque
// account-bound bearer (see internal/devicelink) that the install presents to
// GET /api/relay/entitlement and embeds in the relay tunnel-server token store.
//
// Fail-closed posture:
//   - start is rate-limited per client IP (bounded code creation).
//   - approve is session-authed AND rate-limited per account (bounded guessing).
//   - poll is rate-limited per device_code and returns 428 (Precondition
//     Required) while pending so a well-behaved install honours `interval`.
package cproutes

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/ddos"
	"github.com/vul-os/vulos-management/pkg/devicelink"
	"github.com/vul-os/vulos-management/pkg/httpx"
)

// deviceLinkHandlers holds the devicelink route dependencies.
type deviceLinkHandlers struct {
	store   devicelink.Store
	auth    *auth.Store
	limiter *mintRateLimiter // reused fixed-window limiter (per start-IP / poll-code / approve-account)
	// verificationURL is where the human approves; echoed to the install.
	verificationURL string
	now             func() time.Time
}

// RegisterDeviceLinkRoutes wires the device-link flow into mux.
//
//   - store: the devicelink persistence (cpdb-backed in prod, MemStore in tests)
//   - authStore: session validation for the approve step
//   - verificationURL: absolute URL the human opens to approve (e.g.
//     https://cloud.vulos.dev/app/link)
func RegisterDeviceLinkRoutes(mux *http.ServeMux, store devicelink.Store, authStore *auth.Store, verificationURL string) {
	h := &deviceLinkHandlers{
		store:           store,
		auth:            authStore,
		limiter:         newMintRateLimiter(30, time.Minute),
		verificationURL: verificationURL,
		now:             func() time.Time { return time.Now().UTC() },
	}
	mux.HandleFunc("POST /api/link/device/start", h.start)
	mux.HandleFunc("POST /api/link/device/approve", h.approve)
	mux.HandleFunc("POST /api/link/device/poll", h.poll)
}

// start begins a device-link. Unauthenticated (the install has no account yet),
// but rate-limited per client IP so it cannot be used to flood the code table.
func (h *deviceLinkHandlers) start(w http.ResponseWriter, r *http.Request) {
	if h.limiter != nil && !h.limiter.allow("link-start:"+clientIPCP(r), h.now()) {
		w.Header().Set("Retry-After", "60")
		httpx.Err(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	st, err := h.store.StartLink(r.Context(), h.verificationURL, devicelink.DefaultInterval, devicelink.DefaultTTL)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, "could not start device link")
		return
	}
	httpx.JSON(w, map[string]any{
		"device_code":      st.DeviceCode,
		"user_code":        st.UserCode,
		"verification_url": st.VerificationURL,
		"interval":         int(st.Interval.Seconds()),
		"expires_in":       int(st.ExpiresIn.Seconds()),
	})
}

// approveRequest is the body of POST /api/link/device/approve.
type approveRequest struct {
	UserCode string `json:"user_code"`
}

// approve binds a pending link to the authenticated caller's account.
func (h *deviceLinkHandlers) approve(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	if h.limiter != nil && !h.limiter.allow("link-approve:"+u.ID, h.now()) {
		w.Header().Set("Retry-After", "60")
		httpx.Err(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	var req approveRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	err := h.store.Approve(r.Context(), req.UserCode, u.ID)
	switch {
	case err == nil:
		httpx.JSON(w, map[string]any{"ok": true})
	case errors.Is(err, devicelink.ErrBadInput):
		httpx.Err(w, http.StatusBadRequest, "invalid user code")
	case errors.Is(err, devicelink.ErrConsumed):
		httpx.Err(w, http.StatusConflict, "this code was already approved")
	case errors.Is(err, devicelink.ErrNotFound):
		// Don't reveal whether the code ever existed.
		httpx.Err(w, http.StatusNotFound, "unknown or expired code")
	default:
		httpx.Err(w, http.StatusInternalServerError, "could not approve link")
	}
}

// pollRequest is the body of POST /api/link/device/poll.
type pollRequest struct {
	DeviceCode string `json:"device_code"`
}

// poll advances the device_code. Returns 428 while pending (so the install
// honours the interval), the install credential once approved.
func (h *deviceLinkHandlers) poll(w http.ResponseWriter, r *http.Request) {
	var req pollRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DeviceCode == "" {
		httpx.Err(w, http.StatusBadRequest, "device_code required")
		return
	}
	if h.limiter != nil && !h.limiter.allow("link-poll:"+req.DeviceCode, h.now()) {
		w.Header().Set("Retry-After", "5")
		httpx.Err(w, http.StatusTooManyRequests, "slow_down")
		return
	}
	cred, err := h.store.Poll(r.Context(), req.DeviceCode)
	switch {
	case err == nil:
		httpx.JSON(w, map[string]any{
			"install_credential": cred.Token,
			"account_id":         cred.AccountID,
		})
	case errors.Is(err, devicelink.ErrPending):
		// authorization_pending — the install keeps polling.
		httpx.Err(w, http.StatusPreconditionRequired, "authorization_pending")
	case errors.Is(err, devicelink.ErrDenied):
		httpx.Err(w, http.StatusForbidden, "access_denied")
	case errors.Is(err, devicelink.ErrConsumed):
		// The credential was already handed out — a second poll never re-issues.
		httpx.Err(w, http.StatusGone, "credential already issued")
	case errors.Is(err, devicelink.ErrNotFound):
		httpx.Err(w, http.StatusNotFound, "unknown or expired device_code")
	default:
		httpx.Err(w, http.StatusInternalServerError, "poll failed")
	}
}

// clientIPCP extracts a client IP for rate-limit keying (the device-link flood
// limiter). It uses the canonical trusted-edge resolver rather than a raw
// leftmost X-Forwarded-For — an attacker-settable header would otherwise let a
// caller bypass the flood limit by rotating XFF per request. ddos.RealClientIP
// honours a forwarding header only from a trusted-CIDR upstream, else RemoteAddr.
func clientIPCP(r *http.Request) string {
	return ddos.RealClientIP(r)
}
