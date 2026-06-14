// Package passkeys — login.go
//
// LOGINISO-01: promotes WebAuthn from the AUTH-13 re-auth gate to a full
// registration + assertion LOGIN flow.
//
// LoginService wraps the passkeys.Service and an auth.Store reference so that
// a successful assertion ceremony can create a real OS session (cookie-ready
// token) for the user. The password+2FA path in auth.Handler is untouched.
//
// Two new public HTTP endpoints (no prior session required):
//
//	POST /api/auth/passkey/login/begin    — start assertion for username
//	POST /api/auth/passkey/login/finish   — verify assertion + issue session
//
// And two session-authed endpoints (registration, for enrolled users):
//
//	POST /api/auth/passkey/register/begin  — start registration ceremony
//	POST /api/auth/passkey/register/finish — complete registration ceremony
//
// (These mirror the /api/passkeys/* management endpoints but are wired through
// the login-flow helper so the session-setup logic lives in one place.)
package passkeys

import (
	"fmt"
	"log"
	"time"

	"vulos/backend/services/auth"
)

// LoginService ties a passkeys.Service to an auth.Store so that a verified
// WebAuthn assertion produces a full OS session token.
type LoginService struct {
	svc   *Service
	store *auth.Store
}

// NewLoginService constructs a LoginService.
func NewLoginService(svc *Service, store *auth.Store) *LoginService {
	return &LoginService{svc: svc, store: store}
}

// BeginLogin starts a WebAuthn assertion ceremony for a given username.
// Returns the JSON challenge options and an opaque sessionData blob.
// This is the LOGINISO-01 public entry point — no prior session needed.
func (ls *LoginService) BeginLogin(username string) (challenge []byte, sessionData []byte, err error) {
	if username == "" {
		return nil, nil, fmt.Errorf("passkeys/login: username required")
	}
	u := ls.store.GetUserByUsername(username)
	if u == nil {
		return nil, nil, fmt.Errorf("passkeys/login: user not found")
	}
	return ls.svc.BeginAssertion(u.ID)
}

// FinishLogin completes a WebAuthn assertion ceremony.
//
// On success it creates an OS session via auth.Store.CreateSession and returns
// the session token. The caller is responsible for setting the cookie.
func (ls *LoginService) FinishLogin(username string, assertionResp []byte, sessionData []byte) (*auth.Session, error) {
	if username == "" {
		return nil, fmt.Errorf("passkeys/login: username required")
	}
	u := ls.store.GetUserByUsername(username)
	if u == nil {
		return nil, fmt.Errorf("passkeys/login: user not found")
	}

	verified, err := ls.svc.FinishAssertion(u.ID, assertionResp, sessionData)
	if err != nil {
		return nil, fmt.Errorf("passkeys/login: assertion failed: %w", err)
	}
	if !verified {
		return nil, fmt.Errorf("passkeys/login: assertion not verified")
	}

	// Update last_login timestamp on the user record.
	u.LastLogin = time.Now()

	sess := ls.store.CreateSession(u, "")
	ls.store.Flush()
	log.Printf("[passkeys/login] LOGINISO-01: passkey login OK for user %q (id=%s)", username, u.ID)
	return sess, nil
}

// BeginRegister starts a WebAuthn registration ceremony for an already
// authenticated user. The userID must be resolved by the caller from the
// session (X-User-ID header) before calling this.
func (ls *LoginService) BeginRegister(userID, displayName string) (challenge []byte, sessionData []byte, err error) {
	return ls.svc.BeginRegistration(userID, displayName)
}

// FinishRegister completes a WebAuthn registration ceremony and returns the
// new credential ID (base64url). The userID must match the one from BeginRegister.
func (ls *LoginService) FinishRegister(userID string, attestationResp []byte, sessionData []byte) (credentialID string, err error) {
	return ls.svc.FinishRegistration(userID, attestationResp, sessionData)
}
