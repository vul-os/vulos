package contentseal

import (
	"encoding/base64"
	"testing"
)

// These vectors are produced by the vulos Go reference sealer
//
//	cd vulos/backend && go test ./services/files/ -run TestGoJSFixture -v
//
// and by src/lib/contentSeal.js — the SAME VSEAL1 format both repos must agree on.
const (
	fixRecipPubB64 = "Y5DLT0WlcfYTe6rR9OONEiA+IH1r76m7gTx2TpLcQzw="
	fixBlobB64     = "VlNFQUwxCgAAATZ7InYiOjEsImFlYWQiOiJBRVMtMjU2LUdDTSIsImtkZiI6IkhLREYtU0hBMjU2Iiwia2VtIjoiWDI1NTE5IiwiY25vbmNlIjoieWhUa2NzdTk5OGM1MExTQyIsIndyYXBzIjpbeyJraWQiOiJZNURMVDBXbGNmWVRlNnJSOU9PTkVpQStJSDFyNzZtN2dUeDJUcExjUXp3PSIsImVwayI6ImxuWXkrSzkxNk50WTRkKzBmd1FJNjZPTlN1NUpPRWRUOFJpOGlSZ2N4Z2s9Iiwid25vbmNlIjoiZEwzekVzM2M3aFkxRmlzRiIsIndjdCI6InlmdDh3QjBpeEEvcE02WjMrSXBIeE5IY0V6SVoyWjZNdkwvRG91c0loMEk4aWgyb3JnbWpjV1B5ZnIrOGVVckkifV19MUgVIyf1RlkSkGbO6IrRI2cEu23dInNz2mcROgKgB/k="
)

func fixBlob(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(fixBlobB64)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseCrossRepoFixture(t *testing.T) {
	blob := fixBlob(t)
	if !IsSealed(blob) {
		t.Fatal("fixture not recognised as sealed")
	}
	info, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if info.Version != 1 {
		t.Fatalf("version: got %d", info.Version)
	}
	if len(info.KIDs) != 1 || info.KIDs[0] != fixRecipPubB64 {
		t.Fatalf("KIDs: got %v", info.KIDs)
	}
}

func TestVerifyTargetsFailClosed(t *testing.T) {
	blob := fixBlob(t)
	if !VerifyTargets(blob, fixRecipPubB64) {
		t.Fatal("must verify the intended recipient")
	}
	if VerifyTargets(blob, "c29tZS1vdGhlci1rZXktbm90LWEtcmVjaXBpZW50LXg=") {
		t.Fatal("must NOT verify a non-recipient key")
	}
	if VerifyTargets(blob, "") {
		t.Fatal("empty recipient key must fail closed")
	}
	// Plaintext must never be accepted as sealed.
	if VerifyTargets([]byte("this is plaintext the cell must refuse"), fixRecipPubB64) {
		t.Fatal("plaintext must be rejected (fail closed)")
	}
	if IsSealed([]byte("plaintext")) {
		t.Fatal("plaintext must not look sealed")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	bad := [][]byte{
		nil,
		[]byte("VSEAL1\n"),
		append([]byte(Magic), 0, 0, 0, 0),
		[]byte("plain file bytes"),
	}
	for i, b := range bad {
		if _, err := Parse(b); err == nil {
			t.Fatalf("case %d: expected parse error", i)
		}
	}
}
