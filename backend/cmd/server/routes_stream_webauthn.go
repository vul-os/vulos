// saWebauthn-/A13_ WebAuthn re-auth endpoints for input-injection sessions.
//
// POST /api/stream/webauthn-begin   — start assertion: get challenge + session_data
// POST /api/stream/webauthn-assert  — finish assertion: lift the input gate
//
// Flow:
//  1. Client receives {"t":"need-webauthn"} data-channel message.
//  2. Client POSTs /api/stream/webauthn-begin?id=<session-id> to get challenge.
//  3. Client signs challenge with their authenticator.
//  4. Client POSTs /api/stream/webauthn-assert with:
//     {"session_data":"<from step 2>","assertion_response":{...WebAuthn response...}}
//  5. On success the input gate is lifted and the response is {"status":"ok"}.
//
// 200 {"status":"ok"} means exactly one thing: the caller's passkey was verified
// against this session's gate, and the gate is now down. Anything else is an
// error — 401 when the caller is unauthenticated, 404 when the session does not
// exist or is not theirs, 409 when the session has no gate to lift (nothing was
// verified), 403 when the assertion itself is rejected and the gate holds.
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"vulos/backend/services/auth"
	"vulos/backend/services/passkeys"
	"vulos/backend/services/stream"
)

// registerStreamWebAuthnRoutes wires the WebAuthn begin + assert endpoints.
// sv may be nil if passkeys are unavailable; in that case the begin endpoint
// returns 503 and the assert endpoint falls back to the stub verifier.
func registerStreamWebAuthnRoutes(mux *http.ServeMux, pool *stream.Pool, authStore *auth.Store, sv *passkeys.StreamVerifier) {
	// POST /api/stream/webauthn-begin?id=<session-id>
	// Returns {"challenge":{...},"session_data":"<opaque>"}
	mux.HandleFunc("POST /api/stream/webauthn-begin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if sv == nil {
			writeErr(w, 503, "passkeys not available — devicekey unavailable on this host")
			return
		}

		sessionID := r.URL.Query().Get("id")
		if sessionID == "" {
			writeErr(w, 400, "id parameter required")
			return
		}

		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, 401, "not authenticated")
			return
		}

		if sess := pool.GetForUser(sessionID, userID); sess == nil {
			writeErr(w, 404, "session not found")
			return
		}

		challenge, sessionData, err := sv.BeginStreamAssertion(userID)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}

		writeJSON(w, map[string]json.RawMessage{
			"challenge":    json.RawMessage(challenge),
			"session_data": sawaJSONString(sessionData),
		})
	})

	// POST /api/stream/webauthn-assert?id=<session-id>
	// Body: {"session_data":"<from begin>","assertion_response":{...}} OR raw bytes
	mux.HandleFunc("POST /api/stream/webauthn-assert", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, 401, "not authenticated")
			return
		}

		// Resolve session from query param ?id=<session-id>
		sessionID := r.URL.Query().Get("id")
		if sessionID == "" {
			// Also accept JSON body with {"id":"...","assertion":"..."}
			var body struct {
				ID        string `json:"id"`
				Assertion string `json:"assertion"` // base64url or raw bytes as hex
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.ID != "" {
				sessionID = body.ID
				handleAssertionJSON(w, pool, sessionID, userID, body.Assertion)
				return
			}
			writeErr(w, 400, "id parameter required")
			return
		}

		// Read assertion bytes from body
		assertionBytes, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil || len(assertionBytes) == 0 {
			writeErr(w, 400, "assertion body required")
			return
		}

		handleAssertionBytes(w, pool, sessionID, userID, assertionBytes)
	})
}

// handleAssertionJSON handles the JSON body variant where the assertion is
// base64url-encoded inside a JSON envelope.
func handleAssertionJSON(w http.ResponseWriter, pool *stream.Pool, sessionID, userID, assertionB64 string) {
	var assertionBytes []byte
	var err error
	if assertionB64 != "" {
		assertionBytes, err = base64.RawURLEncoding.DecodeString(assertionB64)
		if err != nil {
			// Try standard base64
			assertionBytes, err = base64.StdEncoding.DecodeString(assertionB64)
			if err != nil {
				writeErr(w, 400, "assertion: invalid base64")
				return
			}
		}
	}
	handleAssertionBytes(w, pool, sessionID, userID, assertionBytes)
}

// handleAssertionBytes calls RequireAssertion and writes the HTTP response.
// The session is resolved through GetForUser, so a session the caller does not
// own is indistinguishable from one that does not exist.
func handleAssertionBytes(w http.ResponseWriter, pool *stream.Pool, sessionID, userID string, assertion []byte) {
	sess := pool.GetForUser(sessionID, userID)
	if sess == nil {
		writeErr(w, 404, "session not found")
		return
	}

	verifier := pool.WebAuthnVerifier()
	switch err := stream.RequireAssertion(sess, userID, assertion, verifier); {
	case err == nil:
		writeJSON(w, map[string]string{"status": "ok"})
	case errors.Is(err, stream.ErrNoGate):
		// Nothing was verified, so we must not answer "ok".
		writeErr(w, 409, "session has no input-injection gate; no assertion required")
	case errors.Is(err, stream.ErrNotSessionOwner):
		writeErr(w, 404, "session not found")
	default:
		writeErr(w, 403, err.Error())
	}
}

// sawaJSONString JSON-encodes b as a JSON string for the session_data field.
func sawaJSONString(b []byte) json.RawMessage {
	out, _ := json.Marshal(string(b))
	return out
}
