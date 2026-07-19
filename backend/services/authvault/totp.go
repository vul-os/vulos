// Package authvault implements OS-level authentication storage for Vula OS.
// It provides encrypted TOTP secret management per RFC 6238 using AES-256-GCM
// at rest under ~/.vulos/auth/totp/.
package authvault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"vulos/backend/internal/datadir"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// ─── Data types ─────────────────────────────────────────────────────────────

// Account holds the plaintext TOTP metadata for one entry.
// Secrets are never stored in this struct — they live in keychain.enc.
type Account struct {
	ID        string    `json:"id"`
	Issuer    string    `json:"issuer"`
	Service   string    `json:"service"` // human label, e.g. "GitHub"
	AccountID string    `json:"account"` // e.g. user@example.com
	Algorithm string    `json:"algorithm"`
	Digits    int       `json:"digits"`
	Period    int       `json:"period"` // seconds, usually 30
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
}

// keychainEntry is what actually gets written to keychain.enc (per-account).
type keychainEntry struct {
	ID     string `json:"id"`
	Secret string `json:"secret"` // base32 TOTP secret, encrypted via outer envelope
}

// keychainEnvelope is the on-disk format for keychain.enc.
// The plaintext JSON of []keychainEntry is encrypted with AES-256-GCM.
type keychainEnvelope struct {
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// ─── Store ───────────────────────────────────────────────────────────────────

// Store manages TOTP accounts: encrypted secrets + plaintext metadata.
type Store struct {
	mu           sync.RWMutex
	accounts     map[string]*Account // id -> Account
	secrets      map[string]string   // id -> base32 secret (in-memory only)
	keychainKey  []byte              // 32-byte AES key derived from keyfile
	keychainPath string
	metaPath     string
}

// dataDir returns ~/.vulos/auth/totp, creating it if needed.
func dataDir() (string, error) {
	_, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("authvault: cannot determine home dir: %w", err)
	}
	dir := datadir.Join("auth", "totp")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("authvault: cannot create dir %s: %w", dir, err)
	}
	return dir, nil
}

// NewStore opens (or creates) the TOTP store in the default location.
// The encryption key is derived from a persistent keyfile; one is generated
// on first run and kept at ~/.vulos/auth/totp/keyfile (mode 0600).
func NewStore() (*Store, error) {
	dir, err := dataDir()
	if err != nil {
		return nil, err
	}
	keyfilePath := filepath.Join(dir, "keyfile")
	key, err := loadOrCreateKeyfile(keyfilePath)
	if err != nil {
		return nil, err
	}

	s := &Store{
		accounts:     make(map[string]*Account),
		secrets:      make(map[string]string),
		keychainKey:  key,
		keychainPath: filepath.Join(dir, "keychain.enc"),
		metaPath:     filepath.Join(dir, "accounts.json"),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewStoreAt opens (or creates) the TOTP store at an explicit directory.
// This is used by tests to isolate storage.
func NewStoreAt(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("authvault: cannot create dir %s: %w", dir, err)
	}
	keyfilePath := filepath.Join(dir, "keyfile")
	key, err := loadOrCreateKeyfile(keyfilePath)
	if err != nil {
		return nil, err
	}
	s := &Store{
		accounts:     make(map[string]*Account),
		secrets:      make(map[string]string),
		keychainKey:  key,
		keychainPath: filepath.Join(dir, "keychain.enc"),
		metaPath:     filepath.Join(dir, "accounts.json"),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// ─── Public API ──────────────────────────────────────────────────────────────

// AddFromURI adds a TOTP account by parsing an otpauth:// URI.
// Returns the new account ID.
func (s *Store) AddFromURI(uri string) (*Account, error) {
	// Validate scheme before handing off to the library, which is permissive.
	if !strings.HasPrefix(uri, "otpauth://totp/") && !strings.HasPrefix(uri, "otpauth://hotp/") {
		return nil, fmt.Errorf("authvault: URI must start with otpauth://totp/ or otpauth://hotp/")
	}

	key, err := otp.NewKeyFromURL(uri)
	if err != nil {
		return nil, fmt.Errorf("authvault: invalid otpauth URI: %w", err)
	}

	// Normalise & validate the raw secret (must be valid base32).
	rawSecret := strings.ToUpper(strings.TrimRight(key.Secret(), "="))
	if _, err := base32.StdEncoding.DecodeString(pad32(rawSecret)); err != nil {
		return nil, fmt.Errorf("authvault: invalid base32 secret in URI: %w", err)
	}

	digits := int(key.Digits())
	if digits == 0 {
		digits = 6
	}
	period := int(key.Period())
	if period == 0 {
		period = 30
	}
	alg := key.Algorithm().String()
	if alg == "" {
		alg = "SHA1"
	}

	acc := &Account{
		ID:        generateID(),
		Issuer:    key.Issuer(),
		Service:   labelService(key),
		AccountID: key.AccountName(),
		Algorithm: alg,
		Digits:    digits,
		Period:    period,
		CreatedAt: time.Now(),
	}
	return s.addAccount(acc, rawSecret)
}

// AddManual adds a TOTP account from individual fields.
// secret must be a base32-encoded string (no padding required).
func (s *Store) AddManual(service, issuer, accountID, secret string) (*Account, error) {
	rawSecret := strings.ToUpper(strings.TrimRight(secret, "="))
	if _, err := base32.StdEncoding.DecodeString(pad32(rawSecret)); err != nil {
		return nil, fmt.Errorf("authvault: invalid base32 secret: %w", err)
	}
	acc := &Account{
		ID:        generateID(),
		Issuer:    issuer,
		Service:   service,
		AccountID: accountID,
		Algorithm: "SHA1",
		Digits:    6,
		Period:    30,
		CreatedAt: time.Now(),
	}
	return s.addAccount(acc, rawSecret)
}

// List returns all accounts (metadata only, no secrets).
func (s *Store) List() []*Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		cp := *a
		out = append(out, &cp)
	}
	return out
}

// Code returns the current TOTP code for the given account ID.
// The code is always 6 digits (or as configured), rolling on 30-second windows.
func (s *Store) Code(id string) (code string, remaining time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	acc, ok := s.accounts[id]
	if !ok {
		return "", 0, fmt.Errorf("authvault: account %q not found", id)
	}
	secret, ok := s.secrets[id]
	if !ok {
		return "", 0, fmt.Errorf("authvault: secret for account %q not loaded", id)
	}

	now := time.Now()
	code, err = totp.GenerateCodeCustom(pad32(secret), now, totp.ValidateOpts{
		Period:    uint(acc.Period),
		Skew:      0,
		Digits:    otp.Digits(acc.Digits),
		Algorithm: parseAlgorithm(acc.Algorithm),
	})
	if err != nil {
		return "", 0, fmt.Errorf("authvault: code generation failed: %w", err)
	}

	// Time remaining in current window.
	period := time.Duration(acc.Period) * time.Second
	elapsed := time.Duration(now.Unix()%int64(acc.Period)) * time.Second
	remaining = period - elapsed

	acc.LastUsed = now
	if err := s.flush(); err != nil {
		// Non-fatal: code was generated, just log would suffice.
		_ = err
	}

	return code, remaining, nil
}

// Delete removes an account and its secret.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.accounts[id]; !ok {
		return fmt.Errorf("authvault: account %q not found", id)
	}
	delete(s.accounts, id)
	delete(s.secrets, id)
	return s.flush()
}

// ─── Internal helpers ────────────────────────────────────────────────────────

func (s *Store) addAccount(acc *Account, secret string) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.accounts[acc.ID] = acc
	s.secrets[acc.ID] = secret
	if err := s.flush(); err != nil {
		// Rollback in-memory state on error.
		delete(s.accounts, acc.ID)
		delete(s.secrets, acc.ID)
		return nil, err
	}
	return acc, nil
}

// flush writes accounts.json and keychain.enc under the write-lock.
// Caller must hold s.mu (write).
func (s *Store) flush() error {
	if err := s.writeMeta(); err != nil {
		return err
	}
	return s.writeKeychain()
}

func (s *Store) writeMeta() error {
	accs := make([]*Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		accs = append(accs, a)
	}
	data, err := json.MarshalIndent(accs, "", "  ")
	if err != nil {
		return fmt.Errorf("authvault: marshal meta: %w", err)
	}
	tmp := s.metaPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("authvault: write meta: %w", err)
	}
	return os.Rename(tmp, s.metaPath)
}

func (s *Store) writeKeychain() error {
	entries := make([]keychainEntry, 0, len(s.secrets))
	for id, sec := range s.secrets {
		entries = append(entries, keychainEntry{ID: id, Secret: sec})
	}
	plaintext, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("authvault: marshal keychain: %w", err)
	}

	block, err := aes.NewCipher(s.keychainKey)
	if err != nil {
		return fmt.Errorf("authvault: AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("authvault: GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("authvault: nonce gen: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	env := keychainEnvelope{Nonce: nonce, Ciphertext: ciphertext}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("authvault: marshal envelope: %w", err)
	}
	tmp := s.keychainPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("authvault: write keychain: %w", err)
	}
	return os.Rename(tmp, s.keychainPath)
}

func (s *Store) load() error {
	// Load metadata.
	if data, err := os.ReadFile(s.metaPath); err == nil {
		var accs []*Account
		if err := json.Unmarshal(data, &accs); err != nil {
			return fmt.Errorf("authvault: parse accounts.json: %w", err)
		}
		for _, a := range accs {
			s.accounts[a.ID] = a
		}
	}
	// Load and decrypt keychain.
	if data, err := os.ReadFile(s.keychainPath); err == nil {
		var env keychainEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			return fmt.Errorf("authvault: parse keychain.enc: %w", err)
		}

		block, err := aes.NewCipher(s.keychainKey)
		if err != nil {
			return fmt.Errorf("authvault: AES cipher (load): %w", err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return fmt.Errorf("authvault: GCM (load): %w", err)
		}
		plaintext, err := gcm.Open(nil, env.Nonce, env.Ciphertext, nil)
		if err != nil {
			return fmt.Errorf("authvault: decrypt keychain: %w", err)
		}

		var entries []keychainEntry
		if err := json.Unmarshal(plaintext, &entries); err != nil {
			return fmt.Errorf("authvault: parse keychain entries: %w", err)
		}
		for _, e := range entries {
			s.secrets[e.ID] = e.Secret
		}
	}
	return nil
}

// ─── Key management ──────────────────────────────────────────────────────────

// loadOrCreateKeyfile reads a 32-byte keyfile or creates one with random data.
// The key is derived via SHA-256 of the raw bytes to ensure uniform length.
func loadOrCreateKeyfile(path string) ([]byte, error) {
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
		h := sha256.Sum256(raw)
		return h[:], nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("authvault: rand read: %w", err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return nil, fmt.Errorf("authvault: write keyfile: %w", err)
	}
	h := sha256.Sum256(raw)
	return h[:], nil
}

// ─── Utility functions ───────────────────────────────────────────────────────

// generateID returns a random 16-character hex string.
func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// pad32 adds base32 padding so the standard decoder accepts strings without '='.
func pad32(s string) string {
	switch len(s) % 8 {
	case 0:
		return s
	case 1:
		return s + "======="
	case 2:
		return s + "======"
	case 3:
		return s + "====="
	case 4:
		return s + "===="
	case 5:
		return s + "==="
	case 6:
		return s + "=="
	case 7:
		return s + "="
	}
	return s
}

// parseAlgorithm converts an algorithm string to the otp.Algorithm type.
func parseAlgorithm(alg string) otp.Algorithm {
	switch strings.ToUpper(alg) {
	case "SHA256":
		return otp.AlgorithmSHA256
	case "SHA512":
		return otp.AlgorithmSHA512
	default:
		return otp.AlgorithmSHA1
	}
}

// labelService extracts a human-friendly service name from an OTP key.
// It prefers the issuer, then falls back to the label host portion.
func labelService(key *otp.Key) string {
	if key.Issuer() != "" {
		return key.Issuer()
	}
	// The key URL label is "issuer:account" or just "account".
	parsed, err := url.Parse(key.URL())
	if err != nil {
		return key.AccountName()
	}
	label := strings.TrimPrefix(parsed.Path, "/")
	if idx := strings.Index(label, ":"); idx >= 0 {
		return label[:idx]
	}
	return label
}
