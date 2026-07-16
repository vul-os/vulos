package ha

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

// SQLStore is the cpdb-backed Store. WAL for SQLite, Postgres via pgx.
//
// The lease state machine relies on read-modify-write atomicity. We obtain
// that via:
//   - SetMaxOpenConns(1)         — single writer for SQLite; handled by cpdb.
//   - A process-local sync.Mutex — additional safety inside this process.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex
	// now is a swappable clock for tests. Defaults to time.Now().UTC().
	now func() time.Time
}

// Open initialises the HA store using the provided cpdb.DB and runs migrations.
func Open(db *cpdb.DB) (*SQLStore, error) {
	s := &SQLStore{db: db, now: func() time.Time { return time.Now().UTC() }}
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("ha: sub fs: %w", err)
	}
	if err := db.Migrate(sub); err != nil {
		return nil, fmt.Errorf("ha: migrate: %w", err)
	}
	return s, nil
}

// SetClock installs a deterministic clock for tests.
func (s *SQLStore) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now != nil {
		s.now = now
	}
}

func (s *SQLStore) Close() error { return s.db.Close() }

var _ Store = (*SQLStore)(nil)

func haTstr(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func haParseT(v string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, v)
	}
	return t.UTC()
}

// ─────────────────────────────────────────────────────────────────────────────
// Leases
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) TryAcquire(ctx context.Context, key, holderID string, ttl time.Duration) (Lease, error) {
	if key == "" || holderID == "" {
		return Lease{}, fmt.Errorf("ha: lease key and holder id required")
	}
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, fmt.Errorf("ha: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		curHolder     string
		acq, ren, exp string
		gen           int64
	)
	row := tx.QueryRowContext(ctx,
		s.db.Rebind(`SELECT holder_id, acquired_at, renewed_at, expires_at, generation
		   FROM ha_leases WHERE lease_key = ?`), key)
	scanErr := row.Scan(&curHolder, &acq, &ren, &exp, &gen)

	if errors.Is(scanErr, sql.ErrNoRows) {
		l := Lease{
			Key:        key,
			HolderID:   holderID,
			AcquiredAt: now,
			RenewedAt:  now,
			ExpiresAt:  now.Add(ttl),
			Generation: 1,
		}
		if _, err := tx.ExecContext(ctx,
			s.db.Rebind(`INSERT INTO ha_leases (lease_key, holder_id, acquired_at, renewed_at, expires_at, generation)
			 VALUES (?, ?, ?, ?, ?, ?)`),
			l.Key, l.HolderID, haTstr(l.AcquiredAt), haTstr(l.RenewedAt), haTstr(l.ExpiresAt), l.Generation,
		); err != nil {
			return Lease{}, fmt.Errorf("ha: insert lease: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return Lease{}, fmt.Errorf("ha: commit: %w", err)
		}
		return l, nil
	}
	if scanErr != nil {
		return Lease{}, fmt.Errorf("ha: read lease: %w", scanErr)
	}

	expiresAt := haParseT(exp)
	live := now.Before(expiresAt)

	if live && curHolder == holderID {
		// Idempotent same-holder re-acquire; renew the lease.
		ren := now
		newExp := now.Add(ttl)
		if _, err := tx.ExecContext(ctx,
			s.db.Rebind(`UPDATE ha_leases SET renewed_at = ?, expires_at = ? WHERE lease_key = ?`),
			haTstr(ren), haTstr(newExp), key,
		); err != nil {
			return Lease{}, fmt.Errorf("ha: renew on acquire: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return Lease{}, fmt.Errorf("ha: commit: %w", err)
		}
		return Lease{
			Key:        key,
			HolderID:   holderID,
			AcquiredAt: haParseT(acq),
			RenewedAt:  ren,
			ExpiresAt:  newExp,
			Generation: gen,
		}, nil
	}

	if live && curHolder != holderID {
		return Lease{}, ErrLeaseHeld
	}

	// Expired. If a different peer steals, bump generation.
	newGen := gen
	newAcq := haParseT(acq)
	if curHolder != holderID {
		newGen++
		newAcq = now
	}
	if _, err := tx.ExecContext(ctx,
		s.db.Rebind(`UPDATE ha_leases SET holder_id = ?, acquired_at = ?, renewed_at = ?, expires_at = ?, generation = ?
		  WHERE lease_key = ?`),
		holderID, haTstr(newAcq), haTstr(now), haTstr(now.Add(ttl)), newGen, key,
	); err != nil {
		return Lease{}, fmt.Errorf("ha: steal lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, fmt.Errorf("ha: commit: %w", err)
	}
	return Lease{
		Key:        key,
		HolderID:   holderID,
		AcquiredAt: newAcq,
		RenewedAt:  now,
		ExpiresAt:  now.Add(ttl),
		Generation: newGen,
	}, nil
}

func (s *SQLStore) RenewLease(ctx context.Context, key, holderID string, ttl time.Duration) (Lease, error) {
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, fmt.Errorf("ha: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		curHolder     string
		acq, ren, exp string
		gen           int64
	)
	scanErr := tx.QueryRowContext(ctx,
		s.db.Rebind(`SELECT holder_id, acquired_at, renewed_at, expires_at, generation
		   FROM ha_leases WHERE lease_key = ?`), key).
		Scan(&curHolder, &acq, &ren, &exp, &gen)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return Lease{}, ErrNotLeader
	}
	if scanErr != nil {
		return Lease{}, fmt.Errorf("ha: read lease: %w", scanErr)
	}
	if curHolder != holderID {
		return Lease{}, ErrNotLeader
	}
	expiresAt := haParseT(exp)
	if !now.Before(expiresAt) {
		// Already expired — caller must re-Acquire (another peer may have
		// stolen by now).
		return Lease{}, ErrNotLeader
	}
	newExp := now.Add(ttl)
	if _, err := tx.ExecContext(ctx,
		s.db.Rebind(`UPDATE ha_leases SET renewed_at = ?, expires_at = ? WHERE lease_key = ?`),
		haTstr(now), haTstr(newExp), key,
	); err != nil {
		return Lease{}, fmt.Errorf("ha: renew: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, fmt.Errorf("ha: commit: %w", err)
	}
	return Lease{
		Key:        key,
		HolderID:   holderID,
		AcquiredAt: haParseT(acq),
		RenewedAt:  now,
		ExpiresAt:  newExp,
		Generation: gen,
	}, nil
}

func (s *SQLStore) ReleaseLease(ctx context.Context, key, holderID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Mark the lease expired rather than deleting it. This preserves the
	// generation counter so a graceful step-down still counts as a leadership
	// transition for clients tracking generation monotonicity.
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE ha_leases SET expires_at = ?, renewed_at = ?
		   WHERE lease_key = ? AND holder_id = ?`),
		haTstr(now), haTstr(now), key, holderID)
	if err != nil {
		return fmt.Errorf("ha: release: %w", err)
	}
	return nil
}

func (s *SQLStore) GetLease(ctx context.Context, key string) (Lease, error) {
	var (
		holder        string
		acq, ren, exp string
		gen           int64
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT holder_id, acquired_at, renewed_at, expires_at, generation
		   FROM ha_leases WHERE lease_key = ?`), key).
		Scan(&holder, &acq, &ren, &exp, &gen)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, ErrLeaseNotFound
	}
	if err != nil {
		return Lease{}, fmt.Errorf("ha: get lease: %w", err)
	}
	return Lease{
		Key:        key,
		HolderID:   holder,
		AcquiredAt: haParseT(acq),
		RenewedAt:  haParseT(ren),
		ExpiresAt:  haParseT(exp),
		Generation: gen,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Peer heartbeats
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) PutPeerHeartbeat(ctx context.Context, hb PeerHeartbeat) error {
	if hb.PeerID == "" {
		return fmt.Errorf("ha: peer id required")
	}
	if hb.Role == "" {
		hb.Role = RoleUnknown
	}
	if hb.LastHeartbeat.IsZero() {
		hb.LastHeartbeat = s.now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// MAX(a, b) is SQLite-only; Postgres requires GREATEST(a, b).
	// Use a CASE expression which is standard SQL and works on both backends.
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO ha_peers (peer_id, address, last_heartbeat, role, generation_seen)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(peer_id) DO UPDATE SET
		   address         = excluded.address,
		   last_heartbeat  = excluded.last_heartbeat,
		   role            = excluded.role,
		   generation_seen = CASE WHEN ha_peers.generation_seen > excluded.generation_seen
		                          THEN ha_peers.generation_seen
		                          ELSE excluded.generation_seen
		                     END`),
		hb.PeerID, hb.Address, haTstr(hb.LastHeartbeat), hb.Role, hb.GenerationSeen,
	)
	if err != nil {
		return fmt.Errorf("ha: put peer: %w", err)
	}
	return nil
}

func (s *SQLStore) GetPeer(ctx context.Context, peerID string) (PeerHeartbeat, error) {
	var (
		hb PeerHeartbeat
		ts string
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT peer_id, address, last_heartbeat, role, generation_seen
		   FROM ha_peers WHERE peer_id = ?`), peerID).
		Scan(&hb.PeerID, &hb.Address, &ts, &hb.Role, &hb.GenerationSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return PeerHeartbeat{}, ErrPeerNotFound
	}
	if err != nil {
		return PeerHeartbeat{}, fmt.Errorf("ha: get peer: %w", err)
	}
	hb.LastHeartbeat = haParseT(ts)
	return hb, nil
}

func (s *SQLStore) ListPeers(ctx context.Context) ([]PeerHeartbeat, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT peer_id, address, last_heartbeat, role, generation_seen
		   FROM ha_peers ORDER BY peer_id`))
	if err != nil {
		return nil, fmt.Errorf("ha: list peers: %w", err)
	}
	defer rows.Close()
	var out []PeerHeartbeat
	for rows.Next() {
		var hb PeerHeartbeat
		var ts string
		if err := rows.Scan(&hb.PeerID, &hb.Address, &ts, &hb.Role, &hb.GenerationSeen); err != nil {
			return nil, fmt.Errorf("ha: scan peer: %w", err)
		}
		hb.LastHeartbeat = haParseT(ts)
		out = append(out, hb)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Leader markers
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) WriteMarker(ctx context.Context, m LeaderMarker) (LeaderMarker, error) {
	if m.LeaseKey == "" || m.HolderID == "" {
		return LeaderMarker{}, fmt.Errorf("ha: lease_key and holder_id required")
	}
	if m.WrittenAt.IsZero() {
		m.WrittenAt = s.now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.Backend() == cpdb.BackendPostgres {
		var id int64
		err := s.db.QueryRowContext(ctx,
			s.db.Rebind(`INSERT INTO ha_markers (lease_key, generation, holder_id, marker, written_at)
			 VALUES (?, ?, ?, ?, ?) RETURNING id`),
			m.LeaseKey, m.Generation, m.HolderID, m.Marker, haTstr(m.WrittenAt),
		).Scan(&id)
		if err != nil {
			return LeaderMarker{}, fmt.Errorf("ha: write marker: %w", err)
		}
		m.ID = id
		return m, nil
	}
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO ha_markers (lease_key, generation, holder_id, marker, written_at)
		 VALUES (?, ?, ?, ?, ?)`),
		m.LeaseKey, m.Generation, m.HolderID, m.Marker, haTstr(m.WrittenAt),
	)
	if err != nil {
		return LeaderMarker{}, fmt.Errorf("ha: write marker: %w", err)
	}
	id, _ := res.LastInsertId()
	m.ID = id
	return m, nil
}

func (s *SQLStore) ListMarkers(ctx context.Context, leaseKey string) ([]LeaderMarker, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, lease_key, generation, holder_id, marker, written_at
		   FROM ha_markers WHERE lease_key = ? ORDER BY id`), leaseKey)
	if err != nil {
		return nil, fmt.Errorf("ha: list markers: %w", err)
	}
	defer rows.Close()
	var out []LeaderMarker
	for rows.Next() {
		var m LeaderMarker
		var ts string
		if err := rows.Scan(&m.ID, &m.LeaseKey, &m.Generation, &m.HolderID, &m.Marker, &ts); err != nil {
			return nil, fmt.Errorf("ha: scan marker: %w", err)
		}
		m.WrittenAt = haParseT(ts)
		out = append(out, m)
	}
	return out, rows.Err()
}
