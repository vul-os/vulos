// Package joincode implements cross-device cluster joins via short-codes and QR payloads.
// A JoinCode carries scoped S3 credentials so a new device can join the cluster without
// requiring the user to manually copy secrets. Codes are one-time-use and expire in 1 h.
package joincode

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Crockford base32 alphabet — omits 0, O, I, L to avoid visual confusion.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ErrExpired is returned by Decode when the join code has passed its TTL.
var ErrExpired = errors.New("join code expired")

// ErrInvalid is returned by Decode when the short code is not found.
var ErrInvalid = errors.New("join code not found or already used")

// JoinCode holds the S3 credentials and expiry for a cluster join operation.
// It is JSON-serializable so it can be embedded in a QR payload or returned over HTTP.
type JoinCode struct {
	Bucket    string    `json:"bucket"`
	Region    string    `json:"region"`
	AccessKey string    `json:"access_key"`
	SecretKey string    `json:"secret_key"`
	ExpiresAt time.Time `json:"expires_at"`
}

// storageConfig mirrors the shape written by storageprov to storage.json.
// Only the fields we need for a JoinCode are captured here.
type storageConfig struct {
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// joincodeDB is the persisted map at ~/.vulos/db/joincodes.json.
type joincodeDB map[string]JoinCode

var mu sync.Mutex // protects read-modify-write on joincodes.json

// dbPath returns the absolute path to joincodes.json for the given home directory.
func dbPath(home string) string {
	return filepath.Join(home, "db", "joincodes.json")
}

// storagePath returns the absolute path to storage.json for the given home directory.
func storagePath(home string) string {
	return filepath.Join(home, "db", "storage.json")
}

// loadDB reads joincodes.json from disk. Returns an empty map if the file is absent.
func loadDB(path string) (joincodeDB, error) {
	db := make(joincodeDB)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return db, nil
		}
		return nil, fmt.Errorf("joincode: read db: %w", err)
	}
	if err := json.Unmarshal(data, &db); err != nil {
		return nil, fmt.Errorf("joincode: parse db: %w", err)
	}
	return db, nil
}

// saveDB writes joincodes.json atomically with mode 0600.
func saveDB(path string, db joincodeDB) error {
	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return fmt.Errorf("joincode: marshal db: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("joincode: write db: %w", err)
	}
	return os.Rename(tmp, path)
}

// readStorageConfig reads ~/.vulos/db/storage.json and returns the S3 fields.
// Returns an empty config (not an error) when the file is absent — the caller
// can then decide whether to gate on configured storage.
func readStorageConfig(home string) (storageConfig, error) {
	var cfg storageConfig
	data, err := os.ReadFile(storagePath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil // graceful absence
		}
		return cfg, fmt.Errorf("joincode: read storage.json: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("joincode: parse storage.json: %w", err)
	}
	return cfg, nil
}

// generateShortCode produces a VULOS-XXXX-XXXX-XXXX code using Crockford base32.
// Each segment is 4 characters from the 32-character alphabet → 5 bits each → 20 bits
// per segment, 60 bits total across three segments (~50+ bits of entropy).
func generateShortCode() (string, error) {
	const segLen = 4
	const segments = 3
	alphabetSize := big.NewInt(int64(len(crockfordAlphabet)))

	seg := func() (string, error) {
		buf := make([]byte, segLen)
		for i := range buf {
			n, err := rand.Int(rand.Reader, alphabetSize)
			if err != nil {
				return "", err
			}
			buf[i] = crockfordAlphabet[n.Int64()]
		}
		return string(buf), nil
	}

	parts := make([]string, segments)
	for i := range parts {
		s, err := seg()
		if err != nil {
			return "", fmt.Errorf("joincode: generate random segment: %w", err)
		}
		parts[i] = s
	}
	return fmt.Sprintf("VULOS-%s-%s-%s", parts[0], parts[1], parts[2]), nil
}

// Issue generates a new JoinCode from the storage config at the given home directory.
// It writes the code to joincodes.json and returns the JoinCode together with its
// human-readable short code (VULA-XXXX-XXXX-XXXX).
func Issue(home string, ttl time.Duration) (*JoinCode, string, error) {
	scfg, err := readStorageConfig(home)
	if err != nil {
		return nil, "", err
	}

	shortCode, err := generateShortCode()
	if err != nil {
		return nil, "", err
	}

	jc := JoinCode{
		Bucket:    scfg.Bucket,
		Region:    scfg.Region,
		AccessKey: scfg.AccessKey,
		SecretKey: scfg.SecretKey,
		ExpiresAt: time.Now().Add(ttl),
	}

	path := dbPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, "", fmt.Errorf("joincode: ensure db dir: %w", err)
	}

	mu.Lock()
	defer mu.Unlock()

	db, err := loadDB(path)
	if err != nil {
		return nil, "", err
	}

	// Prune expired codes on each Issue to keep the file lean.
	pruneExpired(db)

	db[shortCode] = jc
	if err := saveDB(path, db); err != nil {
		return nil, "", err
	}

	return &jc, shortCode, nil
}

// Decode looks up a short code in joincodes.json.
// Returns ErrExpired if the code exists but has passed its TTL.
// Returns ErrInvalid if the code is not found.
// On success the entry is deleted (one-time use) before returning.
func Decode(home string, shortCode string) (*JoinCode, error) {
	path := dbPath(home)

	mu.Lock()
	defer mu.Unlock()

	db, err := loadDB(path)
	if err != nil {
		return nil, err
	}

	jc, ok := db[shortCode]
	if !ok {
		return nil, ErrInvalid
	}

	if time.Now().After(jc.ExpiresAt) {
		delete(db, shortCode)
		_ = saveDB(path, db) // best-effort cleanup; ignore error
		return nil, ErrExpired
	}

	// One-time use: delete before returning so concurrent callers get ErrInvalid.
	delete(db, shortCode)
	if err := saveDB(path, db); err != nil {
		return nil, fmt.Errorf("joincode: delete used code: %w", err)
	}

	return &jc, nil
}

// QRPayload encodes a JoinCode as a vulos://join/v1 URL suitable for embedding in a QR code.
func QRPayload(jc *JoinCode) string {
	q := url.Values{}
	q.Set("bucket", jc.Bucket)
	q.Set("region", jc.Region)
	q.Set("access", jc.AccessKey)
	q.Set("secret", jc.SecretKey)
	q.Set("exp", jc.ExpiresAt.UTC().Format(time.RFC3339))
	return "vulos://join/v1?" + q.Encode()
}

// pruneExpired removes all expired entries from db in-place.
func pruneExpired(db joincodeDB) {
	now := time.Now()
	for code, jc := range db {
		if now.After(jc.ExpiresAt) {
			delete(db, code)
		}
	}
}
