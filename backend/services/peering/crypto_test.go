// crypto_test.go — tests for PEER-37 E2E encryption (crypto.go).
package peering

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"golang.org/x/crypto/curve25519"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// generateTestIdentity generates a fresh Ed25519 keypair for testing.
func generateTestIdentity(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return priv, pub
}

// ─── DeriveX25519FromEd25519 ──────────────────────────────────────────────────

func TestDeriveX25519FromEd25519_RejectsShortKey(t *testing.T) {
	_, _, err := DeriveX25519FromEd25519(ed25519.PrivateKey(make([]byte, 16)))
	if err == nil {
		t.Fatal("expected error for short private key, got nil")
	}
}

func TestDeriveX25519FromEd25519_PublicKeyMatchesScalarBaseMult(t *testing.T) {
	edPriv, _ := generateTestIdentity(t)

	x25519Priv, x25519Pub, err := DeriveX25519FromEd25519(edPriv)
	if err != nil {
		t.Fatalf("DeriveX25519FromEd25519: %v", err)
	}

	// The public key must equal curve25519.X25519(priv, Basepoint).
	want, err := curve25519.X25519(x25519Priv[:], curve25519.Basepoint)
	if err != nil {
		t.Fatalf("curve25519.X25519(basepoint): %v", err)
	}
	if !bytes.Equal(x25519Pub[:], want) {
		t.Errorf("derived public key mismatch\ngot  %x\nwant %x", x25519Pub[:], want)
	}
}

func TestDeriveX25519FromEd25519_Deterministic(t *testing.T) {
	edPriv, _ := generateTestIdentity(t)

	priv1, pub1, err := DeriveX25519FromEd25519(edPriv)
	if err != nil {
		t.Fatalf("first derivation: %v", err)
	}
	priv2, pub2, err := DeriveX25519FromEd25519(edPriv)
	if err != nil {
		t.Fatalf("second derivation: %v", err)
	}

	if priv1 != priv2 {
		t.Error("private key derivation is not deterministic")
	}
	if pub1 != pub2 {
		t.Error("public key derivation is not deterministic")
	}
}

// ─── Ed25519PubToX25519 ───────────────────────────────────────────────────────

func TestEd25519PubToX25519_RejectsShortKey(t *testing.T) {
	_, err := Ed25519PubToX25519(ed25519.PublicKey(make([]byte, 16)))
	if err == nil {
		t.Fatal("expected error for short public key, got nil")
	}
}

func TestEd25519PubToX25519_ConsistentWithPrivateDerivation(t *testing.T) {
	// If we derive the X25519 keypair from an Ed25519 private key, the public
	// key should match Ed25519PubToX25519 applied to the corresponding Ed25519
	// public key.  This is the core correctness property of the conversion.
	edPriv, edPub := generateTestIdentity(t)

	_, x25519PubFromPriv, err := DeriveX25519FromEd25519(edPriv)
	if err != nil {
		t.Fatalf("DeriveX25519FromEd25519: %v", err)
	}

	x25519PubFromPub, err := Ed25519PubToX25519(edPub)
	if err != nil {
		t.Fatalf("Ed25519PubToX25519: %v", err)
	}

	if x25519PubFromPriv != x25519PubFromPub {
		t.Errorf("X25519 public key derived from Ed25519 private key does not match\n"+
			"conversion from Ed25519 public key\n"+
			"from_priv: %x\n"+
			"from_pub:  %x",
			x25519PubFromPriv[:], x25519PubFromPub[:],
		)
	}
}

// ─── PeerSharedSecret ─────────────────────────────────────────────────────────

func TestPeerSharedSecret_BothSidesDeriveTheSameKey(t *testing.T) {
	// Set up two peers: Alice and Bob.
	aliceEdPriv, aliceEdPub := generateTestIdentity(t)
	bobEdPriv, bobEdPub := generateTestIdentity(t)

	alicePriv, alicePub, err := DeriveX25519FromEd25519(aliceEdPriv)
	if err != nil {
		t.Fatalf("alice DeriveX25519: %v", err)
	}
	bobPriv, bobPub, err := DeriveX25519FromEd25519(bobEdPriv)
	if err != nil {
		t.Fatalf("bob DeriveX25519: %v", err)
	}

	aliceVulosID := encodeVulosID(aliceEdPub)
	bobVulosID := encodeVulosID(bobEdPub)

	// Alice computes the shared secret with Bob's public key.
	aliceKey, err := PeerSharedSecret(alicePriv, bobPub, aliceVulosID, bobVulosID)
	if err != nil {
		t.Fatalf("alice PeerSharedSecret: %v", err)
	}

	// Bob computes the shared secret with Alice's public key.
	bobKey, err := PeerSharedSecret(bobPriv, alicePub, bobVulosID, aliceVulosID)
	if err != nil {
		t.Fatalf("bob PeerSharedSecret: %v", err)
	}

	if aliceKey != bobKey {
		t.Errorf("shared secrets differ\nalice: %x\nbob:   %x", aliceKey[:], bobKey[:])
	}
}

func TestPeerSharedSecret_DifferentPeersProduceDifferentKeys(t *testing.T) {
	aliceEdPriv, aliceEdPub := generateTestIdentity(t)
	bobEdPriv, bobEdPub := generateTestIdentity(t)
	_, carolEdPub := generateTestIdentity(t)

	alicePriv, _, _ := DeriveX25519FromEd25519(aliceEdPriv)
	bobPriv, _, _ := DeriveX25519FromEd25519(bobEdPriv)

	_, aliceBobPub, _ := DeriveX25519FromEd25519(aliceEdPriv)
	_, carolPub, _ := DeriveX25519FromEd25519(bobEdPriv)

	// Alice-Bob key
	alicePubX, _ := Ed25519PubToX25519(aliceEdPub)
	bobPubX, _ := Ed25519PubToX25519(bobEdPub)
	carolPubX, _ := Ed25519PubToX25519(carolEdPub)
	_ = carolPub
	_ = aliceBobPub
	_ = bobPriv

	aliceVulosID := encodeVulosID(aliceEdPub)
	bobVulosID := encodeVulosID(bobEdPub)
	carolVulosID := encodeVulosID(carolEdPub)

	_ = alicePubX
	_ = bobPubX
	_ = carolPubX

	keyAliceBob, err := PeerSharedSecret(alicePriv, bobPubX, aliceVulosID, bobVulosID)
	if err != nil {
		t.Fatalf("alice-bob: %v", err)
	}
	keyAliceCarol, err := PeerSharedSecret(alicePriv, carolPubX, aliceVulosID, carolVulosID)
	if err != nil {
		t.Fatalf("alice-carol: %v", err)
	}

	if keyAliceBob == keyAliceCarol {
		t.Error("expected different keys for different remote peers, but got the same key")
	}
}

// ─── ConversationKey ──────────────────────────────────────────────────────────

func TestConversationKey_BothSidesAgree(t *testing.T) {
	aliceEdPriv, aliceEdPub := generateTestIdentity(t)
	bobEdPriv, bobEdPub := generateTestIdentity(t)

	aliceVulosID := encodeVulosID(aliceEdPub)
	bobVulosID := encodeVulosID(bobEdPub)

	aliceKey, err := ConversationKey(aliceEdPriv, bobEdPub, aliceVulosID, bobVulosID)
	if err != nil {
		t.Fatalf("alice ConversationKey: %v", err)
	}

	bobKey, err := ConversationKey(bobEdPriv, aliceEdPub, bobVulosID, aliceVulosID)
	if err != nil {
		t.Fatalf("bob ConversationKey: %v", err)
	}

	if aliceKey != bobKey {
		t.Errorf("conversation keys differ\nalice: %x\nbob:   %x", aliceKey[:], bobKey[:])
	}
}

func TestConversationKey_DifferentPairsProduceDifferentKeys(t *testing.T) {
	aliceEdPriv, aliceEdPub := generateTestIdentity(t)
	bobEdPriv, bobEdPub := generateTestIdentity(t)
	carolEdPriv, carolEdPub := generateTestIdentity(t)

	aliceVulosID := encodeVulosID(aliceEdPub)
	bobVulosID := encodeVulosID(bobEdPub)
	carolVulosID := encodeVulosID(carolEdPub)

	_ = bobEdPriv
	_ = carolEdPriv

	keyAB, err := ConversationKey(aliceEdPriv, bobEdPub, aliceVulosID, bobVulosID)
	if err != nil {
		t.Fatalf("ConversationKey(alice,bob): %v", err)
	}
	keyAC, err := ConversationKey(aliceEdPriv, carolEdPub, aliceVulosID, carolVulosID)
	if err != nil {
		t.Fatalf("ConversationKey(alice,carol): %v", err)
	}

	if keyAB == keyAC {
		t.Error("expected different keys for different conversations, got the same key")
	}
}

// ─── EncryptForPeer / DecryptFromPeer ─────────────────────────────────────────

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	aliceEdPriv, aliceEdPub := generateTestIdentity(t)
	bobEdPriv, bobEdPub := generateTestIdentity(t)

	aliceVulosID := encodeVulosID(aliceEdPub)
	bobVulosID := encodeVulosID(bobEdPub)

	key, err := ConversationKey(aliceEdPriv, bobEdPub, aliceVulosID, bobVulosID)
	if err != nil {
		t.Fatalf("ConversationKey: %v", err)
	}
	_ = bobEdPriv

	plaintext := []byte("hello, bob — this is a secret message")
	ad := []byte("conv:" + aliceVulosID + ":" + bobVulosID)

	ciphertext, err := EncryptForPeer(key, plaintext, ad)
	if err != nil {
		t.Fatalf("EncryptForPeer: %v", err)
	}

	// Ciphertext must not equal the plaintext.
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext equals plaintext — no encryption occurred")
	}

	// Nonce is prepended — total length = nonce + ciphertext + tag.
	minExpected := xchacha20NonceSz + len(plaintext) + 16
	if len(ciphertext) < minExpected {
		t.Errorf("ciphertext too short: got %d, want >= %d", len(ciphertext), minExpected)
	}

	// Bob decrypts using the same key.
	bobKey, err := ConversationKey(bobEdPriv, aliceEdPub, bobVulosID, aliceVulosID)
	if err != nil {
		t.Fatalf("bob ConversationKey: %v", err)
	}

	recovered, err := DecryptFromPeer(bobKey, ciphertext, ad)
	if err != nil {
		t.Fatalf("DecryptFromPeer: %v", err)
	}
	if !bytes.Equal(recovered, plaintext) {
		t.Errorf("decrypted plaintext mismatch\ngot  %q\nwant %q", recovered, plaintext)
	}
}

func TestEncryptDecrypt_WrongKeyFailsClosed(t *testing.T) {
	aliceEdPriv, aliceEdPub := generateTestIdentity(t)
	_, bobEdPub := generateTestIdentity(t)
	carolEdPriv, carolEdPub := generateTestIdentity(t)

	aliceVulosID := encodeVulosID(aliceEdPub)
	bobVulosID := encodeVulosID(bobEdPub)
	carolVulosID := encodeVulosID(carolEdPub)

	// Alice encrypts to Bob.
	aliceBobKey, err := ConversationKey(aliceEdPriv, bobEdPub, aliceVulosID, bobVulosID)
	if err != nil {
		t.Fatalf("ConversationKey(alice,bob): %v", err)
	}

	plaintext := []byte("top secret")
	ad := []byte("conv:" + aliceVulosID + ":" + bobVulosID)

	ciphertext, err := EncryptForPeer(aliceBobKey, plaintext, ad)
	if err != nil {
		t.Fatalf("EncryptForPeer: %v", err)
	}

	// Carol tries to decrypt with the wrong key (her key with Alice).
	carolAliceKey, err := ConversationKey(carolEdPriv, aliceEdPub, carolVulosID, aliceVulosID)
	if err != nil {
		t.Fatalf("ConversationKey(carol,alice): %v", err)
	}

	result, err := DecryptFromPeer(carolAliceKey, ciphertext, ad)
	if err == nil {
		t.Fatal("expected decryption to fail with wrong key, but it succeeded")
	}
	if result != nil {
		t.Errorf("expected nil plaintext on failure, got %q", result)
	}
}

func TestDecryptFromPeer_WrongAdditionalDataFailsClosed(t *testing.T) {
	aliceEdPriv, aliceEdPub := generateTestIdentity(t)
	bobEdPriv, bobEdPub := generateTestIdentity(t)

	aliceVulosID := encodeVulosID(aliceEdPub)
	bobVulosID := encodeVulosID(bobEdPub)

	key, err := ConversationKey(aliceEdPriv, bobEdPub, aliceVulosID, bobVulosID)
	if err != nil {
		t.Fatalf("ConversationKey: %v", err)
	}
	_ = bobEdPriv

	plaintext := []byte("secret payload")
	correctAD := []byte("conversation-id-correct")
	wrongAD := []byte("conversation-id-wrong")

	ciphertext, err := EncryptForPeer(key, plaintext, correctAD)
	if err != nil {
		t.Fatalf("EncryptForPeer: %v", err)
	}

	result, err := DecryptFromPeer(key, ciphertext, wrongAD)
	if err == nil {
		t.Fatal("expected decryption to fail with wrong additional data, but it succeeded")
	}
	if result != nil {
		t.Errorf("expected nil plaintext on failure, got %q", result)
	}
}

func TestDecryptFromPeer_TruncatedCiphertextFailsClosed(t *testing.T) {
	var key SharedKey
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	// Too short to contain a full nonce + tag.
	short := make([]byte, 10)
	result, err := DecryptFromPeer(key, short, nil)
	if err == nil {
		t.Fatal("expected error for truncated ciphertext, got nil")
	}
	if result != nil {
		t.Errorf("expected nil plaintext, got %q", result)
	}
}

func TestDecryptFromPeer_BitFlipFailsClosed(t *testing.T) {
	aliceEdPriv, aliceEdPub := generateTestIdentity(t)
	_, bobEdPub := generateTestIdentity(t)

	aliceVulosID := encodeVulosID(aliceEdPub)
	bobVulosID := encodeVulosID(bobEdPub)

	key, err := ConversationKey(aliceEdPriv, bobEdPub, aliceVulosID, bobVulosID)
	if err != nil {
		t.Fatalf("ConversationKey: %v", err)
	}

	plaintext := []byte("authentic message")
	ad := []byte("test-ad")

	ciphertext, err := EncryptForPeer(key, plaintext, ad)
	if err != nil {
		t.Fatalf("EncryptForPeer: %v", err)
	}

	// Flip a bit in the ciphertext body (past the nonce).
	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[xchacha20NonceSz] ^= 0x01

	result, err := DecryptFromPeer(key, tampered, ad)
	if err == nil {
		t.Fatal("expected decryption to fail after bit-flip, but it succeeded")
	}
	if result != nil {
		t.Errorf("expected nil plaintext on failure, got %q", result)
	}
}

func TestEncryptDecrypt_EmptyPlaintext(t *testing.T) {
	aliceEdPriv, aliceEdPub := generateTestIdentity(t)
	bobEdPriv, bobEdPub := generateTestIdentity(t)

	aliceVulosID := encodeVulosID(aliceEdPub)
	bobVulosID := encodeVulosID(bobEdPub)

	key, err := ConversationKey(aliceEdPriv, bobEdPub, aliceVulosID, bobVulosID)
	if err != nil {
		t.Fatalf("ConversationKey: %v", err)
	}
	bobKey, err := ConversationKey(bobEdPriv, aliceEdPub, bobVulosID, aliceVulosID)
	if err != nil {
		t.Fatalf("bob ConversationKey: %v", err)
	}

	ad := []byte("empty-msg-ad")
	ciphertext, err := EncryptForPeer(key, []byte{}, ad)
	if err != nil {
		t.Fatalf("EncryptForPeer(empty): %v", err)
	}

	recovered, err := DecryptFromPeer(bobKey, ciphertext, ad)
	if err != nil {
		t.Fatalf("DecryptFromPeer(empty): %v", err)
	}
	if len(recovered) != 0 {
		t.Errorf("expected empty plaintext, got %q", recovered)
	}
}

func TestEncryptDecrypt_NoncesAreUnique(t *testing.T) {
	aliceEdPriv, aliceEdPub := generateTestIdentity(t)
	_, bobEdPub := generateTestIdentity(t)

	aliceVulosID := encodeVulosID(aliceEdPub)
	bobVulosID := encodeVulosID(bobEdPub)

	key, err := ConversationKey(aliceEdPriv, bobEdPub, aliceVulosID, bobVulosID)
	if err != nil {
		t.Fatalf("ConversationKey: %v", err)
	}

	plaintext := []byte("same message")
	ad := []byte("nonce-test")

	ct1, err := EncryptForPeer(key, plaintext, ad)
	if err != nil {
		t.Fatalf("first encrypt: %v", err)
	}
	ct2, err := EncryptForPeer(key, plaintext, ad)
	if err != nil {
		t.Fatalf("second encrypt: %v", err)
	}

	// Nonces are the first 24 bytes; they should (almost certainly) differ.
	if bytes.Equal(ct1[:xchacha20NonceSz], ct2[:xchacha20NonceSz]) {
		t.Error("two encryptions of the same plaintext produced the same nonce — RNG may be broken")
	}

	// Full ciphertexts also differ (nonce is different).
	if bytes.Equal(ct1, ct2) {
		t.Error("two encryptions of the same plaintext produced identical ciphertexts")
	}
}

// ─── Key exchange: end-to-end symmetry ───────────────────────────────────────

func TestKeyExchange_Symmetry(t *testing.T) {
	// Verify that the full key-exchange path (Ed25519 → X25519 → ECDH → HKDF)
	// is symmetric: X25519(alicePriv, bobPub) == X25519(bobPriv, alicePub).
	aliceEdPriv, aliceEdPub := generateTestIdentity(t)
	bobEdPriv, bobEdPub := generateTestIdentity(t)

	alicePriv, alicePub, err := DeriveX25519FromEd25519(aliceEdPriv)
	if err != nil {
		t.Fatalf("alice DeriveX25519: %v", err)
	}
	bobPriv, bobPub, err := DeriveX25519FromEd25519(bobEdPriv)
	if err != nil {
		t.Fatalf("bob DeriveX25519: %v", err)
	}

	aliceVulosID := encodeVulosID(aliceEdPub)
	bobVulosID := encodeVulosID(bobEdPub)

	// Raw ECDH outputs must be equal.
	rawAlice, err := curve25519.X25519(alicePriv[:], bobPub[:])
	if err != nil {
		t.Fatalf("alice X25519: %v", err)
	}
	rawBob, err := curve25519.X25519(bobPriv[:], alicePub[:])
	if err != nil {
		t.Fatalf("bob X25519: %v", err)
	}
	if !bytes.Equal(rawAlice, rawBob) {
		t.Errorf("raw ECDH outputs differ\nalice: %x\nbob:   %x", rawAlice, rawBob)
	}

	// HKDF-derived SharedKeys must also be equal.
	keyA, err := PeerSharedSecret(alicePriv, bobPub, aliceVulosID, bobVulosID)
	if err != nil {
		t.Fatalf("PeerSharedSecret(alice): %v", err)
	}
	keyB, err := PeerSharedSecret(bobPriv, alicePub, bobVulosID, aliceVulosID)
	if err != nil {
		t.Fatalf("PeerSharedSecret(bob): %v", err)
	}
	if keyA != keyB {
		t.Errorf("HKDF-derived keys differ\nalice: %x\nbob:   %x", keyA[:], keyB[:])
	}

	// End-to-end: Alice encrypts, Bob decrypts.
	plaintext := []byte("key exchange symmetry test")
	ad := []byte("test-ad")
	ciphertext, err := EncryptForPeer(keyA, plaintext, ad)
	if err != nil {
		t.Fatalf("EncryptForPeer: %v", err)
	}
	recovered, err := DecryptFromPeer(keyB, ciphertext, ad)
	if err != nil {
		t.Fatalf("DecryptFromPeer: %v", err)
	}
	if !bytes.Equal(recovered, plaintext) {
		t.Errorf("recovered plaintext mismatch\ngot  %q\nwant %q", recovered, plaintext)
	}
}
