package main

import (
	"crypto/ed25519"
	"testing"

	"vulos/backend/services/peering"
)

// TestPeerShareSigner_RejectsRevoked proves the Files peer-share signer redeems
// capabilities through the revocation-CHECKED verifier: a key whose signature is
// otherwise valid is rejected once it is revoked, closing the hole where a
// compromised-then-revoked box could still redeem Files capabilities.
func TestPeerShareSigner_RejectsRevoked(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	vulosID := peering.EncodeVulosID(pub)

	signer := peerShareSigner{selfID: vulosID, priv: priv}
	msg := []byte("files-capability-proof")
	sig := signer.Sign(msg)

	// Not revoked → accepted.
	peering.SetRevocationChecker(func(string) bool { return false })
	t.Cleanup(func() { peering.SetRevocationChecker(nil) })
	if err := signer.Verify(vulosID, msg, sig); err != nil {
		t.Fatalf("unrevoked capability proof should verify: %v", err)
	}

	// Revoked → rejected even though the Ed25519 signature is still valid.
	peering.SetRevocationChecker(func(v string) bool { return v == vulosID })
	if err := signer.Verify(vulosID, msg, sig); err == nil {
		t.Fatal("revoked signer's capability proof must be rejected")
	}
}
