// saWebauthn-/A13_ WebAuthn re-auth gate for input-injection sessions.
//
// When a streaming session wires uinput input injection (i.e. the injector is
// non-nil), the session starts in the "gated" state: the data channels for
// mouse/keyboard/gamepad silently drop all events.  The client receives a
// {"t":"need-webauthn"} message over the data channel and must POST
// /api/stream/webauthn-assert with a valid WebAuthn assertion to lift the gate.
//
// Phishing-resistant property: only hardware-backed authenticators can
// produce a valid assertion, so a phished session token alone cannot
// activate input injection.
package stream

import (
	"errors"
	"fmt"
	"log"
	"sync"
)

// A13_WebAuthnVerifier is the interface the stream package requires from the
// auth layer.  If no passkeys/WebAuthn package exists yet, callers should
// supply a stub.  The Pool exposes SetWebAuthnVerifier so main.go can wire
// the real implementation once it exists without touching this file.
type A13_WebAuthnVerifier interface {
	// Verify checks a raw WebAuthn assertion for the given user.
	// It returns nil on success, or a non-nil error if the assertion is
	// missing, malformed, or does not correspond to an enrolled credential.
	Verify(userID string, assertion []byte) error
}

// A13_stubVerifier is a no-op verifier used when no real WebAuthn backend is
// configured.  It always rejects assertions so the gate can never be silently
// bypassed.  Replace via Pool.SetWebAuthnVerifier once passkeys are enrolled.
type A13_stubVerifier struct{}

func (A13_stubVerifier) Verify(_ string, _ []byte) error {
	return errors.New("webauthn: no verifier configured — enroll a passkey first")
}

// saWebauthn_gateState tracks per-session WebAuthn gate state.
// It lives in a package-level sidecar map keyed by session ID so that
// stream.go's Session struct only gains the single `inputGated bool` field
// required by the spec, and this file owns the rest of the state.
type saWebauthn_gateState struct {
	mu       sync.Mutex
	userID   string
	verified bool // true once a valid assertion has been received
}

var (
	saWebauthn_mu    sync.Mutex
	saWebauthn_gates = map[string]*saWebauthn_gateState{} // sessionID → state
)

// saWebauthn_register creates a gate entry for sess when it has an injector.
// Called from Pool.Launch immediately after sess.injector is set.
func saWebauthn_register(sessID, userID string) {
	saWebauthn_mu.Lock()
	defer saWebauthn_mu.Unlock()
	saWebauthn_gates[sessID] = &saWebauthn_gateState{userID: userID}
	log.Printf("[stream/webauthn] A13: gate registered for session %s (user=%s)", sessID, userID)
}

// saWebauthn_unregister removes the gate entry when a session is stopped.
func saWebauthn_unregister(sessID string) {
	saWebauthn_mu.Lock()
	defer saWebauthn_mu.Unlock()
	delete(saWebauthn_gates, sessID)
}

// saWebauthn_isVerified reports whether the gate for sessID has been lifted.
func saWebauthn_isVerified(sessID string) bool {
	saWebauthn_mu.Lock()
	g, ok := saWebauthn_gates[sessID]
	saWebauthn_mu.Unlock()
	if !ok {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.verified
}

// RegisterGatedSessionForTest installs a bare, input-gated session into the
// pool and arms its AUTH-13 gate for userID. It exists so the HTTP handlers in
// package main (which cannot reach the unexported p.sessions map or spin up a
// real compositor via Launch) can exercise the /api/stream/webauthn-assert
// round-trip end to end. It is test-support only — production sessions are
// created via Launch.
func (p *Pool) RegisterGatedSessionForTest(sessID, userID string) *Session {
	sess := &Session{ID: sessID, OwnerID: userID}
	sess.inputGated = true
	p.mu.Lock()
	p.sessions[sessID] = sess
	p.mu.Unlock()
	saWebauthn_register(sessID, userID)
	return sess
}

// IsInputGatedForTest reports whether sess's input-injection gate is still
// active. Test-support only, so package main can observe the gate transition.
func (s *Session) IsInputGatedForTest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inputGated
}

// RequireAssertion verifies a WebAuthn assertion for sess and, on success,
// flips sess.inputGated = false (lifting the input injection gate).
//
// It is called by the POST /api/stream/webauthn-assert handler.
func RequireAssertion(sess *Session, assertion []byte, verifier A13_WebAuthnVerifier) error {
	if sess == nil {
		return errors.New("webauthn: nil session")
	}
	if len(assertion) == 0 {
		return errors.New("webauthn: assertion is empty")
	}

	saWebauthn_mu.Lock()
	g, ok := saWebauthn_gates[sess.ID]
	saWebauthn_mu.Unlock()

	if !ok {
		// Session has no injector — gate not applicable. Treat as success so the
		// caller can still respond 200 without confusing the client.
		return nil
	}

	g.mu.Lock()
	userID := g.userID
	g.mu.Unlock()

	if verifier == nil {
		verifier = A13_stubVerifier{}
	}

	if err := verifier.Verify(userID, assertion); err != nil {
		return fmt.Errorf("webauthn: assertion rejected: %w", err)
	}

	// Lift the gate.
	g.mu.Lock()
	g.verified = true
	g.mu.Unlock()

	sess.mu.Lock()
	sess.inputGated = false
	sess.mu.Unlock()

	log.Printf("[stream/webauthn] A13: gate lifted for session %s (user=%s)", sess.ID, userID)
	return nil
}
