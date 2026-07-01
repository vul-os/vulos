package files

// contentseal.go — WAVE-3 CONTENT-BLIND FILE SHARING: the "VSEAL1" sealed-content
// envelope + its crypto.
//
// ─── Why this exists ──────────────────────────────────────────────────────────
//
// When a self-hosted user shares a file with an account-only (cloud) recipient,
// the Vulos cell PULLs the bytes on the account's behalf and stages them into the
// recipient's Drive (RedeemCapability → DriveStager → /api/files/peer/receive).
// Historically those bytes were PLAINTEXT, so the cell — a machine neither the
// sharer nor the recipient controls — saw the file content. This closes that hole:
// the bytes that travel through the cell are a VSEAL1 envelope the cell CANNOT
// open. The content key is wrapped to the RECIPIENT's published X25519 content key
// (and to the sharer's own, so the sharer keeps access); only a holder of the
// matching master key can unwrap and decrypt.
//
// ─── Crypto choices / browser parity ──────────────────────────────────────────
//
// The sealer and opener that matter in production are CLIENTS (browsers): the
// sharer's browser seals, the recipient's browser opens. So the envelope uses ONLY
// primitives available in the platform WebCrypto SubtleCrypto with zero third-party
// JS — mirroring the deliberate choice in auth/masterkey.go:
//
//   - KEM  : X25519 (curve25519 ECDH)              — WebCrypto "X25519" deriveBits
//   - KDF  : HKDF-SHA256                           — WebCrypto "HKDF"
//   - AEAD : AES-256-GCM, 12-byte nonce            — WebCrypto "AES-GCM"
//
// XChaCha20-Poly1305 (used elsewhere in the tree for box↔box peering) is NOT in
// WebCrypto, so it cannot be the client-parity AEAD; AES-256-GCM is. This Go
// implementation is the byte-for-byte reference for src/lib/contentSeal.js and is
// also used server-side when a box (not a browser) is the recipient.
//
// ─── VSEAL1 wire format (the cross-repo contract) ─────────────────────────────
//
//	 magic   : "VSEAL1\n"                      (7 bytes)
//	 hdr_len : uint32 big-endian               (4 bytes) length of the header JSON
//	 header  : hdr_len bytes of JSON:
//	   {
//	     "v":1, "aead":"AES-256-GCM", "kdf":"HKDF-SHA256", "kem":"X25519",
//	     "cnonce": <b64 12B content nonce>,
//	     "wraps": [ { "kid":<b64 32B recipient pub>, "epk":<b64 32B ephemeral pub>,
//	                  "wnonce":<b64 12B>, "wct":<b64 CEK-ct(32)+tag(16)=48B> }, ... ]
//	   }
//	 body    : AES-256-GCM(plaintext, key=CEK, nonce=cnonce, aad=sealContentAAD)
//
// Per recipient (wrap): ephemeral X25519 keypair (e, E=epk); shared=X25519(e,kid);
// wrapKey = HKDF-SHA256(shared, zeroSalt, info=sealWrapInfo||kid); wct =
// AES-256-GCM(CEK, wrapKey, wnonce, aad=sealWrapAAD).
//
// Fail-closed: every AEAD open authenticates; a wrong key or any tampering fails
// the GCM tag and returns an error with no partial/unauthenticated output.
//
// ─── The cell's guarantee ─────────────────────────────────────────────────────
//
// The cell has NO content private key and NEVER calls Open/Unseal here — it only
// parses the header (ParseSealed) and checks a wrap targets the recipient
// (SealedTargets). It therefore transports ciphertext it cannot read.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// ─── Format constants (part of the wire contract; keep in sync with JS) ───────

const (
	// SealMagic prefixes every VSEAL1 envelope.
	SealMagic = "VSEAL1\n"
	// sealVersion is the envelope schema version.
	sealVersion = 1
	// sealAEAD / sealKDF / sealKEM are the advertised algorithm tags.
	sealAEAD = "AES-256-GCM"
	sealKDF  = "HKDF-SHA256"
	sealKEM  = "X25519"

	// gcmNonceLen is the AES-GCM nonce length in bytes.
	gcmNonceLen = 12
	// x25519KeyLen is the size of an X25519 scalar or public key.
	x25519KeyLen = 32
	// cekLen is the content-encryption-key length (AES-256).
	cekLen = 32
	// maxSealHeader bounds the header JSON so a malformed blob cannot force a huge
	// allocation before we reject it.
	maxSealHeader = 1 << 20 // 1 MiB of header is far more than any real wrap set
)

// Domain-separation strings. These are part of the wire contract and MUST match
// src/lib/contentSeal.js byte for byte.
var (
	sealContentAAD  = []byte("vulos-seal-content-v1")
	sealWrapAAD     = []byte("vulos-seal-wrap-v1")
	sealWrapInfo    = []byte("vulos-seal-wrap-v1")
	contentKeyInfo  = []byte("vulos-content-x25519-v1")
	sealZeroSalt32  = make([]byte, sha256.Size)
	x25519Basepoint = func() []byte { b := make([]byte, 32); b[0] = 9; return b }()
)

// ErrSeal is the coarse, fail-closed error for anything wrong with a sealed
// envelope: malformed framing, bad version, missing wrap, wrong key, or tampering.
var ErrSeal = errors.New("files: content seal invalid or cannot be opened")

// ─── Envelope types ───────────────────────────────────────────────────────────

// sealWrap is one recipient's wrapped copy of the content-encryption key.
type sealWrap struct {
	KID    string `json:"kid"`    // base64(recipient X25519 pubkey), == directory content_pubkey_b64
	EPK    string `json:"epk"`    // base64(ephemeral X25519 pubkey)
	WNonce string `json:"wnonce"` // base64(12-byte AES-GCM nonce)
	WCT    string `json:"wct"`    // base64(AES-256-GCM(CEK) || tag)
}

// sealHeader is the JSON header that precedes the ciphertext body.
type sealHeader struct {
	V      int        `json:"v"`
	AEAD   string     `json:"aead"`
	KDF    string     `json:"kdf"`
	KEM    string     `json:"kem"`
	CNonce string     `json:"cnonce"` // base64(12-byte content AES-GCM nonce)
	Wraps  []sealWrap `json:"wraps"`
}

// SealedInfo is the cell-visible, content-BLIND view of a sealed envelope: the
// recipient key-ids it is wrapped to, and nothing that could decrypt it.
type SealedInfo struct {
	Version int
	KIDs    []string // base64 recipient content pubkeys this blob is wrapped to
}

// ─── Content-key derivation (client + server parity) ──────────────────────────

// DeriveContentX25519 derives a user's X25519 CONTENT keypair from their master
// key: scalar = HKDF-SHA256(masterKey, zeroSalt, "vulos-content-x25519-v1"), then
// pub = X25519(scalar, basepoint). The returned public key, base64-encoded, is
// what the user publishes to the directory (content_pubkey_b64) and what a sharer
// wraps to. Mirrors deriveContentKeyPair() in src/lib/contentSeal.js.
func DeriveContentX25519(masterKey []byte) (priv, pub []byte, err error) {
	if len(masterKey) != 32 {
		return nil, nil, fmt.Errorf("files: master key must be 32 bytes, got %d", len(masterKey))
	}
	scalar := make([]byte, x25519KeyLen)
	hk := hkdf.New(sha256.New, masterKey, sealZeroSalt32, contentKeyInfo)
	if _, err := io.ReadFull(hk, scalar); err != nil {
		return nil, nil, fmt.Errorf("files: derive content scalar: %w", err)
	}
	pubB, err := curve25519.X25519(scalar, curve25519.Basepoint)
	if err != nil {
		return nil, nil, fmt.Errorf("files: derive content pub: %w", err)
	}
	return scalar, pubB, nil
}

// DeriveContentPubKeyB64 returns just the base64(std) published content public key
// for a master key — the value stored in the directory content_pubkey_b64 field.
func DeriveContentPubKeyB64(masterKey []byte) (string, error) {
	_, pub, err := DeriveContentX25519(masterKey)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// ─── Seal ─────────────────────────────────────────────────────────────────────

// Seal encrypts plaintext under a fresh AES-256 content key and wraps that key to
// each recipient X25519 public key in recipientPubsB64 (base64 std). Every
// recipient who should retain access — including the SHARER — must be listed. It
// returns the VSEAL1 envelope bytes. At least one recipient is required (fail
// closed: a share with no openable recipient is refused, never silently plaintext).
func Seal(plaintext []byte, recipientPubsB64 []string) ([]byte, error) {
	if len(recipientPubsB64) == 0 {
		return nil, fmt.Errorf("%w: no recipient content keys (refusing to seal to nobody)", ErrSeal)
	}
	cek := make([]byte, cekLen)
	if _, err := rand.Read(cek); err != nil {
		return nil, err
	}
	defer zeroBytes(cek)

	// Content body.
	cnonce := make([]byte, gcmNonceLen)
	if _, err := rand.Read(cnonce); err != nil {
		return nil, err
	}
	body, err := aesGCMSealSeal(cek, cnonce, plaintext, sealContentAAD)
	if err != nil {
		return nil, err
	}

	// One wrap per recipient.
	wraps := make([]sealWrap, 0, len(recipientPubsB64))
	seen := map[string]bool{}
	for _, kidB64 := range recipientPubsB64 {
		if kidB64 == "" || seen[kidB64] {
			continue
		}
		seen[kidB64] = true
		rpub, derr := base64.StdEncoding.DecodeString(kidB64)
		if derr != nil || len(rpub) != x25519KeyLen {
			return nil, fmt.Errorf("%w: bad recipient content key", ErrSeal)
		}
		w, werr := wrapCEK(cek, rpub, kidB64)
		if werr != nil {
			return nil, werr
		}
		wraps = append(wraps, w)
	}
	if len(wraps) == 0 {
		return nil, fmt.Errorf("%w: no valid recipient content keys", ErrSeal)
	}

	hdr := sealHeader{
		V:      sealVersion,
		AEAD:   sealAEAD,
		KDF:    sealKDF,
		KEM:    sealKEM,
		CNonce: base64.StdEncoding.EncodeToString(cnonce),
		Wraps:  wraps,
	}
	hdrJSON, err := json.Marshal(&hdr)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(SealMagic)+4+len(hdrJSON)+len(body))
	out = append(out, SealMagic...)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(hdrJSON)))
	out = append(out, l[:]...)
	out = append(out, hdrJSON...)
	out = append(out, body...)
	return out, nil
}

// wrapCEK wraps cek to a single recipient X25519 public key rpub.
func wrapCEK(cek, rpub []byte, kidB64 string) (sealWrap, error) {
	eScalar := make([]byte, x25519KeyLen)
	if _, err := rand.Read(eScalar); err != nil {
		return sealWrap{}, err
	}
	defer zeroBytes(eScalar)
	epk, err := curve25519.X25519(eScalar, curve25519.Basepoint)
	if err != nil {
		return sealWrap{}, fmt.Errorf("%w: ephemeral pub", ErrSeal)
	}
	shared, err := curve25519.X25519(eScalar, rpub)
	if err != nil {
		return sealWrap{}, fmt.Errorf("%w: ecdh", ErrSeal)
	}
	defer zeroBytes(shared)
	wrapKey := deriveWrapKey(shared, rpub)
	defer zeroBytes(wrapKey)

	wnonce := make([]byte, gcmNonceLen)
	if _, err := rand.Read(wnonce); err != nil {
		return sealWrap{}, err
	}
	wct, err := aesGCMSealSeal(wrapKey, wnonce, cek, sealWrapAAD)
	if err != nil {
		return sealWrap{}, err
	}
	return sealWrap{
		KID:    kidB64,
		EPK:    base64.StdEncoding.EncodeToString(epk),
		WNonce: base64.StdEncoding.EncodeToString(wnonce),
		WCT:    base64.StdEncoding.EncodeToString(wct),
	}, nil
}

// deriveWrapKey = HKDF-SHA256(sharedSecret, zeroSalt, info="vulos-seal-wrap-v1"||recipientPub).
func deriveWrapKey(shared, recipientPub []byte) []byte {
	info := make([]byte, 0, len(sealWrapInfo)+len(recipientPub))
	info = append(info, sealWrapInfo...)
	info = append(info, recipientPub...)
	hk := hkdf.New(sha256.New, shared, sealZeroSalt32, info)
	key := make([]byte, cekLen)
	_, _ = io.ReadFull(hk, key)
	return key
}

// ─── Parse / inspect (content-BLIND — what the cell uses) ─────────────────────

// ParseSealed reads and validates the VSEAL1 framing and header WITHOUT any key
// material, returning the content-blind SealedInfo. It never decrypts. The cell
// uses this (plus SealedTargets) to confirm it is transporting ciphertext wrapped
// to the intended recipient; it can compute nothing about the plaintext.
func ParseSealed(blob []byte) (SealedInfo, error) {
	hdr, _, err := parseSealed(blob)
	if err != nil {
		return SealedInfo{}, err
	}
	kids := make([]string, 0, len(hdr.Wraps))
	for _, w := range hdr.Wraps {
		kids = append(kids, w.KID)
	}
	return SealedInfo{Version: hdr.V, KIDs: kids}, nil
}

// IsSealed reports whether blob begins with the VSEAL1 magic. Cheap pre-check.
func IsSealed(blob []byte) bool {
	return len(blob) >= len(SealMagic) && string(blob[:len(SealMagic)]) == SealMagic
}

// SealedTargets reports whether blob is a well-formed VSEAL1 envelope that carries
// a wrap for recipientPubB64 (i.e. the recipient can actually open it). This is the
// cell's FAIL-CLOSED gate: it stages a received document only when this returns
// true, guaranteeing what it forwards is ciphertext the intended recipient — and
// not the cell — can decrypt.
func SealedTargets(blob []byte, recipientPubB64 string) bool {
	info, err := ParseSealed(blob)
	if err != nil || recipientPubB64 == "" {
		return false
	}
	for _, k := range info.KIDs {
		if k == recipientPubB64 {
			return true
		}
	}
	return false
}

// parseSealed validates framing + header and returns the header and the ciphertext
// body slice (a subslice of blob).
func parseSealed(blob []byte) (sealHeader, []byte, error) {
	if !IsSealed(blob) {
		return sealHeader{}, nil, fmt.Errorf("%w: bad magic", ErrSeal)
	}
	rest := blob[len(SealMagic):]
	if len(rest) < 4 {
		return sealHeader{}, nil, fmt.Errorf("%w: truncated length", ErrSeal)
	}
	hlen := binary.BigEndian.Uint32(rest[:4])
	if hlen == 0 || hlen > maxSealHeader {
		return sealHeader{}, nil, fmt.Errorf("%w: bad header length", ErrSeal)
	}
	rest = rest[4:]
	if uint32(len(rest)) < hlen {
		return sealHeader{}, nil, fmt.Errorf("%w: truncated header", ErrSeal)
	}
	var hdr sealHeader
	if err := json.Unmarshal(rest[:hlen], &hdr); err != nil {
		return sealHeader{}, nil, fmt.Errorf("%w: bad header json", ErrSeal)
	}
	if hdr.V != sealVersion || hdr.AEAD != sealAEAD || hdr.KEM != sealKEM {
		return sealHeader{}, nil, fmt.Errorf("%w: unsupported version/alg", ErrSeal)
	}
	if len(hdr.Wraps) == 0 {
		return sealHeader{}, nil, fmt.Errorf("%w: no wraps", ErrSeal)
	}
	return hdr, rest[hlen:], nil
}

// ─── Open (recipient side; used when a BOX is the recipient) ───────────────────

// Open decrypts a VSEAL1 envelope with the recipient's master key. It derives the
// recipient's X25519 content key, finds the matching wrap, unwraps the CEK, and
// decrypts the body. Fail-closed: no matching wrap, a wrong key, or any tampering
// returns ErrSeal. Browsers do the same thing in src/lib/contentSeal.js.
func Open(blob, masterKey []byte) ([]byte, error) {
	priv, pub, err := DeriveContentX25519(masterKey)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(priv)
	myKID := base64.StdEncoding.EncodeToString(pub)

	hdr, body, err := parseSealed(blob)
	if err != nil {
		return nil, err
	}
	for _, w := range hdr.Wraps {
		if w.KID != myKID {
			continue
		}
		epk, e1 := base64.StdEncoding.DecodeString(w.EPK)
		wnonce, e2 := base64.StdEncoding.DecodeString(w.WNonce)
		wct, e3 := base64.StdEncoding.DecodeString(w.WCT)
		if e1 != nil || e2 != nil || e3 != nil || len(epk) != x25519KeyLen {
			return nil, ErrSeal
		}
		shared, serr := curve25519.X25519(priv, epk)
		if serr != nil {
			return nil, ErrSeal
		}
		wrapKey := deriveWrapKey(shared, pub)
		zeroBytes(shared)
		cek, oerr := aesGCMOpenSeal(wrapKey, wnonce, wct, sealWrapAAD)
		zeroBytes(wrapKey)
		if oerr != nil {
			return nil, ErrSeal
		}
		defer zeroBytes(cek)
		cnonce, ne := base64.StdEncoding.DecodeString(hdr.CNonce)
		if ne != nil {
			return nil, ErrSeal
		}
		pt, perr := aesGCMOpenSeal(cek, cnonce, body, sealContentAAD)
		if perr != nil {
			return nil, ErrSeal
		}
		return pt, nil
	}
	return nil, fmt.Errorf("%w: no wrap for this recipient", ErrSeal)
}

// ─── AEAD helpers (AES-256-GCM, browser-parity) ───────────────────────────────

func aesGCMSealSeal(key, nonce, plaintext, aad []byte) ([]byte, error) {
	if len(nonce) != gcmNonceLen {
		return nil, fmt.Errorf("%w: bad nonce", ErrSeal)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nil, nonce, plaintext, aad), nil
}

func aesGCMOpenSeal(key, nonce, ct, aad []byte) ([]byte, error) {
	if len(nonce) != gcmNonceLen {
		return nil, ErrSeal
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, ErrSeal
	}
	return pt, nil
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
