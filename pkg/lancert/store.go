package lancert

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// SQLStore is the pure-Go cpdb-backed Store implementation.
// Supports both SQLite (default self-host) and Postgres (cloud / Neon).
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex
}

// Open applies migrations to db and returns a ready *SQLStore.
//
// db should be obtained from cpdb.Open("lancert") for production, or from
// cpdb.OpenSQLiteDSN(dsn) for tests. Migrations are applied automatically
// and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("lancert: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("lancert: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close releases the underlying database.
func (s *SQLStore) Close() error { return s.db.Close() }

// PingDB satisfies the /readyz storeHealth ping contract.
func (s *SQLStore) PingDB(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// UpsertLANIP implements Store.
func (s *SQLStore) UpsertLANIP(ctx context.Context, boxID, fqdn, lanIP string, now time.Time) error {
	if boxID == "" || fqdn == "" || lanIP == "" {
		return ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	nowU := now.UTC().Unix()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO lancert_boxes
		  (box_id, fqdn, lan_ip, lan_ip_updated_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(box_id) DO UPDATE SET
		  fqdn              = excluded.fqdn,
		  lan_ip            = excluded.lan_ip,
		  lan_ip_updated_at = excluded.lan_ip_updated_at,
		  updated_at        = excluded.updated_at
	`), boxID, fqdn, lanIP, nowU, nowU, nowU)
	if err != nil {
		return fmt.Errorf("lancert: upsert lan ip: %w", err)
	}
	return nil
}

// SaveCert implements Store.
func (s *SQLStore) SaveCert(ctx context.Context, c BoxCert, now time.Time) error {
	if c.BoxID == "" || c.FQDN == "" || c.CertPEM == "" || c.KeyPEM == "" {
		return ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	nowU := now.UTC().Unix()
	nbU := c.NotBefore.UTC().Unix()
	naU := c.NotAfter.UTC().Unix()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO lancert_boxes
		  (box_id, fqdn, cert_pem, key_pem, not_before, not_after,
		   last_issued_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(box_id) DO UPDATE SET
		  fqdn           = excluded.fqdn,
		  cert_pem       = excluded.cert_pem,
		  key_pem        = excluded.key_pem,
		  not_before     = excluded.not_before,
		  not_after      = excluded.not_after,
		  last_issued_at = excluded.last_issued_at,
		  updated_at     = excluded.updated_at
	`), c.BoxID, c.FQDN, c.CertPEM, c.KeyPEM, nbU, naU, nowU, nowU, nowU)
	if err != nil {
		return fmt.Errorf("lancert: save cert: %w", err)
	}
	return nil
}

// Get implements Store.
func (s *SQLStore) Get(ctx context.Context, boxID string) (BoxState, error) {
	if boxID == "" {
		return BoxState{}, ErrInvalidInput
	}
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT box_id, fqdn, lan_ip, lan_ip_updated_at,
		       cert_pem, key_pem, not_before, not_after,
		       last_issued_at, created_at, updated_at
		FROM lancert_boxes WHERE box_id = ?
	`), boxID)
	var (
		bs                                     BoxState
		lanUpd, nb, na, last, created, updated int64
	)
	err := row.Scan(&bs.BoxID, &bs.FQDN, &bs.LANIP, &lanUpd,
		&bs.CertPEM, &bs.KeyPEM, &nb, &na, &last, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return BoxState{}, ErrUnknownBox
	}
	if err != nil {
		return BoxState{}, fmt.Errorf("lancert: get: %w", err)
	}
	bs.LANIPUpdatedAt = unixOrZero(lanUpd)
	bs.NotBefore = unixOrZero(nb)
	bs.NotAfter = unixOrZero(na)
	bs.LastIssuedAt = unixOrZero(last)
	bs.CreatedAt = unixOrZero(created)
	bs.UpdatedAt = unixOrZero(updated)
	return bs, nil
}

// DueForRenewal implements Store. Returns boxes that already have a cert
// (not_after > 0) whose not_after is on or before (now + window).
func (s *SQLStore) DueForRenewal(ctx context.Context, now time.Time, window time.Duration) ([]BoxState, error) {
	threshold := now.Add(window).UTC().Unix()
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT box_id, fqdn, lan_ip, lan_ip_updated_at,
		       cert_pem, key_pem, not_before, not_after,
		       last_issued_at, created_at, updated_at
		FROM lancert_boxes
		WHERE not_after > 0 AND not_after <= ?
	`), threshold)
	if err != nil {
		return nil, fmt.Errorf("lancert: due for renewal: %w", err)
	}
	defer rows.Close()
	var out []BoxState
	for rows.Next() {
		var (
			bs                                     BoxState
			lanUpd, nb, na, last, created, updated int64
		)
		if err := rows.Scan(&bs.BoxID, &bs.FQDN, &bs.LANIP, &lanUpd,
			&bs.CertPEM, &bs.KeyPEM, &nb, &na, &last, &created, &updated); err != nil {
			return nil, fmt.Errorf("lancert: due for renewal scan: %w", err)
		}
		bs.LANIPUpdatedAt = unixOrZero(lanUpd)
		bs.NotBefore = unixOrZero(nb)
		bs.NotAfter = unixOrZero(na)
		bs.LastIssuedAt = unixOrZero(last)
		bs.CreatedAt = unixOrZero(created)
		bs.UpdatedAt = unixOrZero(updated)
		out = append(out, bs)
	}
	return out, nil
}

// AppendLog implements Store.
func (s *SQLStore) AppendLog(ctx context.Context, e LogEntry) error {
	if e.BoxID == "" || e.Event == "" {
		return ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO lancert_issuance_log (box_id, fqdn, event, detail, occurred_at)
		VALUES (?, ?, ?, ?, ?)
	`), e.BoxID, e.FQDN, e.Event, e.Detail, e.OccurredAt.UTC().Unix())
	if err != nil {
		return fmt.Errorf("lancert: append log: %w", err)
	}
	return nil
}

// RecentLog implements Store.
func (s *SQLStore) RecentLog(ctx context.Context, boxID string, limit int) ([]LogEntry, error) {
	if boxID == "" {
		return nil, ErrInvalidInput
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT box_id, fqdn, event, detail, occurred_at
		FROM lancert_issuance_log
		WHERE box_id = ?
		ORDER BY occurred_at DESC, id DESC
		LIMIT ?
	`), boxID, limit)
	if err != nil {
		return nil, fmt.Errorf("lancert: recent log: %w", err)
	}
	defer rows.Close()
	var out []LogEntry
	for rows.Next() {
		var (
			le LogEntry
			ts int64
		)
		if err := rows.Scan(&le.BoxID, &le.FQDN, &le.Event, &le.Detail, &ts); err != nil {
			return nil, fmt.Errorf("lancert: recent log scan: %w", err)
		}
		le.OccurredAt = unixOrZero(ts)
		out = append(out, le)
	}
	return out, nil
}

func unixOrZero(u int64) time.Time {
	if u == 0 {
		return time.Time{}
	}
	return time.Unix(u, 0).UTC()
}

// compile-time assertion
var _ Store = (*SQLStore)(nil)
