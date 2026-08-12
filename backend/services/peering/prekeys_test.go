package peering

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

// pkIdentity returns a fresh identity (Ed25519 priv + base58 VulosID).
func pkIdentity(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return priv, encodeVulosID(pub)
}

// TestX3DH_BothSidesDeriveSameKey is the core correctness check: the sender and
// recipient independently derive the SAME content key from a one-time-prekey
// handshake, and that key actually decrypts a message.
func TestX3DH_BothSidesDeriveSameKey(t *testing.T) {
	bobPriv, bobID := pkIdentity(t)
	alicePriv, aliceID := pkIdentity(t)

	store, err := NewPreKeyStore(t.TempDir(), bobID, bobPriv, 4)
	if err != nil {
		t.Fatalf("NewPreKeyStore: %v", err)
	}
	bundle := store.PublicBundle()

	hdr, skSend, err := X3DHInitiate(alicePriv, aliceID, bundle)
	if err != nil {
		t.Fatalf("X3DHInitiate: %v", err)
	}
	if hdr.OneTimePreKeyID == "" {
		t.Fatal("expected a one-time prekey to be used when the pool is non-empty")
	}

	skRecv, err := X3DHRespond(store, bobPriv, bobID, hdr)
	if err != nil {
		t.Fatalf("X3DHRespond: %v", err)
	}
	if skSend != skRecv {
		t.Fatal("sender and recipient derived different keys")
	}

	// The derived key must actually work for AEAD.
	ad := []byte("conv-1")
	ct, err := EncryptForPeer(skSend, []byte("hello forward secrecy"), ad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	pt, err := DecryptFromPeer(skRecv, ct, ad)
	if err != nil || string(pt) != "hello forward secrecy" {
		t.Fatalf("decrypt: got %q err %v", pt, err)
	}
}

// TestX3DH_OneTimePreKeyConsumed pins the forward-secrecy mechanism: a one-time
// prekey is deleted after first use, so a replay of the same header fails closed
// (the private scalar no longer exists to re-derive the key).
func TestX3DH_OneTimePreKeyConsumed(t *testing.T) {
	bobPriv, bobID := pkIdentity(t)
	alicePriv, aliceID := pkIdentity(t)
	store, _ := NewPreKeyStore(t.TempDir(), bobID, bobPriv, 2)
	bundle := store.PublicBundle()

	before := store.OneTimePreKeyCount()
	hdr, _, err := X3DHInitiate(alicePriv, aliceID, bundle)
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	if _, err := X3DHRespond(store, bobPriv, bobID, hdr); err != nil {
		t.Fatalf("respond: %v", err)
	}
	if after := store.OneTimePreKeyCount(); after != before-1 {
		t.Fatalf("one-time prekey count = %d, want %d (consumed)", after, before-1)
	}
	// Replay the SAME handshake header → must fail closed (key deleted).
	if _, err := X3DHRespond(store, bobPriv, bobID, hdr); err == nil {
		t.Fatal("replayed one-time-prekey handshake must be rejected after consumption")
	}
}

// TestX3DH_SignedPreKeyFallback verifies that when the one-time pool is empty the
// handshake still succeeds using only the signed prekey (reduced-FS fallback) and
// both sides agree.
func TestX3DH_SignedPreKeyFallback(t *testing.T) {
	bobPriv, bobID := pkIdentity(t)
	alicePriv, aliceID := pkIdentity(t)
	store, _ := NewPreKeyStore(t.TempDir(), bobID, bobPriv, 0) // no one-time prekeys
	bundle := store.PublicBundle()
	if len(bundle.OneTimePreKeys) != 0 {
		t.Fatal("expected an empty one-time prekey pool")
	}
	hdr, skSend, err := X3DHInitiate(alicePriv, aliceID, bundle)
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	if hdr.OneTimePreKeyID != "" {
		t.Fatal("no one-time prekey should be referenced on fallback")
	}
	skRecv, err := X3DHRespond(store, bobPriv, bobID, hdr)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if skSend != skRecv {
		t.Fatal("fallback: sender/recipient keys differ")
	}
}

// TestX3DH_ForgedSignedPreKeyRejected ensures a bundle whose signed prekey is not
// signed by the claimed identity is rejected (prevents a MITM substituting a
// prekey they control).
func TestX3DH_ForgedSignedPreKeyRejected(t *testing.T) {
	bobPriv, bobID := pkIdentity(t)
	alicePriv, aliceID := pkIdentity(t)
	store, _ := NewPreKeyStore(t.TempDir(), bobID, bobPriv, 1)
	bundle := store.PublicBundle()
	// Tamper: flip a byte of the signed prekey public key (sig no longer matches).
	bundle.SignedPreKey.Pub[0] ^= 0xff
	if _, _, err := X3DHInitiate(alicePriv, aliceID, bundle); err == nil {
		t.Fatal("forged signed prekey must be rejected")
	}
}

// TestX3DH_DifferentSessionsDifferentKeys pins that two separate initiations to
// the same recipient produce DIFFERENT content keys (per-message ephemerality),
// which is the basis of forward secrecy.
func TestX3DH_DifferentSessionsDifferentKeys(t *testing.T) {
	bobPriv, bobID := pkIdentity(t)
	alicePriv, aliceID := pkIdentity(t)
	store, _ := NewPreKeyStore(t.TempDir(), bobID, bobPriv, 4)

	_, sk1, err := X3DHInitiate(alicePriv, aliceID, store.PublicBundle())
	if err != nil {
		t.Fatalf("initiate 1: %v", err)
	}
	_, sk2, err := X3DHInitiate(alicePriv, aliceID, store.PublicBundle())
	if err != nil {
		t.Fatalf("initiate 2: %v", err)
	}
	if sk1 == sk2 {
		t.Fatal("two initiations produced the same content key; no per-message ephemerality")
	}
}

func TestNegotiateContentVersion(t *testing.T) {
	bobPriv, bobID := pkIdentity(t)
	store, _ := NewPreKeyStore(t.TempDir(), bobID, bobPriv, 1)

	if v, fs := NegotiateContentVersion(store.PublicBundle()); v != ContentCryptoV2X3DH || !fs {
		t.Fatalf("with bundle: got (v%d, fs=%v), want (v2, true)", v, fs)
	}
	if v, fs := NegotiateContentVersion(nil); v != ContentCryptoV1Legacy || fs {
		t.Fatalf("no bundle: got (v%d, fs=%v), want (v1, false)", v, fs)
	}
}

func TestPreKeyStore_PersistAndReplenish(t *testing.T) {
	bobPriv, bobID := pkIdentity(t)
	dir := t.TempDir()
	store, err := NewPreKeyStore(dir, bobID, bobPriv, 3)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	spkID := store.PublicBundle().SignedPreKey.ID

	// Reopen — signed prekey id must persist (so in-flight handshakes still resolve).
	store2, err := NewPreKeyStore(dir, bobID, bobPriv, 3)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if store2.PublicBundle().SignedPreKey.ID != spkID {
		t.Fatal("signed prekey id changed across reopen")
	}
	// Replenish back to a larger target.
	if err := store2.Replenish(10); err != nil {
		t.Fatalf("replenish: %v", err)
	}
	if store2.OneTimePreKeyCount() != 10 {
		t.Fatalf("after replenish count = %d, want 10", store2.OneTimePreKeyCount())
	}
}

func TestNewPreKeyStore_RejectsMismatchedKey(t *testing.T) {
	_, bobID := pkIdentity(t)
	otherPriv, _ := pkIdentity(t)
	if _, err := NewPreKeyStore(t.TempDir(), bobID, otherPriv, 1); err == nil {
		t.Fatal("prekey store must reject an identity key that does not match the VulosID")
	}
}
