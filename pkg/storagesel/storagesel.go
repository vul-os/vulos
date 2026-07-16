// Package storagesel implements the per-account storage-backend selector
// (TASK: STORE-BYO-01, CP-STORE-01).
//
// Each account has a StorageBackend record that specifies whether it uses the
// Vulos-managed Tigris bucket (default) or a customer-provided MinIO/S3
// endpoint (BYO). The selector is backed by cpdb (SQLite or Postgres).
//
// Usage:
//
//	db, _ := cpdb.Open("storagesel")
//	sel, err := storagesel.Open(db)
//	backend, err := sel.Get(ctx, accountID)   // defaults to Tigris if absent
//	err = sel.Set(ctx, accountID, b)           // validates + upserts
package storagesel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// Kind identifies the storage backend kind.
type Kind string

const (
	KindTigris Kind = "tigris" // Vulos-managed Tigris (default)
	KindMinIO  Kind = "minio"  // Customer-provided MinIO / S3-compatible
)

// SyncMode is the central/local backup-sync axis (STORE-LOCAL-01), orthogonal
// to Kind. The org-admin Backup tab toggles it; the bundle node reads it to
// pick its effective storage source of truth.
type SyncMode string

const (
	SyncModeCentral SyncMode = "central" // central bucket (Tigris) is source of truth (default)
	SyncModeLocal   SyncMode = "local"   // local MinIO is source of truth + syncs to central rendezvous
)

// Backend holds the storage backend configuration for one account.
type Backend struct {
	AccountID string   `json:"account_id"`
	Kind      Kind     `json:"kind"`       // 'tigris' | 'minio'
	Endpoint  string   `json:"endpoint"`   // required for MinIO; empty for Tigris
	Region    string   `json:"region"`     // default "auto"
	Bucket    string   `json:"bucket"`     // required for MinIO; empty = use Tigris default
	CredRef   string   `json:"cred_ref"`   // opaque env/vault ref for credentials
	SyncMode  SyncMode `json:"sync_mode"`  // 'central' | 'local' (STORE-LOCAL-01)
	UpdatedAt string   `json:"updated_at"` // RFC3339 UTC
}

// Sentinel errors.
var (
	ErrNotFound        = errors.New("storagesel: account not found")
	ErrInvalidKind     = errors.New("storagesel: kind must be 'tigris' or 'minio'")
	ErrInvalidEndpt    = errors.New("storagesel: minio endpoint must be https://…")
	ErrBucketRequired  = errors.New("storagesel: bucket is required for minio")
	ErrInvalidSyncMode = errors.New("storagesel: sync_mode must be 'central' or 'local'")
)

// Selector is the storage-backend selector. Safe for concurrent use.
type Selector struct {
	db *cpdb.DB
}

// Open applies migrations to db and returns a ready Selector.
//
// db should be obtained from cpdb.Open("storagesel") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests.
func Open(db *cpdb.DB) (*Selector, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("storagesel: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("storagesel: migrate: %w", err)
	}
	return &Selector{db: db}, nil
}

// Close releases resources held by the Selector.
func (s *Selector) Close() error { return s.db.Close() }

// Get returns the storage backend for accountID.  If no record exists the
// Tigris default backend is returned (so new accounts default to managed
// storage without needing an explicit row).
func (s *Selector) Get(ctx context.Context, accountID string) (Backend, error) {
	if accountID == "" {
		return Backend{}, fmt.Errorf("storagesel: account_id required")
	}
	row := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT kind, endpoint, region, bucket, cred_ref, sync_mode, updated_at
	             FROM account_storage WHERE account_id = ?`), accountID)
	var b Backend
	b.AccountID = accountID
	var syncMode string
	err := row.Scan(&b.Kind, &b.Endpoint, &b.Region, &b.Bucket, &b.CredRef, &syncMode, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// Default: Tigris-managed, central sync, no explicit config needed.
		return Backend{
			AccountID: accountID,
			Kind:      KindTigris,
			Region:    "auto",
			SyncMode:  SyncModeCentral,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		}, nil
	}
	if err != nil {
		return Backend{}, fmt.Errorf("storagesel: get: %w", err)
	}
	b.SyncMode = normaliseSyncMode(SyncMode(syncMode))
	return b, nil
}

// GetSyncMode returns the central/local backup-sync mode for accountID. Absent
// rows + unknown values default to central (historical behaviour).
func (s *Selector) GetSyncMode(ctx context.Context, accountID string) (SyncMode, error) {
	b, err := s.Get(ctx, accountID)
	if err != nil {
		return SyncModeCentral, err
	}
	return b.SyncMode, nil
}

// SetSyncMode upserts ONLY the central/local sync mode for accountID, leaving
// the BYO endpoint config (kind/endpoint/bucket/…) untouched. A row is created
// with the Tigris default backend if none exists yet.
func (s *Selector) SetSyncMode(ctx context.Context, accountID string, mode SyncMode) error {
	if accountID == "" {
		return fmt.Errorf("storagesel: account_id required")
	}
	switch mode {
	case SyncModeCentral, SyncModeLocal:
	default:
		return ErrInvalidSyncMode
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO account_storage (account_id, kind, endpoint, region, bucket, cred_ref, sync_mode, updated_at)
	            VALUES (?, 'tigris', '', 'auto', '', '', ?, ?)
	            ON CONFLICT(account_id) DO UPDATE SET
	              sync_mode=excluded.sync_mode,
	              updated_at=excluded.updated_at`),
		accountID, string(mode), now)
	if err != nil {
		return fmt.Errorf("storagesel: set sync mode: %w", err)
	}
	return nil
}

// normaliseSyncMode coerces an unknown/empty mode to the central default.
func normaliseSyncMode(m SyncMode) SyncMode {
	if m == SyncModeLocal {
		return SyncModeLocal
	}
	return SyncModeCentral
}

// Set validates and upserts the storage backend for accountID.
//
// Validation rules:
//   - kind must be "tigris" or "minio"
//   - for minio: endpoint must be non-empty and start with "https://"
//   - for minio: bucket must be non-empty
func (s *Selector) Set(ctx context.Context, accountID string, b Backend) error {
	if accountID == "" {
		return fmt.Errorf("storagesel: account_id required")
	}
	b.AccountID = accountID

	// Validate kind.
	switch b.Kind {
	case KindTigris, KindMinIO:
	default:
		return ErrInvalidKind
	}

	// MinIO-specific validation.
	if b.Kind == KindMinIO {
		if !strings.HasPrefix(b.Endpoint, "https://") {
			return ErrInvalidEndpt
		}
		if b.Bucket == "" {
			return ErrBucketRequired
		}
	}

	if b.Region == "" {
		b.Region = "auto"
	}
	b.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO account_storage (account_id, kind, endpoint, region, bucket, cred_ref, updated_at)
	            VALUES (?, ?, ?, ?, ?, ?, ?)
	            ON CONFLICT(account_id) DO UPDATE SET
	              kind=excluded.kind,
	              endpoint=excluded.endpoint,
	              region=excluded.region,
	              bucket=excluded.bucket,
	              cred_ref=excluded.cred_ref,
	              updated_at=excluded.updated_at`),
		b.AccountID, string(b.Kind), b.Endpoint, b.Region, b.Bucket, b.CredRef, b.UpdatedAt)
	if err != nil {
		return fmt.Errorf("storagesel: set: %w", err)
	}
	return nil
}

// Delete removes the account's storage backend record (if present).
// After deletion, Get will again return the Tigris default.
func (s *Selector) Delete(ctx context.Context, accountID string) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM account_storage WHERE account_id = ?`), accountID)
	if err != nil {
		return fmt.Errorf("storagesel: delete: %w", err)
	}
	return nil
}

// Ping verifies the underlying database connection is alive.
// Used by the /readyz health probe.
func (s *Selector) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}
