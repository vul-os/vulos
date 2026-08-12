// identity.go — Ed25519 keypair, Vula ID, and identity HTTP handlers.
//
// Vula ID format:
//
//	vulos:ed25519:<base58-encoded-32-byte-public-key>
//
// Address format (used for discovery / contact requests):
//
//	<vulos_id>@<host>:<port>
//
// First-boot behaviour:
//
//	New() calls loadOrGenerate() which looks for
//	  ~/.vulos/peering/identity/ed25519.priv  (raw 64-byte seed+pub key, 0600)
//	  ~/.vulos/peering/identity/ed25519.pub   (raw 32-byte public key, 0644)
//	  ~/.vulos/peering/identity/vulos_id       (text, 0644)
//	If any file is absent the full keypair is re-generated and all three are
//	written atomically (temp-file + rename).
package peering

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// ─── Base58 (Bitcoin alphabet) ────────────────────────────────────────────────

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// base58Encode encodes b using the Bitcoin base58 alphabet.
func base58Encode(b []byte) string {
	// Count leading zero bytes.
	leadingZeros := 0
	for _, v := range b {
		if v != 0 {
			break
		}
		leadingZeros++
	}

	// Convert big-endian bytes to a big.Int.
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

// base58Decode decodes a base58-encoded string.
func base58Decode(s string) ([]byte, error) {
	// Build lookup table.
	lookup := [256]int{}
	for i := range lookup {
		lookup[i] = -1
	}
	for i, c := range base58Alphabet {
		lookup[c] = i
	}

	// Count leading '1's.
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

	// Re-prepend leading zero bytes.
	result := make([]byte, leadingZeros+len(decoded))
	copy(result[leadingZeros:], decoded)
	return result, nil
}

// ─── Vula ID ──────────────────────────────────────────────────────────────────

const vulosIDPrefix = "vulos:ed25519:"

// encodeVulosID encodes a 32-byte Ed25519 public key as a Vula ID string.
func encodeVulosID(pub ed25519.PublicKey) string {
	return vulosIDPrefix + base58Encode(pub)
}

// decodeVulosID decodes a Vula ID string and returns the raw public key.
func decodeVulosID(id string) (ed25519.PublicKey, error) {
	if !strings.HasPrefix(id, vulosIDPrefix) {
		return nil, fmt.Errorf("vula id: expected prefix %q, got %q", vulosIDPrefix, id)
	}
	raw, err := base58Decode(strings.TrimPrefix(id, vulosIDPrefix))
	if err != nil {
		return nil, fmt.Errorf("vula id: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("vula id: decoded key length %d, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// EncodeVulosID encodes a raw Ed25519 public key as its canonical Vula ID
// ("vulos:ed25519:<base58>"). It is the exported counterpart of PublicKeyForVulosID
// so other services (and cross-package tests) can construct a Vula ID from a key
// without reimplementing the base58 / prefix machinery.
func EncodeVulosID(pub ed25519.PublicKey) string {
	return encodeVulosID(pub)
}

// PublicKeyForVulosID decodes a Vula ID ("vulos:ed25519:<base58>") into the raw
// Ed25519 public key it encodes. It is the exported counterpart of the internal
// decodeVulosID, provided so other services (e.g. the Files OS peer-share
// capability layer) can verify signatures made by a remote box's identity key
// WITHOUT importing the base58 / Vula-ID machinery themselves. Vula-ID handling
// stays centralized in this package.
func PublicKeyForVulosID(id string) (ed25519.PublicKey, error) {
	return decodeVulosID(id)
}

// VerifyVulaSignature reports whether sig is a valid Ed25519 signature by the
// identity behind vulosID over msg. It is a small, self-contained verification
// primitive for cross-box capability/proof checks: the verifier needs only the
// peer's Vula ID (which embeds the public key) — no prior key exchange. Returns
// a non-nil error when the Vula ID is malformed or the signature does not match.
func VerifyVulaSignature(vulosID string, msg, sig []byte) error {
	pub, err := decodeVulosID(vulosID)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, msg, sig) {
		return errors.New("peering: vula signature mismatch")
	}
	return nil
}

// ─── Address parsing ──────────────────────────────────────────────────────────

// VulaAddress is a Vula ID combined with a server host and port.
// Wire format: <vulos_id>@<host>:<port>
type VulaAddress struct {
	VulosID string
	Host    string
	Port    int
}

// ParseVulaAddress parses a Vula address string of the form
// "vulos:ed25519:<base58>@host:port".
func ParseVulaAddress(s string) (VulaAddress, error) {
	// Split on the last '@' to allow ':' inside the Vula ID prefix.
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return VulaAddress{}, errors.New("vula address: missing '@'")
	}
	id := s[:at]
	hostport := s[at+1:]

	colon := strings.LastIndex(hostport, ":")
	if colon < 0 {
		return VulaAddress{}, errors.New("vula address: missing ':' in host:port")
	}
	host := hostport[:colon]
	portStr := hostport[colon+1:]
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return VulaAddress{}, fmt.Errorf("vula address: invalid port %q", portStr)
	}

	// Validate the Vula ID portion.
	if _, err := decodeVulosID(id); err != nil {
		return VulaAddress{}, fmt.Errorf("vula address: %w", err)
	}

	return VulaAddress{VulosID: id, Host: host, Port: port}, nil
}

// String returns the canonical wire form of the address.
func (a VulaAddress) String() string {
	return fmt.Sprintf("%s@%s:%d", a.VulosID, a.Host, a.Port)
}

// ─── Keypair persistence ──────────────────────────────────────────────────────

const (
	privKeyFile = "ed25519.priv" // 64 bytes: 32-byte seed || 32-byte public key
	pubKeyFile  = "ed25519.pub"  // 32 bytes
	vulosIDFile = "vulos_id"     // text, e.g. "vulos:ed25519:..."
)

// loadOrGenerate loads the Ed25519 keypair from the identity directory, or
// generates a new one if any file is missing/corrupt. Returns private key, public
// key, and the canonical Vula ID string.
func loadOrGenerate(identityDir string) (ed25519.PrivateKey, ed25519.PublicKey, string, error) {
	privPath := filepath.Join(identityDir, privKeyFile)
	pubPath := filepath.Join(identityDir, pubKeyFile)
	idPath := filepath.Join(identityDir, vulosIDFile)

	// Try to load existing keypair.
	privBytes, privErr := os.ReadFile(privPath)
	pubBytes, pubErr := os.ReadFile(pubPath)

	if privErr == nil && pubErr == nil &&
		len(privBytes) == ed25519.PrivateKeySize &&
		len(pubBytes) == ed25519.PublicKeySize {

		priv := ed25519.PrivateKey(privBytes)
		pub := ed25519.PublicKey(pubBytes)
		vulosID := encodeVulosID(pub)

		// Re-write vulos_id if absent (idempotent).
		if _, err := os.Stat(idPath); os.IsNotExist(err) {
			_ = writeFile(idPath, []byte(vulosID), 0644)
		}

		log.Printf("[peering/identity] loaded existing keypair: %s", vulosID)
		return priv, pub, vulosID, nil
	}

	// Generate a new keypair.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, "", fmt.Errorf("identity: generate key: %w", err)
	}
	vulosID := encodeVulosID(pub)

	if err := writeFile(privPath, []byte(priv), 0600); err != nil {
		return nil, nil, "", fmt.Errorf("identity: write priv key: %w", err)
	}
	if err := writeFile(pubPath, []byte(pub), 0644); err != nil {
		return nil, nil, "", fmt.Errorf("identity: write pub key: %w", err)
	}
	if err := writeFile(idPath, []byte(vulosID), 0644); err != nil {
		return nil, nil, "", fmt.Errorf("identity: write vulos_id: %w", err)
	}

	log.Printf("[peering/identity] generated new keypair: %s", vulosID)
	return priv, pub, vulosID, nil
}

// writeFile writes data to path via a sibling temp file + atomic rename, then
// sets the given permission bits.
func writeFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// ─── Encrypted export / import ────────────────────────────────────────────────

// exportPayload is the JSON envelope written during export.
type exportPayload struct {
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`  // "argon2id"
	Salt       []byte `json:"salt"` // 16 bytes
	Time       uint32 `json:"time"`
	Memory     uint32 `json:"memory"`
	Threads    uint8  `json:"threads"`
	Nonce      []byte `json:"nonce"`      // 12 bytes (XChaCha20-Poly1305 uses 24; we use chacha20poly1305 IETF = 12)
	Ciphertext []byte `json:"ciphertext"` // encrypted 64-byte private key
	VulosID    string `json:"vulos_id"`
}

const (
	argon2Time    = 3
	argon2Memory  = 64 * 1024 // 64 MiB
	argon2Threads = 4
	argon2KeyLen  = chacha20poly1305.KeySize // 32
)

// encryptExport encrypts the private key with a passphrase using
// Argon2id → ChaCha20-Poly1305. Returns a JSON byte slice.
func encryptExport(priv ed25519.PrivateKey, vulosID, passphrase string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}

	key := argon2.IDKey([]byte(passphrase), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	ciphertext := aead.Seal(nil, nonce, []byte(priv), []byte(vulosID))

	payload := exportPayload{
		Version:    1,
		KDF:        "argon2id",
		Salt:       salt,
		Time:       argon2Time,
		Memory:     argon2Memory,
		Threads:    argon2Threads,
		Nonce:      nonce,
		Ciphertext: ciphertext,
		VulosID:    vulosID,
	}
	return json.Marshal(payload)
}

// decryptImport decrypts a JSON export bundle produced by encryptExport.
// Returns the private key and its Vula ID.
func decryptImport(data []byte, passphrase string) (ed25519.PrivateKey, string, error) {
	var p exportPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, "", fmt.Errorf("import: %w", err)
	}
	if p.Version != 1 {
		return nil, "", fmt.Errorf("import: unsupported version %d", p.Version)
	}

	key := argon2.IDKey([]byte(passphrase), p.Salt, p.Time, p.Memory, p.Threads, argon2KeyLen)

	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, "", err
	}

	plain, err := aead.Open(nil, p.Nonce, p.Ciphertext, []byte(p.VulosID))
	if err != nil {
		return nil, "", errors.New("import: decryption failed (wrong passphrase?)")
	}
	if len(plain) != ed25519.PrivateKeySize {
		return nil, "", fmt.Errorf("import: unexpected key length %d", len(plain))
	}
	return ed25519.PrivateKey(plain), p.VulosID, nil
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

// identityResponse is the JSON shape returned by GET /api/peering/identity.
type identityResponse struct {
	VulosID      string `json:"vulos_id"`
	PublicKeyB58 string `json:"public_key_b58"`
	StorageRoot  string `json:"storage_root"`
}

// handleGetIdentity implements GET /api/peering/identity.
func (s *Service) handleGetIdentity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	pub := s.pub
	resp := identityResponse{
		VulosID:      s.vulosID,
		PublicKeyB58: base58Encode(pub),
		StorageRoot:  s.root,
	}
	json.NewEncoder(w).Encode(resp)
}

// handleExportIdentity implements POST /api/peering/identity/export.
//
// Request JSON: { "passphrase": "..." }
// Response JSON: the encrypted export bundle (exportPayload).
func (s *Service) handleExportIdentity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Passphrase == "" {
		http.Error(w, `{"error":"passphrase is required"}`, http.StatusBadRequest)
		return
	}

	bundle, err := encryptExport(s.priv, s.vulosID, req.Passphrase)
	if err != nil {
		log.Printf("[peering/identity] export error: %v", err)
		http.Error(w, `{"error":"export failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(bundle)
}

// handleImportIdentity implements POST /api/peering/identity/import.
//
// Request JSON: the full exportPayload JSON + "passphrase" field.
// On success, the keypair on disk is overwritten and the in-memory state updated.
func (s *Service) handleImportIdentity(w http.ResponseWriter, r *http.Request) {
	// Read the raw body — it is the export bundle with an extra "passphrase" field.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"read error"}`, http.StatusBadRequest)
		return
	}

	// Extract passphrase separately.
	var meta struct {
		Passphrase string `json:"passphrase"`
	}
	if err := json.Unmarshal(body, &meta); err != nil || meta.Passphrase == "" {
		http.Error(w, `{"error":"passphrase is required"}`, http.StatusBadRequest)
		return
	}

	priv, vulosID, err := decryptImport(body, meta.Passphrase)
	if err != nil {
		log.Printf("[peering/identity] import error: %v", err)
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	pub := priv.Public().(ed25519.PublicKey)

	// Persist to disk.
	identityDir := filepath.Join(s.root, "identity")
	if err := writeFile(filepath.Join(identityDir, privKeyFile), []byte(priv), 0600); err != nil {
		log.Printf("[peering/identity] import persist priv: %v", err)
		http.Error(w, `{"error":"failed to persist keypair"}`, http.StatusInternalServerError)
		return
	}
	if err := writeFile(filepath.Join(identityDir, pubKeyFile), []byte(pub), 0644); err != nil {
		log.Printf("[peering/identity] import persist pub: %v", err)
		http.Error(w, `{"error":"failed to persist keypair"}`, http.StatusInternalServerError)
		return
	}
	if err := writeFile(filepath.Join(identityDir, vulosIDFile), []byte(vulosID), 0644); err != nil {
		log.Printf("[peering/identity] import persist vulos_id: %v", err)
		http.Error(w, `{"error":"failed to persist vulos_id"}`, http.StatusInternalServerError)
		return
	}

	// Update in-memory state.
	s.priv = priv
	s.pub = pub
	s.vulosID = vulosID

	log.Printf("[peering/identity] imported keypair: %s", vulosID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(identityResponse{
		VulosID:      vulosID,
		PublicKeyB58: base58Encode(pub),
		StorageRoot:  s.root,
	})
}
