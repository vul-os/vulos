package auth

// AT10 – rotating admin token for headless / API-only admin access.
//
// Token file: ~/.vulos/db/admin-token.json (0600, atomic write).
// Shape: {token, issued_at, rotates_at}.
// Token format: "vulos-admin-<32-byte URL-safe base64 random>".
// Rotation window: 30 days.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// AT10TokenPrefix is the mandatory prefix for admin bearer tokens.
	AT10TokenPrefix = "vulos-admin-"

	// AT10RotationDays is the default token lifetime in days.
	AT10RotationDays = 30
)

// at10Record is the on-disk JSON shape for the admin token file.
type at10Record struct {
	Token     string    `json:"token"`
	IssuedAt  time.Time `json:"issued_at"`
	RotatesAt time.Time `json:"rotates_at"`
}

// at10TokenPath returns the path to the admin token file.
func at10TokenPath(home string) string {
	return filepath.Join(home, "db", "admin-token.json")
}

var at10Mu sync.Mutex

// at10Read loads the token record from disk; returns nil if not present or unreadable.
func at10Read(home string) *at10Record {
	data, err := os.ReadFile(at10TokenPath(home))
	if err != nil {
		return nil
	}
	var r at10Record
	if json.Unmarshal(data, &r) != nil {
		return nil
	}
	return &r
}

// at10Write atomically writes a token record to disk with 0600 permissions.
func at10Write(home string, r *at10Record) error {
	p := at10TokenPath(home)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return fmt.Errorf("admin-token: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("admin-token: marshal: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("admin-token: write tmp: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("admin-token: rename: %w", err)
	}
	return nil
}

// at10Generate creates a new random token string (prefixed).
func at10Generate() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("admin-token: rand: %w", err)
	}
	return AT10TokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// IssueAdminToken creates a new admin token and stores it.
// If a token already exists it is replaced (use RotateAdminToken for explicit rotation).
// Returns the full token — caller must store it; subsequent reads only expose a prefix.
func IssueAdminToken(home string) (string, error) {
	at10Mu.Lock()
	defer at10Mu.Unlock()

	tok, err := at10Generate()
	if err != nil {
		return "", err
	}
	now := time.Now()
	r := &at10Record{
		Token:     tok,
		IssuedAt:  now,
		RotatesAt: now.Add(AT10RotationDays * 24 * time.Hour),
	}
	if err := at10Write(home, r); err != nil {
		return "", err
	}
	return tok, nil
}

// ValidateAdminToken checks whether presented matches the stored token and has not expired.
// Returns (valid bool, rotatesAt time.Time, err error).
// err is non-nil only on I/O / parse failures; an expired or mismatched token returns valid=false.
func ValidateAdminToken(home, presented string) (bool, time.Time, error) {
	at10Mu.Lock()
	defer at10Mu.Unlock()

	r := at10Read(home)
	if r == nil {
		return false, time.Time{}, nil
	}
	if time.Now().After(r.RotatesAt) {
		return false, r.RotatesAt, nil
	}
	// Constant-time comparison to prevent timing attacks.
	if !at10Equal(r.Token, presented) {
		return false, r.RotatesAt, nil
	}
	return true, r.RotatesAt, nil
}

// RotateAdminToken invalidates the current token and issues a new one.
// Returns the new token.
func RotateAdminToken(home string) (string, error) {
	return IssueAdminToken(home)
}

// AT10TokenPrefix8 returns the first 8 characters of a token after the prefix,
// safe to expose in API responses.
func AT10TokenPrefix8(tok string) string {
	bare := tok
	if len(bare) > len(AT10TokenPrefix) {
		bare = bare[len(AT10TokenPrefix):]
	}
	if len(bare) > 8 {
		bare = bare[:8]
	}
	return AT10TokenPrefix + bare + "..."
}

// at10Equal does a constant-time string comparison using crypto/subtle.
// subtle.ConstantTimeCompare returns 0 on mismatch (including length mismatch),
// so it already avoids the length-early-exit timing oracle.
func at10Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
