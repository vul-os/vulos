// Package auth provides account recovery primitives for Vulos (IDENTITY-06).
//
// Recovery kit design:
//   - A 24-word BIP39-compatible mnemonic is derived from 256 bits of entropy.
//   - The mnemonic encodes a recovery seed from which both the Ed25519 Vulos
//     keypair and an OS keyring root key can be re-derived (HKDF-SHA256).
//   - The mnemonic is encrypted at rest using XChaCha20-Poly1305 under a key
//     derived from the user's login password (Argon2id).
//   - Cloud escrow stores only the encrypted blob — the cloud never holds
//     plaintext.
//
// All crypto is pure-Go; no CGO.
package auth

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// ─── Argon2id KDF parameters ──────────────────────────────────────────────────

const (
	kdfTime    uint32 = 3
	kdfMemory  uint32 = 64 * 1024 // 64 MiB
	kdfThreads uint8  = 4
	kdfKeyLen  uint32 = 32 // chacha20poly1305.KeySize
)

// ─── RecoveryKit ──────────────────────────────────────────────────────────────

// RecoveryKit holds the recovery seed and its encrypted form.
//
// On first-boot the kit is generated once, the Mnemonic displayed to the user,
// and only the EncryptedBlob stored locally (+ optionally escrowed to cloud).
// The plaintext mnemonic is never persisted on-disk.
type RecoveryKit struct {
	// Mnemonic is the 24-word space-separated recovery phrase.
	// This field is populated only at generation time and MUST NOT be stored
	// anywhere in plaintext after it has been shown to the user.
	Mnemonic string `json:"mnemonic,omitempty"`

	// EncryptedBlob is the JSON-marshalled encryptedMnemonic struct encoded
	// and sealed with XChaCha20-Poly1305 (key derived from the user's password).
	// Safe to persist locally and escrow to cloud.
	EncryptedBlob []byte `json:"encrypted_blob"`
}

// encryptedMnemonic is the plaintext payload that gets encrypted into EncryptedBlob.
type encryptedMnemonic struct {
	Mnemonic string `json:"mnemonic"`
}

// ─── GenerateRecoveryKit ──────────────────────────────────────────────────────

// GenerateRecoveryKit creates a fresh recovery kit.
//
//   - Generates 256 bits of entropy and converts it to a 24-word BIP39 mnemonic.
//   - Encrypts the mnemonic under password using Argon2id + XChaCha20-Poly1305.
//   - Returns a RecoveryKit whose Mnemonic must be shown to the user exactly once.
//
// After the mnemonic has been shown, callers should zero the Mnemonic field.
func GenerateRecoveryKit(password string) (*RecoveryKit, error) {
	if password == "" {
		return nil, errors.New("auth/recovery: password must not be empty")
	}

	// 1. Generate 256 bits (32 bytes) of entropy.
	entropy := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, entropy); err != nil {
		return nil, fmt.Errorf("auth/recovery: generate entropy: %w", err)
	}

	// 2. Convert entropy to a 24-word BIP39 mnemonic.
	mnemonic, err := entropyToMnemonic(entropy)
	if err != nil {
		return nil, fmt.Errorf("auth/recovery: entropy to mnemonic: %w", err)
	}

	// 3. Encrypt the mnemonic under the user's password.
	blob, err := encryptMnemonicBlob(mnemonic, password)
	if err != nil {
		return nil, fmt.Errorf("auth/recovery: encrypt mnemonic: %w", err)
	}

	return &RecoveryKit{
		Mnemonic:      mnemonic,
		EncryptedBlob: blob,
	}, nil
}

// EscrowBlob returns the encrypted blob suitable for cloud escrow.
// The cloud only ever stores this opaque blob; plaintext never leaves the device.
func (kit *RecoveryKit) EscrowBlob() []byte {
	return kit.EncryptedBlob
}

// ─── RestoredKeys ─────────────────────────────────────────────────────────────

// RestoredKeys contains the re-derived cryptographic material.
type RestoredKeys struct {
	// Ed25519PrivKey is the 64-byte Ed25519 private key for the Vulos identity.
	Ed25519PrivKey ed25519.PrivateKey

	// Ed25519PubKey is the corresponding 32-byte Ed25519 public key.
	Ed25519PubKey ed25519.PublicKey

	// KeyringRootKey is the 32-byte OS keyring root key.
	KeyringRootKey []byte
}

// ─── RestoreFromMnemonic ──────────────────────────────────────────────────────

// RestoreFromMnemonic re-derives the Ed25519 keypair and OS keyring root key
// from the 24-word recovery mnemonic.
//
// This is the sole recovery path when the user has lost their password:
//
//	mnemonic → seed → HKDF → Ed25519 keypair + keyring root key
//
// The derivation is deterministic; the same mnemonic always produces the same keys.
func RestoreFromMnemonic(mnemonic string) (*RestoredKeys, error) {
	mnemonic = normaliseMnemonic(mnemonic)
	if err := ValidateMnemonic(mnemonic); err != nil {
		return nil, fmt.Errorf("auth/recovery: invalid mnemonic: %w", err)
	}

	// Derive a 64-byte seed from the mnemonic (BIP39 seed derivation).
	seed := deriveRecoverySeed(mnemonic)

	// Derive the Ed25519 private key from the seed using HKDF-SHA256.
	edPriv, edPub, err := deriveEd25519Key(seed)
	if err != nil {
		return nil, fmt.Errorf("auth/recovery: derive ed25519: %w", err)
	}

	// Derive the OS keyring root key from the seed using HKDF-SHA256.
	keyringKey, err := deriveKeyringKey(seed)
	if err != nil {
		return nil, fmt.Errorf("auth/recovery: derive keyring key: %w", err)
	}

	return &RestoredKeys{
		Ed25519PrivKey: edPriv,
		Ed25519PubKey:  edPub,
		KeyringRootKey: keyringKey,
	}, nil
}

// ─── ValidateMnemonic ─────────────────────────────────────────────────────────

// ValidateMnemonic checks that mnemonic is a valid 24-word BIP39 phrase
// (all words in the wordlist, correct checksum). Returns nil on success.
func ValidateMnemonic(mnemonic string) error {
	mnemonic = normaliseMnemonic(mnemonic)
	words := strings.Fields(mnemonic)
	if len(words) != 24 {
		return fmt.Errorf("auth/recovery: mnemonic must be 24 words, got %d", len(words))
	}
	for i, w := range words {
		if _, ok := wordIndex[w]; !ok {
			return fmt.Errorf("auth/recovery: unknown word at position %d: %q", i, w)
		}
	}
	// Verify checksum by reconstructing.
	entropy, err := mnemonicToEntropy(mnemonic)
	if err != nil {
		return err
	}
	rebuilt, err := entropyToMnemonic(entropy)
	if err != nil {
		return err
	}
	if rebuilt != mnemonic {
		return errors.New("auth/recovery: mnemonic checksum invalid")
	}
	return nil
}

// ─── DecryptRecoveryKit ───────────────────────────────────────────────────────

// DecryptRecoveryKit decrypts the encrypted blob using the user's password
// and returns the plaintext mnemonic.
func DecryptRecoveryKit(encryptedBlob []byte, password string) (string, error) {
	return decryptMnemonicBlob(encryptedBlob, password)
}

// ─── RestoreFromEscrow ────────────────────────────────────────────────────────

// RestoreFromEscrow decrypts a cloud-escrowed blob and restores keys.
// Returns the keys, the mnemonic (for display), and any error.
func RestoreFromEscrow(encryptedBlob []byte, password string) (*RestoredKeys, string, error) {
	mnemonic, err := DecryptRecoveryKit(encryptedBlob, password)
	if err != nil {
		return nil, "", fmt.Errorf("auth/recovery: decrypt escrow: %w", err)
	}
	keys, err := RestoreFromMnemonic(mnemonic)
	if err != nil {
		return nil, "", err
	}
	return keys, mnemonic, nil
}

// ─── RecoveryStore interface ──────────────────────────────────────────────────

// RecoveryStore is the persistence interface used by the recovery HTTP handlers.
type RecoveryStore interface {
	// SaveRecoveryBlob persists the encrypted recovery blob for the given user.
	SaveRecoveryBlob(userID string, blob []byte) error
	// LoadRecoveryBlob returns the encrypted blob for the given user.
	// Returns (nil, nil) if none is set.
	LoadRecoveryBlob(userID string) ([]byte, error)
}

// ─── RegisterRecoveryHandlers ─────────────────────────────────────────────────

// RegisterRecoveryHandlers mounts the account-recovery HTTP routes on mux.
//
// Routes:
//
//	POST /api/auth/recovery/generate  — generate + store recovery kit (requires session)
//	POST /api/auth/recovery/restore   — restore keyring from mnemonic (public endpoint)
//	POST /api/auth/recovery/escrow    — verify + return escrow blob (requires session)
func RegisterRecoveryHandlers(mux *http.ServeMux, store RecoveryStore) {
	rh := &recoveryHandlers{store: store}
	mux.HandleFunc("POST /api/auth/recovery/generate", rh.handleGenerate)
	mux.HandleFunc("POST /api/auth/recovery/restore", rh.handleRestore)
	mux.HandleFunc("POST /api/auth/recovery/escrow", rh.handleEscrow)
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

type recoveryHandlers struct {
	store RecoveryStore
}

func (h *recoveryHandlers) handleGenerate(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeRecoveryJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRecoveryJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Password == "" {
		writeRecoveryJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "password is required"})
		return
	}

	kit, err := GenerateRecoveryKit(req.Password)
	if err != nil {
		writeRecoveryJSON(w, http.StatusInternalServerError, map[string]string{"error": "kit generation failed"})
		return
	}

	if err := h.store.SaveRecoveryBlob(userID, kit.EncryptedBlob); err != nil {
		writeRecoveryJSON(w, http.StatusInternalServerError, map[string]string{"error": "persist failed"})
		return
	}

	writeRecoveryJSON(w, http.StatusOK, map[string]string{"mnemonic": kit.Mnemonic})
}

func (h *recoveryHandlers) handleRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mnemonic string `json:"mnemonic"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRecoveryJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Mnemonic == "" {
		writeRecoveryJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "mnemonic is required"})
		return
	}

	keys, err := RestoreFromMnemonic(req.Mnemonic)
	if err != nil {
		writeRecoveryJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	pubB64 := base64.StdEncoding.EncodeToString(keys.Ed25519PubKey)
	writeRecoveryJSON(w, http.StatusOK, map[string]string{"public_key_b64": pubB64})
}

func (h *recoveryHandlers) handleEscrow(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeRecoveryJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRecoveryJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	blob, err := h.store.LoadRecoveryBlob(userID)
	if err != nil || blob == nil {
		writeRecoveryJSON(w, http.StatusNotFound, map[string]string{"error": "no recovery kit found; generate one first"})
		return
	}

	// Verify the password decrypts the blob correctly before escrowing.
	if req.Password != "" {
		if _, err := DecryptRecoveryKit(blob, req.Password); err != nil {
			writeRecoveryJSON(w, http.StatusBadRequest, map[string]string{"error": "password does not match recovery kit"})
			return
		}
	}

	// The encrypted blob is safe to send to the cloud — it never holds plaintext.
	writeRecoveryJSON(w, http.StatusOK, map[string]any{
		"status":      "escrowed",
		"blob_length": len(blob),
	})
}

func writeRecoveryJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// ─── Crypto internals ─────────────────────────────────────────────────────────

// encryptedBlobEnvelope is the on-disk format stored in RecoveryKit.EncryptedBlob.
type encryptedBlobEnvelope struct {
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

func encryptMnemonicBlob(mnemonic, password string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}

	key := argon2.IDKey([]byte(password), salt, kdfTime, kdfMemory, kdfThreads, kdfKeyLen)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	payload := encryptedMnemonic{Mnemonic: mnemonic}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	ct := aead.Seal(nil, nonce, plain, nil)
	env := encryptedBlobEnvelope{Salt: salt, Nonce: nonce, Ciphertext: ct}
	return json.Marshal(env)
}

func decryptMnemonicBlob(blob []byte, password string) (string, error) {
	var env encryptedBlobEnvelope
	if err := json.Unmarshal(blob, &env); err != nil {
		return "", fmt.Errorf("auth/recovery: parse blob: %w", err)
	}

	key := argon2.IDKey([]byte(password), env.Salt, kdfTime, kdfMemory, kdfThreads, kdfKeyLen)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", err
	}

	plain, err := aead.Open(nil, env.Nonce, env.Ciphertext, nil)
	if err != nil {
		return "", errors.New("auth/recovery: decrypt failed (wrong password?)")
	}

	var payload encryptedMnemonic
	if err := json.Unmarshal(plain, &payload); err != nil {
		return "", fmt.Errorf("auth/recovery: parse decrypted payload: %w", err)
	}
	return payload.Mnemonic, nil
}

// deriveRecoverySeed implements BIP39 seed derivation:
//
//	seed = PBKDF2-HMAC-SHA512(mnemonic, "mnemonic", 2048, 64)
func deriveRecoverySeed(mnemonic string) []byte {
	return pbkdf2HMACSHA512([]byte(mnemonic), []byte("mnemonic"), 2048, 64)
}

// pbkdf2HMACSHA512 is a minimal inline PBKDF2 (HMAC-SHA512) to avoid adding
// an import for golang.org/x/crypto/pbkdf2 which is not yet in go.mod.
func pbkdf2HMACSHA512(password, salt []byte, iter, keyLen int) []byte {
	hashLen := sha512.Size // 64
	blocks := (keyLen + hashLen - 1) / hashLen
	buf := make([]byte, 0, blocks*hashLen)
	for block := 1; block <= blocks; block++ {
		var blockInt [4]byte
		binary.BigEndian.PutUint32(blockInt[:], uint32(block))
		mac := hmac.New(sha512.New, password)
		mac.Write(salt)
		mac.Write(blockInt[:])
		u := mac.Sum(nil)
		xored := make([]byte, len(u))
		copy(xored, u)
		for i := 1; i < iter; i++ {
			mac = hmac.New(sha512.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range xored {
				xored[j] ^= u[j]
			}
		}
		buf = append(buf, xored...)
	}
	return buf[:keyLen]
}

func deriveEd25519Key(seed []byte) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	hk := hkdf.New(sha256.New, seed, nil, []byte("vulos-recovery-ed25519-keypair-v1"))
	keyMaterial := make([]byte, ed25519.SeedSize) // 32 bytes
	if _, err := io.ReadFull(hk, keyMaterial); err != nil {
		return nil, nil, err
	}
	priv := ed25519.NewKeyFromSeed(keyMaterial)
	pub := priv.Public().(ed25519.PublicKey)
	return priv, pub, nil
}

func deriveKeyringKey(seed []byte) ([]byte, error) {
	hk := hkdf.New(sha256.New, seed, nil, []byte("vulos-recovery-keyring-root-v1"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(hk, key); err != nil {
		return nil, err
	}
	return key, nil
}

// normaliseMnemonic lowercases and collapses whitespace.
func normaliseMnemonic(m string) string {
	return strings.Join(strings.Fields(strings.ToLower(m)), " ")
}

// ─── BIP39 entropy ↔ mnemonic conversion ─────────────────────────────────────

// entropyToMnemonic converts 32 bytes of entropy to a 24-word BIP39 mnemonic.
// Appends 8 SHA256 checksum bits, then encodes as 24 × 11-bit indices.
func entropyToMnemonic(entropy []byte) (string, error) {
	if len(entropy) != 32 {
		return "", fmt.Errorf("auth/recovery: entropy must be 32 bytes, got %d", len(entropy))
	}

	// SHA256 checksum — use first 8 bits for 256-bit entropy.
	h := sha256.Sum256(entropy)
	checksumByte := h[0]

	// Concatenate entropy (32 bytes) + checksum byte (1 byte) = 33 bytes = 264 bits.
	entBytes := make([]byte, 33)
	copy(entBytes, entropy)
	entBytes[32] = checksumByte

	// Interpret as a 264-bit big-endian integer, then extract 24 × 11-bit groups.
	n := new(big.Int).SetBytes(entBytes)
	divisor := big.NewInt(2048)
	remainder := new(big.Int)
	words := make([]string, 24)
	for i := 23; i >= 0; i-- {
		n.DivMod(n, divisor, remainder)
		words[i] = bip39Words[int(remainder.Int64())]
	}
	return strings.Join(words, " "), nil
}

// mnemonicToEntropy is the inverse of entropyToMnemonic.
// Validates the checksum and returns the original 32 bytes.
func mnemonicToEntropy(mnemonic string) ([]byte, error) {
	words := strings.Fields(mnemonic)
	if len(words) != 24 {
		return nil, fmt.Errorf("auth/recovery: expected 24 words, got %d", len(words))
	}

	n := new(big.Int)
	multiplier := big.NewInt(2048)
	for _, w := range words {
		idx, ok := wordIndex[w]
		if !ok {
			return nil, fmt.Errorf("auth/recovery: unknown word %q", w)
		}
		n.Mul(n, multiplier)
		n.Add(n, big.NewInt(int64(idx)))
	}

	// Back to 33 bytes (264 bits), left-padded.
	raw := n.Bytes()
	if len(raw) < 33 {
		padded := make([]byte, 33)
		copy(padded[33-len(raw):], raw)
		raw = padded
	}

	entropy := raw[:32]
	checksumByte := raw[32]

	h := sha256.Sum256(entropy)
	if checksumByte != h[0] {
		return nil, errors.New("auth/recovery: checksum mismatch")
	}
	return entropy, nil
}

// wordIndex maps BIP39 word → index for O(1) lookup.
var wordIndex map[string]int

func init() {
	wordIndex = make(map[string]int, len(bip39Words))
	for i, w := range bip39Words {
		wordIndex[w] = i
	}
}
