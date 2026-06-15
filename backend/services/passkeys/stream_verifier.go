package passkeys

import (
	"encoding/json"
	"fmt"
)

// StreamVerifier adapts *Service to the stream.A13_WebAuthnVerifier duck-type interface.
// The "assertion" bytes it receives must be a JSON object:
//
//	{"session_data": "<opaque>", "assertion_response": {...WebAuthn assertion...}}
//
// The begin step (generating the challenge and session_data) is handled by
// POST /api/stream/webauthn-begin, which the client calls before signing.
// The client must include the returned session_data verbatim when POSTing the assertion.
type StreamVerifier struct {
	svc *Service
}

// NewStreamVerifier creates a StreamVerifier wrapping svc.
func NewStreamVerifier(svc *Service) *StreamVerifier {
	return &StreamVerifier{svc: svc}
}

// streamAssertionEnvelope is the JSON the client POSTs to /api/stream/webauthn-assert.
type streamAssertionEnvelope struct {
	SessionData       string          `json:"session_data"`
	AssertionResponse json.RawMessage `json:"assertion_response"`
}

// Verify implements the stream.A13_WebAuthnVerifier duck-type interface.
// assertion must be JSON: {"session_data":"<opaque>","assertion_response":{...}}
// The client obtains session_data from POST /api/stream/webauthn-begin.
func (v *StreamVerifier) Verify(userID string, assertion []byte) error {
	var env streamAssertionEnvelope
	if err := json.Unmarshal(assertion, &env); err != nil {
		return fmt.Errorf("webauthn: assertion must be JSON {session_data, assertion_response}: %w", err)
	}
	if env.SessionData == "" {
		return fmt.Errorf("webauthn: session_data missing — call POST /api/stream/webauthn-begin first to obtain a challenge")
	}
	if len(env.AssertionResponse) == 0 {
		return fmt.Errorf("webauthn: assertion_response missing")
	}
	ok, err := v.svc.FinishAssertion(userID, []byte(env.AssertionResponse), []byte(env.SessionData))
	if err != nil {
		return fmt.Errorf("webauthn: %w", err)
	}
	if !ok {
		return fmt.Errorf("webauthn: assertion not verified")
	}
	return nil
}

// BeginStreamAssertion starts a WebAuthn assertion ceremony for the given user,
// returning the challenge JSON (to send to the client) and the opaque sessionData
// (which the client must include verbatim in the assertion envelope).
func (v *StreamVerifier) BeginStreamAssertion(userID string) (challenge []byte, sessionData []byte, err error) {
	return v.svc.BeginAssertion(userID)
}
