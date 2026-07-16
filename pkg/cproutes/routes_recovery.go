// routes_recovery.go — account recovery: the way back into an account whose
// only mailbox IS the account.
//
// THE CIRCLE. A Vulos account is minted from a handle and the CP provisions
// <handle>@<domain> as its anchor inbox. POST /api/auth/forgot-password
// (internal/auth/vumail_reset.go) delivers the reset link to THAT inbox and
// rejects external addresses — so a user locked out of the inbox is asked to
// read a mail they cannot reach. These routes are the ways out, in ascending
// order of cost:
//
//	POST /api/auth/password/recovery-code   — a recovery code (minted at signup,
//	                                          held out of band) resets the
//	                                          password. No mailbox needed.
//	GET/POST/DELETE /api/auth/recovery-email — optional out-of-network address a
//	                                          recovery link is COPIED to. Never an
//	                                          identity, never a login path.
//	POST /api/auth/recovery/submit          — the reviewed 14-day flow, for a
//	                                          user who has lost the codes too.
//
// Plus the flow's own surfaces: the account holder's cancel-from-a-live-session
// (the whole point of the 14-day window) and the admin review surface that
// issues, verifies and completes a request.
//
// Every route here is rate-limited per IP; the submit + recovery-code paths are
// additionally abuse-gated per account (recovery.MaxAbuse24h freezes an account
// after 3 requests in 24h; a wrong recovery code counts toward the same 2FA
// lockout as a wrong TOTP code).
package cproutes

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/auth/recovery"
	"github.com/vul-os/vulos-management/pkg/env"
	"github.com/vul-os/vulos-management/pkg/httpx"
)

// auth.Store must satisfy the engine's completion seam — recovery.Complete()
// cannot run without it, and until now only a test stub implemented it.
var _ recovery.TOTPResetter = (*auth.Store)(nil)

// maxIDUploadBytes caps a government-ID upload (decoded). Big enough for a
// phone photo of a passport page, small enough that the unauthenticated route
// cannot be used to fill the disk.
const maxIDUploadBytes = 5 << 20

// recoverySubmitMessage is returned for EVERY well-formed submit — known
// account, unknown account, or one that already has a request in flight. The
// page shows it as a success screen; it says nothing about whether the account
// exists.
const recoverySubmitMessage = "If that account exists, a recovery request has been opened and a 14-day security review has begun. Notifications have been sent to the account."

// recoveryEnumerationDelay mirrors the ~250 ms pretend-200 latency of
// /api/auth/forgot-password (routes_auth.go) so timing cannot separate a known
// account from an unknown one.
const recoveryEnumerationDelay = 250 * time.Millisecond

// registerRecoveryRoutes wires the recovery endpoints into mux. rec may be nil
// (the store failed to open); every recovery route then answers 503 rather than
// silently accepting a request it cannot durably record.
func registerRecoveryRoutes(mux *http.ServeMux, st *auth.Store, rec *recovery.Store) {
	// Recovery is a low-frequency, high-value flow: a much tighter bucket than
	// the 3/s of the ordinary auth routes. 1 request per 10 s sustained, burst 5.
	rl := newAuthRateLimiter(0.1, 5)
	// The recovery-code reset is a credential check, so it gets the same
	// shape of limiter as login (3/s, burst 10) on top of the per-account
	// 2FA lockout it feeds.
	codeRL := newAuthRateLimiter(3, 10)

	// ── POST /api/auth/recovery/submit ───────────────────────────────────────
	// Public. Body: {email, phone_last4?, review_token}.
	//
	// The page sends an EMAIL; the engine takes an account ID. The lookup is
	// enumeration-safe: an unknown address takes the same delay and returns the
	// same 200 as a known one (the pretend-200 pattern of forgot-password).
	//
	// review_token is the token support issued OFFLINE after verifying identity.
	// It is stored hashed (SetManualReviewToken) and can only be redeemed by an
	// admin who presents the SAME raw token from the support record — so a
	// self-service submission with a made-up token can never be verified.
	mux.HandleFunc("POST /api/auth/recovery/submit", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		if rec == nil {
			httpx.Err(w, http.StatusServiceUnavailable, "account recovery is unavailable")
			return
		}

		var body struct {
			Email       string `json:"email"`
			PhoneLast4  string `json:"phone_last4"`
			ReviewToken string `json:"review_token"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}

		email := strings.ToLower(strings.TrimSpace(body.Email))
		reviewToken := strings.TrimSpace(body.ReviewToken)
		if email == "" || !strings.Contains(email, "@") {
			httpx.Err(w, http.StatusBadRequest, "a valid account email is required")
			return
		}
		if len(reviewToken) < 6 {
			httpx.Err(w, http.StatusBadRequest, "a manual review token from security@"+env.Domain()+" is required")
			return
		}
		phone := digitsOnly(body.PhoneLast4)
		if phone != "" && len(phone) != 4 {
			httpx.Err(w, http.StatusBadRequest, "phone_last4 must be the last 4 digits of the phone on file")
			return
		}

		accountID, err := st.UserIDForEmail(r.Context(), email)
		if err != nil {
			log.Printf("[recovery] submit lookup: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if accountID == "" {
			// Unknown account: same delay, same body. No record is created.
			recoverySubmitOK(w)
			return
		}

		req, err := rec.SubmitRequest(r.Context(), accountID, email, phone)
		switch {
		case errors.Is(err, recovery.ErrAbuseThreshold):
			// The account is frozen for review — the page renders this 429 as
			// "frozen for security review". This IS an existence signal, but only
			// after 3 requests in 24h against a single address: the abuse gate
			// firing is worth more than the oracle it costs.
			httpx.Err(w, http.StatusTooManyRequests, "too many recovery attempts; this account is frozen pending security review")
			return
		case errors.Is(err, recovery.ErrAlreadyPending):
			// A request is already in flight. Indistinguishable from success.
			recoverySubmitOK(w)
			return
		case err != nil:
			log.Printf("[recovery] submit: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Bind the support-issued token to the request (hashed) and move it into
		// review. A failure here is not fatal: the request exists and an admin can
		// re-issue a token against it.
		if err := rec.SetManualReviewToken(r.Context(), req.ID, reviewToken); err != nil {
			log.Printf("[recovery] submit set review token: %v", err)
		}

		notifyRecoveryOpened(r, st, email, req)
		recoverySubmitOK(w)
	})

	// ── GET /api/auth/recovery/status ────────────────────────────────────────
	// Full session. Lists this account's recovery requests so the owner can see
	// one they did not make — and cancel it.
	mux.HandleFunc("GET /api/auth/recovery/status", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		if rec == nil {
			httpx.Err(w, http.StatusServiceUnavailable, "account recovery is unavailable")
			return
		}
		reqs, err := rec.ListForAccount(r.Context(), u.ID)
		if err != nil {
			log.Printf("[recovery] status: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, map[string]any{"requests": recoveryViews(reqs)})
	})

	// ── POST /api/auth/recovery/cancel ───────────────────────────────────────
	// Full session. Body: {request_id}. The 14-day window exists precisely so a
	// legitimate owner with a live session can kill a request they did not make.
	// Cancel is scoped to the caller's own account by the engine's WHERE clause.
	mux.HandleFunc("POST /api/auth/recovery/cancel", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		if rec == nil {
			httpx.Err(w, http.StatusServiceUnavailable, "account recovery is unavailable")
			return
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		var body struct {
			RequestID string `json:"request_id"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		if body.RequestID == "" {
			httpx.Err(w, http.StatusBadRequest, "request_id is required")
			return
		}
		if err := rec.Cancel(r.Context(), body.RequestID, u.ID); err != nil {
			if errors.Is(err, recovery.ErrNotFound) {
				httpx.Err(w, http.StatusNotFound, "no such open recovery request for this account")
				return
			}
			log.Printf("[recovery] cancel: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// ── POST /api/auth/recovery/id-upload ────────────────────────────────────
	// Public (the submitter is by definition locked out). The capability is the
	// request ID: 26 random Crockford characters, known only to the submitter and
	// the reviewer.
	//
	// FAIL-CLOSED: without a 32-byte RECOVERY_KEK this answers 503. The document
	// is encrypted under a per-upload DEK wrapped by the KEK before it touches
	// the disk; the engine refuses to write one otherwise.
	mux.HandleFunc("POST /api/auth/recovery/id-upload", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		if rec == nil {
			httpx.Err(w, http.StatusServiceUnavailable, "account recovery is unavailable")
			return
		}
		if recoveryIDDir == "" {
			httpx.Err(w, http.StatusServiceUnavailable, "identity-document upload is not configured on this server")
			return
		}

		var body struct {
			RequestID   string `json:"request_id"`
			DocumentB64 string `json:"document_b64"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		if body.RequestID == "" {
			httpx.Err(w, http.StatusBadRequest, "request_id is required")
			return
		}
		doc, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body.DocumentB64))
		if err != nil || len(doc) == 0 {
			httpx.Err(w, http.StatusBadRequest, "document_b64 must be a base64-encoded document")
			return
		}
		if len(doc) > maxIDUploadBytes {
			httpx.Err(w, http.StatusRequestEntityTooLarge, "identity document exceeds 5 MiB")
			return
		}

		req, err := rec.GetRequest(r.Context(), body.RequestID)
		if err != nil {
			if errors.Is(err, recovery.ErrNotFound) {
				httpx.Err(w, http.StatusNotFound, "no such recovery request")
				return
			}
			log.Printf("[recovery] id-upload get: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if req.Status != recovery.StatusPending && req.Status != recovery.StatusReviewing {
			httpx.Err(w, http.StatusConflict, "this recovery request is closed")
			return
		}
		if req.IDUploadPath != "" {
			httpx.Err(w, http.StatusConflict, "an identity document has already been uploaded for this request")
			return
		}

		path := filepath.Join(recoveryIDDir, req.ID+".enc")
		if err := rec.StoreIDUpload(r.Context(), req.ID, doc, path); err != nil {
			log.Printf("[recovery] id-upload store: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "could not store the identity document")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// ── POST /api/auth/password/recovery-code ────────────────────────────────
	// Public. Body: {handle, recovery_code, new_password}.
	//
	// THE escape from the circle: no mailbox, no session, no TOTP device — just a
	// code the user was given at signup. The code is burned on use and every live
	// session is revoked.
	//
	// Enumeration-safe: an unknown handle and a wrong code are the same 401 after
	// the same delay. Abuse-gated: a wrong code feeds the account's 2FA lockout
	// (5 failures → locked, same counter as a wrong TOTP code).
	mux.HandleFunc("POST /api/auth/password/recovery-code", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !codeRL.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		var body struct {
			Handle       string `json:"handle"`
			RecoveryCode string `json:"recovery_code"`
			NewPassword  string `json:"new_password"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}

		// Resolve the account only to drive the per-account abuse gate. The
		// result is never surfaced: an unknown handle falls through to the same
		// 401 as a wrong code.
		accountID := ""
		if h := strings.ToLower(strings.TrimSpace(body.Handle)); h != "" {
			addr := h
			if !strings.Contains(addr, "@") {
				addr = addr + "@" + env.Domain()
			}
			if id, err := st.UserIDForEmail(r.Context(), addr); err == nil {
				accountID = id
			}
		}
		if accountID != "" {
			if lockedUntil, locked := st.Check2FALockout(r.Context(), accountID); locked {
				retryAfter := lockedUntil - timeNow().Unix()
				if retryAfter < 0 {
					retryAfter = 0
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprintf(w, `{"error":"account locked","retry_after":%d}`, retryAfter)
				return
			}
		}

		if auth.MaybeCheckBreached(r.Context(), hibpClient, body.NewPassword, hibpBaseURL) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"error":"password_breached","message":"password found in breach corpus; choose a different password"}`)
			return
		}

		err := st.ResetPasswordWithRecoveryCode(r.Context(), body.Handle, body.RecoveryCode, body.NewPassword)
		switch {
		case err == nil:
			if accountID != "" {
				if resetErr := st.ResetFailed2FA(r.Context(), accountID); resetErr != nil {
					log.Printf("[recovery] recovery-code reset lockout: %v", resetErr)
				}
			}
			auth.ClearSessionCookie(w)
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, auth.ErrExternalEmail):
			httpx.Err(w, http.StatusBadRequest, "recovery supports in-network addresses only; use the 'handle' field")
		case errors.Is(err, auth.ErrRecoveryCodeInvalid):
			if accountID != "" {
				if recErr := st.RecordFailed2FA(r.Context(), accountID); recErr != nil {
					log.Printf("[recovery] recovery-code record failure: %v", recErr)
				}
			}
			time.Sleep(recoveryEnumerationDelay)
			httpx.Err(w, http.StatusUnauthorized, "invalid or already-used recovery code")
		case isValidationErr(err):
			httpx.Err(w, http.StatusBadRequest, err.Error())
		default:
			log.Printf("[recovery] recovery-code reset: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
		}
	})

	// The recovery-address and reviewer surfaces are BEHIND a session (plus a
	// password re-auth / the fleet_admin flag). The submitter's 1-per-10s bucket
	// is sized for an anonymous, locked-out caller and would throttle a signed-in
	// user mid-settings-change or an admin working through the queue — so both
	// get the ordinary auth limiter instead.
	registerRecoveryEmailRoutes(mux, st, newAuthRateLimiter(3, 10))
	registerRecoveryAdminRoutes(mux, st, rec, newAuthRateLimiter(3, 10))
}

// registerRecoveryEmailRoutes wires the optional out-of-network recovery
// address. It is a DESTINATION only: nothing resolves an account from it, and
// no login path reads it (internal/auth/recovery_email.go).
func registerRecoveryEmailRoutes(mux *http.ServeMux, st *auth.Store, rl *authRateLimiter) {
	// GET /api/auth/recovery-email — full session.
	mux.HandleFunc("GET /api/auth/recovery-email", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		addr, err := st.RecoveryEmail(r.Context(), u.ID)
		if err != nil {
			log.Printf("[recovery] get recovery email: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		codes, err := st.UnusedRecoveryCodeCount(r.Context(), u.ID)
		if err != nil {
			log.Printf("[recovery] count recovery codes: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, map[string]any{"recovery_email": addr, "recovery_codes_remaining": codes})
	})

	// POST /api/auth/recovery-email — full session + password re-auth.
	// Body: {password, recovery_email}.
	mux.HandleFunc("POST /api/auth/recovery-email", func(w http.ResponseWriter, r *http.Request) {
		u := recoveryReauthSession(w, r, st, rl)
		if u == nil {
			return
		}
		var body struct {
			Password      string `json:"password"`
			RecoveryEmail string `json:"recovery_email"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		if !recoveryVerifyPassword(w, r, st, u, body.Password) {
			return
		}
		if err := st.SetRecoveryEmail(r.Context(), u.ID, body.RecoveryEmail); err != nil {
			switch {
			case errors.Is(err, auth.ErrRecoveryEmailInNetwork):
				httpx.Err(w, http.StatusBadRequest, "the recovery address must be outside "+env.Domain()+" — an in-network address is the mailbox you would be locked out of")
			case errors.Is(err, auth.ErrRecoveryEmailInvalid):
				httpx.Err(w, http.StatusBadRequest, "that is not a valid email address")
			default:
				log.Printf("[recovery] set recovery email: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// DELETE /api/auth/recovery-email — full session + password re-auth.
	// Body: {password}. The address is fully declinable, at any time.
	mux.HandleFunc("DELETE /api/auth/recovery-email", func(w http.ResponseWriter, r *http.Request) {
		u := recoveryReauthSession(w, r, st, rl)
		if u == nil {
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		if !recoveryVerifyPassword(w, r, st, u, body.Password) {
			return
		}
		if err := st.ClearRecoveryEmail(r.Context(), u.ID); err != nil {
			log.Printf("[recovery] clear recovery email: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// registerRecoveryAdminRoutes wires the reviewer's surface. fleet_admin only —
// and a fleet admin cannot disable TOTP (routes_auth.go), so the surface that can
// finish a recovery is always behind 2FA.
func registerRecoveryAdminRoutes(mux *http.ServeMux, st *auth.Store, rec *recovery.Store, rl *authRateLimiter) {
	// GET /api/admin/recovery/requests — the review queue.
	mux.HandleFunc("GET /api/admin/recovery/requests", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := recoveryAdmin(w, r, st, rec, rl); !ok {
			return
		}
		reqs, err := rec.ListActive(r.Context())
		if err != nil {
			log.Printf("[recovery] admin list: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, map[string]any{"requests": recoveryViews(reqs)})
	})

	// POST /api/admin/recovery/token — issue (or re-issue) the manual review
	// token for a request, after verifying identity offline. Body:
	// {request_id, token}. Stored SHA-256 hashed.
	mux.HandleFunc("POST /api/admin/recovery/token", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := recoveryAdmin(w, r, st, rec, rl); !ok {
			return
		}
		var body struct {
			RequestID string `json:"request_id"`
			Token     string `json:"token"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		if body.RequestID == "" || len(strings.TrimSpace(body.Token)) < 6 {
			httpx.Err(w, http.StatusBadRequest, "request_id and a token of at least 6 characters are required")
			return
		}
		if err := rec.SetManualReviewToken(r.Context(), body.RequestID, strings.TrimSpace(body.Token)); err != nil {
			if errors.Is(err, recovery.ErrNotFound) {
				httpx.Err(w, http.StatusNotFound, "no such open recovery request")
				return
			}
			log.Printf("[recovery] admin set token: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /api/admin/recovery/verify — redeem the review token. ONE-SHOT: the
	// token is marked used and TOTP is suspended for the account. Body:
	// {request_id, token}.
	mux.HandleFunc("POST /api/admin/recovery/verify", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := recoveryAdmin(w, r, st, rec, rl); !ok {
			return
		}
		var body struct {
			RequestID string `json:"request_id"`
			Token     string `json:"token"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		if body.RequestID == "" || body.Token == "" {
			httpx.Err(w, http.StatusBadRequest, "request_id and token are required")
			return
		}
		err := rec.VerifyReviewToken(r.Context(), body.RequestID, strings.TrimSpace(body.Token))
		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, recovery.ErrNotFound):
			httpx.Err(w, http.StatusNotFound, "no such recovery request")
		case errors.Is(err, recovery.ErrReviewTokenUsed):
			httpx.Err(w, http.StatusConflict, "this review token has already been used")
		case errors.Is(err, recovery.ErrReviewTokenBad):
			httpx.Err(w, http.StatusUnauthorized, "review token does not match the one on this request")
		default:
			log.Printf("[recovery] admin verify: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
		}
	})

	// POST /api/admin/recovery/complete — finish the recovery. The engine
	// re-checks the 14-day clock and the reviewing status inside its own mutex;
	// neither this route nor an admin can shorten the window. Returns the new
	// recovery codes ONCE, for the reviewer to hand to the account holder.
	mux.HandleFunc("POST /api/admin/recovery/complete", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := recoveryAdmin(w, r, st, rec, rl); !ok {
			return
		}
		var body struct {
			RequestID string `json:"request_id"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		if body.RequestID == "" {
			httpx.Err(w, http.StatusBadRequest, "request_id is required")
			return
		}
		codes, err := rec.Complete(r.Context(), body.RequestID, st)
		switch {
		case err == nil:
			httpx.JSON(w, map[string]any{"recovery_codes": codes})
		case errors.Is(err, recovery.ErrNotFound):
			httpx.Err(w, http.StatusNotFound, "no such recovery request")
		case errors.Is(err, recovery.ErrWindowNotElapsed):
			httpx.Err(w, http.StatusConflict, "the 14-day recovery window has not elapsed")
		case errors.Is(err, recovery.ErrNotEligible):
			httpx.Err(w, http.StatusConflict, "this request has not been through review")
		default:
			log.Printf("[recovery] admin complete: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
		}
	})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// recoveryAdmin gates a reviewer route: rate limit, store availability, full
// session, fleet_admin.
func recoveryAdmin(w http.ResponseWriter, r *http.Request, st *auth.Store, rec *recovery.Store, rl *authRateLimiter) (*auth.User, bool) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !rl.Allow(ip) {
		httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
		return nil, false
	}
	u := st.RequireSession(r.Context(), w, r)
	if u == nil {
		return nil, false
	}
	if !st.IsFleetAdmin(r.Context(), u.ID) {
		httpx.Err(w, http.StatusForbidden, "forbidden: not admin")
		return nil, false
	}
	if rec == nil {
		httpx.Err(w, http.StatusServiceUnavailable, "account recovery is unavailable")
		return nil, false
	}
	return u, true
}

// recoveryReauthSession is the first half of the gate on a route that changes
// WHERE recovery mail is delivered: rate limit + full session.
func recoveryReauthSession(w http.ResponseWriter, r *http.Request, st *auth.Store, rl *authRateLimiter) *auth.User {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !rl.Allow(ip) {
		httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
		return nil
	}
	return st.RequireSession(r.Context(), w, r)
}

// recoveryVerifyPassword is the second half: the account password. A stolen
// session alone must never be able to redirect an account's recovery mail.
func recoveryVerifyPassword(w http.ResponseWriter, r *http.Request, st *auth.Store, u *auth.User, password string) bool {
	var hash string
	if err := st.QueryPasswordHash(r.Context(), u.ID, &hash); err != nil {
		log.Printf("[recovery] reauth query hash: %v", err)
		httpx.Err(w, http.StatusInternalServerError, "internal error")
		return false
	}
	if err := auth.VerifyPassword(hash, password); err != nil {
		httpx.Err(w, http.StatusUnauthorized, "invalid password")
		return false
	}
	return true
}

// recoverySubmitOK writes the one response every well-formed submit gets.
func recoverySubmitOK(w http.ResponseWriter) {
	time.Sleep(recoveryEnumerationDelay)
	httpx.JSON(w, map[string]string{"message": recoverySubmitMessage})
}

// recoveryView is the wire shape of a request. It deliberately omits the review
// token hash, the ID-upload path and the encrypted DEK.
type recoveryView struct {
	ID                 string `json:"id"`
	AccountID          string `json:"account_id"`
	Email              string `json:"email"`
	Status             string `json:"status"`
	ReviewTokenUsed    bool   `json:"review_token_used"`
	TOTPSuspended      bool   `json:"totp_suspended"`
	HasIDUpload        bool   `json:"has_id_upload"`
	SubmittedAt        string `json:"submitted_at"`
	RecoveryEligibleAt string `json:"recovery_eligible_at"`
}

func recoveryViews(reqs []recovery.RecoveryRequest) []recoveryView {
	out := make([]recoveryView, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, recoveryView{
			ID:                 r.ID,
			AccountID:          r.AccountID,
			Email:              r.Email,
			Status:             r.Status,
			ReviewTokenUsed:    r.ReviewTokenUsed,
			TOTPSuspended:      r.TOTPSuspended,
			HasIDUpload:        r.IDUploadPath != "",
			SubmittedAt:        r.SubmittedAt.UTC().Format(time.RFC3339),
			RecoveryEligibleAt: r.RecoveryEligibleAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// notifyRecoveryOpened tells the account holder, through every channel they
// have, that a 14-day recovery clock is now running against their account — and
// how to stop it. Best-effort: delivery failures never fail the request.
func notifyRecoveryOpened(r *http.Request, st *auth.Store, email string, req recovery.RecoveryRequest) {
	handle := email
	if at := strings.Index(handle, "@"); at > 0 {
		handle = handle[:at]
	}
	subject := "A recovery request was opened on your Vulos account"
	body := "Someone submitted an account-recovery request for your Vulos account.\n\n" +
		"A mandatory 14-day security review has begun. Your 2FA is NOT yet reset — until " +
		req.RecoveryEligibleAt.UTC().Format(time.RFC3339) + " nothing about your account changes.\n\n" +
		"IF THIS WAS NOT YOU: sign in and cancel it at " + env.SiteURL() + "/account (request " + req.ID + "),\n" +
		"or write to security@" + env.Domain() + " immediately.\n\n" +
		"— security@" + env.Domain()

	if err := forgotPasswordSender.DeliverSystemMessage(r.Context(), handle, subject, body); err != nil {
		log.Printf("[recovery] open notice: inbox delivery failed for handle=%s: %v", handle, err)
	}
	st.NotifyRecoveryAddress(r.Context(), req.AccountID, subject, body)
}

// digitsOnly strips every non-digit rune. The page already restricts the field
// to 4 digits; this makes the server independent of it.
func digitsOnly(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}
