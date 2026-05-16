// Package passkeys implements a server-resident FIDO2/WebAuthn authenticator
// (AUTH-12).
//
// The service acts as a WebAuthn Relying Party (RP). Registered credentials
// hold private/sensitive material (the COSE public key, sign counter, flags,
// etc.) that must never sit on disk in plaintext.
//
// # Sealing approach (chosen: option (a) — devicekey.KeyStore)
//
// Each credential is sealed at rest via the real main-branch
// devicekey.KeyStore API: KeyStore.Seal(plaintext) / KeyStore.Unseal(blob).
// This is the same pattern used by services/clientcerts: the KeyStore binds
// the AES-256-GCM wrapping key to this device (TPM-derived on hardware,
// device-secret-derived in software), so credentials are device-bound and
// never recoverable off-box. We deliberately do NOT layer a separate
// Argon2id key on top — devicekey already provides authenticated, device-
// bound encryption, and adding a second independent KDF would only add a
// key that has to be stored next to the ciphertext (weaker, not stronger).
//
// # On-disk layout
//
// One file per credential under the per-user directory:
//
//	<vaultDir>/<userID>/passkey-<credID>.json
//
// where <credID> is the base64url (raw, no padding) credential ID. Each file
// is an au12StoredCredential whose Sealed field is the devicekey-sealed JSON
// of the go-webauthn wa.Credential. The id / created_at / last_used metadata
// is stored in the clear (it is not sensitive) so List/Delete need not unseal.
package passkeys

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"crypto/rand"

	wa "github.com/go-webauthn/webauthn/webauthn"

	"vulos/backend/services/devicekey"
)

// A12 defaults for the Relying Party. Overridable via env for deployment.
const (
	a12DefaultRPID   = "localhost"
	a12DefaultOrigin = "http://localhost:8080"
	a12SessionTTL    = 5 * time.Minute
	a12FilePrefix    = "passkey-"
	a12FileSuffix    = ".json"
)

// Credential is the public-facing metadata for a stored passkey. It carries
// no private/cryptographic material.
type Credential struct {
	ID        string    `json:"id"` // base64url credential ID
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
}

// au12StoredCredential is the on-disk JSON for one credential file.
// Sealed is the devicekey-sealed JSON encoding of a wa.Credential.
type au12StoredCredential struct {
	ID        string    `json:"id"`     // base64url credential ID (plaintext metadata)
	Sealed    []byte    `json:"sealed"` // devicekey.KeyStore.Seal( json(wa.Credential) )
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
}

// au12Session holds pending ceremony state keyed by an opaque token.
type au12Session struct {
	Data      wa.SessionData
	UserID    string
	ExpiresAt time.Time
}

// Service is the passkeys service. Construct it with New.
type Service struct {
	mu       sync.Mutex
	vaultDir string
	ks       devicekey.KeyStore
	sessions map[string]*au12Session // opaque token -> pending ceremony
	rpID     string
	origins  []string
}

// New creates a Service that stores per-user credential files under vaultDir
// and seals each credential with ks (the device KeyStore).
//
// Per-user files live at <vaultDir>/<userID>/passkey-<credID>.json.
func New(vaultDir string, ks devicekey.KeyStore) *Service {
	rpID := os.Getenv("VULOS_RPID")
	if rpID == "" {
		rpID = a12DefaultRPID
	}
	origin := os.Getenv("VULOS_ORIGIN")
	if origin == "" {
		origin = a12DefaultOrigin
	}
	return &Service{
		vaultDir: vaultDir,
		ks:       ks,
		sessions: make(map[string]*au12Session),
		rpID:     rpID,
		origins:  []string{origin},
	}
}

// BeginRegistration starts a WebAuthn registration ceremony.
//
// Returns:
//   - challenge: JSON-encoded PublicKeyCredentialCreationOptions (send to client).
//   - sessionData: opaque blob the caller must store transiently and pass to
//     FinishRegistration.
func (svc *Service) BeginRegistration(userID, displayName string) (challenge []byte, sessionData []byte, err error) {
	if userID == "" {
		return nil, nil, errors.New("passkeys: empty userID")
	}
	webAuthn, err := svc.waInstance()
	if err != nil {
		return nil, nil, fmt.Errorf("passkeys: init WA: %w", err)
	}

	creds, _ := svc.au12LoadCreds(userID) // existing creds for exclusion
	user := &au12User{
		id:          []byte(userID),
		name:        userID,
		displayName: displayNameOr(displayName, userID),
		credentials: creds,
	}

	creation, session, err := webAuthn.BeginRegistration(user)
	if err != nil {
		return nil, nil, fmt.Errorf("passkeys: begin registration: %w", err)
	}

	challenge, err = json.Marshal(creation)
	if err != nil {
		return nil, nil, fmt.Errorf("passkeys: marshal options: %w", err)
	}

	tok := au12RandToken()
	svc.mu.Lock()
	svc.sessions[tok] = &au12Session{
		Data:      *session,
		UserID:    userID,
		ExpiresAt: time.Now().Add(a12SessionTTL),
	}
	svc.mu.Unlock()

	sessionData, err = json.Marshal(map[string]string{"token": tok})
	if err != nil {
		return nil, nil, err
	}
	return challenge, sessionData, nil
}

// FinishRegistration completes a WebAuthn registration ceremony.
//
// attestationResp is the JSON body received from the client (the
// AuthenticatorAttestationResponse). sessionData is the opaque blob returned
// by BeginRegistration. Returns the base64url credential ID on success.
func (svc *Service) FinishRegistration(userID string, attestationResp []byte, sessionData []byte) (credentialID string, err error) {
	sess, err := svc.au12ConsumeSession(userID, sessionData)
	if err != nil {
		return "", err
	}

	webAuthn, err := svc.waInstance()
	if err != nil {
		return "", fmt.Errorf("passkeys: init WA: %w", err)
	}

	creds, _ := svc.au12LoadCreds(userID)
	user := &au12User{
		id:          []byte(userID),
		name:        userID,
		displayName: userID,
		credentials: creds,
	}

	req, err := http.NewRequest(http.MethodPost, "/", bytes.NewReader(attestationResp))
	if err != nil {
		return "", fmt.Errorf("passkeys: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	cred, err := webAuthn.FinishRegistration(user, sess.Data, req)
	if err != nil {
		return "", fmt.Errorf("passkeys: finish registration: %w", err)
	}

	if err := svc.au12SealAndStore(userID, cred); err != nil {
		return "", fmt.Errorf("passkeys: persist: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(cred.ID), nil
}

// BeginAssertion starts a WebAuthn assertion (authentication) ceremony.
func (svc *Service) BeginAssertion(userID string) (challenge []byte, sessionData []byte, err error) {
	webAuthn, err := svc.waInstance()
	if err != nil {
		return nil, nil, fmt.Errorf("passkeys: init WA: %w", err)
	}

	creds, err := svc.au12LoadCreds(userID)
	if err != nil || len(creds) == 0 {
		return nil, nil, fmt.Errorf("passkeys: no credentials for user %s", userID)
	}

	user := &au12User{
		id:          []byte(userID),
		name:        userID,
		displayName: userID,
		credentials: creds,
	}

	assertion, session, err := webAuthn.BeginLogin(user)
	if err != nil {
		return nil, nil, fmt.Errorf("passkeys: begin assertion: %w", err)
	}

	challenge, err = json.Marshal(assertion)
	if err != nil {
		return nil, nil, fmt.Errorf("passkeys: marshal assertion options: %w", err)
	}

	tok := au12RandToken()
	svc.mu.Lock()
	svc.sessions[tok] = &au12Session{
		Data:      *session,
		UserID:    userID,
		ExpiresAt: time.Now().Add(a12SessionTTL),
	}
	svc.mu.Unlock()

	sessionData, err = json.Marshal(map[string]string{"token": tok})
	if err != nil {
		return nil, nil, err
	}
	return challenge, sessionData, nil
}

// FinishAssertion completes a WebAuthn assertion ceremony.
//
// assertionResp is the JSON body from the client. sessionData is the opaque
// blob from BeginAssertion.
func (svc *Service) FinishAssertion(userID string, assertionResp []byte, sessionData []byte) (verified bool, err error) {
	sess, err := svc.au12ConsumeSession(userID, sessionData)
	if err != nil {
		return false, err
	}

	webAuthn, err := svc.waInstance()
	if err != nil {
		return false, fmt.Errorf("passkeys: init WA: %w", err)
	}

	creds, err := svc.au12LoadCreds(userID)
	if err != nil || len(creds) == 0 {
		return false, fmt.Errorf("passkeys: no credentials for user %s", userID)
	}

	user := &au12User{
		id:          []byte(userID),
		name:        userID,
		displayName: userID,
		credentials: creds,
	}

	req, err := http.NewRequest(http.MethodPost, "/", bytes.NewReader(assertionResp))
	if err != nil {
		return false, fmt.Errorf("passkeys: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	cred, err := webAuthn.FinishLogin(user, sess.Data, req)
	if err != nil {
		return false, fmt.Errorf("passkeys: finish assertion: %w", err)
	}

	svc.au12UpdateLastUsed(userID, base64.RawURLEncoding.EncodeToString(cred.ID))
	return true, nil
}

// List returns public metadata for all passkeys belonging to userID.
func (svc *Service) List(userID string) ([]Credential, error) {
	stored := svc.au12ReadAll(userID)
	out := make([]Credential, 0, len(stored))
	for _, s := range stored {
		out = append(out, Credential{
			ID:        s.ID,
			CreatedAt: s.CreatedAt,
			LastUsed:  s.LastUsed,
		})
	}
	return out, nil
}

// Delete removes a passkey by credentialID (base64url) for userID.
func (svc *Service) Delete(userID, credentialID string) error {
	path, err := svc.au12CredPath(userID, credentialID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("passkeys: credential %s not found for user %s", credentialID, userID)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("passkeys: delete credential: %w", err)
	}
	return nil
}

// ─── Internal helpers ──────────────────────────────────────────────────────

func (svc *Service) waInstance() (*wa.WebAuthn, error) {
	return wa.New(&wa.Config{
		RPDisplayName: "Vula OS",
		RPID:          svc.rpID,
		RPOrigins:     svc.origins,
	})
}

// userDir returns the per-user directory, guarding against path traversal in
// userID.
func (svc *Service) userDir(userID string) (string, error) {
	clean := filepath.Clean(userID)
	if clean == "." || clean == ".." || strings.ContainsAny(clean, "/\\") {
		return "", fmt.Errorf("passkeys: invalid userID %q", userID)
	}
	return filepath.Join(svc.vaultDir, clean), nil
}

// au12CredPath returns the on-disk path for a single credential file,
// guarding against path traversal in credID.
func (svc *Service) au12CredPath(userID, credID string) (string, error) {
	dir, err := svc.userDir(userID)
	if err != nil {
		return "", err
	}
	if credID == "" || strings.ContainsAny(credID, "/\\") || strings.Contains(credID, "..") {
		return "", fmt.Errorf("passkeys: invalid credential id %q", credID)
	}
	return filepath.Join(dir, a12FilePrefix+credID+a12FileSuffix), nil
}

// au12ReadAll reads every stored credential file for userID. Missing/corrupt
// files are skipped; first use simply yields an empty slice.
func (svc *Service) au12ReadAll(userID string) []au12StoredCredential {
	dir, err := svc.userDir(userID)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []au12StoredCredential
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, a12FilePrefix) || !strings.HasSuffix(name, a12FileSuffix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var sc au12StoredCredential
		if err := json.Unmarshal(data, &sc); err != nil {
			continue
		}
		out = append(out, sc)
	}
	return out
}

// au12LoadCreds reads and unseals all wa.Credential values for userID.
func (svc *Service) au12LoadCreds(userID string) ([]wa.Credential, error) {
	stored := svc.au12ReadAll(userID)
	if len(stored) == 0 {
		return nil, nil
	}
	var out []wa.Credential
	for _, sc := range stored {
		plain, err := svc.ks.Unseal(sc.Sealed)
		if err != nil {
			continue // skip unrecoverable record
		}
		var cred wa.Credential
		if err := json.Unmarshal(plain, &cred); err != nil {
			continue
		}
		out = append(out, cred)
	}
	return out, nil
}

// au12SealAndStore seals a wa.Credential with the device KeyStore and writes
// it to its own per-credential file.
func (svc *Service) au12SealAndStore(userID string, cred *wa.Credential) error {
	svc.mu.Lock()
	defer svc.mu.Unlock()

	plain, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	sealed, err := svc.ks.Seal(plain)
	if err != nil {
		return fmt.Errorf("passkeys: seal credential: %w", err)
	}

	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	rec := au12StoredCredential{
		ID:        id,
		Sealed:    sealed,
		CreatedAt: time.Now(),
	}

	path, err := svc.au12CredPath(userID, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return au12AtomicWriteJSON(path, rec)
}

// au12UpdateLastUsed refreshes the LastUsed timestamp for a credential.
func (svc *Service) au12UpdateLastUsed(userID, credID string) {
	svc.mu.Lock()
	defer svc.mu.Unlock()

	path, err := svc.au12CredPath(userID, credID)
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var sc au12StoredCredential
	if err := json.Unmarshal(data, &sc); err != nil {
		return
	}
	sc.LastUsed = time.Now()
	_ = au12AtomicWriteJSON(path, sc)
}

// au12ConsumeSession validates and removes a pending ceremony session.
//
// Security invariant (SECAUDIT2 L-1): the userID binding check is performed
// BEFORE the session is deleted. On a mismatch the session is left intact so
// the legitimate owner's in-flight ceremony is not disrupted by a wrong-user
// (DoS) caller that merely knows the session token.
func (svc *Service) au12ConsumeSession(userID string, sessionData []byte) (*au12Session, error) {
	tok, err := au12ExtractToken(sessionData)
	if err != nil {
		return nil, err
	}

	svc.mu.Lock()
	sess, ok := svc.sessions[tok]
	if !ok || sess == nil {
		svc.mu.Unlock()
		return nil, errors.New("passkeys: session not found or expired")
	}
	// Check expiry and userID binding BEFORE consuming (deleting) the session.
	// An expired or mismatched session must NOT be removed so that accidental or
	// adversarial wrong-user calls cannot DoS the legitimate owner's ceremony.
	if time.Now().After(sess.ExpiresAt) {
		svc.mu.Unlock()
		return nil, errors.New("passkeys: session expired")
	}
	if sess.UserID != userID {
		svc.mu.Unlock()
		return nil, errors.New("passkeys: session userID mismatch")
	}
	// All checks passed — now consume (delete) the session (single-use).
	delete(svc.sessions, tok)
	svc.mu.Unlock()

	return sess, nil
}

// au12AtomicWriteJSON marshals v and writes it via a temp file + rename.
func au12AtomicWriteJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// au12RandToken generates a random opaque session token.
func au12RandToken() string {
	b := make([]byte, 24)
	_, _ = io.ReadFull(rand.Reader, b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// au12ExtractToken parses the opaque sessionData blob and returns the token.
func au12ExtractToken(sessionData []byte) (string, error) {
	var m map[string]string
	if err := json.Unmarshal(sessionData, &m); err != nil {
		return "", fmt.Errorf("passkeys: invalid session data: %w", err)
	}
	tok := m["token"]
	if tok == "" {
		return "", errors.New("passkeys: missing session token")
	}
	return tok, nil
}

func displayNameOr(displayName, fallback string) string {
	if strings.TrimSpace(displayName) == "" {
		return fallback
	}
	return displayName
}

// au12User implements wa.User for the go-webauthn library.
type au12User struct {
	id          []byte
	name        string
	displayName string
	credentials []wa.Credential
}

func (u *au12User) WebAuthnID() []byte                   { return u.id }
func (u *au12User) WebAuthnName() string                 { return u.name }
func (u *au12User) WebAuthnDisplayName() string          { return u.displayName }
func (u *au12User) WebAuthnCredentials() []wa.Credential { return u.credentials }
