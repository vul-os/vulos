package files

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

// TestSealedCapabilityServesCiphertextEndToEnd is the vulos half of the wave-3
// content-blind guarantee: a sealed capability serves the pre-sealed VSEAL1 artifact
// (never the live plaintext node), the relaying party sees only ciphertext, and the
// recipient decrypts client-side. Here we exercise the full Go path: client seals →
// IssueSealedCapability stages the artifact → ServeCapability streams ciphertext →
// contentseal confirms the cell would accept it → recipient Open recovers plaintext.
func TestSealedCapabilityServesCiphertextEndToEnd(t *testing.T) {
	owner, recip, _, recipSigner := peerServices(t)
	ctx := context.Background()

	// A node the sharer owns (its plaintext must NEVER be served for a sealed share).
	plaintext := "TOP SECRET — the relaying cell must never read this"
	n := seedFile(t, owner, "userOwner", "secret.txt", plaintext)

	// The recipient's client derives + publishes a content keypair from its master
	// key; the sharer's client seals to that published key (and to the sharer's own).
	recipMaster := bytes.Repeat([]byte{0x22}, 32)
	sharerMaster := bytes.Repeat([]byte{0x33}, 32)
	recipPub, err := DeriveContentPubKeyB64(recipMaster)
	if err != nil {
		t.Fatal(err)
	}
	sharerPub, err := DeriveContentPubKeyB64(sharerMaster)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal([]byte(plaintext), []string{recipPub, sharerPub})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Issue a SEALED capability bound to the recipient.
	cap, _, err := owner.IssueSealedCapability(
		"userOwner", n.ID, RoleViewer, recipSigner.SelfID(), "https://owner.example",
		time.Hour, bytes.NewReader(sealed))
	if err != nil {
		t.Fatalf("IssueSealedCapability: %v", err)
	}
	if !cap.Sealed || cap.IsDir {
		t.Fatalf("capability should be sealed + non-dir: %+v", cap)
	}

	// The recipient (or the cell on its behalf) PULLs via the loopback transport.
	freq := fetchReq(cap, recipSigner)
	rc, _, err := recip.transport.Fetch(ctx, cap.OwnerAddr, freq)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer rc.Close()
	served, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}

	// What was served is the SEALED artifact, byte-for-byte — not the plaintext.
	if !bytes.Equal(served, sealed) {
		t.Fatal("served bytes are not the sealed artifact")
	}
	if bytes.Contains(served, []byte("TOP SECRET")) {
		t.Fatal("served bytes leak plaintext — content-blindness broken")
	}
	if !IsSealed(served) {
		t.Fatal("served bytes are not a VSEAL1 envelope")
	}
	// The relaying cell's content-blind gate would accept it (sealed + targets recip).
	if !SealedTargets(served, recipPub) {
		t.Fatal("served bytes are not addressed to the recipient")
	}

	// The recipient decrypts client-side and recovers the plaintext.
	got, err := Open(served, recipMaster)
	if err != nil {
		t.Fatalf("recipient Open: %v", err)
	}
	if string(got) != plaintext {
		t.Fatalf("decrypted mismatch: %q", string(got))
	}
	// A stranger cannot.
	if _, err := Open(served, bytes.Repeat([]byte{0x44}, 32)); err == nil {
		t.Fatal("a non-recipient must not be able to open the served bytes")
	}
}

// TestIssueSealedRejectsPlaintextArtifact: a sealed capability must never be minted
// over non-sealed bytes (fail closed), so it can never serve plaintext.
func TestIssueSealedRejectsPlaintextArtifact(t *testing.T) {
	owner, _, _, recipSigner := peerServices(t)
	n := seedFile(t, owner, "userOwner", "f.txt", "data")
	_, _, err := owner.IssueSealedCapability(
		"userOwner", n.ID, RoleViewer, recipSigner.SelfID(), "https://owner.example",
		time.Hour, bytes.NewReader([]byte("this is plaintext, not a VSEAL1 envelope")))
	if err == nil {
		t.Fatal("IssueSealedCapability must reject a non-sealed artifact (fail closed)")
	}
}

// TestSealedCapabilityRequiresRecipient: a content-blind share must name who can
// open it.
func TestSealedCapabilityRequiresRecipient(t *testing.T) {
	owner, _, _, _ := peerServices(t)
	n := seedFile(t, owner, "userOwner", "f.txt", "data")
	sealed, _ := Seal([]byte("data"), []string{mustPubB64(t)})
	_, _, err := owner.IssueSealedCapability(
		"userOwner", n.ID, RoleViewer, "", "https://owner.example",
		time.Hour, bytes.NewReader(sealed))
	if err == nil {
		t.Fatal("a sealed share with no recipient must be refused")
	}
}

func mustPubB64(t *testing.T) string {
	t.Helper()
	p, err := DeriveContentPubKeyB64(bytes.Repeat([]byte{0x55}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
