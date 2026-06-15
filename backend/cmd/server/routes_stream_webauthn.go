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
// On failure the gate remains active and the response is 403 {"error":"..."}.
package main

import (
	"encoding/base64"
	"encoding/json"
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

		sess := pool.Get(sessionID)
		if sess == nil {
			writeErr(w, 404, "session not found")
			return
		}

		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, 401, "not authenticated")
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
				handleAssertionJSON(w, pool, sessionID, body.Assertion)
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

		handleAssertionBytes(w, pool, sessionID, assertionBytes)
	})
}

// handleAssertionJSON handles the JSON body variant where the assertion is
// base64url-encoded inside a JSON envelope.
func handleAssertionJSON(w http.ResponseWriter, pool *stream.Pool, sessionID, assertionB64 string) {
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
	handleAssertionBytes(w, pool, sessionID, assertionBytes)
}

// handleAssertionBytes calls RequireAssertion and writes the HTTP response.
func handleAssertionBytes(w http.ResponseWriter, pool *stream.Pool, sessionID string, assertion []byte) {
	sess := pool.Get(sessionID)
	if sess == nil {
		writeErr(w, 404, "session not found")
		return
	}

	verifier := pool.WebAuthnVerifier()
	if err := stream.RequireAssertion(sess, assertion, verifier); err != nil {
		writeErr(w, 403, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "ok"})
}

// sawaJSONString JSON-encodes b as a JSON string for the session_data field.
func sawaJSONString(b []byte) json.RawMessage {
	out, _ := json.Marshal(string(b))
	return out
}
