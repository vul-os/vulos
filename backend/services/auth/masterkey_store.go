package auth

// masterkey_store.go — persistence + signup provisioning for the per-user MASTER
// KEY (WAVE2-RECOVERY). The wrapped envelope lives in the `master_key_blobs`
// table (one row per user_id). The blob is opaque and doubly wrapped (password +
// recovery-phrase); this store NEVER holds the plaintext master key or the phrase.
//
// Degraded mode: when db == nil (in-memory-only store) the methods fall back to
// an in-memory map so the server still works in dev / no-disk environments.

import (
	"fmt"
	"log"
	"sync"

	internalauth "vulos/backend/internal/auth"
)

var masterKeyMu sync.RWMutex
var masterKeyMem = map[string][]byte{} // user_id → envelope blob

// SaveMasterKeyBlob persists the wrapped master-key envelope for userID.
func (s *Store) SaveMasterKeyBlob(userID string, blob []byte) error {
	if userID == "" {
		return fmt.Errorf("auth: SaveMasterKeyBlob: userID must not be empty")
	}
	if len(blob) == 0 {
		return fmt.Errorf("auth: SaveMasterKeyBlob: empty blob")
	}
	cp := make([]byte, len(blob))
	copy(cp, blob)

	if s.db != nil {
		_, err := s.db.Exec(
			`INSERT INTO master_key_blobs(user_id, blob) VALUES(?, ?)
			 ON CONFLICT(user_id) DO UPDATE SET blob=excluded.blob`,
			userID, cp,
		)
		if err != nil {
			log.Printf("[auth] sqlite: save master-key blob for %s: %v", userID, err)
			return fmt.Errorf("auth: SaveMasterKeyBlob: %w", err)
		}
		return nil
	}

	masterKeyMu.Lock()
	defer masterKeyMu.Unlock()
	masterKeyMem[userID] = cp
	return nil
}

// LoadMasterKeyBlob returns the wrapped master-key envelope for userID, or
// (nil, nil) if none has been stored.
func (s *Store) LoadMasterKeyBlob(userID string) ([]byte, error) {
	if userID == "" {
		return nil, nil
	}

	if s.db != nil {
		var blob []byte
		err := s.db.QueryRow(
			`SELECT blob FROM master_key_blobs WHERE user_id=?`, userID,
		).Scan(&blob)
		if err != nil {
			return nil, nil // sql.ErrNoRows → not found
		}
		cp := make([]byte, len(blob))
		copy(cp, blob)
		return cp, nil
	}

	masterKeyMu.RLock()
	defer masterKeyMu.RUnlock()
	b := masterKeyMem[userID]
	if b == nil {
		return nil, nil
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, nil
}

// HasMasterKey reports whether a master-key envelope exists for userID.
func (s *Store) HasMasterKey(userID string) bool {
	blob, _ := s.LoadMasterKeyBlob(userID)
	return len(blob) > 0
}

// ProvisionMasterKey generates a fresh per-user master key and 24-word recovery
// phrase, wraps the key under BOTH the password and the phrase, persists ONLY the
// opaque envelope, and returns the phrase for ONE-TIME display. It is idempotent-
// safe to *skip* (returns the sentinel below) if a master key already exists.
//
// The plaintext master key never leaves internal/auth; the phrase is returned to
// the caller which MUST show it once and never persist it. Neither is ever logged.
func (s *Store) ProvisionMasterKey(userID, password string) (mnemonic string, err error) {
	if userID == "" || password == "" {
		return "", fmt.Errorf("auth: ProvisionMasterKey: userID and password required")
	}
	if s.HasMasterKey(userID) {
		return "", ErrMasterKeyExists
	}
	blob, phrase, err := internalauth.ProvisionMasterKey(password)
	if err != nil {
		return "", fmt.Errorf("auth: ProvisionMasterKey: %w", err)
	}
	if err := s.SaveMasterKeyBlob(userID, blob); err != nil {
		return "", err
	}
	log.Printf("[auth] provisioned master key for user %s (phrase shown once, not stored)", userID)
	return phrase, nil
}

// ResetPasswordWithPhrase performs a phrase-based password reset of the master
// key: it unwraps the envelope with the recovery phrase and re-wraps the SAME
// master key under newPassword, preserving decryptability of all existing
// content. Fails closed if the phrase is wrong or no envelope exists.
//
// Trust boundary: the server briefly reconstructs the master key in memory here
// (it already receives the phrase in the request). This is the recovery path; the
// normal login path unwraps client-side and the server never sees the key.
func (s *Store) ResetPasswordWithPhrase(userID, mnemonic, newPassword string) error {
	blob, err := s.LoadMasterKeyBlob(userID)
	if err != nil || len(blob) == 0 {
		return ErrNoMasterKey
	}
	env, err := internalauth.ParseMasterKeyEnvelope(blob)
	if err != nil {
		return fmt.Errorf("auth: ResetPasswordWithPhrase: %w", err)
	}
	newEnv, err := internalauth.RewrapPassword(env, mnemonic, newPassword)
	if err != nil {
		return fmt.Errorf("auth: ResetPasswordWithPhrase: %w", err)
	}
	newBlob, err := newEnv.Marshal()
	if err != nil {
		return fmt.Errorf("auth: ResetPasswordWithPhrase: %w", err)
	}
	if err := s.SaveMasterKeyBlob(userID, newBlob); err != nil {
		return err
	}
	s.fireSensitiveActionHook(userID, "recovery_used", "", "")
	return nil
}

// RewrapMasterKeyOnPasswordChange keeps the master-key password wrap in sync when
// a user changes their password through the normal (old-password-authenticated)
// flow. No-op (nil) if the user has no master key yet. Fails closed if the old
// password does not unwrap the envelope.
func (s *Store) RewrapMasterKeyOnPasswordChange(userID, oldPassword, newPassword string) error {
	blob, err := s.LoadMasterKeyBlob(userID)
	if err != nil || len(blob) == 0 {
		return nil // legacy account without a master key — nothing to rewrap
	}
	env, err := internalauth.ParseMasterKeyEnvelope(blob)
	if err != nil {
		return fmt.Errorf("auth: RewrapMasterKeyOnPasswordChange: %w", err)
	}
	newEnv, err := internalauth.RewrapPasswordWithPassword(env, oldPassword, newPassword)
	if err != nil {
		return fmt.Errorf("auth: RewrapMasterKeyOnPasswordChange: %w", err)
	}
	newBlob, err := newEnv.Marshal()
	if err != nil {
		return fmt.Errorf("auth: RewrapMasterKeyOnPasswordChange: %w", err)
	}
	if err := s.SaveMasterKeyBlob(userID, newBlob); err != nil {
		return err
	}
	s.fireSensitiveActionHook(userID, "masterkey_reset", "", "")
	return nil
}

// PasswordEnvelope returns the password-only wrap slot (JSON) for userID so the
// browser can unwrap the master key client-side after login. Returns
// (nil, ErrNoMasterKey) if the user has no master key.
func (s *Store) PasswordEnvelope(userID string) ([]byte, error) {
	blob, err := s.LoadMasterKeyBlob(userID)
	if err != nil || len(blob) == 0 {
		return nil, ErrNoMasterKey
	}
	env, err := internalauth.ParseMasterKeyEnvelope(blob)
	if err != nil {
		return nil, err
	}
	return env.PasswordEnvelopeJSON()
}

// RecoverAccountWithPhrase is the full "I forgot my password" flow. Possession of
// the recovery phrase is the authorization: it must unwrap the master key. On
// success it (1) re-wraps the master key under newPassword and (2) resets the
// bcrypt login credential and revokes all sessions. If the phrase is wrong, or the
// account never had a master key, it fails closed and changes nothing.
func (s *Store) RecoverAccountWithPhrase(username, mnemonic, newPassword string) error {
	if len(newPassword) < minPasswordLength {
		return fmt.Errorf("password must be %d+ chars", minPasswordLength)
	}
	if len(newPassword) > bcryptMaxBytes {
		return fmt.Errorf("password must be %d chars or fewer", bcryptMaxBytes)
	}
	u := s.GetUserByUsername(username)
	if u == nil {
		return ErrNoMasterKey // uniform failure — do not reveal whether the user exists
	}

	// 1. Verify the phrase + re-wrap the master key under the new password. This
	//    fails closed on a wrong phrase, so the login reset below never runs
	//    unless the caller genuinely holds the recovery phrase. ResetPasswordWithPhrase
	//    fires the "recovery_used" sensitive-action hook on success, which covers
	//    this call path too — no separate hook call needed here.
	if err := s.ResetPasswordWithPhrase(u.ID, mnemonic, newPassword); err != nil {
		return err
	}

	// 2. Reset the bcrypt login credential and revoke every session.
	s.mu.Lock()
	defer s.mu.Unlock()
	u.PasswordHash = hashPassword(newPassword)
	revoked := make([]string, 0)
	for token, sess := range s.sessions {
		if sess.UserID == u.ID {
			delete(s.sessions, token)
			revoked = append(revoked, token)
		}
	}
	s.persistUser(u)
	for _, token := range revoked {
		s.deleteSession(token)
	}
	log.Printf("[auth] account recovered via recovery phrase for user %s (login password reset)", u.ID)
	return nil
}

// ResetPasswordWithSessionKey performs a Tier-2 (active-session / trusted-device)
// password reset. Authorization is the caller's ACTIVE SESSION (enforced by the
// handler) PLUS the client's possession of the in-memory master key, which the
// client used to produce newPasswordSlotJSON — a fresh password-wrap of the SAME
// master key. This path deliberately does NOT take the OLD password (the user
// forgot it) and the server NEVER sees the master key: it only stores the wrapped
// slot the client produced.
//
// It (1) swaps the password wrap slot of the envelope for the client-produced one,
// leaving the PHRASE slot untouched, (2) resets the bcrypt login credential to
// newPassword, and (3) revokes every OTHER session for the user, keeping the
// caller's current session (keepToken) alive so they stay signed in.
//
// Fail-closed: if the account has no master key it returns ErrNoMasterKey (the
// caller should fall back to the recovery phrase — the active session alone cannot
// authorize a zero-access reset), and if the client slot is structurally invalid
// nothing is changed (the login credential is only touched AFTER the new envelope
// is persisted).
func (s *Store) ResetPasswordWithSessionKey(userID, keepToken string, newPasswordSlotJSON []byte, newPassword string) error {
	if len(newPassword) < minPasswordLength {
		return fmt.Errorf("password must be %d+ chars", minPasswordLength)
	}
	if len(newPassword) > bcryptMaxBytes {
		return fmt.Errorf("password must be %d chars or fewer", bcryptMaxBytes)
	}

	// 1. Swap ONLY the password wrap slot for the client-produced one. This runs
	//    BEFORE the login credential is touched, so a missing master key or a
	//    malformed slot fails closed and changes nothing.
	blob, err := s.LoadMasterKeyBlob(userID)
	if err != nil || len(blob) == 0 {
		return ErrNoMasterKey
	}
	env, err := internalauth.ParseMasterKeyEnvelope(blob)
	if err != nil {
		return fmt.Errorf("auth: ResetPasswordWithSessionKey: %w", err)
	}
	newEnv, err := internalauth.RewrapPasswordFromClientSlot(env, newPasswordSlotJSON)
	if err != nil {
		return fmt.Errorf("auth: ResetPasswordWithSessionKey: %w", err)
	}
	newBlob, err := newEnv.Marshal()
	if err != nil {
		return fmt.Errorf("auth: ResetPasswordWithSessionKey: %w", err)
	}
	if err := s.SaveMasterKeyBlob(userID, newBlob); err != nil {
		return err
	}

	// 2. Reset the bcrypt login credential and revoke every OTHER session (standard
	//    on password change), keeping the caller's active session alive.
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		return fmt.Errorf("user not found")
	}
	u.PasswordHash = hashPassword(newPassword)
	revoked := make([]string, 0)
	for token, sess := range s.sessions {
		if sess.UserID == userID && token != keepToken {
			delete(s.sessions, token)
			revoked = append(revoked, token)
		}
	}
	s.persistUser(u)
	for _, token := range revoked {
		s.deleteSession(token)
	}
	log.Printf("[auth] Tier-2 active-session password reset for user %s (%d other session(s) revoked; server never saw the master key)", userID, len(revoked))
	return nil
}

// Sentinels.
var (
	ErrMasterKeyExists = fmt.Errorf("auth: master key already provisioned")
	ErrNoMasterKey     = fmt.Errorf("auth: no master key for user")
)
