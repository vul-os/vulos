package peering

import (
	"encoding/hex"
	"testing"
)

// CHANGED 2026-08-12 with the VulaID → VulosID rename. The peer identifier
// prefix "vulos:ed25519:" became "vulos:ed25519:", and the identifiers are
// concatenated into the HKDF SALT — so the derived key necessarily moved. The
// new value was recomputed INDEPENDENTLY (a standalone HKDF-SHA256 in Python)
// and only then compared against this implementation; both agree. Pasting the
// Go output would have proved nothing except that the code equals itself.
//
// The KDF info string is still "vula-x3dh-content-v2" — deliberately. It is a
// separate protocol constant, not an identifier, and changing more crypto
// material than the rename requires would widen an already-breaking change.
//
// TestX3DH_KDF_ReferenceVector pins the byte-exact output of the content-key KDF
// (X3DHDeriveSharedKey) for fixed inputs. The vulos-relay JS initiator and the
// cloud-home cell MUST reproduce this exact value to interop with the Go
// responder. If this vector changes, the wire format changed — bump the version.
//
// Inputs (all fixed, no randomness):
//
//	DH1 = 0x01 * 32, DH2 = 0x02 * 32, DH3 = 0x03 * 32, DH4 = 0x04 * 32
//	idA = "vulos:ed25519:AAAA", idB = "vulos:ed25519:ZZZZ"
//	IKM  = 0xFF*32 || DH1 || DH2 || DH3 || DH4
//	salt = "vulos:ed25519:AAAA:vulos:ed25519:ZZZZ"   (sorted lo:hi)
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

	idA := "vulos:ed25519:AAAA"
	idB := "vulos:ed25519:ZZZZ"

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
		t.Fatal("KDF is not order-independent over the VulosID pair")
	}

	got := hex.EncodeToString(sk[:])
	// Reference vector — recomputed by JS/cell and asserted equal there.
	const want = "5f31b181fbcd5edc27bd9e636a5b3216b2ff373ca072d7aed069ffbe3cc0f722"
	if got != want {
		t.Fatalf("X3DH KDF reference vector mismatch:\n got  %s\n want %s\n(if intentional, update the vector AND the JS/cell agents)", got, want)
	}

	// Label is the exported authoritative constant.
	if X3DHKDFInfoLabel != "vula-x3dh-content-v2" {
		t.Fatalf("X3DHKDFInfoLabel = %q, want vula-x3dh-content-v2", X3DHKDFInfoLabel)
	}
}
