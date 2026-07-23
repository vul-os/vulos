// Package contentseal is the CELL's content-BLIND view of the "VSEAL1" sealed-file
// envelope produced by vulos clients (src/lib/contentSeal.js) and vulos
// backend/services/files/contentseal.go.
//
// ─── Why this package deliberately CANNOT decrypt ─────────────────────────────
//
// When a self-hosted user shares a file with an account-only (cloud) recipient, the
// cell PULLs the bytes on the account's behalf and stages them into the recipient's
// Drive (cloudhome.RedeemCapability → DriveStager). For the cell to be content-
// blind those bytes MUST be ciphertext the cell cannot open. This package gives the
// cell exactly two capabilities and no more:
//
//   - Parse: read the VSEAL1 framing + header (public metadata only).
//   - VerifyTargets: confirm the envelope is wrapped to the recipient's published
//     content public key, so the recipient — and NOT the cell — can decrypt it.
//
// There is intentionally NO Open/Unseal function and NO X25519 private-key handling
// here: the cell holds no content private key, so it structurally cannot read the
// content. The wire format is documented authoritatively in vulos
// backend/services/files/contentseal.go; this package parses, never seals or opens.
//
//	magic   "VSEAL1\n" (7 bytes)
//	hdr_len uint32 big-endian
//	header  JSON { v, aead, kdf, kem, cnonce, wraps:[{kid,epk,wnonce,wct}] }
//	body    AES-256-GCM ciphertext (opaque to the cell)
//
// WAVE-7: the file's name/content-type/is_dir are packed INSIDE the AEAD body (a
// "VMETA1" container the sharer's client prepends before sealing), so they are part
// of the opaque ciphertext too — the cell sees no filename/type, only the header's
// crypto params + recipient wraps. The capability carries only a placeholder name.
package contentseal

import (
	"encoding/binary"
	"encoding/json"
	"errors"
)

const (
	// Magic prefixes every VSEAL1 envelope.
	Magic = "VSEAL1\n"

	sealVersion = 1
	sealAEAD    = "AES-256-GCM"
	sealKEM     = "X25519"
	maxSealHdr  = 1 << 20 // 1 MiB header cap (anti-DoS before allocation)
)

// ErrNotSealed is returned when bytes are not a well-formed VSEAL1 envelope. The
// cell maps this to a fail-closed refusal: it will not stage anything it cannot
// confirm is ciphertext.
var ErrNotSealed = errors.New("contentseal: bytes are not a VSEAL1 sealed envelope")

type wrap struct {
	KID    string `json:"kid"`
	EPK    string `json:"epk"`
	WNonce string `json:"wnonce"`
	WCT    string `json:"wct"`
}

type header struct {
	V      int    `json:"v"`
	AEAD   string `json:"aead"`
	KDF    string `json:"kdf"`
	KEM    string `json:"kem"`
	CNonce string `json:"cnonce"`
	Wraps  []wrap `json:"wraps"`
}

// Info is the content-blind view of a sealed envelope: the recipient key-ids it is
// wrapped to. It carries nothing that could decrypt the content.
type Info struct {
	Version int
	KIDs    []string // base64 recipient content public keys
}

// IsSealed reports whether blob begins with the VSEAL1 magic.
func IsSealed(blob []byte) bool {
	return len(blob) >= len(Magic) && string(blob[:len(Magic)]) == Magic
}

// Parse validates the VSEAL1 framing + header WITHOUT any key material and returns
// the content-blind Info. It never decrypts and cannot recover the plaintext.
func Parse(blob []byte) (Info, error) {
	if !IsSealed(blob) {
		return Info{}, ErrNotSealed
	}
	rest := blob[len(Magic):]
	if len(rest) < 4 {
		return Info{}, ErrNotSealed
	}
	hlen := binary.BigEndian.Uint32(rest[:4])
	if hlen == 0 || hlen > maxSealHdr {
		return Info{}, ErrNotSealed
	}
	rest = rest[4:]
	if uint32(len(rest)) < hlen {
		return Info{}, ErrNotSealed
	}
	var h header
	if err := json.Unmarshal(rest[:hlen], &h); err != nil {
		return Info{}, ErrNotSealed
	}
	if h.V != sealVersion || h.AEAD != sealAEAD || h.KEM != sealKEM || len(h.Wraps) == 0 {
		return Info{}, ErrNotSealed
	}
	kids := make([]string, 0, len(h.Wraps))
	for _, w := range h.Wraps {
		if w.KID == "" || w.EPK == "" || w.WCT == "" {
			return Info{}, ErrNotSealed
		}
		kids = append(kids, w.KID)
	}
	return Info{Version: h.V, KIDs: kids}, nil
}

// VerifyTargets reports whether blob is a well-formed VSEAL1 envelope carrying a
// wrap whose key-id STRING equals recipientPubB64 (the recipient's published
// content_pub_key). It is a fail-closed integrity gate: the cell stages a pulled
// document only when this returns true, which keeps plaintext out and confirms the
// envelope claims this recipient.
//
// It does NOT prove the wrap decrypts under that key — it cannot, being content-
// blind, and the recipient pubkey is public, so a malicious sharer can forge a wrap
// with the right key-id but junk contents. Such a blob passes here yet fails the
// recipient's open: an undecryptable-junk (integrity/liveness) outcome, never a
// confidentiality break. Returns false for plaintext, a malformed envelope, an empty
// recipient key, or a seal wrapped only to other recipients.
func VerifyTargets(blob []byte, recipientPubB64 string) bool {
	if recipientPubB64 == "" {
		return false
	}
	info, err := Parse(blob)
	if err != nil {
		return false
	}
	for _, k := range info.KIDs {
		if k == recipientPubB64 {
			return true
		}
	}
	return false
}
