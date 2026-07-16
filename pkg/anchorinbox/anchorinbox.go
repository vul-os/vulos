// Package anchorinbox manages the always-on central anchor inbox for every
// Vulos account (ANCHOR-01 / CP-ANCHOR-01).
//
// Every account gets a small (~1 GiB) @vulos.org mailbox hosted on OUR
// Tigris bucket — regardless of whether their primary mail is hosted or
// self-hosted/BYO.  This is the durability + never-locked-out guarantee:
// mail to <handle>@vulos.org always lands somewhere the user can reach.
//
// Storage: cpdb-backed — SQLite (modernc.org/sqlite, pure-Go, no CGO) by
// default; Postgres via pgx/v5/stdlib when DATABASE_URL / VULOS_DATABASE_URL
// is set.
//
// Provisioning is called once at account creation.  Re-provisioning an
// existing account is a no-op (idempotent).
package anchorinbox

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

// ErrAlreadyProvisioned is returned by Provision when the account already has
// an anchor inbox.  Callers may treat this as a successful no-op.
var ErrAlreadyProvisioned = errors.New("anchorinbox: already provisioned")

// ErrNotFound is returned by Get when no anchor inbox exists for the account.
var ErrNotFound = errors.New("anchorinbox: not found")

// DefaultQuotaMB is the default per-inbox quota in mebibytes (1 GiB).
const DefaultQuotaMB = 1024

// VulosDomain is the domain used for anchor inbox addresses.
const VulosDomain = "vulos.org"

// AnchorInbox is the provisioned anchor inbox record for an account.
type AnchorInbox struct {
	AccountID     string `json:"account_id"`
	Address       string `json:"address"`        // <handle>@vulos.org
	ProvisionedAt string `json:"provisioned_at"` // RFC3339
	QuotaMB       int    `json:"quota_mb"`
}

// Store manages the anchor_inboxes table, backed by cpdb (SQLite or Postgres).
type Store struct {
	db *cpdb.DB
}

// Open applies migrations to db and returns a ready Store. The caller must
// call Close.
//
// db should be obtained from cpdb.Open("anchorinbox") for production, or from
// cpdb.OpenSQLiteDSN(dsn) for tests. Migrations are applied automatically
// and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("anchorinbox: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("anchorinbox: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Ping pings the database (used by /readyz).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Provision creates the anchor inbox for accountID with address
// <handle>@vulos.org.  handle must be a non-empty, lowercase alphanumeric+
// special-char string (validated server-side at call time).
//
// Returns ErrAlreadyProvisioned when the account already has an inbox
// (idempotent: callers may safely ignore this error).
//
// The inbox is ALWAYS provisioned on the central Tigris bucket — never on a
// customer's MinIO.  This method only writes the registry row; the caller
// (account-creation hook) is responsible for creating the bucket key prefix
// on Tigris if needed.
func (s *Store) Provision(ctx context.Context, accountID, handle string) (AnchorInbox, error) {
	handle = strings.ToLower(strings.TrimSpace(handle))
	if handle == "" {
		return AnchorInbox{}, fmt.Errorf("anchorinbox: handle must not be empty")
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return AnchorInbox{}, fmt.Errorf("anchorinbox: accountID must not be empty")
	}

	address := handle + "@" + VulosDomain
	now := time.Now().UTC().Format(time.RFC3339)

	// Check for idempotent re-provision (same account_id) before inserting.
	// We do this explicitly so the error discrimination is unambiguous regardless
	// of which UNIQUE constraint the backend picks when both could fire.
	var existingAccountID string
	checkErr := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT account_id FROM anchor_inboxes WHERE account_id = ?`), accountID,
	).Scan(&existingAccountID)
	if checkErr == nil {
		// Row already exists for this account — idempotent re-provision.
		return AnchorInbox{}, ErrAlreadyProvisioned
	}
	if !errors.Is(checkErr, sql.ErrNoRows) {
		return AnchorInbox{}, fmt.Errorf("anchorinbox: provision pre-check: %w", checkErr)
	}

	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO anchor_inboxes (account_id, address, provisioned_at, quota_mb)
		VALUES (?, ?, ?, ?)`),
		accountID, address, now, DefaultQuotaMB,
	)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "UNIQUE constraint failed: anchor_inboxes.address") {
			// Address taken by a different account — return a descriptive error.
			return AnchorInbox{}, fmt.Errorf("anchorinbox: address %q already in use by another account", address)
		}
		return AnchorInbox{}, fmt.Errorf("anchorinbox: provision insert: %w", err)
	}

	return AnchorInbox{
		AccountID:     accountID,
		Address:       address,
		ProvisionedAt: now,
		QuotaMB:       DefaultQuotaMB,
	}, nil
}

// Get returns the anchor inbox for accountID.
// Returns ErrNotFound when no inbox has been provisioned.
func (s *Store) Get(ctx context.Context, accountID string) (AnchorInbox, error) {
	var inbox AnchorInbox
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT account_id, address, provisioned_at, quota_mb
		  FROM anchor_inboxes WHERE account_id = ?`), accountID,
	).Scan(&inbox.AccountID, &inbox.Address, &inbox.ProvisionedAt, &inbox.QuotaMB)
	if errors.Is(err, sql.ErrNoRows) {
		return AnchorInbox{}, ErrNotFound
	}
	if err != nil {
		return AnchorInbox{}, fmt.Errorf("anchorinbox: get: %w", err)
	}
	return inbox, nil
}

// GetByAddress returns the anchor inbox for the given full address
// (e.g. "alice@vulos.org").  Returns ErrNotFound when not found.
func (s *Store) GetByAddress(ctx context.Context, address string) (AnchorInbox, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	var inbox AnchorInbox
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT account_id, address, provisioned_at, quota_mb
		  FROM anchor_inboxes WHERE address = ?`), address,
	).Scan(&inbox.AccountID, &inbox.Address, &inbox.ProvisionedAt, &inbox.QuotaMB)
	if errors.Is(err, sql.ErrNoRows) {
		return AnchorInbox{}, ErrNotFound
	}
	if err != nil {
		return AnchorInbox{}, fmt.Errorf("anchorinbox: get by address: %w", err)
	}
	return inbox, nil
}
