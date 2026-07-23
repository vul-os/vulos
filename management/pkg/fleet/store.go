package fleet

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io/fs"
	"sort"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// SQLStore is the production dual-backend (SQLite + Postgres) implementation
// of Store, backed by a *cpdb.DB.
// Concurrency-safe: WAL mode (SQLite) or connection pool (Postgres) + single
// writer mutex.
// Behaviour is semantically identical to MemStore.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex // serialises all writes
}

// Open migrates and returns a ready-to-use *SQLStore backed by db.
// db must already be opened via cpdb.Open or cpdb.OpenSQLiteDSN.
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("fleet: migrations sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("fleet: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion: SQLStore satisfies Store.
var _ Store = (*SQLStore)(nil)

// --------------------------------------------------------------------------
// ID generation
// --------------------------------------------------------------------------

// genULID generates a random 26-char Crockford base32 ID using crypto/rand.
func genULID() string {
	const alpha = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// Fallback to time-based if crypto/rand fails (should never happen).
		t := uint64(time.Now().UnixNano())
		binary.BigEndian.PutUint64(raw[:8], t)
		binary.BigEndian.PutUint64(raw[8:], t^0xdeadbeefcafe1234)
	}
	// Clamp first byte to 3 bits so value fits in 128 bits (Crockford 26-char).
	raw[0] &= 0x07
	// Pack 128 bits → 26 x 5-bit symbols.
	bits := make([]byte, 26)
	bits[0] = raw[0] >> 5
	bits[1] = raw[0] & 0x1f
	bits[2] = raw[1] >> 3
	bits[3] = (raw[1]&0x07)<<2 | raw[2]>>6
	bits[4] = (raw[2] >> 1) & 0x1f
	bits[5] = (raw[2]&0x01)<<4 | raw[3]>>4
	bits[6] = (raw[3]&0x0f)<<1 | raw[4]>>7
	bits[7] = (raw[4] >> 2) & 0x1f
	bits[8] = (raw[4]&0x03)<<3 | raw[5]>>5
	bits[9] = raw[5] & 0x1f
	bits[10] = raw[6] >> 3
	bits[11] = (raw[6]&0x07)<<2 | raw[7]>>6
	bits[12] = (raw[7] >> 1) & 0x1f
	bits[13] = (raw[7]&0x01)<<4 | raw[8]>>4
	bits[14] = (raw[8]&0x0f)<<1 | raw[9]>>7
	bits[15] = (raw[9] >> 2) & 0x1f
	bits[16] = (raw[9]&0x03)<<3 | raw[10]>>5
	bits[17] = raw[10] & 0x1f
	bits[18] = raw[11] >> 3
	bits[19] = (raw[11]&0x07)<<2 | raw[12]>>6
	bits[20] = (raw[12] >> 1) & 0x1f
	bits[21] = (raw[12]&0x01)<<4 | raw[13]>>4
	bits[22] = (raw[13]&0x0f)<<1 | raw[14]>>7
	bits[23] = (raw[14] >> 2) & 0x1f
	bits[24] = (raw[14]&0x03)<<3 | raw[15]>>5
	bits[25] = raw[15] & 0x1f
	out := make([]byte, 26)
	for i, v := range bits {
		out[i] = alpha[v]
	}
	return string(out)
}

// --------------------------------------------------------------------------
// Time helpers
// --------------------------------------------------------------------------

func parseTimePtr(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s.String)
	}
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t.UTC()
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// --------------------------------------------------------------------------
// Org management
// --------------------------------------------------------------------------

func (s *SQLStore) CreateOrg(ctx context.Context, name, ownerAccountID string) (Org, error) {
	id := genULID()
	ts := now()
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Org{}, fmt.Errorf("fleet: create org begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO fleet_orgs (id, name, owner_account, created_at)
		 VALUES (?, ?, ?, ?)`),
		id, name, ownerAccountID, ts,
	)
	if err != nil {
		return Org{}, fmt.Errorf("fleet: create org insert: %w", err)
	}

	// Auto-assign owner role.
	_, err = tx.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO fleet_org_members (org_id, account_id, role, joined_at)
		 VALUES (?, ?, 'owner', ?)`),
		id, ownerAccountID, ts,
	)
	if err != nil {
		return Org{}, fmt.Errorf("fleet: create org member: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Org{}, fmt.Errorf("fleet: create org commit: %w", err)
	}

	return Org{
		ID:           id,
		Name:         name,
		OwnerAccount: ownerAccountID,
		CreatedAt:    parseTime(ts),
	}, nil
}

func (s *SQLStore) GetOrg(ctx context.Context, id string) (Org, error) {
	var o Org
	var createdAt string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id, name, owner_account, created_at FROM fleet_orgs WHERE id = ?`), id,
	).Scan(&o.ID, &o.Name, &o.OwnerAccount, &createdAt)
	if err == sql.ErrNoRows {
		return Org{}, ErrOrgNotFound
	}
	if err != nil {
		return Org{}, fmt.Errorf("fleet: get org: %w", err)
	}
	o.CreatedAt = parseTime(createdAt)
	return o, nil
}

// --------------------------------------------------------------------------
// Membership management
// --------------------------------------------------------------------------

func (s *SQLStore) AddMember(ctx context.Context, orgID, accountID, role string) error {
	if !ValidRoles[role] {
		return ErrInvalidRole
	}
	// Verify org exists.
	if _, err := s.GetOrg(ctx, orgID); err != nil {
		return err
	}
	ts := now()
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO fleet_org_members (org_id, account_id, role, joined_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(org_id, account_id) DO UPDATE SET role = excluded.role`),
		orgID, accountID, role, ts,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("fleet: add member: %w", err)
	}
	return nil
}

func (s *SQLStore) SetRole(ctx context.Context, orgID, accountID, role string) error {
	if !ValidRoles[role] {
		return ErrInvalidRole
	}
	// Check member exists.
	if _, err := s.MemberRole(ctx, orgID, accountID); err != nil {
		return err
	}
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE fleet_org_members SET role = ? WHERE org_id = ? AND account_id = ?`),
		role, orgID, accountID,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("fleet: set role: %w", err)
	}
	return nil
}

func (s *SQLStore) Members(ctx context.Context, orgID string) ([]Member, error) {
	if _, err := s.GetOrg(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT org_id, account_id, role, joined_at, display_name
		   FROM fleet_org_members WHERE org_id = ?
		   ORDER BY account_id`), orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("fleet: list members: %w", err)
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var mem Member
		var joinedAt string
		if err := rows.Scan(&mem.OrgID, &mem.AccountID, &mem.Role, &joinedAt, &mem.DisplayName); err != nil {
			return nil, fmt.Errorf("fleet: scan member: %w", err)
		}
		mem.JoinedAt = parseTime(joinedAt)
		out = append(out, mem)
	}
	return out, rows.Err()
}

func (s *SQLStore) MemberRole(ctx context.Context, orgID, accountID string) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT role FROM fleet_org_members WHERE org_id = ? AND account_id = ?`),
		orgID, accountID,
	).Scan(&role)
	if err == sql.ErrNoRows {
		return "", ErrNotMember
	}
	if err != nil {
		return "", fmt.Errorf("fleet: member role: %w", err)
	}
	return role, nil
}

// --------------------------------------------------------------------------
// Device management
// --------------------------------------------------------------------------

func (s *SQLStore) UpsertDevice(ctx context.Context, d Device) error {
	ts := now()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = time.Now().UTC()
	}
	if d.Channel == "" {
		d.Channel = "stable"
	}
	if d.Health == "" {
		d.Health = "unknown"
	}
	var lhb interface{}
	if d.LastHeartbeat != nil {
		lhb = d.LastHeartbeat.UTC().Format(time.RFC3339Nano)
	}
	var orgID interface{}
	if d.OrgID != "" {
		orgID = d.OrgID
	}
	decom := 0
	if d.Decommissioned {
		decom = 1
	}

	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO fleet_devices
		   (ulid, org_id, account_id, name, group_label, version, channel,
		    last_heartbeat, health, crash_count, decommissioned, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(ulid) DO UPDATE SET
		   org_id         = excluded.org_id,
		   account_id     = excluded.account_id,
		   name           = excluded.name,
		   group_label    = excluded.group_label,
		   version        = excluded.version,
		   channel        = excluded.channel,
		   last_heartbeat = excluded.last_heartbeat,
		   health         = excluded.health,
		   crash_count    = excluded.crash_count,
		   decommissioned = excluded.decommissioned,
		   updated_at     = ?`),
		d.ULID, orgID, d.AccountID, d.Name, d.GroupLabel, d.Version, d.Channel,
		lhb, d.Health, d.CrashCount, decom,
		d.CreatedAt.UTC().Format(time.RFC3339Nano),
		d.UpdatedAt.UTC().Format(time.RFC3339Nano),
		ts,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("fleet: upsert device: %w", err)
	}
	return nil
}

func scanDevice(rows interface {
	Scan(...any) error
}) (Device, error) {
	var d Device
	var orgID sql.NullString
	var lhbStr sql.NullString
	var createdAt, updatedAt string
	var decom int
	err := rows.Scan(
		&d.ULID, &orgID, &d.AccountID, &d.Name, &d.GroupLabel,
		&d.Version, &d.Channel, &lhbStr, &d.Health,
		&d.CrashCount, &decom, &createdAt, &updatedAt,
	)
	if err != nil {
		return Device{}, err
	}
	if orgID.Valid {
		d.OrgID = orgID.String
	}
	d.LastHeartbeat = parseTimePtr(lhbStr)
	d.Decommissioned = decom != 0
	d.CreatedAt = parseTime(createdAt)
	d.UpdatedAt = parseTime(updatedAt)
	return d, nil
}

const deviceSelectCols = `ulid, org_id, account_id, name, group_label,
       version, channel, last_heartbeat, health,
       crash_count, decommissioned, created_at, updated_at`

func (s *SQLStore) GetDevice(ctx context.Context, ulid string) (Device, error) {
	row := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT `+deviceSelectCols+` FROM fleet_devices WHERE ulid = ?`), ulid,
	)
	d, err := scanDevice(row)
	if err == sql.ErrNoRows {
		return Device{}, ErrDeviceNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("fleet: get device: %w", err)
	}
	return d, nil
}

func (s *SQLStore) ListDevices(ctx context.Context, orgID string, limit, offset int) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT `+deviceSelectCols+`
		   FROM fleet_devices WHERE org_id = ?
		   ORDER BY ulid
		   LIMIT ? OFFSET ?`),
		orgID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("fleet: list devices: %w", err)
	}
	defer rows.Close()
	return scanDeviceRows(rows)
}

func (s *SQLStore) ListByAccount(ctx context.Context, accountID string, limit, offset int) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT `+deviceSelectCols+`
		   FROM fleet_devices WHERE account_id = ?
		   ORDER BY ulid
		   LIMIT ? OFFSET ?`),
		accountID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("fleet: list by account: %w", err)
	}
	defer rows.Close()
	return scanDeviceRows(rows)
}

func scanDeviceRows(rows *sql.Rows) ([]Device, error) {
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("fleet: scan device: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *SQLStore) SetName(ctx context.Context, ulid, name string) error {
	return s.deviceUpdate(ctx, ulid, `UPDATE fleet_devices SET name = ?, updated_at = ? WHERE ulid = ?`, name, now(), ulid)
}

func (s *SQLStore) SetGroup(ctx context.Context, ulid, group string) error {
	return s.deviceUpdate(ctx, ulid, `UPDATE fleet_devices SET group_label = ?, updated_at = ? WHERE ulid = ? `, group, now(), ulid)
}

func (s *SQLStore) SetChannel(ctx context.Context, ulid, channel string) error {
	return s.deviceUpdate(ctx, ulid, `UPDATE fleet_devices SET channel = ?, updated_at = ? WHERE ulid = ?`, channel, now(), ulid)
}

func (s *SQLStore) Decommission(ctx context.Context, ulid string) error {
	return s.deviceUpdate(ctx, ulid, `UPDATE fleet_devices SET decommissioned = 1, updated_at = ? WHERE ulid = ?`, now(), ulid)
}

// deviceUpdate runs a write query and verifies the ULID exists (ErrDeviceNotFound if rows affected = 0).
func (s *SQLStore) deviceUpdate(ctx context.Context, ulid, query string, args ...any) error {
	// Verify device exists first (consistent with MemStore returning ErrDeviceNotFound).
	var dummy string
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT ulid FROM fleet_devices WHERE ulid = ?`), ulid).Scan(&dummy)
	if err == sql.ErrNoRows {
		return ErrDeviceNotFound
	}
	if err != nil {
		return fmt.Errorf("fleet: device check: %w", err)
	}

	s.mu.Lock()
	_, err = s.db.ExecContext(ctx, s.db.Rebind(query), args...)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("fleet: device update: %w", err)
	}
	return nil
}

// --------------------------------------------------------------------------
// Heartbeat / telemetry
// --------------------------------------------------------------------------

func (s *SQLStore) InsertHeartbeat(ctx context.Context, hb Heartbeat) (Device, error) {
	ts := now()
	hb.ReceivedAt = time.Now().UTC()

	s.mu.Lock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.mu.Unlock()
		return Device{}, fmt.Errorf("fleet: heartbeat begin: %w", err)
	}

	// Insert heartbeat record.
	_, err = tx.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO fleet_heartbeats (ulid, version, health, sync_lag_sec, uptime_sec, crash_count, received_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`),
		hb.ULID, hb.Version, hb.Health, hb.SyncLagSec, hb.UptimeSec, hb.CrashCount, ts,
	)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		s.mu.Unlock()
		return Device{}, fmt.Errorf("fleet: insert heartbeat: %w", err)
	}

	// Upsert device row (creates if absent — heartbeat is idempotent device creation).
	_, err = tx.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO fleet_devices
		   (ulid, account_id, name, group_label, version, channel, last_heartbeat, health, crash_count, decommissioned, created_at, updated_at)
		 VALUES (?, '', '', '', ?, 'stable', ?, ?, ?, 0, ?, ?)
		 ON CONFLICT(ulid) DO UPDATE SET
		   version        = excluded.version,
		   last_heartbeat = excluded.last_heartbeat,
		   health         = excluded.health,
		   crash_count    = excluded.crash_count,
		   updated_at     = excluded.updated_at`),
		hb.ULID, hb.Version, ts, hb.Health, hb.CrashCount, ts, ts,
	)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		s.mu.Unlock()
		return Device{}, fmt.Errorf("fleet: heartbeat upsert device: %w", err)
	}

	if err := tx.Commit(); err != nil {
		s.mu.Unlock()
		return Device{}, fmt.Errorf("fleet: heartbeat commit: %w", err)
	}
	s.mu.Unlock()

	return s.GetDevice(ctx, hb.ULID)
}

func (s *SQLStore) RecentHeartbeats(ctx context.Context, ulid string, n int) ([]Heartbeat, error) {
	if n <= 0 {
		n = 20
	}
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT ulid, version, health, sync_lag_sec, uptime_sec, crash_count, received_at
		   FROM fleet_heartbeats WHERE ulid = ?
		   ORDER BY received_at DESC
		   LIMIT ?`),
		ulid, n,
	)
	if err != nil {
		return nil, fmt.Errorf("fleet: recent heartbeats: %w", err)
	}
	defer rows.Close()
	var out []Heartbeat
	for rows.Next() {
		var hb Heartbeat
		var recAt string
		if err := rows.Scan(&hb.ULID, &hb.Version, &hb.Health,
			&hb.SyncLagSec, &hb.UptimeSec, &hb.CrashCount, &recAt); err != nil {
			return nil, fmt.Errorf("fleet: scan heartbeat: %w", err)
		}
		hb.ReceivedAt = parseTime(recAt)
		out = append(out, hb)
	}
	return out, rows.Err()
}

// PurgeHeartbeatsBefore deletes heartbeats received before cutoff. Nothing else
// prunes fleet_heartbeats, so without this the append-only table grows without
// bound; the per-box status view only needs recent history.
func (s *SQLStore) PurgeHeartbeatsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM fleet_heartbeats WHERE received_at < ?`),
		cutoff.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("fleet: purge heartbeats: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// --------------------------------------------------------------------------
// Seat count
// --------------------------------------------------------------------------

func (s *SQLStore) SeatCount(ctx context.Context, orgID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM fleet_devices WHERE org_id = ? AND decommissioned = 0`), orgID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("fleet: seat count: %w", err)
	}
	return count, nil
}

// --------------------------------------------------------------------------
// Invites
// --------------------------------------------------------------------------

func (s *SQLStore) CreateInvite(ctx context.Context, orgID, email, role string) (Invite, error) {
	// Delegate to the named variant (invite_name.go) with an empty name so the
	// frozen 3-arg path and the named path share one insert + stay identical.
	return s.CreateInviteWithName(ctx, orgID, email, role, "")
}

// VersionDistribution returns version→count map for non-decommissioned devices in an org.
// This is a read-only helper used by routes; not part of the Store interface to
// keep the interface minimal.
func VersionDistribution(ctx context.Context, db *cpdb.DB, orgID string) (map[string]int, error) {
	rows, err := db.QueryContext(ctx,
		db.Rebind(`SELECT version, COUNT(*) FROM fleet_devices
		  WHERE org_id = ? AND decommissioned = 0
		  GROUP BY version`), orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("fleet: version dist: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var ver string
		var cnt int
		if err := rows.Scan(&ver, &cnt); err != nil {
			return nil, err
		}
		out[ver] = cnt
	}
	return out, rows.Err()
}

// VersionDistributionMem returns version→count for a MemStore (used in tests / routes).
func VersionDistributionMem(m *MemStore, orgID string) map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for _, d := range m.devices {
		if d.OrgID == orgID && !d.Decommissioned {
			out[d.Version]++
		}
	}
	return out
}

func (s *SQLStore) GetInvite(ctx context.Context, id string) (Invite, error) {
	var inv Invite
	var createdAt string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id, org_id, email, role, state, created_at, display_name FROM fleet_invites WHERE id = ?`), id,
	).Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.State, &createdAt, &inv.DisplayName)
	if err == sql.ErrNoRows {
		return Invite{}, ErrInviteNotFound
	}
	if err != nil {
		return Invite{}, fmt.Errorf("fleet: get invite: %w", err)
	}
	inv.CreatedAt = parseTime(createdAt)
	return inv, nil
}

func (s *SQLStore) RevokeInvite(ctx context.Context, id string) error {
	// Verify invite exists.
	if _, err := s.GetInvite(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE fleet_invites SET state = 'revoked' WHERE id = ?`), id,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("fleet: revoke invite: %w", err)
	}
	return nil
}

func (s *SQLStore) ListInvites(ctx context.Context, orgID string) ([]Invite, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, org_id, email, role, state, created_at, display_name
		   FROM fleet_invites WHERE org_id = ?
		   ORDER BY id`), orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("fleet: list invites: %w", err)
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var inv Invite
		var createdAt string
		if err := rows.Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.State, &createdAt, &inv.DisplayName); err != nil {
			return nil, fmt.Errorf("fleet: scan invite: %w", err)
		}
		inv.CreatedAt = parseTime(createdAt)
		out = append(out, inv)
	}
	return out, rows.Err()
}

// VersionDistributionFromStore computes version distribution using the Store
// interface (MemStore path via type assertion; SQLStore path via DB query).
func VersionDistributionFromStore(ctx context.Context, st Store, orgID string) (map[string]int, error) {
	switch v := st.(type) {
	case *SQLStore:
		return VersionDistribution(ctx, v.db, orgID)
	case *MemStore:
		return VersionDistributionMem(v, orgID), nil
	default:
		// Fallback: list all devices (paginated) and compute in-memory.
		dist := map[string]int{}
		offset := 0
		for {
			devs, err := st.ListDevices(ctx, orgID, 200, offset)
			if err != nil {
				return nil, err
			}
			if len(devs) == 0 {
				break
			}
			for _, d := range devs {
				if !d.Decommissioned {
					dist[d.Version]++
				}
			}
			offset += len(devs)
			if len(devs) < 200 {
				break
			}
		}
		return dist, nil
	}
}

// SortedDevicesByULID is a helper for pagination tests.
func SortedDevicesByULID(devs []Device) []Device {
	out := make([]Device, len(devs))
	copy(out, devs)
	sort.Slice(out, func(i, j int) bool { return out[i].ULID < out[j].ULID })
	return out
}
