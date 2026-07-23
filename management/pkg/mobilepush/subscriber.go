package mobilepush

import (
	"context"
	"fmt"
	"io/fs"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// Subscriber manages the device-token registry.
type Subscriber struct {
	db *cpdb.DB
}

// OpenSubscriber runs migrations against db and returns a ready *Subscriber.
func OpenSubscriber(db *cpdb.DB) (*Subscriber, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("mobilepush: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("mobilepush: migrate: %w", err)
	}
	return &Subscriber{db: db}, nil
}

// Close releases the underlying database connection.
func (s *Subscriber) Close() error { return s.db.Close() }

// Register persists a device token for the given account.  If the (accountID,
// token, platform) triple already exists the updated_at timestamp is refreshed
// and no duplicate row is inserted.
func (s *Subscriber) Register(ctx context.Context, accountID, token string, platform Platform) (DeviceToken, error) {
	if accountID == "" || token == "" {
		return DeviceToken{}, ErrInvalidToken
	}
	if platform != PlatformAPNs && platform != PlatformFCM {
		return DeviceToken{}, ErrInvalidPlatform
	}

	now := time.Now().UTC()

	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO device_tokens (account_id, token, platform, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(account_id, token, platform) DO UPDATE
		  SET updated_at = excluded.updated_at
	`), accountID, token, string(platform), now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return DeviceToken{}, fmt.Errorf("mobilepush: upsert token: %w", err)
	}

	// Fetch the persisted row so we can return its ID.
	var dt DeviceToken
	var createdStr, updatedStr string
	err = s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT id, account_id, token, platform, created_at, updated_at
		FROM device_tokens
		WHERE account_id = ? AND token = ? AND platform = ?
	`), accountID, token, string(platform)).Scan(
		&dt.ID, &dt.AccountID, &dt.Token, &dt.Platform,
		&createdStr, &updatedStr,
	)
	if err != nil {
		return DeviceToken{}, fmt.Errorf("mobilepush: fetch token after upsert: %w", err)
	}
	dt.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	dt.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	return dt, nil
}

// ListTokens returns all device tokens registered for the given account.
func (s *Subscriber) ListTokens(ctx context.Context, accountID string) ([]DeviceToken, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT id, account_id, token, platform, created_at, updated_at
		FROM device_tokens
		WHERE account_id = ?
		ORDER BY id ASC
	`), accountID)
	if err != nil {
		return nil, fmt.Errorf("mobilepush: list tokens: %w", err)
	}
	defer rows.Close()

	var out []DeviceToken
	for rows.Next() {
		var dt DeviceToken
		var createdStr, updatedStr string
		if err := rows.Scan(
			&dt.ID, &dt.AccountID, &dt.Token, &dt.Platform,
			&createdStr, &updatedStr,
		); err != nil {
			return nil, fmt.Errorf("mobilepush: scan token: %w", err)
		}
		dt.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		dt.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
		out = append(out, dt)
	}
	return out, rows.Err()
}

// DeleteToken removes a specific device token.
func (s *Subscriber) DeleteToken(ctx context.Context, accountID, token string, platform Platform) error {
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		DELETE FROM device_tokens
		WHERE account_id = ? AND token = ? AND platform = ?
	`), accountID, token, string(platform))
	return err
}
