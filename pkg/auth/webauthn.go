// webauthn.go -- WebAuthn (FIDO2) passkey + security-key support.
//
// Supports both platform authenticators (Touch ID, Windows Hello, ChromeOS,
// Linux fprintd, Android biometrics) and roaming authenticators (YubiKey,
// security keys over USB/NFC/BLE).
//
// No biometric data is ever stored -- only the credential ID and public key
// emitted by the authenticator (as per WebAuthn spec §4). Sign-count monotonic
// validation is enforced on every assertion.
//
// Rate-limiting and lockout re-use the same Check2FALockout / RecordFailed2FA /
// ResetFailed2FA primitives as AUTH-07 TOTP.
//
// Session challenge storage: the caller (route handler) is responsible for
// serialising the *webauthn.SessionData to a short-lived server-side store
// (e.g. an in-memory map keyed by userID or a pending-session cookie). This
// keeps the Store layer stateless with respect to pending ceremonies.
package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ──────────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ──────────────────────────────────────────────────────────────────────────────

var (
	// ErrWebAuthnNotConfigured is returned when the WebAuthn RP config env vars
	// are not set (WEBAUTHN_RPID / WEBAUTHN_ORIGIN).
	ErrWebAuthnNotConfigured = errors.New("auth: WebAuthn not configured")

	// ErrWebAuthnCredentialNotFound is returned when a credential ID is not
	// found in the database for the given user.
	ErrWebAuthnCredentialNotFound = errors.New("auth: WebAuthn credential not found")

	// ErrWebAuthnSignCountReplay is returned when the incoming sign-count is
	// not greater than the stored value (potential cloned authenticator).
	ErrWebAuthnSignCountReplay = errors.New("auth: WebAuthn sign-count replay detected")

	// ErrWebAuthnNoCredentials is returned when a user has no registered
	// WebAuthn credentials.
	ErrWebAuthnNoCredentials = errors.New("auth: user has no WebAuthn credentials")
)

// ──────────────────────────────────────────────────────────────────────────────
// WebAuthn instance factory
// ──────────────────────────────────────────────────────────────────────────────

// NewWebAuthn returns a configured *webauthn.WebAuthn instance for the RP.
//
// Configuration is read from environment variables:
//
//	WEBAUTHN_RPID      – relying-party domain, e.g. "app.vulos.org"
//	WEBAUTHN_ORIGIN    – full origin, e.g. "https://app.vulos.org"
//	WEBAUTHN_RPNAME    – display name shown in browser UI (default "Vulos Cloud")
//
// Returns ErrWebAuthnNotConfigured when WEBAUTHN_RPID or WEBAUTHN_ORIGIN are absent.
func NewWebAuthn() (*webauthn.WebAuthn, error) {
	rpid := os.Getenv("WEBAUTHN_RPID")
	origin := os.Getenv("WEBAUTHN_ORIGIN")
	if rpid == "" || origin == "" {
		return nil, ErrWebAuthnNotConfigured
	}
	rpName := os.Getenv("WEBAUTHN_RPNAME")
	if rpName == "" {
		rpName = "Vulos Cloud"
	}
	return webauthn.New(&webauthn.Config{
		RPID:          rpid,
		RPDisplayName: rpName,
		RPOrigins:     []string{origin},
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// WebAuthn User adapter (implements webauthn.User)
// ──────────────────────────────────────────────────────────────────────────────

// WebAuthnUserAdapter adapts our User record + credential list to the
// webauthn.User interface required by the go-webauthn library.
// Exported so route handlers can keep a typed reference for BeginLogin.
type WebAuthnUserAdapter struct {
	ID          string
	Email       string
	Credentials []webauthn.Credential
}

func (u *WebAuthnUserAdapter) WebAuthnID() []byte          { return []byte(u.ID) }
func (u *WebAuthnUserAdapter) WebAuthnName() string        { return u.Email }
func (u *WebAuthnUserAdapter) WebAuthnDisplayName() string { return u.Email }
func (u *WebAuthnUserAdapter) WebAuthnCredentials() []webauthn.Credential {
	return u.Credentials
}

// ──────────────────────────────────────────────────────────────────────────────
// Public-facing credential record
// ──────────────────────────────────────────────────────────────────────────────

// WebAuthnCredential is the externally visible credential record returned by
// ListWebAuthnCredentials and FinishWebAuthnRegistration.
type WebAuthnCredential struct {
	ID           string   `json:"id"`
	CredentialID string   `json:"credential_id"` // base64url-encoded
	FriendlyName string   `json:"friendly_name"`
	Transports   []string `json:"transports"`
	AAGUID       string   `json:"aaguid"`
	LastUsedAt   string   `json:"last_used_at"` // RFC3339 or ""
	CreatedAt    string   `json:"created_at"`   // RFC3339
}

// ──────────────────────────────────────────────────────────────────────────────
// Store: registration
// ──────────────────────────────────────────────────────────────────────────────

// BeginWebAuthnRegistration generates a credential creation options payload for
// the given user. The returned *webauthn.SessionData must be persisted by the
// caller (e.g. in a pending-ceremony map) and provided to
// FinishWebAuthnRegistration.
//
// Already-registered credentials are included in the ExcludeCredentials list so
// the browser prevents re-registering the same authenticator.
func (s *Store) BeginWebAuthnRegistration(
	ctx context.Context,
	u *User,
) (*protocol.CredentialCreation, *webauthn.SessionData, error) {
	wa, err := NewWebAuthn()
	if err != nil {
		return nil, nil, err
	}

	existing, err := s.webAuthnCredentialsForUser(ctx, u.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: webauthn begin registration: %w", err)
	}

	wu := &WebAuthnUserAdapter{ID: u.ID, Email: u.Email, Credentials: existing}

	var excludeList []protocol.CredentialDescriptor
	for i := range existing {
		excludeList = append(excludeList, existing[i].Descriptor())
	}

	creation, session, err := wa.BeginRegistration(wu, webauthn.WithExclusions(excludeList))
	if err != nil {
		return nil, nil, fmt.Errorf("auth: webauthn begin registration: %w", err)
	}
	return creation, session, nil
}

// FinishWebAuthnRegistration validates the authenticator's attestation response,
// stores the new credential, and returns its public WebAuthnCredential record.
//
// friendlyName is the user-supplied label (may be "").
func (s *Store) FinishWebAuthnRegistration(
	ctx context.Context,
	u *User,
	session webauthn.SessionData,
	r *http.Request,
	friendlyName string,
) (*WebAuthnCredential, error) {
	wa, err := NewWebAuthn()
	if err != nil {
		return nil, err
	}

	existing, err := s.webAuthnCredentialsForUser(ctx, u.ID)
	if err != nil {
		return nil, fmt.Errorf("auth: webauthn finish registration: %w", err)
	}

	wu := &WebAuthnUserAdapter{ID: u.ID, Email: u.Email, Credentials: existing}

	cred, err := wa.FinishRegistration(wu, session, r)
	if err != nil {
		return nil, fmt.Errorf("auth: webauthn finish registration: %w", err)
	}

	// Derive AAGUID string from the authenticator data.
	aaguid := ""
	if len(cred.Authenticator.AAGUID) == 16 {
		aaguid = fmt.Sprintf("%x", cred.Authenticator.AAGUID)
	}

	return s.storeWebAuthnCredential(ctx, u.ID, cred, aaguid, friendlyName)
}

// ──────────────────────────────────────────────────────────────────────────────
// Store: login
// ──────────────────────────────────────────────────────────────────────────────

// LoadWebAuthnUserForLogin looks up the user by email and loads all their
// registered WebAuthn credentials. Returns ErrNotFound if the email is unknown
// and ErrWebAuthnNoCredentials if the user has no passkeys.
func (s *Store) LoadWebAuthnUserForLogin(ctx context.Context, email string) (*WebAuthnUserAdapter, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var userID string
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT id FROM users WHERE email = ?`), email).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth: webauthn user lookup: %w", err)
	}
	creds, err := s.webAuthnCredentialsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(creds) == 0 {
		return nil, ErrWebAuthnNoCredentials
	}
	return &WebAuthnUserAdapter{ID: userID, Email: email, Credentials: creds}, nil
}

// LoadWebAuthnUserByID looks up a user by their internal ID and loads all their
// registered WebAuthn credentials. Used by the passkey-as-2FA route (AUTH-12)
// where the user identity is known from the partial session rather than an email
// form field. Returns ErrNotFound if the user ID is unknown and
// ErrWebAuthnNoCredentials if the user has no registered passkeys.
func (s *Store) LoadWebAuthnUserByID(ctx context.Context, userID string) (*WebAuthnUserAdapter, error) {
	var email string
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT email FROM users WHERE id = ?`), userID).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth: webauthn user by id: %w", err)
	}
	creds, err := s.webAuthnCredentialsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(creds) == 0 {
		return nil, ErrWebAuthnNoCredentials
	}
	return &WebAuthnUserAdapter{ID: userID, Email: email, Credentials: creds}, nil
}

// FinishWebAuthnLoginNoSession validates the authenticator's assertion and
// enforces the sign-count monotonic check, but does NOT create a session.
// Used by the passkey-as-2FA flow (AUTH-12) where UpgradePartialSession is
// called separately to promote the existing partial session to a full one.
//
// On failure it records a failed 2FA attempt (shared lockout with TOTP).
// The caller should check Check2FALockout before calling this method.
func (s *Store) FinishWebAuthnLoginNoSession(
	ctx context.Context,
	wu *WebAuthnUserAdapter,
	session webauthn.SessionData,
	r *http.Request,
) error {
	wa, err := NewWebAuthn()
	if err != nil {
		return err
	}

	cred, err := wa.FinishLogin(wu, session, r)
	if err != nil {
		_ = s.RecordFailed2FA(ctx, wu.ID)
		return fmt.Errorf("auth: webauthn finish login: %w", err)
	}

	// Sign-count monotonic check.
	credIDBase64 := base64.RawURLEncoding.EncodeToString(cred.ID)
	if scErr := s.updateWebAuthnSignCount(ctx, wu.ID, credIDBase64, cred.Authenticator.SignCount); scErr != nil {
		_ = s.RecordFailed2FA(ctx, wu.ID)
		return scErr
	}

	// Success: reset lockout counter.
	_ = s.ResetFailed2FA(ctx, wu.ID)
	return nil
}

// BeginWebAuthnLogin creates the assertion options payload for the given user.
// The returned *webauthn.SessionData must be stored server-side by the caller.
func (s *Store) BeginWebAuthnLogin(
	ctx context.Context,
	wu *WebAuthnUserAdapter,
) (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	wa, err := NewWebAuthn()
	if err != nil {
		return nil, nil, err
	}
	assertion, session, err := wa.BeginLogin(wu)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: webauthn begin login: %w", err)
	}
	return assertion, session, nil
}

// FinishWebAuthnLogin validates the authenticator's assertion, enforces the
// sign-count monotonic check, and on success issues a full session.
//
// On failure it records a failed 2FA attempt (shared lockout with TOTP from
// AUTH-07). The caller should check Check2FALockout before calling this method.
func (s *Store) FinishWebAuthnLogin(
	ctx context.Context,
	wu *WebAuthnUserAdapter,
	session webauthn.SessionData,
	r *http.Request,
	ip, ua string,
) (string, error) {
	wa, err := NewWebAuthn()
	if err != nil {
		return "", err
	}

	cred, err := wa.FinishLogin(wu, session, r)
	if err != nil {
		_ = s.RecordFailed2FA(ctx, wu.ID)
		return "", fmt.Errorf("auth: webauthn finish login: %w", err)
	}

	// Sign-count monotonic check.
	credIDBase64 := base64.RawURLEncoding.EncodeToString(cred.ID)
	if scErr := s.updateWebAuthnSignCount(ctx, wu.ID, credIDBase64, cred.Authenticator.SignCount); scErr != nil {
		_ = s.RecordFailed2FA(ctx, wu.ID)
		return "", scErr
	}

	// Success: reset lockout counter.
	_ = s.ResetFailed2FA(ctx, wu.ID)

	token, err := s.createSession(ctx, wu.ID, ip, ua)
	if err != nil {
		return "", err
	}
	return token, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Store: list + revoke
// ──────────────────────────────────────────────────────────────────────────────

// ListWebAuthnCredentials returns all registered WebAuthn credentials for userID.
func (s *Store) ListWebAuthnCredentials(ctx context.Context, userID string) ([]WebAuthnCredential, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, credential_id, friendly_name, transports, aaguid,
		        COALESCE(last_used_at, ''), created_at
		 FROM webauthn_credentials
		 WHERE user_id = ?
		 ORDER BY created_at ASC`),
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("auth: webauthn list credentials: %w", err)
	}
	defer rows.Close()

	var out []WebAuthnCredential
	for rows.Next() {
		var c WebAuthnCredential
		var transportsJS string
		if err := rows.Scan(
			&c.ID, &c.CredentialID, &c.FriendlyName,
			&transportsJS, &c.AAGUID, &c.LastUsedAt, &c.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("auth: webauthn scan credential row: %w", err)
		}
		var tSlice []string
		if err := json.Unmarshal([]byte(transportsJS), &tSlice); err == nil {
			c.Transports = tSlice
		} else {
			c.Transports = []string{}
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: webauthn list rows: %w", err)
	}
	if out == nil {
		out = []WebAuthnCredential{}
	}
	return out, nil
}

// RevokeWebAuthnCredential deletes the credential row identified by credRowID,
// enforcing that it belongs to userID. Returns ErrWebAuthnCredentialNotFound if
// the credential is absent or belongs to a different user.
func (s *Store) RevokeWebAuthnCredential(ctx context.Context, userID, credRowID string) error {
	s.mu.Lock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM webauthn_credentials WHERE id = ? AND user_id = ?`),
		credRowID, userID,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("auth: webauthn revoke credential: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: webauthn revoke rows affected: %w", err)
	}
	if n == 0 {
		return ErrWebAuthnCredentialNotFound
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ──────────────────────────────────────────────────────────────────────────────

// webAuthnCredentialsForUser loads all active webauthn.Credential objects for a
// user, suitable for passing to the go-webauthn library.
func (s *Store) webAuthnCredentialsForUser(ctx context.Context, userID string) ([]webauthn.Credential, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT credential_id, public_key, sign_count, transports
		 FROM webauthn_credentials
		 WHERE user_id = ?
		 ORDER BY created_at ASC`),
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("auth: webauthn load credentials: %w", err)
	}
	defer rows.Close()

	var creds []webauthn.Credential
	for rows.Next() {
		var (
			credIDBase64 string
			pubKeyBlob   []byte
			signCount    uint32
			transportsJS string
		)
		if err := rows.Scan(&credIDBase64, &pubKeyBlob, &signCount, &transportsJS); err != nil {
			return nil, fmt.Errorf("auth: webauthn scan credential: %w", err)
		}
		credID, err := base64.RawURLEncoding.DecodeString(credIDBase64)
		if err != nil {
			// Fall back to std base64 for credentials registered with padding.
			credID, err = base64.StdEncoding.DecodeString(credIDBase64)
			if err != nil {
				return nil, fmt.Errorf("auth: webauthn decode credential_id: %w", err)
			}
		}
		var transports []protocol.AuthenticatorTransport
		var rawTransports []string
		if err := json.Unmarshal([]byte(transportsJS), &rawTransports); err == nil {
			for _, t := range rawTransports {
				transports = append(transports, protocol.AuthenticatorTransport(t))
			}
		}
		creds = append(creds, webauthn.Credential{
			ID:        credID,
			PublicKey: pubKeyBlob,
			Authenticator: webauthn.Authenticator{
				SignCount: signCount,
			},
			Transport: transports,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: webauthn rows error: %w", err)
	}
	return creds, nil
}

// storeWebAuthnCredential persists a freshly-verified webauthn.Credential.
func (s *Store) storeWebAuthnCredential(
	ctx context.Context,
	userID string,
	cred *webauthn.Credential,
	aaguid string,
	friendlyName string,
) (*WebAuthnCredential, error) {
	id, err := newULID()
	if err != nil {
		return nil, err
	}

	credIDBase64 := base64.RawURLEncoding.EncodeToString(cred.ID)

	transportsRaw := make([]string, len(cred.Transport))
	for i, t := range cred.Transport {
		transportsRaw[i] = string(t)
	}
	transportsJSON, err := json.Marshal(transportsRaw)
	if err != nil {
		return nil, fmt.Errorf("auth: marshal transports: %w", err)
	}

	createdAt := now().Format(time.RFC3339)

	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO webauthn_credentials
		   (id, user_id, credential_id, public_key, sign_count,
		    transports, friendly_name, aaguid, last_used_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`),
		id, userID, credIDBase64, cred.PublicKey,
		cred.Authenticator.SignCount,
		string(transportsJSON),
		friendlyName,
		aaguid,
		createdAt,
	)
	s.mu.Unlock()
	if err != nil {
		if cpdb.IsUniqueViolation(err) {
			return nil, fmt.Errorf("auth: webauthn credential already registered")
		}
		return nil, fmt.Errorf("auth: webauthn insert credential: %w", err)
	}

	return &WebAuthnCredential{
		ID:           id,
		CredentialID: credIDBase64,
		FriendlyName: friendlyName,
		Transports:   transportsRaw,
		AAGUID:       aaguid,
		LastUsedAt:   "",
		CreatedAt:    createdAt,
	}, nil
}

// updateWebAuthnSignCount writes back the new sign-count and last_used_at after
// a successful assertion.
//
// Sign-count = 0 means the authenticator does not implement a counter (many
// platform authenticators, e.g. Touch ID); such credentials are always accepted
// (no replay detection via counter -- the challenge randomness is sufficient).
//
// Returns ErrWebAuthnSignCountReplay when newCount > 0 but <= storedCount.
func (s *Store) updateWebAuthnSignCount(ctx context.Context, userID, credIDBase64 string, newCount uint32) error {
	var storedCount uint32
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT sign_count FROM webauthn_credentials WHERE credential_id = ? AND user_id = ?`),
		credIDBase64, userID,
	).Scan(&storedCount)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrWebAuthnCredentialNotFound
	}
	if err != nil {
		return fmt.Errorf("auth: webauthn sign_count query: %w", err)
	}

	// Counter disabled on this authenticator: skip monotonic check.
	if storedCount == 0 && newCount == 0 {
		// Still update last_used_at.
		s.mu.Lock()
		_, err = s.db.ExecContext(ctx,
			s.db.Rebind(`UPDATE webauthn_credentials SET last_used_at = ? WHERE credential_id = ? AND user_id = ?`),
			now().Format(time.RFC3339), credIDBase64, userID,
		)
		s.mu.Unlock()
		return err
	}

	if newCount > 0 && newCount <= storedCount {
		return ErrWebAuthnSignCountReplay
	}

	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE webauthn_credentials SET sign_count = ?, last_used_at = ?
		 WHERE credential_id = ? AND user_id = ?`),
		newCount, now().Format(time.RFC3339), credIDBase64, userID,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("auth: webauthn update sign_count: %w", err)
	}
	return nil
}
