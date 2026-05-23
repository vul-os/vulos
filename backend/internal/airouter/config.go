// Package airouter is the OS-level model-API router.
//
// Two supply modes:
//
//   - byo  – user provides provider API keys stored encrypted in SQLite.
//   - cloud – requests are proxied to POST /api/ai/proxy on the Vulos cloud
//     control plane, authenticated with the OS device cert.
//
// Callers always receive an io.ReadCloser carrying a Server-Sent Events stream.
package airouter

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations
var migrationsFS embed.FS

// Mode is the routing supply mode.
type Mode string

const (
	ModeBYO   Mode = "byo"
	ModeCloud Mode = "cloud"
)

// Config is the router's persisted configuration (singleton row).
type Config struct {
	Mode        Mode   `json:"mode"`
	ActiveModel string `json:"active_model"`
}

// Provider is one configured AI provider in BYO mode.
type Provider struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	BaseURL     string   `json:"base_url"`
	APIKeyEnc   string   `json:"api_key_enc,omitempty"` // encrypted at rest; not sent to callers
	Models      []string `json:"models"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Store holds the durable config for the AI Router.
type Store struct {
	db         *sql.DB
	encryptKey []byte // 32-byte AES-256 key derived from OS keyring / env
}

// NewStore opens (creating if needed) the SQLite database at path and runs
// migrations. encryptKey must be exactly 32 bytes (AES-256-GCM).
// On any failure it returns an error; callers must not call methods on a nil Store.
func NewStore(path string, encryptKey []byte) (*Store, error) {
	if len(encryptKey) != 32 {
		return nil, fmt.Errorf("airouter: encryptKey must be 32 bytes, got %d", len(encryptKey))
	}
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, encryptKey: encryptKey}, nil
}

// NewStoreFromEnv opens the store using AIROUTER_DB (default: airouter.db)
// and derives the encrypt key from AIROUTER_KEY_HEX.
//
// Production fail-closed behaviour: when VULOS_ENV=prod and AIROUTER_KEY_HEX is
// unset, this function returns an error and refuses to initialise the Store so
// the deterministic dev key can never silently encrypt operator API keys in a
// production deployment. In dev / local (or when VULOS_ENV is unset and the
// process is clearly non-prod), it logs a loud warning and falls back to the
// dev key so tests and developer workflows continue to work.
func NewStoreFromEnv() (*Store, error) {
	dbPath := getenv("AIROUTER_DB", "airouter.db")
	keyHex := os.Getenv("AIROUTER_KEY_HEX")
	var key []byte
	if keyHex != "" {
		var err error
		key, err = hex.DecodeString(keyHex)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("airouter: AIROUTER_KEY_HEX must be 64 hex chars (32 bytes)")
		}
	} else {
		if os.Getenv("VULOS_ENV") == "prod" {
			return nil, fmt.Errorf("airouter: AIROUTER_KEY_HEX is unset in VULOS_ENV=prod — refusing to initialise with the dev key (set AIROUTER_KEY_HEX to a 64-hex-char value)")
		}
		// Dev fallback: derive from a constant string — insecure, but allows
		// tests and dev builds to run without env setup. Log loudly so operators
		// who accidentally land here in a staging-like environment notice.
		log.Printf("[airouter] WARNING: AIROUTER_KEY_HEX unset — using deterministic dev key (NEVER use in production; set VULOS_ENV=prod to enforce)")
		h := sha256.Sum256([]byte("vulos-airouter-dev-key-do-not-use-in-prod"))
		key = h[:]
	}
	return NewStore(dbPath, key)
}

// Close closes the underlying SQLite database.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// GetConfig returns the current router configuration.
func (s *Store) GetConfig() (Config, error) {
	var mode, model string
	err := s.db.QueryRow(`SELECT mode, active_model FROM airouter_config WHERE id=1`).Scan(&mode, &model)
	if err != nil {
		return Config{}, fmt.Errorf("airouter: GetConfig: %w", err)
	}
	return Config{Mode: Mode(mode), ActiveModel: model}, nil
}

// SetConfig updates the router mode and/or active model.
func (s *Store) SetConfig(cfg Config) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(
		`UPDATE airouter_config SET mode=?, active_model=?, updated_at=? WHERE id=1`,
		string(cfg.Mode), cfg.ActiveModel, now,
	)
	return err
}

// AddProvider inserts a new provider. APIKey is encrypted before storage.
func (s *Store) AddProvider(p Provider) error {
	enc, err := s.encryptKey32(p.APIKeyEnc)
	if err != nil {
		return fmt.Errorf("airouter: encrypt api key: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(
		`INSERT INTO airouter_providers(id, display_name, base_url, api_key_enc, models, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.DisplayName, p.BaseURL, enc, joinModels(p.Models), now, now,
	)
	return err
}

// UpdateProvider replaces a provider's mutable fields (name, URL, key, models).
func (s *Store) UpdateProvider(p Provider) error {
	enc, err := s.encryptKey32(p.APIKeyEnc)
	if err != nil {
		return fmt.Errorf("airouter: encrypt api key: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.Exec(
		`UPDATE airouter_providers SET display_name=?, base_url=?, api_key_enc=?, models=?, updated_at=?
		 WHERE id=?`,
		p.DisplayName, p.BaseURL, enc, joinModels(p.Models), now, p.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("airouter: provider %q not found", p.ID)
	}
	return nil
}

// DeleteProvider removes a provider by ID.
func (s *Store) DeleteProvider(id string) error {
	_, err := s.db.Exec(`DELETE FROM airouter_providers WHERE id=?`, id)
	return err
}

// ListProviders returns all providers with decrypted API keys.
func (s *Store) ListProviders() ([]Provider, error) {
	rows, err := s.db.Query(
		`SELECT id, display_name, base_url, api_key_enc, models, created_at, updated_at FROM airouter_providers ORDER BY created_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("airouter: ListProviders: %w", err)
	}
	defer rows.Close()

	var providers []Provider
	for rows.Next() {
		var p Provider
		var enc, models, createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.BaseURL, &enc, &models, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		apiKey, err := s.decryptKey32(enc)
		if err != nil {
			log.Printf("[airouter] decrypt key for provider %s: %v (skipping)", p.ID, err)
		} else {
			p.APIKeyEnc = apiKey
		}
		p.Models = splitModels(models)
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

// GetProvider returns a single provider with decrypted API key.
func (s *Store) GetProvider(id string) (Provider, error) {
	var p Provider
	var enc, models, createdAt, updatedAt string
	err := s.db.QueryRow(
		`SELECT id, display_name, base_url, api_key_enc, models, created_at, updated_at FROM airouter_providers WHERE id=?`, id,
	).Scan(&p.ID, &p.DisplayName, &p.BaseURL, &enc, &models, &createdAt, &updatedAt)
	if err != nil {
		return Provider{}, fmt.Errorf("airouter: GetProvider %q: %w", id, err)
	}
	apiKey, err := s.decryptKey32(enc)
	if err == nil {
		p.APIKeyEnc = apiKey
	}
	p.Models = splitModels(models)
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return p, nil
}

// --- encryption helpers (AES-256-GCM) ---

func (s *Store) encryptKey32(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	block, err := aes.NewCipher(s.encryptKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(ct), nil
}

func (s *Store) decryptKey32(cipherHex string) (string, error) {
	if cipherHex == "" {
		return "", nil
	}
	data, err := hex.DecodeString(cipherHex)
	if err != nil {
		return "", fmt.Errorf("decode hex: %w", err)
	}
	block, err := aes.NewCipher(s.encryptKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return "", fmt.Errorf("ciphertext too short")
	}
	pt, err := gcm.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), nil
}

// --- DB helpers ---

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func runMigrations(db *sql.DB) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("airouter: read migrations dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("airouter: read migration %s: %w", e.Name(), err)
		}
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			return fmt.Errorf("airouter: exec migration %s: %w", e.Name(), err)
		}
	}
	return nil
}

// --- model list helpers ---

func joinModels(models []string) string {
	result := ""
	for i, m := range models {
		if i > 0 {
			result += ","
		}
		result += m
	}
	return result
}

func splitModels(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := s[start:i]
			if part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
