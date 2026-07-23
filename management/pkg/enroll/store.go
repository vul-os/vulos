package enroll

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// SQLStore is the production database-backed implementation of Store.
// Concurrency-safe (WAL mode + single writer connection).
// Behaviour is semantically identical to MemStore.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex // serialises all writes
}

// OpenSQLStore applies embedded migrations to db and returns a ready-to-use *SQLStore.
// db should be obtained from cpdb.Open("enroll") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests.
func OpenSQLStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("enroll: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("enroll: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// PubKeyForUserCode retrieves the raw ed25519 device public key for the grant
// identified by userCode. This is an optional extension beyond the Store
// interface, used by the approve handler to sign the device cert.
// Returns ErrUnknownUserCode if not found.
func (s *SQLStore) PubKeyForUserCode(userCode string) ([]byte, error) {
	var pubKey []byte
	err := s.db.QueryRow(
		s.db.Rebind(`SELECT device_pubkey FROM enroll_grants WHERE user_code = ?`), userCode,
	).Scan(&pubKey)
	if err == sql.ErrNoRows {
		return nil, ErrUnknownUserCode
	}
	if err != nil {
		return nil, fmt.Errorf("enroll: get pubkey for user code: %w", err)
	}
	return pubKey, nil
}

// compile-time assertion.
var _ Store = (*SQLStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// StartGrant
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) StartGrant(ctx context.Context, g EnrollGrant, pubKey []byte) error {
	if len(pubKey) != ed25519.PublicKeySize {
		return ErrInvalidPubKey
	}
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO enroll_grants
		   (device_code, user_code, state, device_pubkey, expires_at)
		 VALUES (?, ?, 'pending', ?, ?)`),
		g.DeviceCode, g.UserCode, pubKey,
		g.ExpiresAt.UTC().Format(time.RFC3339Nano),
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("enroll: start grant: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// PollGrant
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) PollGrant(ctx context.Context, deviceCode string) (EnrollResult, error) {
	var (
		state      string
		expiresAt  string
		accountID  sql.NullString
		ulid       sql.NullString
		deviceCert []byte
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT state, expires_at, account_id, ulid, device_cert
		   FROM enroll_grants WHERE device_code = ?`), deviceCode,
	).Scan(&state, &expiresAt, &accountID, &ulid, &deviceCert)
	if err == sql.ErrNoRows {
		return EnrollResult{}, ErrUnknownDeviceCode
	}
	if err != nil {
		return EnrollResult{}, fmt.Errorf("enroll: poll grant query: %w", err)
	}

	exp := parseEnrollTime(expiresAt)
	if time.Now().UTC().After(exp) {
		return EnrollResult{}, ErrExpired
	}

	switch grantState(state) {
	case statePending:
		return EnrollResult{}, ErrAuthPending
	case stateDenied:
		return EnrollResult{}, ErrAccessDenied
	case stateConsumed:
		return EnrollResult{}, ErrAlreadyConsumed
	case stateApproved:
		// Transition to consumed.
		s.mu.Lock()
		_, updateErr := s.db.ExecContext(ctx,
			s.db.Rebind(`UPDATE enroll_grants SET state = 'consumed' WHERE device_code = ?`), deviceCode,
		)
		s.mu.Unlock()
		if updateErr != nil {
			return EnrollResult{}, fmt.Errorf("enroll: consume grant: %w", updateErr)
		}
		return EnrollResult{
			ULID:       ulid.String,
			DeviceCert: deviceCert,
			AccountID:  accountID.String,
		}, nil
	}
	return EnrollResult{}, ErrAuthPending
}

// ─────────────────────────────────────────────────────────────────────────────
// ApproveGrant
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) ApproveGrant(ctx context.Context, userCode, accountID, ulid string, deviceCert []byte) error {
	var (
		state     string
		expiresAt string
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT state, expires_at FROM enroll_grants WHERE user_code = ?`), userCode,
	).Scan(&state, &expiresAt)
	if err == sql.ErrNoRows {
		return ErrUnknownUserCode
	}
	if err != nil {
		return fmt.Errorf("enroll: approve grant query: %w", err)
	}

	exp := parseEnrollTime(expiresAt)
	if time.Now().UTC().After(exp) {
		return ErrExpired
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE enroll_grants
		    SET state = 'approved', account_id = ?, ulid = ?, device_cert = ?, approved_at = ?
		  WHERE user_code = ?`),
		accountID, ulid, deviceCert, now, userCode,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("enroll: approve grant update: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// DenyGrant
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) DenyGrant(ctx context.Context, userCode string) error {
	var (
		state     string
		expiresAt string
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT state, expires_at FROM enroll_grants WHERE user_code = ?`), userCode,
	).Scan(&state, &expiresAt)
	if err == sql.ErrNoRows {
		return ErrUnknownUserCode
	}
	if err != nil {
		return fmt.Errorf("enroll: deny grant query: %w", err)
	}

	exp := parseEnrollTime(expiresAt)
	if time.Now().UTC().After(exp) {
		return ErrExpired
	}

	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE enroll_grants SET state = 'denied' WHERE user_code = ?`), userCode,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("enroll: deny grant update: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GetByUserCode
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) GetByUserCode(ctx context.Context, userCode string) (EnrollGrant, error) {
	var (
		deviceCode string
		expiresAt  string
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT device_code, expires_at FROM enroll_grants WHERE user_code = ?`), userCode,
	).Scan(&deviceCode, &expiresAt)
	if err == sql.ErrNoRows {
		return EnrollGrant{}, ErrUnknownUserCode
	}
	if err != nil {
		return EnrollGrant{}, fmt.Errorf("enroll: get by user code: %w", err)
	}

	return EnrollGrant{
		DeviceCode: deviceCode,
		UserCode:   userCode,
		ExpiresAt:  parseEnrollTime(expiresAt),
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// ─────────────────────────────────────────────────────────────────────────────
// Exported helpers
// ─────────────────────────────────────────────────────────────────────────────

// GenDeviceCode exports the unexported genDeviceCode for use outside the
// enroll package (e.g. HTTP handlers in cmd/server). The contract file keeps
// genDeviceCode unexported per its freeze policy; this thin wrapper is the
// approved extension point.
func GenDeviceCode() string { return genDeviceCode() }

// PubKeyForUserCode is an optional extension on MemStore (mirrors SQLStore)
// to expose the stored device public key to the approve handler.
func (m *MemStore) PubKeyForUserCode(userCode string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byUC[userCode]
	if !ok {
		return nil, ErrUnknownUserCode
	}
	pk := make([]byte, len(r.DevicePubKey))
	copy(pk, r.DevicePubKey)
	return pk, nil
}

func parseEnrollTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}
