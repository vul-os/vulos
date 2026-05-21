package main

// routes_passkeys.go — AUTH-12: server-side passkey (FIDO2/WebAuthn) routes.
//
// Endpoints (all session-authed via the X-User-ID header set by the existing
// auth middleware):
//
//	POST   /api/passkeys/register/begin   — start registration ceremony
//	POST   /api/passkeys/register/finish  — complete registration ceremony
//	POST   /api/passkeys/assert/begin     — start assertion (login) ceremony
//	POST   /api/passkeys/assert/finish    — complete assertion ceremony
//	GET    /api/passkeys                  — list credentials for current user
//	DELETE /api/passkeys/{id}             — delete a credential
//
// See ROUTES.md for the per-area routes file contract; all dependencies are
// passed in explicitly (no globals captured from main.go).

import (
	"encoding/json"
	"net/http"

	"vulos/backend/services/auth"
	"vulos/backend/services/passkeys"
)

// registerPasskeysRoutes wires the passkeys REST API into mux.
// authStore is accepted for parity with other session-authed route files;
// the authenticated user is taken from the X-User-ID header set by the
// existing auth middleware.
func registerPasskeysRoutes(mux *http.ServeMux, svc *passkeys.Service, authStore *auth.Store) {
	h := &au12PasskeysHandler{svc: svc, authStore: authStore}

	mux.HandleFunc("POST /api/passkeys/register/begin", h.beginRegister)
	mux.HandleFunc("POST /api/passkeys/register/finish", h.finishRegister)
	mux.HandleFunc("POST /api/passkeys/assert/begin", h.beginAssert)
	mux.HandleFunc("POST /api/passkeys/assert/finish", h.finishAssert)
	mux.HandleFunc("GET /api/passkeys", h.list)
	mux.HandleFunc("DELETE /api/passkeys/{id}", h.delete)
}

// au12PasskeysHandler is the HTTP handler for passkey routes.
type au12PasskeysHandler struct {
	svc       *passkeys.Service
	authStore *auth.Store
}

// POST /api/passkeys/register/begin
// Body:     { "display_name": "Alice" }
// Response: { "challenge": <PublicKeyCredentialCreationOptions>,
//
//	"session_data": <opaque string> }
func (h *au12PasskeysHandler) beginRegister(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}

	var req struct {
		DisplayName string `json:"display_name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.DisplayName == "" {
		req.DisplayName = userID
	}

	challenge, sessionData, err := h.svc.BeginRegistration(userID, req.DisplayName)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]json.RawMessage{
		"challenge":    json.RawMessage(challenge),
		"session_data": au12JSONString(sessionData),
	})
}

// POST /api/passkeys/register/finish
// Body:     { "session_data": <opaque from begin>,
//
//	"attestation_response": <AuthenticatorAttestationResponse JSON> }
//
// Response: { "credential_id": "..." }
func (h *au12PasskeysHandler) finishRegister(w http.ResponseWriter, r *http.Request) {
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

	credID, err := h.svc.FinishRegistration(userID, req.AttestationResponse, au12UnquoteSession(req.SessionData))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]string{"credential_id": credID})
}

// POST /api/passkeys/assert/begin
// Response: { "challenge": <PublicKeyCredentialRequestOptions>,
//
//	"session_data": <opaque string> }
func (h *au12PasskeysHandler) beginAssert(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}

	challenge, sessionData, err := h.svc.BeginAssertion(userID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]json.RawMessage{
		"challenge":    json.RawMessage(challenge),
		"session_data": au12JSONString(sessionData),
	})
}

// POST /api/passkeys/assert/finish
// Body:     { "session_data": <opaque from begin>,
//
//	"assertion_response": <AuthenticatorAssertionResponse JSON> }
//
// Response: { "verified": true }
func (h *au12PasskeysHandler) finishAssert(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}

	var req struct {
		SessionData       json.RawMessage `json:"session_data"`
		AssertionResponse json.RawMessage `json:"assertion_response"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request body")
		return
	}
	if len(req.SessionData) == 0 || len(req.AssertionResponse) == 0 {
		writeErr(w, 400, "session_data and assertion_response required")
		return
	}

	verified, err := h.svc.FinishAssertion(userID, req.AssertionResponse, au12UnquoteSession(req.SessionData))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"verified": verified})
}

// GET /api/passkeys
// Response: [ { "id": "...", "created_at": "...", "last_used": "..." }, ... ]
func (h *au12PasskeysHandler) list(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}

	creds, err := h.svc.List(userID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if creds == nil {
		creds = []passkeys.Credential{}
	}
	writeJSON(w, creds)
}

// DELETE /api/passkeys/{id}
// Response: { "status": "deleted" }
func (h *au12PasskeysHandler) delete(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeErr(w, 400, "credential id required")
		return
	}

	if err := h.svc.Delete(userID, id); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

// au12JSONString JSON-encodes b as a JSON string so the opaque session blob
// is delivered to the client as a single string value.
func au12JSONString(b []byte) json.RawMessage {
	out, _ := json.Marshal(string(b))
	return out
}

// au12UnquoteSession accepts the session_data field whether it arrives as a
// JSON string (the form beginRegister/beginAssert emit) or as a raw JSON
// object, and returns the underlying opaque bytes the service expects.
func au12UnquoteSession(raw json.RawMessage) []byte {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []byte(s)
	}
	return raw
}
