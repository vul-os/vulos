// tokens.go — per-account API token generation for the vulos.cloud public REST API.
//
// API tokens are separate from:
//   - Session cookies (user login)
//   - SCIM bearer tokens (org-scoped provisioning)
//
// Tokens are cryptographically random (32 bytes, base64url-encoded).
// Only the SHA-256 hash is stored; the raw token is returned exactly once.
package publicapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ErrAPITokenNotFound is returned when the token hash is not in the store.
var ErrAPITokenNotFound = errors.New("publicapi: token not found")

// ErrAPITokenRevoked is returned when the token is revoked.
var ErrAPITokenRevoked = errors.New("publicapi: token revoked")

// APIToken is the public representation of a token row.
type APIToken struct {
	ID        string
	AccountID string
	Label     string
	CreatedAt time.Time
	Revoked   bool
}

// generateRawToken returns a 32-byte base64url-encoded random token.
func generateRawToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("publicapi: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken returns SHA-256 hex of the raw token.
func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// GenerateToken mints a new API token for accountID.
// Returns the raw token (the only time it is returned).
func (s *Store) GenerateToken(ctx context.Context, accountID, label string) (rawToken string, t APIToken, err error) {
	raw, err := generateRawToken()
	if err != nil {
		return "", APIToken{}, err
	}
	hash := hashToken(raw)
	id, err := newAPIID()
	if err != nil {
		return "", APIToken{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO api_tokens (id, account_id, token_hash, label, created_at, revoked)
		VALUES (?,?,?,?,?,0)`),
		id, accountID, hash, label, now,
	)
	if err != nil {
		return "", APIToken{}, fmt.Errorf("publicapi: insert token: %w", err)
	}
	return raw, APIToken{
		ID:        id,
		AccountID: accountID,
		Label:     label,
		CreatedAt: time.Now().UTC(),
		Revoked:   false,
	}, nil
}

// RevokeToken revokes a token by id.
func (s *Store) RevokeToken(ctx context.Context, tokenID string) error {
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE api_tokens SET revoked=1 WHERE id=?`), tokenID)
	if err != nil {
		return fmt.Errorf("publicapi: revoke token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAPITokenNotFound
	}
	return nil
}

// AuthenticateToken verifies a raw bearer token and returns the accountID.
func (s *Store) AuthenticateToken(ctx context.Context, rawToken string) (accountID string, err error) {
	hash := hashToken(rawToken)
	var acctID string
	var revoked int
	err = s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT account_id, revoked FROM api_tokens WHERE token_hash=?`), hash,
	).Scan(&acctID, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAPITokenNotFound
	}
	if err != nil {
		return "", fmt.Errorf("publicapi: auth token: %w", err)
	}
	if revoked != 0 {
		return "", ErrAPITokenRevoked
	}
	return acctID, nil
}

// ListTokens returns all tokens for an account.
func (s *Store) ListTokens(ctx context.Context, accountID string) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, account_id, label, created_at, revoked
		FROM api_tokens WHERE account_id=? ORDER BY created_at DESC`),
		accountID)
	if err != nil {
		return nil, fmt.Errorf("publicapi: list tokens: %w", err)
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		var cat string
		var rev int
		if err := rows.Scan(&t.ID, &t.AccountID, &t.Label, &cat, &rev); err != nil {
			return nil, err
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339, cat)
		t.Revoked = rev != 0
		out = append(out, t)
	}
	return out, rows.Err()
}
