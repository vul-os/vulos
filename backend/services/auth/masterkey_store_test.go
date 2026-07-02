package auth

import (
	"bytes"
	"testing"

	internalauth "vulos/backend/internal/auth"
)

// TestProvisionMasterKey_WrapsUnderBoth confirms signup provisioning stores an
// envelope that unwraps under BOTH the password and the returned phrase to the
// same master key, and that the store never persists the plaintext.
func TestProvisionMasterKey_WrapsUnderBoth(t *testing.T) {
	s := newTestStore(t)
	u, err := s.Register("alice", "pass1234-word", "Alice")
	if err != nil {
		t.Fatal(err)
	}

	phrase, err := s.ProvisionMasterKey(u.ID, "pass1234-word")
	if err != nil {
		t.Fatalf("ProvisionMasterKey: %v", err)
	}
	if !s.HasMasterKey(u.ID) {
		t.Fatal("HasMasterKey false after provisioning")
	}

	blob, _ := s.LoadMasterKeyBlob(u.ID)
	env, err := internalauth.ParseMasterKeyEnvelope(blob)
	if err != nil {
		t.Fatal(err)
	}
	mkPw, err := internalauth.UnwrapWithPassword(env, "pass1234-word")
	if err != nil {
		t.Fatalf("unwrap password: %v", err)
	}
	mkPhrase, err := internalauth.UnwrapWithMnemonic(env, phrase)
	if err != nil {
		t.Fatalf("unwrap phrase: %v", err)
	}
	if !bytes.Equal(mkPw, mkPhrase) {
		t.Fatal("password and phrase unwrap disagree")
	}

	// Server-never-holds-plaintext: the stored blob must not contain the key/phrase.
	if bytes.Contains(blob, mkPw) {
		t.Fatal("stored blob contains the master key")
	}
	if bytes.Contains(blob, []byte(phrase)) {
		t.Fatal("stored blob contains the recovery phrase")
	}

	// Wrong password fails closed.
	if _, err := internalauth.UnwrapWithPassword(env, "wrong-password-xx"); err == nil {
		t.Fatal("wrong password should not unwrap")
	}

	// Double provisioning is refused.
	if _, err := s.ProvisionMasterKey(u.ID, "pass1234-word"); err != ErrMasterKeyExists {
		t.Fatalf("expected ErrMasterKeyExists, got %v", err)
	}
}

// TestRecoverAccountWithPhrase covers the forgot-password flow: the phrase resets
// the login password and re-wraps the SAME master key.
func TestRecoverAccountWithPhrase(t *testing.T) {
	s := newTestStore(t)
	u, _ := s.Register("bob", "old-password-123", "Bob")
	phrase, err := s.ProvisionMasterKey(u.ID, "old-password-123")
	if err != nil {
		t.Fatal(err)
	}
	origBlob, _ := s.LoadMasterKeyBlob(u.ID)
	origEnv, _ := internalauth.ParseMasterKeyEnvelope(origBlob)
	origMK, _ := internalauth.UnwrapWithMnemonic(origEnv, phrase)

	// Wrong phrase fails closed and changes nothing.
	_, wrongPhrase, _ := internalauth.ProvisionMasterKey("x")
	if err := s.RecoverAccountWithPhrase("bob", wrongPhrase, "new-password-456"); err == nil {
		t.Fatal("recovery with wrong phrase should fail")
	}
	if _, lErr := s.Login("bob", "old-password-123"); lErr != nil {
		t.Fatal("old password should still work after a failed recovery")
	}

	// Correct phrase resets the login password.
	if err := s.RecoverAccountWithPhrase("bob", phrase, "new-password-456"); err != nil {
		t.Fatalf("RecoverAccountWithPhrase: %v", err)
	}
	if _, lErr := s.Login("bob", "new-password-456"); lErr != nil {
		t.Fatalf("new password should log in: %v", lErr)
	}
	if _, lErr := s.Login("bob", "old-password-123"); lErr == nil {
		t.Fatal("old password should no longer work")
	}

	// The master key is preserved: new password unwraps to the same key.
	newBlob, _ := s.LoadMasterKeyBlob(u.ID)
	newEnv, _ := internalauth.ParseMasterKeyEnvelope(newBlob)
	mkNew, err := internalauth.UnwrapWithPassword(newEnv, "new-password-456")
	if err != nil {
		t.Fatalf("unwrap after recovery: %v", err)
	}
	if !bytes.Equal(mkNew, origMK) {
		t.Fatal("recovery changed the master key — content would be lost")
	}
}

// TestRewrapOnPasswordChange confirms a normal password change keeps the
// password wrap usable and the master key unchanged.
func TestRewrapOnPasswordChange(t *testing.T) {
	s := newTestStore(t)
	u, _ := s.Register("carol", "first-password-1", "Carol")
	if _, err := s.ProvisionMasterKey(u.ID, "first-password-1"); err != nil {
		t.Fatal(err)
	}
	b0, _ := s.LoadMasterKeyBlob(u.ID)
	e0, _ := internalauth.ParseMasterKeyEnvelope(b0)
	mk0, _ := internalauth.UnwrapWithPassword(e0, "first-password-1")

	if err := s.RewrapMasterKeyOnPasswordChange(u.ID, "first-password-1", "second-password-2"); err != nil {
		t.Fatalf("rewrap: %v", err)
	}
	b1, _ := s.LoadMasterKeyBlob(u.ID)
	e1, _ := internalauth.ParseMasterKeyEnvelope(b1)
	mk1, err := internalauth.UnwrapWithPassword(e1, "second-password-2")
	if err != nil {
		t.Fatalf("unwrap after rewrap: %v", err)
	}
	if !bytes.Equal(mk0, mk1) {
		t.Fatal("password change altered the master key")
	}
	// Wrong old password fails closed (no silent rewrap).
	if err := s.RewrapMasterKeyOnPasswordChange(u.ID, "not-the-old-pw", "third-pw-333"); err == nil {
		t.Fatal("rewrap with wrong old password should fail")
	}

	// Legacy account (no master key) is a no-op, not an error.
	u2, _ := s.Register("dave", "dave-password-11", "Dave")
	if err := s.RewrapMasterKeyOnPasswordChange(u2.ID, "dave-password-11", "dave-password-22"); err != nil {
		t.Fatalf("no-op rewrap for keyless account should not error: %v", err)
	}
}

// TestResetPasswordWithSessionKey covers the Tier-2 (active-session) reset: a
// signed-in session re-wraps the master key under a new password client-side; the
// key still unwraps under the NEW password AND the phrase, other sessions are
// revoked (current kept), and no plaintext key is stored.
func TestResetPasswordWithSessionKey(t *testing.T) {
	s := newTestStore(t)
	u, _ := s.Register("frank", "old-password-123", "Frank")
	phrase, err := s.ProvisionMasterKey(u.ID, "old-password-123")
	if err != nil {
		t.Fatal(err)
	}

	// Baseline master key (recovered via the phrase, as the client would hold it).
	origBlob, _ := s.LoadMasterKeyBlob(u.ID)
	origEnv, _ := internalauth.ParseMasterKeyEnvelope(origBlob)
	origMK, _ := internalauth.UnwrapWithMnemonic(origEnv, phrase)

	// Simulate the signed-in CLIENT: it holds origMK and re-wraps under a new pw.
	clientEnv, _ := internalauth.WrapMasterKey(origMK, "new-session-pass-1", phrase)
	slotJSON, _ := clientEnv.PasswordEnvelopeJSON()

	// Two live sessions: one kept (current), one to be revoked.
	keep := s.CreateSession(u, "keep-device")
	other := s.CreateSession(u, "other-device")

	if err := s.ResetPasswordWithSessionKey(u.ID, keep.Token, slotJSON, "new-session-pass-1"); err != nil {
		t.Fatalf("ResetPasswordWithSessionKey: %v", err)
	}

	// New password logs in; old does not.
	if _, err := s.Login("frank", "new-session-pass-1"); err != nil {
		t.Fatalf("new password should log in: %v", err)
	}
	if _, err := s.Login("frank", "old-password-123"); err == nil {
		t.Fatal("old password should no longer work")
	}

	// Master key preserved: unwraps to the SAME key under new password AND phrase.
	nb, _ := s.LoadMasterKeyBlob(u.ID)
	ne, _ := internalauth.ParseMasterKeyEnvelope(nb)
	mkPw, err := internalauth.UnwrapWithPassword(ne, "new-session-pass-1")
	if err != nil {
		t.Fatalf("unwrap new pw: %v", err)
	}
	if !bytes.Equal(mkPw, origMK) {
		t.Fatal("master key changed under the new password")
	}
	mkPh, err := internalauth.UnwrapWithMnemonic(ne, phrase)
	if err != nil {
		t.Fatalf("unwrap phrase: %v", err)
	}
	if !bytes.Equal(mkPh, origMK) {
		t.Fatal("phrase no longer unwraps the same key")
	}

	// Current session survives; the other is revoked.
	if _, ok := s.ValidateToken(keep.Token); !ok {
		t.Fatal("current session should survive the reset")
	}
	if _, ok := s.ValidateToken(other.Token); ok {
		t.Fatal("other session should be revoked")
	}

	// Server stores no plaintext key.
	if bytes.Contains(nb, origMK) {
		t.Fatal("stored blob contains the raw master key")
	}
}

// TestResetPasswordWithSessionKeyNoMasterKeyFailsClosed proves a session that holds
// no master key (legacy account) fails closed to ErrNoMasterKey — the caller must
// fall back to the phrase — and changes nothing.
func TestResetPasswordWithSessionKeyNoMasterKeyFailsClosed(t *testing.T) {
	s := newTestStore(t)
	u, _ := s.Register("grace", "grace-password-1", "Grace") // no master key provisioned
	sess := s.CreateSession(u, "d")

	fakeMK := make([]byte, 32)
	_, phrase, _ := internalauth.ProvisionMasterKey("x")
	ce, _ := internalauth.WrapMasterKey(fakeMK, "brand-new-pass-9", phrase)
	slotJSON, _ := ce.PasswordEnvelopeJSON()

	if err := s.ResetPasswordWithSessionKey(u.ID, sess.Token, slotJSON, "brand-new-pass-9"); err != ErrNoMasterKey {
		t.Fatalf("expected ErrNoMasterKey, got %v", err)
	}
	if _, err := s.Login("grace", "grace-password-1"); err != nil {
		t.Fatal("password must be unchanged after a fail-closed reset")
	}
}

// TestResetPasswordWithSessionKeyRejectsBadSlot proves a structurally invalid slot
// is rejected WITHOUT touching the login credential.
func TestResetPasswordWithSessionKeyRejectsBadSlot(t *testing.T) {
	s := newTestStore(t)
	u, _ := s.Register("heidi", "heidi-password-1", "Heidi")
	if _, err := s.ProvisionMasterKey(u.ID, "heidi-password-1"); err != nil {
		t.Fatal(err)
	}
	sess := s.CreateSession(u, "d")

	bad := []byte(`{"v":1,"kdf":"pbkdf2-sha256","iter":10,"salt":"AAAA","iv":"AAAA","ct":"AAAA"}`)
	if err := s.ResetPasswordWithSessionKey(u.ID, sess.Token, bad, "another-new-pass-1"); err == nil {
		t.Fatal("expected rejection of a structurally invalid slot")
	}
	if _, err := s.Login("heidi", "heidi-password-1"); err != nil {
		t.Fatal("password must be unchanged after an invalid-slot reset")
	}
}

// TestPasswordEnvelopeOmitsPhrase confirms the client-facing envelope never
// carries the phrase slot.
func TestPasswordEnvelopeOmitsPhrase(t *testing.T) {
	s := newTestStore(t)
	u, _ := s.Register("erin", "erin-password-99", "Erin")
	if _, err := s.ProvisionMasterKey(u.ID, "erin-password-99"); err != nil {
		t.Fatal(err)
	}
	env, err := s.PasswordEnvelope(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(env, []byte("phrase")) {
		t.Fatal("password envelope leaked the phrase slot")
	}
	if _, err := s.PasswordEnvelope("no-such-user"); err != ErrNoMasterKey {
		t.Fatalf("expected ErrNoMasterKey, got %v", err)
	}
}
