package main

// routes_passkey_login.go — LOGINISO-01 + LOGINISO-02 HTTP endpoints.
//
// LOGINISO-01: full passkey registration + login flow.
//
//	POST /api/auth/passkey/register/begin   — (authed) start registration
//	POST /api/auth/passkey/register/finish  — (authed) complete registration
//	POST /api/auth/passkey/login/begin      — (PUBLIC) start assertion (login)
//	POST /api/auth/passkey/login/finish     — (PUBLIC) finish assertion → session
//
// LOGINISO-02: QR / phone-approval kiosk login.
//
//	POST /api/auth/qr/begin    — (PUBLIC)  kiosk requests a QR challenge
//	POST /api/auth/qr/approve  — (authed)  authenticated phone approves
//	GET  /api/auth/qr/poll     — (PUBLIC)  kiosk polls for approval
//
// All public endpoints are listed in publicPaths (handlers.go) so the auth
// middleware does not block them.

import (
	"encoding/json"
	"io"
	"net/http"

	"vulos/backend/services/auth"
	"vulos/backend/services/passkeys"
)

// registerPasskeyLoginRoutes wires the LOGINISO endpoints into mux.
func registerPasskeyLoginRoutes(mux *http.ServeMux, ls *passkeys.LoginService, qr *passkeys.QRLoginService) {
	h := &loginISOHandler{ls: ls, qr: qr}

	// LOGINISO-01: passkey register (requires existing session).
	mux.HandleFunc("POST /api/auth/passkey/register/begin", h.beginRegister)
	mux.HandleFunc("POST /api/auth/passkey/register/finish", h.finishRegister)

	// LOGINISO-01: passkey login (public — no prior session).
	mux.HandleFunc("POST /api/auth/passkey/login/begin", h.beginLogin)
	mux.HandleFunc("POST /api/auth/passkey/login/finish", h.finishLogin)

	// LOGINISO-02: QR kiosk login.
	mux.HandleFunc("POST /api/auth/qr/begin", h.qrBegin)
	mux.HandleFunc("POST /api/auth/qr/approve", h.qrApprove)
	mux.HandleFunc("GET /api/auth/qr/poll", h.qrPoll)
}

type loginISOHandler struct {
	ls *passkeys.LoginService
	qr *passkeys.QRLoginService
}

// ─── LOGINISO-01: register ────────────────────────────────────────────────────

// POST /api/auth/passkey/register/begin
// Requires existing session (X-User-ID header set by auth middleware).
// Body (optional): { "display_name": "Alice" }
// Response: { "challenge": <PublicKeyCredentialCreationOptions>, "session_data": "..." }
func (h *loginISOHandler) beginRegister(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}

	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErr(w, 400, "invalid request body")
		return
	}
	if req.DisplayName == "" {
		req.DisplayName = userID
	}

	challenge, sessionData, err := h.ls.BeginRegister(userID, req.DisplayName)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]json.RawMessage{
		"challenge":    json.RawMessage(challenge),
		"session_data": lisoJSONString(sessionData),
	})
}

// POST /api/auth/passkey/register/finish
// Requires existing session.
// Body: { "session_data": "...", "attestation_response": {...} }
// Response: { "credential_id": "..." }
func (h *loginISOHandler) finishRegister(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}

	var req struct {
		SessionData         json.RawMessage `json:"session_data"`
		AttestationResponse json.RawMessage `json:"attestation_response"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request body")
		return
	}
	if len(req.SessionData) == 0 || len(req.AttestationResponse) == 0 {
		writeErr(w, 400, "session_data and attestation_response required")
		return
	}

	credID, err := h.ls.FinishRegister(userID, req.AttestationResponse, lisoUnquoteSession(req.SessionData))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]string{"credential_id": credID})
}

// ─── LOGINISO-01: login ───────────────────────────────────────────────────────

// POST /api/auth/passkey/login/begin
// PUBLIC — no prior session required.
// Body: { "username": "alice" }
// Response: { "challenge": <PublicKeyCredentialRequestOptions>, "session_data": "..." }
func (h *loginISOHandler) beginLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request body")
		return
	}
	if req.Username == "" {
		writeErr(w, 400, "username required")
		return
	}

	challenge, sessionData, err := h.ls.BeginLogin(req.Username)
	if err != nil {
		// Return 400 rather than 404 to avoid username enumeration.
		writeErr(w, 400, "passkey login unavailable for this user")
		return
	}
	writeJSON(w, map[string]json.RawMessage{
		"challenge":    json.RawMessage(challenge),
		"session_data": lisoJSONString(sessionData),
	})
}

// POST /api/auth/passkey/login/finish
// PUBLIC — no prior session required.
// Body: { "username": "alice", "session_data": "...", "assertion_response": {...} }
// Response: { "user": {...}, "session": {...} } + sets vulos_session cookie.
func (h *loginISOHandler) finishLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username          string          `json:"username"`
		SessionData       json.RawMessage `json:"session_data"`
		AssertionResponse json.RawMessage `json:"assertion_response"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request body")
		return
	}
	if req.Username == "" || len(req.SessionData) == 0 || len(req.AssertionResponse) == 0 {
		writeErr(w, 400, "username, session_data and assertion_response required")
		return
	}

	sess, err := h.ls.FinishLogin(req.Username, req.AssertionResponse, lisoUnquoteSession(req.SessionData))
	if err != nil {
		writeErr(w, 401, "passkey authentication failed")
		return
	}

	auth.SetSessionCookie(w, r, sess.Token)
	writeJSON(w, map[string]any{"session": sess})
}

// ─── LOGINISO-02: QR kiosk login ─────────────────────────────────────────────

// POST /api/auth/qr/begin
// PUBLIC — called by the kiosk before the user is authenticated.
// Response: { "challenge_id": "...", "qr_data": "vulos://qr-login/...", "expires_at": "..." }
func (h *loginISOHandler) qrBegin(w http.ResponseWriter, r *http.Request) {
	result, err := h.qr.Begin()
	if err != nil {
		writeErr(w, 500, "could not create QR challenge")
		return
	}
	writeJSON(w, result)
}

// POST /api/auth/qr/approve
// Requires an authenticated session (phone calling this has a valid session).
// Body: { "challenge_id": "...", "nonce": "..." }
// Response: { "status": "approved" }
//
// The nonce field is required (QRSEC-01): the approving phone app decodes the
// QR payload and must echo back the nonce embedded in it. This prevents an
// attacker who knows only the challenge_id from approving via an authenticated
// session without having actually scanned the QR code.
func (h *loginISOHandler) qrApprove(w http.ResponseWriter, r *http.Request) {
	approverUserID := r.Header.Get("X-User-ID")
	if approverUserID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}

	var req struct {
		ChallengeID string `json:"challenge_id"`
		Nonce       string `json:"nonce"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request body")
		return
	}
	if req.ChallengeID == "" {
		writeErr(w, 400, "challenge_id required")
		return
	}
	if req.Nonce == "" {
		writeErr(w, 400, "nonce required")
		return
	}

	if err := h.qr.Approve(req.ChallengeID, approverUserID, req.Nonce); err != nil {
		switch err {
		case passkeys.ErrQRChallengeNotFound:
			writeErr(w, 404, "challenge not found or expired")
		case passkeys.ErrQRChallengeExpired:
			writeErr(w, 410, "challenge has expired")
		case passkeys.ErrQRChallengeAlreadyUsed:
			writeErr(w, 409, "challenge already used")
		case passkeys.ErrQRNonceMismatch:
			writeErr(w, 403, "nonce mismatch")
		default:
			writeErr(w, 400, err.Error())
		}
		return
	}
	writeJSON(w, map[string]string{"status": "approved"})
}

// GET /api/auth/qr/poll?id=<challenge_id>
// PUBLIC — kiosk polls this until approved or expired.
// Response: { "pending": bool, "approved": bool, "expired": bool }
// The session token is NEVER included in the JSON body; it is set as an httponly
// cookie only (so it is inaccessible to JavaScript).
// Basic rate-limiting: the per-IP rate limiter applied by auth middleware covers
// this path; the endpoint also rejects requests with an empty id to avoid
// burning cycles on garbage.
func (h *loginISOHandler) qrPoll(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, 400, "id query parameter required")
		return
	}

	result, err := h.qr.Poll(id)
	if err != nil {
		writeErr(w, 404, "challenge not found")
		return
	}

	if result.Approved {
		tok := result.SessionToken()
		if tok != "" {
			// Deliver the session exclusively via httponly cookie — not in the body.
			auth.SetSessionCookie(w, r, tok)
		}
	}

	// JSON body intentionally omits the session token (unexported field).
	writeJSON(w, result)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func lisoJSONString(b []byte) json.RawMessage {
	out, _ := json.Marshal(string(b))
	return out
}

func lisoUnquoteSession(raw json.RawMessage) []byte {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []byte(s)
	}
	return raw
}
