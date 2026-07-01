package files

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func mkMaster(t *testing.T) []byte {
	t.Helper()
	mk := make([]byte, 32)
	if _, err := rand.Read(mk); err != nil {
		t.Fatal(err)
	}
	return mk
}

func TestSealOpenRoundTrip(t *testing.T) {
	sharer := mkMaster(t)
	recipient := mkMaster(t)
	sharerPub, err := DeriveContentPubKeyB64(sharer)
	if err != nil {
		t.Fatal(err)
	}
	recipPub, err := DeriveContentPubKeyB64(recipient)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("the cell must never read this content — top secret file bytes")

	blob, err := Seal(plaintext, []string{recipPub, sharerPub})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !IsSealed(blob) {
		t.Fatal("blob not recognised as sealed")
	}

	// Recipient opens.
	got, err := Open(blob, recipient)
	if err != nil {
		t.Fatalf("recipient Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatal("recipient plaintext mismatch")
	}
	// Sharer retains access.
	got2, err := Open(blob, sharer)
	if err != nil {
		t.Fatalf("sharer Open: %v", err)
	}
	if !bytes.Equal(got2, plaintext) {
		t.Fatal("sharer plaintext mismatch")
	}
}

func TestOpenWrongRecipientFailsClosed(t *testing.T) {
	recipient := mkMaster(t)
	stranger := mkMaster(t)
	recipPub, _ := DeriveContentPubKeyB64(recipient)
	blob, err := Seal([]byte("secret"), []string{recipPub})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(blob, stranger); err == nil {
		t.Fatal("stranger must NOT be able to open the seal")
	}
}

func TestSealTamperFailsClosed(t *testing.T) {
	recipient := mkMaster(t)
	recipPub, _ := DeriveContentPubKeyB64(recipient)
	blob, err := Seal([]byte("secret bytes here"), []string{recipPub})
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the ciphertext body (last byte).
	blob[len(blob)-1] ^= 0xff
	if _, err := Open(blob, recipient); err == nil {
		t.Fatal("tampered ciphertext must fail closed")
	}
}

func TestSealNoRecipientRefused(t *testing.T) {
	if _, err := Seal([]byte("x"), nil); err == nil {
		t.Fatal("sealing to nobody must be refused (fail closed)")
	}
	if _, err := Seal([]byte("x"), []string{""}); err == nil {
		t.Fatal("sealing to empty key must be refused")
	}
}

func TestSealedTargetsIsContentBlind(t *testing.T) {
	recipient := mkMaster(t)
	other := mkMaster(t)
	recipPub, _ := DeriveContentPubKeyB64(recipient)
	otherPub, _ := DeriveContentPubKeyB64(other)
	blob, err := Seal([]byte("hello"), []string{recipPub})
	if err != nil {
		t.Fatal(err)
	}
	if !SealedTargets(blob, recipPub) {
		t.Fatal("SealedTargets should confirm the intended recipient")
	}
	if SealedTargets(blob, otherPub) {
		t.Fatal("SealedTargets must be false for a non-recipient")
	}
	if SealedTargets([]byte("not sealed at all"), recipPub) {
		t.Fatal("plaintext must not be reported as sealed-to-recipient")
	}
	// ParseSealed exposes only KIDs, never plaintext.
	info, err := ParseSealed(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.KIDs) != 1 || info.KIDs[0] != recipPub {
		t.Fatalf("unexpected KIDs: %v", info.KIDs)
	}
}

func TestParseSealedRejectsGarbage(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte("VSEAL1\n"),                 // no length
		append([]byte(SealMagic), 0, 0, 0, 0), // zero header length
		[]byte("plain plaintext file"),
	}
	for i, c := range cases {
		if _, err := ParseSealed(c); err == nil {
			t.Fatalf("case %d: expected parse error", i)
		}
	}
}

// TestDeriveContentPubKeyDeterministic locks the derivation: same master key ->
// same published content pubkey (so a re-login republishes the same key).
func TestDeriveContentPubKeyDeterministic(t *testing.T) {
	mk := mkMaster(t)
	a, _ := DeriveContentPubKeyB64(mk)
	b, _ := DeriveContentPubKeyB64(mk)
	if a != b || a == "" {
		t.Fatal("content pubkey derivation must be deterministic")
	}
	raw, err := base64.StdEncoding.DecodeString(a)
	if err != nil || len(raw) != 32 {
		t.Fatalf("content pubkey must be 32 raw bytes, got %d (%v)", len(raw), err)
	}
}

// TestGoJSFixture prints a fixture consumed by the JS parity test. Run with
// `go test -run TestGoJSFixture -v` to regenerate src/lib/__fixtures__ values.
func TestGoJSFixture(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	// Fixed master keys so the fixture is stable.
	recip := bytes.Repeat([]byte{0x11}, 32)
	recipPub, _ := DeriveContentPubKeyB64(recip)
	blob, err := Seal([]byte("go-sealed-for-js"), []string{recipPub})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("RECIP_MASTER_HEX=%x", recip)
	t.Logf("RECIP_PUB_B64=%s", recipPub)
	t.Logf("BLOB_B64=%s", base64.StdEncoding.EncodeToString(blob))
	if !strings.HasPrefix(string(blob), SealMagic) {
		t.Fatal("bad magic")
	}
}
