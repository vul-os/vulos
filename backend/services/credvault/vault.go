// Package credvault implements an encrypted credential vault (password manager backend)
// for Vula OS. Credentials are stored in AES-256-GCM encrypted files under
// ~/.vulos/auth/vault/. The encryption key is derived from a master password via
// Argon2id and kept only in memory while the vault is unlocked.
//
// This is a library/struct-only package — no HTTP handlers are included here.
package credvault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// ---- Constants & tunables ------------------------------------------------

const (
	// Argon2id parameters: memory=64 MiB, time=3, parallelism=4.
	// These are OWASP-recommended minimums for password hashing.
	argonMemory      uint32 = 64 * 1024 // KiB
	argonTime        uint32 = 3
	argonParallelism uint8  = 4
	argonKeyLen      uint32 = 32 // 256-bit AES key

	saltLen  = 32 // bytes
	nonceLen = 12 // GCM standard nonce

	vaultVersion = 1

	// defaultAutoLockDuration is how long the vault stays unlocked without activity.
	defaultAutoLockDuration = 5 * time.Minute
)

// ---- Error sentinel values -----------------------------------------------

var (
	ErrLocked          = errors.New("credvault: vault is locked")
	ErrWrongPassword   = errors.New("credvault: wrong master password")
	ErrNotFound        = errors.New("credvault: entry not found")
	ErrAlreadyExists   = errors.New("credvault: entry already exists")
	ErrAlreadyLocked   = errors.New("credvault: vault is already locked")
	ErrAlreadyUnlocked = errors.New("credvault: vault is already unlocked")
)

// ---- Persistent data structures ------------------------------------------

// vaultFile is the top-level structure stored in vault.enc (JSON, then encrypted).
type vaultFile struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Entry is a single credential record in the vault.
type Entry struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	Username     string    `json:"username"`
	Password     string    `json:"password"`
	Notes        string    `json:"notes,omitempty"`
	LinkedTOTPID string    `json:"linked_totp_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// saltFile is stored as vault.key (plain JSON — it is NOT secret; security
// comes from Argon2id hardness, not salt secrecy).
type saltFile struct {
	Salt []byte `json:"salt"`
}

// ---- Vault ---------------------------------------------------------------

// Vault is the credential vault. Create one with New, then call Unlock.
//
// All exported methods are safe for concurrent use.
type Vault struct {
	mu sync.Mutex

	dir string // ~/.vulos/auth/vault/

	// key is the AES-256 key derived via Argon2id from the master password.
	// It is non-nil only while the vault is unlocked.
	key []byte

	// entries is the in-memory cache of decrypted entries.
	// Only valid while unlocked.
	entries map[string]*Entry

	autoLockDuration time.Duration
	autoLockTimer    *time.Timer
}

// New creates a Vault whose data lives in dir (typically ~/.vulos/auth/vault/).
// The directory is created if it does not exist.
func New(dir string) (*Vault, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("credvault: create vault dir: %w", err)
	}
	return &Vault{
		dir:              dir,
		autoLockDuration: defaultAutoLockDuration,
	}, nil
}

// SetAutoLockDuration configures how long the vault stays unlocked without
// activity. Call before Unlock, or the change takes effect on the next unlock.
func (v *Vault) SetAutoLockDuration(d time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.autoLockDuration = d
}

// ---- Lock/Unlock state machine -------------------------------------------

// Unlock derives the AES key from masterPassword and decrypts vault.enc.
// Returns ErrWrongPassword if the password is incorrect.
// Returns ErrAlreadyUnlocked if the vault is already open.
func (v *Vault) Unlock(masterPassword string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.key != nil {
		return ErrAlreadyUnlocked
	}

	salt, err := v.loadOrCreateSalt()
	if err != nil {
		return err
	}

	key := deriveKey(masterPassword, salt)

	// If vault.enc exists, try to decrypt it.
	encPath := filepath.Join(v.dir, "vault.enc")
	if _, err := os.Stat(encPath); err == nil {
		data, err := os.ReadFile(encPath)
		if err != nil {
			return fmt.Errorf("credvault: read vault: %w", err)
		}
		plain, err := aesGCMDecrypt(key, data)
		if err != nil {
			// Decryption failure = wrong password.
			return ErrWrongPassword
		}
		var vf vaultFile
		if err := json.Unmarshal(plain, &vf); err != nil {
			return fmt.Errorf("credvault: parse vault: %w", err)
		}
		v.entries = make(map[string]*Entry, len(vf.Entries))
		for i := range vf.Entries {
			e := vf.Entries[i]
			v.entries[e.ID] = &e
		}
	} else {
		// First unlock — start with an empty vault.
		v.entries = make(map[string]*Entry)
	}

	v.key = key
	v.resetAutoLock()
	return nil
}

// Lock clears the in-memory key and entry cache, making entries inaccessible
// until Unlock is called again.
func (v *Vault) Lock() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.lockLocked()
}

// lockLocked performs the lock operation. Caller must hold v.mu.
func (v *Vault) lockLocked() error {
	if v.key == nil {
		return ErrAlreadyLocked
	}
	// Zero the key bytes before releasing the slice.
	for i := range v.key {
		v.key[i] = 0
	}
	v.key = nil
	v.entries = nil

	if v.autoLockTimer != nil {
		v.autoLockTimer.Stop()
		v.autoLockTimer = nil
	}
	return nil
}

// IsUnlocked reports whether the vault is currently unlocked.
func (v *Vault) IsUnlocked() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.key != nil
}

// resetAutoLock (re)starts the auto-lock timer. Caller must hold v.mu.
func (v *Vault) resetAutoLock() {
	if v.autoLockTimer != nil {
		v.autoLockTimer.Stop()
	}
	if v.autoLockDuration <= 0 {
		return
	}
	v.autoLockTimer = time.AfterFunc(v.autoLockDuration, func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		_ = v.lockLocked()
	})
}

// touch resets the auto-lock timer on any activity. Caller must hold v.mu.
func (v *Vault) touch() {
	v.resetAutoLock()
}

// ---- Entry CRUD ----------------------------------------------------------

// AddEntry creates a new credential entry.
// Returns ErrLocked if the vault is not open.
// Returns ErrAlreadyExists if an entry with the same ID already exists.
func (v *Vault) AddEntry(e Entry) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.key == nil {
		return ErrLocked
	}
	if _, ok := v.entries[e.ID]; ok {
		return ErrAlreadyExists
	}
	now := time.Now().UTC()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	v.entries[e.ID] = &e
	v.touch()
	return v.saveLocked()
}

// GetEntry returns the entry with the given ID.
// Returns ErrLocked if the vault is not open.
// Returns ErrNotFound if no entry has that ID.
func (v *Vault) GetEntry(id string) (Entry, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.key == nil {
		return Entry{}, ErrLocked
	}
	e, ok := v.entries[id]
	if !ok {
		return Entry{}, ErrNotFound
	}
	v.touch()
	return *e, nil
}

// ListEntries returns all entries. Passwords are included — callers should
// only surface them when explicitly requested by the user.
func (v *Vault) ListEntries() ([]Entry, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.key == nil {
		return nil, ErrLocked
	}
	out := make([]Entry, 0, len(v.entries))
	for _, e := range v.entries {
		out = append(out, *e)
	}
	v.touch()
	return out, nil
}

// UpdateEntry replaces the entry with the given ID.
// Returns ErrNotFound if the ID does not exist.
func (v *Vault) UpdateEntry(e Entry) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.key == nil {
		return ErrLocked
	}
	if _, ok := v.entries[e.ID]; !ok {
		return ErrNotFound
	}
	e.UpdatedAt = time.Now().UTC()
	v.entries[e.ID] = &e
	v.touch()
	return v.saveLocked()
}

// DeleteEntry removes the entry with the given ID.
// Returns ErrNotFound if the ID does not exist.
func (v *Vault) DeleteEntry(id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.key == nil {
		return ErrLocked
	}
	if _, ok := v.entries[id]; !ok {
		return ErrNotFound
	}
	delete(v.entries, id)
	v.touch()
	return v.saveLocked()
}

// ---- Persistence ---------------------------------------------------------

// saveLocked encrypts entries and writes vault.enc. Caller must hold v.mu.
func (v *Vault) saveLocked() error {
	entries := make([]Entry, 0, len(v.entries))
	for _, e := range v.entries {
		entries = append(entries, *e)
	}
	vf := vaultFile{
		Version: vaultVersion,
		Entries: entries,
	}
	plain, err := json.Marshal(vf)
	if err != nil {
		return fmt.Errorf("credvault: marshal vault: %w", err)
	}
	ciphertext, err := aesGCMEncrypt(v.key, plain)
	if err != nil {
		return fmt.Errorf("credvault: encrypt vault: %w", err)
	}
	encPath := filepath.Join(v.dir, "vault.enc")
	// Write atomically via a temp file.
	tmp := encPath + ".tmp"
	if err := os.WriteFile(tmp, ciphertext, 0600); err != nil {
		return fmt.Errorf("credvault: write vault tmp: %w", err)
	}
	if err := os.Rename(tmp, encPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("credvault: rename vault: %w", err)
	}
	return nil
}

// loadOrCreateSalt reads vault.key or creates a new random salt and saves it.
func (v *Vault) loadOrCreateSalt() ([]byte, error) {
	keyPath := filepath.Join(v.dir, "vault.key")

	data, err := os.ReadFile(keyPath)
	if err == nil {
		var sf saltFile
		if err := json.Unmarshal(data, &sf); err != nil {
			return nil, fmt.Errorf("credvault: parse vault.key: %w", err)
		}
		if len(sf.Salt) != saltLen {
			return nil, fmt.Errorf("credvault: unexpected salt length %d", len(sf.Salt))
		}
		return sf.Salt, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("credvault: read vault.key: %w", err)
	}

	// First run — generate a fresh salt.
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("credvault: generate salt: %w", err)
	}
	sf := saltFile{Salt: salt}
	raw, _ := json.Marshal(sf)
	if err := os.WriteFile(keyPath, raw, 0600); err != nil {
		return nil, fmt.Errorf("credvault: write vault.key: %w", err)
	}
	return salt, nil
}

// ---- Crypto helpers ------------------------------------------------------

// deriveKey runs Argon2id to produce a 256-bit AES key from a master password.
func deriveKey(password string, salt []byte) []byte {
	return argon2.IDKey(
		[]byte(password),
		salt,
		argonTime,
		argonMemory,
		argonParallelism,
		argonKeyLen,
	)
}

// aesGCMEncrypt encrypts plaintext with AES-256-GCM.
// Output format: [12-byte nonce][ciphertext+tag].
func aesGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nonce, nonce, plaintext, nil)
	return ct, nil
}

// aesGCMDecrypt decrypts ciphertext produced by aesGCMEncrypt.
func aesGCMDecrypt(key, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < nonceLen {
		return nil, errors.New("ciphertext too short")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := ciphertext[:nonceLen]
	ct := ciphertext[nonceLen:]
	return gcm.Open(nil, nonce, ct, nil)
}
