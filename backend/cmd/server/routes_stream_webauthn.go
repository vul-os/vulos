// saWebauthn-/A13_ WebAuthn re-auth endpoint for input-injection sessions.
//
// POST /api/stream/webauthn-assert
//
// The client POSTs a raw WebAuthn assertion (base64url-encoded or binary) after
// receiving a {"t":"need-webauthn"} data-channel message from the streaming
// session.  On success the input gate on the session is lifted and the response
// is {"status":"ok"}.  On failure the gate remains active and the response is
// 403 {"error":"..."}.
package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"

	"vulos/backend/services/auth"
	"vulos/backend/services/stream"
)

// registerStreamWebAuthnRoutes wires the WebAuthn assertion endpoint.
// It is called from main.go with a single line:
//
//	registerStreamWebAuthnRoutes(mux, streamPool, authStore)
func registerStreamWebAuthnRoutes(mux *http.ServeMux, pool *stream.Pool, authStore *auth.Store) {
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
