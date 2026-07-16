package servingpool

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// SQLStore is the cpdb-backed Store implementation (SQLite or Postgres).
// Identical semantics to MemStore.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex
}

// OpenSQLStore applies migrations to db and returns a ready *SQLStore.
//
// db should be obtained from cpdb.Open("servingpool") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func OpenSQLStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("servingpool: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("servingpool: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) Close() error { return s.db.Close() }

var _ Store = (*SQLStore)(nil)

func nowUTC() time.Time { return time.Now().UTC() }
func tstr(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
func parseT(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t.UTC()
}
func parseTPtr(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseT(s.String)
	return &t
}

// --------------------------------------------------------------------------
// Nodes
// --------------------------------------------------------------------------

func (s *SQLStore) RegisterNode(ctx context.Context, n Node) error {
	if n.ID == "" {
		return fmt.Errorf("servingpool: node id required")
	}
	if n.Capacity <= 0 {
		n.Capacity = DefaultNodeCapacity
	}
	if n.Health == "" {
		n.Health = HealthUnknown
	}
	now := tstr(nowUTC())
	if n.RegisteredAt.IsZero() {
		n.RegisteredAt = nowUTC()
	}
	var lhb interface{}
	if n.LastHeartbeat != nil {
		lhb = tstr(*n.LastHeartbeat)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO pool_nodes
		   (node_id, region, capacity, health, last_heartbeat, load_score, registered_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(node_id) DO UPDATE SET
		   region         = excluded.region,
		   capacity       = excluded.capacity,
		   health         = excluded.health,
		   last_heartbeat = COALESCE(excluded.last_heartbeat, pool_nodes.last_heartbeat),
		   updated_at     = excluded.updated_at`),
		n.ID, n.Region, n.Capacity, n.Health, lhb, n.LoadScore,
		tstr(n.RegisteredAt), now,
	)
	if err != nil {
		return fmt.Errorf("servingpool: register node: %w", err)
	}
	return nil
}

func (s *SQLStore) RemoveNode(ctx context.Context, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM pool_nodes WHERE node_id = ?`), nodeID)
	if err != nil {
		return fmt.Errorf("servingpool: remove node: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNodeNotFound
	}
	return nil
}

func (s *SQLStore) GetNode(ctx context.Context, nodeID string) (Node, error) {
	var n Node
	var lhb sql.NullString
	var reg, upd string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT node_id, region, capacity, health, last_heartbeat, load_score,
		        registered_at, updated_at
		   FROM pool_nodes WHERE node_id = ?`), nodeID,
	).Scan(&n.ID, &n.Region, &n.Capacity, &n.Health, &lhb, &n.LoadScore, &reg, &upd)
	if err == sql.ErrNoRows {
		return Node{}, ErrNodeNotFound
	}
	if err != nil {
		return Node{}, fmt.Errorf("servingpool: get node: %w", err)
	}
	n.LastHeartbeat = parseTPtr(lhb)
	n.RegisteredAt = parseT(reg)
	n.UpdatedAt = parseT(upd)
	return n, nil
}

func (s *SQLStore) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT node_id, region, capacity, health, last_heartbeat, load_score,
		        registered_at, updated_at
		   FROM pool_nodes ORDER BY node_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("servingpool: list nodes: %w", err)
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		var lhb sql.NullString
		var reg, upd string
		if err := rows.Scan(&n.ID, &n.Region, &n.Capacity, &n.Health, &lhb,
			&n.LoadScore, &reg, &upd); err != nil {
			return nil, fmt.Errorf("servingpool: scan node: %w", err)
		}
		n.LastHeartbeat = parseTPtr(lhb)
		n.RegisteredAt = parseT(reg)
		n.UpdatedAt = parseT(upd)
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *SQLStore) SetNodeHealth(ctx context.Context, nodeID, health string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE pool_nodes SET health = ?, updated_at = ? WHERE node_id = ?`),
		health, tstr(nowUTC()), nodeID,
	)
	if err != nil {
		return fmt.Errorf("servingpool: set health: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNodeNotFound
	}
	return nil
}

func (s *SQLStore) SetNodeLoad(ctx context.Context, nodeID string, load float64) error {
	if load < 0 {
		load = 0
	}
	if load > 1 {
		load = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE pool_nodes SET load_score = ?, updated_at = ? WHERE node_id = ?`),
		load, tstr(nowUTC()), nodeID,
	)
	if err != nil {
		return fmt.Errorf("servingpool: set load: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNodeNotFound
	}
	return nil
}

func (s *SQLStore) Heartbeat(ctx context.Context, nodeID string, when time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE pool_nodes SET last_heartbeat = ?, updated_at = ? WHERE node_id = ?`),
		tstr(when), tstr(nowUTC()), nodeID,
	)
	if err != nil {
		return fmt.Errorf("servingpool: heartbeat: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNodeNotFound
	}
	return nil
}

// --------------------------------------------------------------------------
// Leases
// --------------------------------------------------------------------------

func (s *SQLStore) UpsertLease(ctx context.Context, l Lease) error {
	if l.TenantID == "" || l.NodeID == "" {
		return fmt.Errorf("servingpool: tenant_id and node_id required")
	}
	if l.AcquiredAt.IsZero() {
		l.AcquiredAt = nowUTC()
	}
	if l.RenewedAt.IsZero() {
		l.RenewedAt = nowUTC()
	}
	if l.ExpiresAt.IsZero() {
		l.ExpiresAt = nowUTC().Add(DefaultLeaseTTL)
	}
	if l.Generation <= 0 {
		l.Generation = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO pool_leases
		   (tenant_id, node_id, acquired_at, renewed_at, expires_at, generation)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(tenant_id) DO UPDATE SET
		   node_id     = excluded.node_id,
		   renewed_at  = excluded.renewed_at,
		   expires_at  = excluded.expires_at,
		   generation  = CASE
		     WHEN excluded.node_id <> pool_leases.node_id THEN pool_leases.generation + 1
		     ELSE pool_leases.generation
		   END`),
		l.TenantID, l.NodeID, tstr(l.AcquiredAt), tstr(l.RenewedAt),
		tstr(l.ExpiresAt), l.Generation,
	)
	if err != nil {
		return fmt.Errorf("servingpool: upsert lease: %w", err)
	}
	return nil
}

func (s *SQLStore) GetLease(ctx context.Context, tenantID string) (Lease, error) {
	var l Lease
	var acq, ren, exp string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT tenant_id, node_id, acquired_at, renewed_at, expires_at, generation
		   FROM pool_leases WHERE tenant_id = ?`), tenantID,
	).Scan(&l.TenantID, &l.NodeID, &acq, &ren, &exp, &l.Generation)
	if err == sql.ErrNoRows {
		return Lease{}, ErrLeaseNotFound
	}
	if err != nil {
		return Lease{}, fmt.Errorf("servingpool: get lease: %w", err)
	}
	l.AcquiredAt = parseT(acq)
	l.RenewedAt = parseT(ren)
	l.ExpiresAt = parseT(exp)
	return l, nil
}

func (s *SQLStore) ListLeasesByNode(ctx context.Context, nodeID string) ([]Lease, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT tenant_id, node_id, acquired_at, renewed_at, expires_at, generation
		   FROM pool_leases WHERE node_id = ? ORDER BY tenant_id`), nodeID,
	)
	if err != nil {
		return nil, fmt.Errorf("servingpool: list leases by node: %w", err)
	}
	defer rows.Close()
	var out []Lease
	for rows.Next() {
		var l Lease
		var acq, ren, exp string
		if err := rows.Scan(&l.TenantID, &l.NodeID, &acq, &ren, &exp, &l.Generation); err != nil {
			return nil, fmt.Errorf("servingpool: scan lease: %w", err)
		}
		l.AcquiredAt = parseT(acq)
		l.RenewedAt = parseT(ren)
		l.ExpiresAt = parseT(exp)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *SQLStore) DeleteLease(ctx context.Context, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM pool_leases WHERE tenant_id = ?`), tenantID)
	if err != nil {
		return fmt.Errorf("servingpool: delete lease: %w", err)
	}
	return nil
}

func (s *SQLStore) CountLeases(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pool_leases`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("servingpool: count leases: %w", err)
	}
	return n, nil
}

// --------------------------------------------------------------------------
// Autoscale signals
// --------------------------------------------------------------------------

func (s *SQLStore) EmitSignal(ctx context.Context, sig AutoscaleSignal) error {
	if sig.EmittedAt.IsZero() {
		sig.EmittedAt = nowUTC()
	}
	if sig.Scope == "" {
		sig.Scope = "global"
	}
	if sig.Action == "" {
		sig.Action = ActionHold
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO pool_autoscale_signals (emitted_at, scope, action, reason, load_score)
		 VALUES (?, ?, ?, ?, ?)`),
		tstr(sig.EmittedAt), sig.Scope, sig.Action, sig.Reason, sig.LoadScore,
	)
	if err != nil {
		return fmt.Errorf("servingpool: emit signal: %w", err)
	}
	return nil
}

// --------------------------------------------------------------------------
// Tenant pins (CLOUD-DEDICATED-01)
// --------------------------------------------------------------------------

func (s *SQLStore) UpsertTenantPin(ctx context.Context, p TenantPin) error {
	if p.TenantID == "" || p.NodeID == "" {
		return fmt.Errorf("servingpool: tenant_id and node_id required")
	}
	now := nowUTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO tenant_pins (tenant_id, node_id, reason, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(tenant_id) DO UPDATE SET
		   node_id    = excluded.node_id,
		   reason     = excluded.reason,
		   updated_at = excluded.updated_at`),
		p.TenantID, p.NodeID, p.Reason, tstr(p.CreatedAt), tstr(p.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("servingpool: upsert tenant pin: %w", err)
	}
	return nil
}

func (s *SQLStore) GetTenantPin(ctx context.Context, tenantID string) (TenantPin, error) {
	var p TenantPin
	var created, updated string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT tenant_id, node_id, reason, created_at, updated_at
		   FROM tenant_pins WHERE tenant_id = ?`), tenantID,
	).Scan(&p.TenantID, &p.NodeID, &p.Reason, &created, &updated)
	if err == sql.ErrNoRows {
		return TenantPin{}, ErrPinNotFound
	}
	if err != nil {
		return TenantPin{}, fmt.Errorf("servingpool: get tenant pin: %w", err)
	}
	p.CreatedAt = parseT(created)
	p.UpdatedAt = parseT(updated)
	return p, nil
}

func (s *SQLStore) DeleteTenantPin(ctx context.Context, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM tenant_pins WHERE tenant_id = ?`), tenantID)
	if err != nil {
		return fmt.Errorf("servingpool: delete tenant pin: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrPinNotFound
	}
	return nil
}

func (s *SQLStore) ListTenantPins(ctx context.Context) ([]TenantPin, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tenant_id, node_id, reason, created_at, updated_at
		   FROM tenant_pins ORDER BY tenant_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("servingpool: list tenant pins: %w", err)
	}
	defer rows.Close()
	var out []TenantPin
	for rows.Next() {
		var p TenantPin
		var created, updated string
		if err := rows.Scan(&p.TenantID, &p.NodeID, &p.Reason, &created, &updated); err != nil {
			return nil, fmt.Errorf("servingpool: scan tenant pin: %w", err)
		}
		p.CreatedAt = parseT(created)
		p.UpdatedAt = parseT(updated)
		out = append(out, p)
	}
	return out, rows.Err()
}

// --------------------------------------------------------------------------
// VDI slices (CLOUD-DEDICATED-01)
// --------------------------------------------------------------------------

func (s *SQLStore) UpsertVDISlice(ctx context.Context, v VDISlice) error {
	if v.UserID == "" || v.NodeID == "" {
		return fmt.Errorf("servingpool: user_id and node_id required")
	}
	if v.HostKind != VDIHostPool && v.HostKind != VDIHostDedicated {
		return ErrInvalidHostKind
	}
	now := nowUTC()
	if v.ScheduledAt.IsZero() {
		v.ScheduledAt = now
	}
	v.UpdatedAt = now
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO vdi_slices
		   (user_id, account_id, tenant_id, host_kind, node_id, scheduled_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   account_id   = excluded.account_id,
		   tenant_id    = excluded.tenant_id,
		   host_kind    = excluded.host_kind,
		   node_id      = excluded.node_id,
		   updated_at   = excluded.updated_at`),
		v.UserID, v.AccountID, v.TenantID, v.HostKind, v.NodeID,
		tstr(v.ScheduledAt), tstr(v.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("servingpool: upsert vdi slice: %w", err)
	}
	return nil
}

func (s *SQLStore) GetVDISlice(ctx context.Context, userID string) (VDISlice, error) {
	var v VDISlice
	var sched, upd string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT user_id, account_id, tenant_id, host_kind, node_id, scheduled_at, updated_at
		   FROM vdi_slices WHERE user_id = ?`), userID,
	).Scan(&v.UserID, &v.AccountID, &v.TenantID, &v.HostKind, &v.NodeID, &sched, &upd)
	if err == sql.ErrNoRows {
		return VDISlice{}, ErrVDINotFound
	}
	if err != nil {
		return VDISlice{}, fmt.Errorf("servingpool: get vdi slice: %w", err)
	}
	v.ScheduledAt = parseT(sched)
	v.UpdatedAt = parseT(upd)
	return v, nil
}

func (s *SQLStore) DeleteVDISlice(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM vdi_slices WHERE user_id = ?`), userID)
	if err != nil {
		return fmt.Errorf("servingpool: delete vdi slice: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrVDINotFound
	}
	return nil
}

func (s *SQLStore) ListVDISlices(ctx context.Context) ([]VDISlice, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id, account_id, tenant_id, host_kind, node_id, scheduled_at, updated_at
		   FROM vdi_slices ORDER BY user_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("servingpool: list vdi slices: %w", err)
	}
	defer rows.Close()
	var out []VDISlice
	for rows.Next() {
		var v VDISlice
		var sched, upd string
		if err := rows.Scan(&v.UserID, &v.AccountID, &v.TenantID, &v.HostKind,
			&v.NodeID, &sched, &upd); err != nil {
			return nil, fmt.Errorf("servingpool: scan vdi slice: %w", err)
		}
		v.ScheduledAt = parseT(sched)
		v.UpdatedAt = parseT(upd)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *SQLStore) RecentSignals(ctx context.Context, n int) ([]AutoscaleSignal, error) {
	if n <= 0 {
		n = 50
	}
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, emitted_at, scope, action, reason, load_score
		   FROM pool_autoscale_signals
		   ORDER BY id DESC LIMIT ?`), n,
	)
	if err != nil {
		return nil, fmt.Errorf("servingpool: recent signals: %w", err)
	}
	defer rows.Close()
	var out []AutoscaleSignal
	for rows.Next() {
		var sig AutoscaleSignal
		var emit string
		if err := rows.Scan(&sig.ID, &emit, &sig.Scope, &sig.Action, &sig.Reason, &sig.LoadScore); err != nil {
			return nil, fmt.Errorf("servingpool: scan signal: %w", err)
		}
		sig.EmittedAt = parseT(emit)
		out = append(out, sig)
	}
	return out, rows.Err()
}
