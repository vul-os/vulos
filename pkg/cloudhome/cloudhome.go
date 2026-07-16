// Package cloudhome implements Contract 1 of account-only document sharing:
// a CLOUD-HOME identity per Vulos account.
//
// "The cloud is the account-only user's box." Every account can be given a
// STANDARD self-certifying Ed25519 VulaID — byte-identical in format to a box
// VulaID (`vula:ed25519:<base64(pubkey)>`) — which the cell holds on the
// account's behalf so other Vulos instances can peer-share documents with an
// account that runs no box of its own.
//
// Custody model (high-value):
//   - The Ed25519 PRIVATE KEY is stored ONLY as AES-256-GCM ciphertext under the
//     cloud-home KEK (CLOUDHOME_KEK). There is never plaintext key material at
//     rest. Per-op random nonce. This mirrors internal/integrations/crypto.go.
//   - The cell now custodies MANY accounts' peering private keys; the KEK MUST be
//     set in production (fail closed). A dev all-zeros fallback is permitted ONLY
//     under VULOS_DEV=true and is rejected in production.
//
// Identities are provisioned LAZILY — on the first directory resolve or first
// inbound share for the account — and BOUND to the account (the account_id is
// the primary key, reusing the cloudenroll account-binding seam: management
// identity is the account, the cloud-home key grants peering, not management).
//
// ─────────────────────────────────────────────────────────────────────────────
// CONTENT-BLIND INVARIANT (do not violate):
//
// The cell custodies an account's cloud-home Ed25519 IDENTITY/signing key and can
// therefore VOUCH for the account's identity (federation addressing, fetch-proof
// signing, self-revocation) and REVOKE it. That is the ONLY private key the cell
// holds for a cloud-home account.
//
// The cell holds NO content-decryption key and CANNOT read message content. It
// does not generate, seal, store, or serve X3DH/message-encryption prekeys or any
// other key capable of decrypting user CONTENT. Content forward secrecy is
// established CLIENT-SIDE ONLY (browser↔browser); a compromised or coerced cell
// can impersonate/deny an identity but can never decrypt a message.
//
// As of WAVE 3 the account-only file-sharing path is ALSO content-blind. The cell
// still PULLs a shared FILE on the account's behalf (RedeemCapability → DriveStager)
// and stages it into the account's Drive, but the bytes are now a VSEAL1 sealed
// envelope (internal/contentseal): the file content is encrypted client-side under
// a per-share key wrapped to the recipient's PUBLISHED X25519 content key. The cell
// holds no content private key and contentseal has no Open/decrypt path — it only
// PARSES the public framing to confirm it is transporting ciphertext (and, when a
// ContentKeyLookup is wired, that the ciphertext is addressed to the recipient).
// RedeemCapability FAILS CLOSED (ErrNotContentBlind) on any unsealed bytes, so the
// cell can never again read shared file plaintext. This closes the last cloud-read
// path; there is no remaining path by which the cell touches user CONTENT plaintext.
// ─────────────────────────────────────────────────────────────────────────────
package cloudhome

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/env"
)

// ─────────────────────────────────────────────────────────────────────────────
// VulaID format — MUST match a box VulaID byte-for-byte so vulos peering treats
// a cloud-home VulaID exactly like any other peer (no special-casing).
//
// CANONICAL ENCODING (reconciled with vulos
// backend/services/peering/identity.go): a VulaID is
//
//	vula:ed25519:<base58(raw-32-byte-ed25519-pubkey)>
//
// where base58 is Bitcoin-alphabet base58 with leading-zero-byte → '1'
// handling. This is BYTE-IDENTICAL to what vulos's base58Encode/encodeVulaID
// produce; cross-checked by TestVulaIDMatchesVulosVector. Do NOT switch this to
// base64 — a base64-encoded VulaID is unparseable by every vulos peer and no
// peer would resolve.
// ─────────────────────────────────────────────────────────────────────────────

// VulaIDPrefix is the scheme prefix of every Ed25519 VulaID.
const VulaIDPrefix = "vula:ed25519:"

// base58Alphabet is the Bitcoin base58 alphabet (identical to vulos's).
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// base58Encode encodes b using the Bitcoin base58 alphabet. Replicated verbatim
// from vulos backend/services/peering/identity.go so a cloud-home VulaID is
// byte-identical to a box VulaID for the same public key.
func base58Encode(b []byte) string {
	// Count leading zero bytes.
	leadingZeros := 0
	for _, v := range b {
		if v != 0 {
			break
		}
		leadingZeros++
	}

	n := new(big.Int).SetBytes(b)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := new(big.Int)

	var result []byte
	for n.Cmp(zero) > 0 {
		n.DivMod(n, base, mod)
		result = append(result, base58Alphabet[mod.Int64()])
	}

	// Prepend '1' for each leading zero byte.
	for i := 0; i < leadingZeros; i++ {
		result = append(result, base58Alphabet[0])
	}

	// Reverse.
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return string(result)
}

// base58Decode decodes a base58-encoded string. Replicated verbatim from vulos.
func base58Decode(s string) ([]byte, error) {
	lookup := [256]int{}
	for i := range lookup {
		lookup[i] = -1
	}
	for i, c := range base58Alphabet {
		lookup[c] = i
	}

	leadingZeros := 0
	for _, c := range s {
		if c != '1' {
			break
		}
		leadingZeros++
	}

	n := new(big.Int)
	base := big.NewInt(58)
	for _, c := range s {
		idx := lookup[c]
		if idx < 0 {
			return nil, fmt.Errorf("base58: invalid character %q", c)
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(idx)))
	}

	decoded := n.Bytes()
	result := make([]byte, leadingZeros+len(decoded))
	copy(result[leadingZeros:], decoded)
	return result, nil
}

// FormatVulaID renders an Ed25519 public key as a VulaID string (base58).
func FormatVulaID(pub ed25519.PublicKey) string {
	return VulaIDPrefix + base58Encode(pub)
}

// ParseVulaID extracts the Ed25519 public key from a VulaID string.
func ParseVulaID(vulaID string) (ed25519.PublicKey, error) {
	if !strings.HasPrefix(vulaID, VulaIDPrefix) {
		return nil, fmt.Errorf("cloudhome: not an ed25519 VulaID: %q", vulaID)
	}
	raw, err := base58Decode(strings.TrimPrefix(vulaID, VulaIDPrefix))
	if err != nil {
		return nil, fmt.Errorf("cloudhome: VulaID base58: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("cloudhome: VulaID pubkey is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Domain types & sentinel errors
// ─────────────────────────────────────────────────────────────────────────────

// Identity is the public-facing cloud-home record for an account. It NEVER
// carries private key material — signing happens inside the Service.
type Identity struct {
	AccountID    string    `json:"account_id"`
	VulaID       string    `json:"vula_id"`
	PublicKeyB64 string    `json:"public_key_b64"`
	Server       string    `json:"server"`
	CreatedAt    time.Time `json:"created_at"`
}

// record is the persisted row, including the encrypted private key.
type record struct {
	AccountID    string
	VulaID       string
	PublicKeyB64 string
	EncPrivKey   string // base64(AES-256-GCM nonce||ct||tag) under the KEK of KEKVersion
	KEKVersion   int    // version of the cloud-home KEK enc_priv_key was sealed under
	Server       string
	CreatedAt    time.Time
}

func (r record) identity() Identity {
	return Identity{
		AccountID:    r.AccountID,
		VulaID:       r.VulaID,
		PublicKeyB64: r.PublicKeyB64,
		Server:       r.Server,
		CreatedAt:    r.CreatedAt,
	}
}

var (
	// ErrNotFound is returned when no cloud-home identity exists for the key.
	ErrNotFound = errors.New("cloudhome: identity not found")
	// ErrExists is returned by Insert when the account already has an identity.
	ErrExists = errors.New("cloudhome: identity already exists for account")
	// ErrInvalidInput is returned for malformed arguments.
	ErrInvalidInput = errors.New("cloudhome: invalid input")
)

// ─────────────────────────────────────────────────────────────────────────────
// Store — persistence contract (SQLStore and MemStore satisfy it)
// ─────────────────────────────────────────────────────────────────────────────

// Store persists cloud-home identity records (with the private key already
// encrypted by the Service). Implementations MUST be safe for concurrent use.
type Store interface {
	// Insert persists a new record. Returns ErrExists if the account already has
	// one OR the vula_id collides (the latter is effectively impossible).
	Insert(ctx context.Context, r record) error
	// GetByAccount returns the record for accountID, or ErrNotFound.
	GetByAccount(ctx context.Context, accountID string) (record, error)
	// GetByVulaID returns the record for vulaID, or ErrNotFound.
	GetByVulaID(ctx context.Context, vulaID string) (record, error)
	// ListAll returns every record (used by KEK rotation). Order is unspecified.
	ListAll(ctx context.Context) ([]record, error)
	// UpdateEncKey re-seals one record's encrypted private key under a new KEK
	// version (KEK rotation). It MUST NOT change any other column.
	UpdateEncKey(ctx context.Context, accountID, encPrivKey string, kekVersion int) error

	// NOTE: this store deliberately holds NO content-decryption keys. There are no
	// X3DH/message prekey methods — content forward secrecy is client-side only
	// (see the CONTENT-BLIND INVARIANT in the package doc). The cell custodies the
	// account's IDENTITY key (enc_priv_key) for signing/vouching/revocation only.

	// ── Lifecycle (revocation) ────────────────────────────────────────────────
	// PutRevocation records (or overwrites) the verbatim self-revocation cert for
	// a cloud-home VulaID.
	PutRevocation(ctx context.Context, vulaID, accountID, certJSON string, revokedAt time.Time) error
	// GetRevocation returns the verbatim revocation cert for vulaID, ok=false when
	// the identity is not revoked.
	GetRevocation(ctx context.Context, vulaID string) (certJSON string, ok bool, err error)

	Close() error
}

// ─────────────────────────────────────────────────────────────────────────────
// KEK custody — mirrors internal/integrations/crypto.go (AES-256-GCM, per-op
// nonce). Production MUST set CLOUDHOME_KEK; the all-zeros dev fallback is only
// permitted under VULOS_DEV=true.
// ─────────────────────────────────────────────────────────────────────────────

var devFallbackKEK = make([]byte, 32)

// LoadKEK returns the 32-byte AES-256 key from CLOUDHOME_KEK (base64). It FAILS
// CLOSED in production: when CLOUDHOME_KEK is unset and VULOS_DEV != "true" it
// returns an error so the cloud-home subsystem refuses to custody peering keys
// under a known/default key.
func LoadKEK() ([]byte, error) {
	raw := os.Getenv("CLOUDHOME_KEK")
	if raw == "" {
		// WAVE34: gate the all-zeros dev fallback on env.DevKEKFallbackAllowed so a
		// stray VULOS_DEV on a prod box can never activate a zero KEK.
		if env.DevKEKFallbackAllowed() {
			return devFallbackKEK, nil
		}
		return nil, fmt.Errorf("cloudhome: CLOUDHOME_KEK must be set in production (base64-encoded 32-byte key)")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		// Tolerate URL-safe base64 too (mirrors other key loaders in this repo).
		key, err = base64.RawURLEncoding.DecodeString(raw)
	}
	if err != nil {
		return nil, fmt.Errorf("cloudhome: CLOUDHOME_KEK is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("cloudhome: CLOUDHOME_KEK must decode to exactly 32 bytes, got %d", len(key))
	}
	return key, nil
}

// decodeKEK decodes a base64 (std or url-safe) 32-byte AES-256 key.
func decodeKEK(raw string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		key, err = base64.RawURLEncoding.DecodeString(raw)
	}
	if err != nil {
		return nil, fmt.Errorf("cloudhome: KEK is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("cloudhome: KEK must decode to exactly 32 bytes, got %d", len(key))
	}
	return key, nil
}

// LoadKeyring returns the cloud-home KEK keyring (version → 32-byte key) and the
// CURRENT key version used for new writes. It FAILS CLOSED exactly like LoadKEK:
// the current key must be present (CLOUDHOME_KEK) in production.
//
// Rotation env (no-downtime):
//   - CLOUDHOME_KEK          base64 current key (required in prod).
//   - CLOUDHOME_KEK_VERSION  integer version of the current key (default 1).
//   - CLOUDHOME_KEK_PRIOR    optional; comma-separated "<version>:<base64key>"
//     entries for prior KEKs still needed to decrypt rows not yet re-sealed by
//     RotateKEK. Keep an old key here until every row has migrated to a newer
//     version, then drop it.
func LoadKeyring() (keyring map[int][]byte, currentVersion int, err error) {
	cur, err := LoadKEK()
	if err != nil {
		return nil, 0, err
	}
	currentVersion = 1
	if v := strings.TrimSpace(os.Getenv("CLOUDHOME_KEK_VERSION")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 {
			return nil, 0, fmt.Errorf("cloudhome: CLOUDHOME_KEK_VERSION must be a positive integer, got %q", v)
		}
		currentVersion = n
	}
	keyring = map[int][]byte{currentVersion: cur}
	if prior := strings.TrimSpace(os.Getenv("CLOUDHOME_KEK_PRIOR")); prior != "" {
		for _, ent := range strings.Split(prior, ",") {
			ent = strings.TrimSpace(ent)
			if ent == "" {
				continue
			}
			i := strings.IndexByte(ent, ':')
			if i <= 0 {
				return nil, 0, fmt.Errorf("cloudhome: CLOUDHOME_KEK_PRIOR entry %q must be \"<version>:<base64key>\"", ent)
			}
			ver, perr := strconv.Atoi(strings.TrimSpace(ent[:i]))
			if perr != nil || ver < 1 {
				return nil, 0, fmt.Errorf("cloudhome: CLOUDHOME_KEK_PRIOR bad version in %q", ent)
			}
			k, kerr := decodeKEK(strings.TrimSpace(ent[i+1:]))
			if kerr != nil {
				return nil, 0, fmt.Errorf("cloudhome: CLOUDHOME_KEK_PRIOR version %d: %w", ver, kerr)
			}
			if ver == currentVersion {
				return nil, 0, fmt.Errorf("cloudhome: CLOUDHOME_KEK_PRIOR reuses current version %d", ver)
			}
			keyring[ver] = k
		}
	}
	return keyring, currentVersion, nil
}

// sealKEK seals plaintext with AES-256-GCM under kek, returning
// base64(nonce||ct||tag). Empty plaintext is rejected (a key is never empty).
func sealKEK(plain, kek []byte) (string, error) {
	if len(plain) == 0 {
		return "", fmt.Errorf("cloudhome: refusing to seal empty plaintext")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return "", fmt.Errorf("cloudhome: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("cloudhome: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("cloudhome: rand nonce: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, plain, nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// openKEK reverses sealKEK.
func openKEK(enc string, kek []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, fmt.Errorf("cloudhome: base64 decode: %w", err)
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("cloudhome: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cloudhome: gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return nil, fmt.Errorf("cloudhome: ciphertext too short")
	}
	plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("cloudhome: gcm open: %w", err)
	}
	return plain, nil
}
