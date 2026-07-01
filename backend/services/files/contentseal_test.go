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

// TestSealedMetaRoundTrip (WAVE-7): the file's name/type/is_dir are packed INTO the
// sealed body (VMETA1) and recovered by the recipient on decrypt — the cell that
// only parses the VSEAL1 header sees none of it.
func TestSealedMetaRoundTrip(t *testing.T) {
	recipient := mkMaster(t)
	recipPub, _ := DeriveContentPubKeyB64(recipient)

	meta := &SealedMeta{Name: "Quarterly Results.xlsx", ContentType: "application/vnd.ms-excel", IsDir: false}
	payload := []byte("SECRET spreadsheet bytes")
	packed, err := PackSealedPayload(meta, payload)
	if err != nil {
		t.Fatalf("PackSealedPayload: %v", err)
	}
	blob, err := Seal(packed, []string{recipPub})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// The cell's content-blind view must not contain the filename anywhere.
	if bytes.Contains(blob, []byte("Quarterly Results")) {
		t.Fatal("filename leaked into the sealed envelope bytes")
	}
	info, err := ParseSealed(blob)
	if err != nil || len(info.KIDs) != 1 {
		t.Fatalf("ParseSealed: %v info=%+v", err, info)
	}

	// Recipient opens and recovers metadata + payload.
	pt, err := Open(blob, recipient)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gotMeta, gotPayload, err := UnpackSealedPayload(pt)
	if err != nil {
		t.Fatalf("UnpackSealedPayload: %v", err)
	}
	if gotMeta == nil || gotMeta.Name != meta.Name || gotMeta.ContentType != meta.ContentType || gotMeta.IsDir {
		t.Fatalf("recovered metadata mismatch: %+v", gotMeta)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatal("recovered payload mismatch")
	}
}

// TestSealedFolderTarRoundTrip (WAVE-7 item 1): a sealed FOLDER is a sealed tar with
// is_dir=true; the recipient recovers the tar and the is_dir flag. The cell only
// ever handles the opaque ciphertext.
func TestSealedFolderTarRoundTrip(t *testing.T) {
	recipient := mkMaster(t)
	recipPub, _ := DeriveContentPubKeyB64(recipient)

	tarBytes := []byte("PRETEND-TAR-ARCHIVE-BYTES-OF-A-FOLDER-SUBTREE")
	packed, err := PackSealedPayload(&SealedMeta{Name: "Project", IsDir: true}, tarBytes)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := Seal(packed, []string{recipPub})
	if err != nil {
		t.Fatal(err)
	}
	pt, err := Open(blob, recipient)
	if err != nil {
		t.Fatal(err)
	}
	meta, payload, err := UnpackSealedPayload(pt)
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || !meta.IsDir || meta.Name != "Project" {
		t.Fatalf("folder metadata mismatch: %+v", meta)
	}
	if !bytes.Equal(payload, tarBytes) {
		t.Fatal("tar payload mismatch")
	}
}

// TestUnpackSealedPayloadBackCompat: a legacy plaintext (no VMETA1 magic) is
// returned verbatim as the payload with nil metadata.
func TestUnpackSealedPayloadBackCompat(t *testing.T) {
	raw := []byte("legacy raw file bytes, no metadata container")
	meta, payload, err := UnpackSealedPayload(raw)
	if err != nil {
		t.Fatalf("legacy unpack must not error: %v", err)
	}
	if meta != nil {
		t.Fatal("legacy plaintext must have nil metadata")
	}
	if !bytes.Equal(payload, raw) {
		t.Fatal("legacy payload must pass through unchanged")
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
