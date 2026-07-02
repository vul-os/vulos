package auth

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const testPassword = "correct horse battery staple"

// provision is a test helper: fresh master key + phrase, wrapped envelope.
func provision(t *testing.T, password string) (*MasterKeyEnvelope, []byte, string) {
	t.Helper()
	blob, mnemonic, err := ProvisionMasterKey(password)
	if err != nil {
		t.Fatalf("ProvisionMasterKey: %v", err)
	}
	env, err := ParseMasterKeyEnvelope(blob)
	if err != nil {
		t.Fatalf("ParseMasterKeyEnvelope: %v", err)
	}
	mk, err := UnwrapWithPassword(env, password)
	if err != nil {
		t.Fatalf("UnwrapWithPassword (baseline): %v", err)
	}
	return env, mk, mnemonic
}

func TestProvisionWrapsUnderBothAndUnwrapsEqual(t *testing.T) {
	env, mkFromPassword, mnemonic := provision(t, testPassword)

	if len(mkFromPassword) != MasterKeyLen {
		t.Fatalf("master key wrong length: %d", len(mkFromPassword))
	}
	mkFromPhrase, err := UnwrapWithMnemonic(env, mnemonic)
	if err != nil {
		t.Fatalf("UnwrapWithMnemonic: %v", err)
	}
	if !bytes.Equal(mkFromPassword, mkFromPhrase) {
		t.Fatal("password-unwrap and phrase-unwrap produced different master keys")
	}
	if !constantTimeEqual(mkFromPassword, mkFromPhrase) {
		t.Fatal("constantTimeEqual disagrees with bytes.Equal")
	}
	if len(strings.Fields(mnemonic)) != 24 {
		t.Fatalf("expected 24-word phrase, got %d words", len(strings.Fields(mnemonic)))
	}
}

func TestWrongPasswordFailsClosed(t *testing.T) {
	env, _, _ := provision(t, testPassword)
	if _, err := UnwrapWithPassword(env, "wrong password entirely"); err == nil {
		t.Fatal("expected error unwrapping with wrong password, got nil")
	}
}

func TestWrongPhraseFailsClosed(t *testing.T) {
	env, _, _ := provision(t, testPassword)
	// A different valid BIP39 phrase must not unwrap.
	_, otherPhrase, err := ProvisionMasterKey("another")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnwrapWithMnemonic(env, otherPhrase); err == nil {
		t.Fatal("expected error unwrapping with a different valid phrase, got nil")
	}
	// A non-BIP39 phrase is rejected before any AEAD work.
	if _, err := UnwrapWithMnemonic(env, "not a real mnemonic at all"); err == nil {
		t.Fatal("expected error for invalid mnemonic, got nil")
	}
}

func TestTamperedCiphertextFailsClosed(t *testing.T) {
	env, _, _ := provision(t, testPassword)
	env.Password.CT[0] ^= 0xFF
	if _, err := UnwrapWithPassword(env, testPassword); err == nil {
		t.Fatal("expected error unwrapping tampered ciphertext, got nil")
	}
}

func TestPhraseResetRewrapsSameMasterKey(t *testing.T) {
	env, originalMK, mnemonic := provision(t, testPassword)

	newEnv, err := RewrapPassword(env, mnemonic, "a brand new password 123")
	if err != nil {
		t.Fatalf("RewrapPassword: %v", err)
	}

	// New password unwraps and yields the SAME master key.
	mkNew, err := UnwrapWithPassword(newEnv, "a brand new password 123")
	if err != nil {
		t.Fatalf("UnwrapWithPassword(new): %v", err)
	}
	if !bytes.Equal(mkNew, originalMK) {
		t.Fatal("phrase-reset changed the master key — content would become undecryptable")
	}

	// Old password no longer works against the new envelope.
	if _, err := UnwrapWithPassword(newEnv, testPassword); err == nil {
		t.Fatal("old password should not unwrap the re-wrapped envelope")
	}

	// The phrase still unwraps the same key.
	mkPhrase, err := UnwrapWithMnemonic(newEnv, mnemonic)
	if err != nil {
		t.Fatalf("UnwrapWithMnemonic(after reset): %v", err)
	}
	if !bytes.Equal(mkPhrase, originalMK) {
		t.Fatal("phrase wrap changed after reset")
	}
}

func TestRewrapWithWrongPhraseFailsClosed(t *testing.T) {
	env, _, _ := provision(t, testPassword)
	_, otherPhrase, _ := ProvisionMasterKey("x")
	if _, err := RewrapPassword(env, otherPhrase, "whatever new"); err == nil {
		t.Fatal("expected RewrapPassword to fail with the wrong phrase")
	}
}

// TestRewrapPasswordFromClientSlot is the Tier-2 (active-session) crypto property:
// a client-produced password slot wrapping the SAME master key replaces the
// password slot, leaves the phrase slot verbatim, and the server never touches the
// key.
func TestRewrapPasswordFromClientSlot(t *testing.T) {
	env, originalMK, mnemonic := provision(t, testPassword)

	// Simulate the signed-in CLIENT: it already holds originalMK in memory and
	// re-wraps it under a NEW password entirely client-side.
	clientEnv, err := WrapMasterKey(originalMK, "a brand new password 123", mnemonic)
	if err != nil {
		t.Fatalf("client WrapMasterKey: %v", err)
	}
	slotJSON, err := clientEnv.PasswordEnvelopeJSON()
	if err != nil {
		t.Fatal(err)
	}

	newEnv, err := RewrapPasswordFromClientSlot(env, slotJSON)
	if err != nil {
		t.Fatalf("RewrapPasswordFromClientSlot: %v", err)
	}

	// New password unwraps to the SAME master key.
	mkNew, err := UnwrapWithPassword(newEnv, "a brand new password 123")
	if err != nil {
		t.Fatalf("unwrap new: %v", err)
	}
	if !bytes.Equal(mkNew, originalMK) {
		t.Fatal("client-slot reset changed the master key")
	}
	// Old password no longer works.
	if _, err := UnwrapWithPassword(newEnv, testPassword); err == nil {
		t.Fatal("old password should not unwrap the re-wrapped envelope")
	}
	// Phrase slot preserved verbatim and still unwraps the same key.
	if !bytes.Equal(newEnv.Phrase.CT, env.Phrase.CT) || !bytes.Equal(newEnv.Phrase.IV, env.Phrase.IV) {
		t.Fatal("phrase slot must be preserved unchanged")
	}
	mkPhrase, err := UnwrapWithMnemonic(newEnv, mnemonic)
	if err != nil {
		t.Fatalf("phrase unwrap after reset: %v", err)
	}
	if !bytes.Equal(mkPhrase, originalMK) {
		t.Fatal("phrase wrap changed after client-slot reset")
	}
}

// TestRewrapPasswordFromClientSlotRejectsBadSlots proves the server fails closed on
// structurally invalid or KDF-downgraded client slots (the only checks it can make
// without possessing the key).
func TestRewrapPasswordFromClientSlotRejectsBadSlots(t *testing.T) {
	env, mk, mnemonic := provision(t, testPassword)
	good, _ := WrapMasterKey(mk, "well formed new pw!!", mnemonic)
	goodJSON, _ := good.PasswordEnvelopeJSON()

	// Sanity: a well-formed slot is accepted.
	if _, err := RewrapPasswordFromClientSlot(env, goodJSON); err != nil {
		t.Fatalf("well-formed slot rejected: %v", err)
	}

	mutate := func(k string, v any) []byte {
		var m map[string]any
		if err := json.Unmarshal(goodJSON, &m); err != nil {
			t.Fatal(err)
		}
		m[k] = v
		b, _ := json.Marshal(m)
		return b
	}

	// Downgraded KDF iteration count must be rejected (no silent KDF weakening).
	if _, err := RewrapPasswordFromClientSlot(env, mutate("iter", 1000)); err == nil {
		t.Fatal("expected rejection of downgraded iteration count")
	}
	// Unsupported KDF rejected.
	if _, err := RewrapPasswordFromClientSlot(env, mutate("kdf", "argon2id")); err == nil {
		t.Fatal("expected rejection of unsupported kdf")
	}
	// Wrong-size ciphertext rejected.
	if _, err := RewrapPasswordFromClientSlot(env, mutate("ct", "AAAA")); err == nil {
		t.Fatal("expected rejection of wrong-size ciphertext")
	}
	// Garbage JSON rejected.
	if _, err := RewrapPasswordFromClientSlot(env, []byte("not json")); err == nil {
		t.Fatal("expected rejection of non-JSON slot")
	}
}

// TestEnvelopeNeverContainsPlaintext is the core trust-boundary property: the
// serialised blob must not contain the master key or the phrase in any form.
func TestEnvelopeNeverContainsPlaintext(t *testing.T) {
	env, mk, mnemonic := provision(t, testPassword)
	blob, err := env.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, mk) {
		t.Fatal("serialised envelope contains the raw master key")
	}
	if bytes.Contains(blob, []byte(mnemonic)) {
		t.Fatal("serialised envelope contains the recovery phrase")
	}
	// Sanity: the blob is valid JSON with the expected shape and no stray fields.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(blob, &m); err != nil {
		t.Fatalf("blob not valid JSON: %v", err)
	}
	for _, k := range []string{"v", "pw", "phrase"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("blob missing field %q", k)
		}
	}
}

func TestDeriveContentKeyDeterministicAndScoped(t *testing.T) {
	_, mk, _ := provision(t, testPassword)

	a1, err := DeriveContentKey(mk, "mail", "msg-1")
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := DeriveContentKey(mk, "mail", "msg-1")
	if !bytes.Equal(a1, a2) {
		t.Fatal("content key derivation is not deterministic")
	}
	// Different id → different key.
	b, _ := DeriveContentKey(mk, "mail", "msg-2")
	if bytes.Equal(a1, b) {
		t.Fatal("different id produced the same content key")
	}
	// Different domain → different key.
	c, _ := DeriveContentKey(mk, "files", "msg-1")
	if bytes.Equal(a1, c) {
		t.Fatal("different domain produced the same content key")
	}
	// Domain/id boundary must be injective ("a","bc" != "ab","c").
	x, _ := DeriveContentKey(mk, "a", "bc")
	y, _ := DeriveContentKey(mk, "ab", "c")
	if bytes.Equal(x, y) {
		t.Fatal("content key domain/id separation is ambiguous")
	}
}

func TestPasswordEnvelopeJSONOmitsPhrase(t *testing.T) {
	env, _, _ := provision(t, testPassword)
	b, err := env.PasswordEnvelopeJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("phrase")) {
		t.Fatal("password-only envelope leaked the phrase slot")
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["kdf"] != kdfPassword {
		t.Fatalf("unexpected kdf: %v", m["kdf"])
	}
}
