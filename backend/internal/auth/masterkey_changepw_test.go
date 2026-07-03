package auth

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The ordinary change-password flow (RewrapPasswordWithPassword) must re-wrap the
// SAME master key under the new password given the OLD one: content stays
// decryptable, the new password unlocks, the old one no longer does, and the
// phrase slot is preserved verbatim.
func TestRewrapPasswordWithPasswordRoundTrip(t *testing.T) {
	env, mk0, mnemonic := provision(t, testPassword)
	const newPassword = "a whole new set of correct words"

	env2, err := RewrapPasswordWithPassword(env, testPassword, newPassword)
	if err != nil {
		t.Fatalf("RewrapPasswordWithPassword: %v", err)
	}

	// New password unwraps the SAME key.
	mk1, err := UnwrapWithPassword(env2, newPassword)
	if err != nil {
		t.Fatalf("unwrap with new password: %v", err)
	}
	if !bytes.Equal(mk0, mk1) {
		t.Fatal("master key changed across change-password — content would be lost")
	}
	// Old password no longer works (fail-closed on the rotated slot).
	if _, err := UnwrapWithPassword(env2, testPassword); err == nil {
		t.Fatal("old password still unwraps after change — rotation ineffective")
	}
	// Phrase slot preserved verbatim: recovery still yields the same key.
	mkPhrase, err := UnwrapWithMnemonic(env2, mnemonic)
	if err != nil {
		t.Fatalf("phrase unwrap after change-password: %v", err)
	}
	if !bytes.Equal(mk0, mkPhrase) {
		t.Fatal("phrase slot no longer recovers the master key after change-password")
	}
	if !bytes.Equal(env.Phrase.CT, env2.Phrase.CT) {
		t.Fatal("phrase ciphertext was altered by change-password")
	}
}

// A wrong OLD password fails closed and returns no re-wrapped envelope, so a
// caller who does not know the current password cannot rotate it.
func TestRewrapPasswordWithWrongOldPasswordFailsClosed(t *testing.T) {
	env, _, _ := provision(t, testPassword)
	env2, err := RewrapPasswordWithPassword(env, "not the old password", "new-secret-1234")
	if err == nil {
		t.Fatal("expected failure with wrong old password")
	}
	if env2 != nil {
		t.Fatal("no envelope must be returned on a failed change-password")
	}
}

// An empty new password is rejected up-front (both change-password variants).
func TestRewrapRejectsEmptyNewPassword(t *testing.T) {
	env, _, mnemonic := provision(t, testPassword)
	if _, err := RewrapPasswordWithPassword(env, testPassword, ""); err == nil {
		t.Error("RewrapPasswordWithPassword must reject empty new password")
	}
	if _, err := RewrapPassword(env, mnemonic, ""); err == nil {
		t.Error("RewrapPassword must reject empty new password")
	}
}

// ParseMasterKeyEnvelope fails closed on a version it does not understand and on
// non-JSON garbage — the server never operates on an envelope it cannot vouch for.
func TestParseEnvelopeRejectsBadVersionAndGarbage(t *testing.T) {
	env, _, _ := provision(t, testPassword)
	env.Version = 99
	blob, _ := env.Marshal()
	if _, err := ParseMasterKeyEnvelope(blob); err == nil {
		t.Error("unsupported envelope version must be rejected")
	}
	if _, err := ParseMasterKeyEnvelope([]byte("{not json")); err == nil {
		t.Error("garbage envelope must be rejected")
	}
}

// UnwrapWithPassword fails closed when the stored slot declares a KDF the server
// does not implement, or an invalid iteration count — it never guesses.
func TestUnwrapRejectsUnsupportedKDF(t *testing.T) {
	env, _, _ := provision(t, testPassword)

	bad := *env
	bad.Password.KDF = "argon2id-future"
	if _, err := UnwrapWithPassword(&bad, testPassword); err == nil {
		t.Error("unknown password KDF must be rejected")
	}

	badIter := *env
	badIter.Password.Iter = 0
	if _, err := UnwrapWithPassword(&badIter, testPassword); err == nil {
		t.Error("zero iterations must be rejected")
	}

	badPhrase := *env
	badPhrase.Phrase.KDF = "pbkdf2-sha256" // wrong KDF for the phrase slot
	if _, err := UnwrapWithMnemonic(&badPhrase, "x"); err == nil {
		t.Error("wrong phrase KDF must be rejected")
	}

	if _, err := UnwrapWithPassword(nil, testPassword); err == nil {
		t.Error("nil envelope must be rejected")
	}
	if _, err := UnwrapWithMnemonic(nil, "x"); err == nil {
		t.Error("nil envelope must be rejected (mnemonic path)")
	}
}

// A password slot substituted with the phrase slot's AAD must not decrypt: the
// distinct AEAD associated-data tags stop a blob sealed for one slot being
// silently accepted by the other.
func TestSlotAADDomainSeparation(t *testing.T) {
	env, _, _ := provision(t, testPassword)
	// Splice the phrase slot's IV/CT into the password slot but keep the password
	// KDF metadata; the AAD mismatch (pw vs phrase) must fail the tag check.
	spliced := *env
	spliced.Password.IV = env.Phrase.IV
	spliced.Password.CT = env.Phrase.CT
	if _, err := UnwrapWithPassword(&spliced, testPassword); err == nil {
		t.Fatal("phrase-sealed blob must not open under the password slot (AAD separation)")
	}
}

// WrapMasterKey rejects a wrong-length key, an empty password, and an invalid
// mnemonic — the three preconditions of a well-formed envelope.
func TestWrapMasterKeyRejectsBadInputs(t *testing.T) {
	valid := make([]byte, MasterKeyLen)
	_, _, mnemonic := provision(t, testPassword)

	if _, err := WrapMasterKey(make([]byte, 8), testPassword, mnemonic); err == nil {
		t.Error("short master key must be rejected")
	}
	if _, err := WrapMasterKey(valid, "", mnemonic); err == nil {
		t.Error("empty password must be rejected")
	}
	if _, err := WrapMasterKey(valid, testPassword, "not a real bip39 phrase"); err == nil {
		t.Error("invalid mnemonic must be rejected")
	}
}

// DeriveContentKey is injective across domain/id boundaries: length-prefixing
// means ("a","bc") and ("ab","c") derive DIFFERENT keys (no separator collision).
func TestDeriveContentKeyInjectiveBoundary(t *testing.T) {
	mk := make([]byte, MasterKeyLen)
	for i := range mk {
		mk[i] = byte(i)
	}
	k1, err := DeriveContentKey(mk, "a", "bc")
	if err != nil {
		t.Fatal(err)
	}
	k2, err := DeriveContentKey(mk, "ab", "c")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(k1, k2) {
		t.Fatal("domain/id boundary collision — derivation is not injective")
	}
	if _, err := DeriveContentKey(make([]byte, 8), "a", "b"); err == nil {
		t.Error("short master key must be rejected by DeriveContentKey")
	}
}

// sanity: the two change-password variants both preserve the exported phrase JSON
// shape (used by the browser), i.e. the password slot is still a valid slot.
func TestChangePasswordSlotStaysWellFormed(t *testing.T) {
	env, _, _ := provision(t, testPassword)
	env2, err := RewrapPasswordWithPassword(env, testPassword, "brand new password here")
	if err != nil {
		t.Fatal(err)
	}
	j, err := env2.PasswordEnvelopeJSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(j, &m); err != nil {
		t.Fatalf("password envelope JSON not valid: %v", err)
	}
	if m["kdf"] != kdfPassword {
		t.Fatalf("kdf = %v, want %q", m["kdf"], kdfPassword)
	}
}
