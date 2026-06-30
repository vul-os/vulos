package peering

import (
	"encoding/hex"
	"testing"
)

// TestX3DH_KDF_ReferenceVector pins the byte-exact output of the content-key KDF
// (X3DHDeriveSharedKey) for fixed inputs. The vula-relay JS initiator and the
// cloud-home cell MUST reproduce this exact value to interop with the Go
// responder. If this vector changes, the wire format changed — bump the version.
//
// Inputs (all fixed, no randomness):
//
//	DH1 = 0x01 * 32, DH2 = 0x02 * 32, DH3 = 0x03 * 32, DH4 = 0x04 * 32
//	idA = "vula:ed25519:AAAA", idB = "vula:ed25519:ZZZZ"
//	IKM  = 0xFF*32 || DH1 || DH2 || DH3 || DH4
//	salt = "vula:ed25519:AAAA:vula:ed25519:ZZZZ"   (sorted lo:hi)
//	info = "vula-x3dh-content-v2"
//	SK   = HKDF-SHA256(IKM, salt, info)[:32]
func TestX3DH_KDF_ReferenceVector(t *testing.T) {
	dh := func(b byte) []byte {
		out := make([]byte, 32)
		for i := range out {
			out[i] = b
		}
		return out
	}
	concat := append(append(append(append([]byte{}, dh(0x01)...), dh(0x02)...), dh(0x03)...), dh(0x04)...)

	idA := "vula:ed25519:AAAA"
	idB := "vula:ed25519:ZZZZ"

	sk, err := X3DHDeriveSharedKey(concat, idA, idB)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	// Salt sorting must be order-independent: swapping idA/idB yields the same key.
	skSwap, err := X3DHDeriveSharedKey(concat, idB, idA)
	if err != nil {
		t.Fatalf("derive swap: %v", err)
	}
	if sk != skSwap {
		t.Fatal("KDF is not order-independent over the VulaID pair")
	}

	got := hex.EncodeToString(sk[:])
	// Reference vector — recomputed by JS/cell and asserted equal there.
	const want = "836c34d7e4256c1229d95603e68c61526cecde9066f3f9b5981a3f20ad3d8ca3"
	if got != want {
		t.Fatalf("X3DH KDF reference vector mismatch:\n got  %s\n want %s\n(if intentional, update the vector AND the JS/cell agents)", got, want)
	}

	// Label is the exported authoritative constant.
	if X3DHKDFInfoLabel != "vula-x3dh-content-v2" {
		t.Fatalf("X3DHKDFInfoLabel = %q, want vula-x3dh-content-v2", X3DHKDFInfoLabel)
	}
}
