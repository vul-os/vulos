package orgadmin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ErrInviteStoreUnavailable is returned by service invite paths when no invite
// backing is wired (neither a fleet-backed MemberStore.Invite nor this store).
var ErrInviteStoreUnavailable = errors.New("orgadmin: invite store unavailable")

// BackupModeStore persists the per-tenant backup-mode setting and any
// per-location overrides. SQLStore is the production implementation; MemStore
// is the reference used by tests / dev mode.
//
// The org-admin dashboard backup-mode toggle propagates to the storage selector
// (storagesel.SetSyncMode) so the bundle node reads the effective mode back via
// GET /api/storage/sync-mode. The propagation is wired in
// cmd/server/wire_orgadmin.go (orgadminBackupModeStore) and cmd/server/main.go
// (ORGADMIN_WIRE). SetBackupMode here is intent-only (persists to the orgadmin
// SQL row); the actual storagesel write happens in the wrapper.
type BackupModeStore interface {
	// GetBackupMode returns the tenant's setting. A tenant with no row gets the
	// central default (mode="central", empty per_location) — never an error.
	GetBackupMode(ctx context.Context, tenantID string) (BackupModeResponse, error)
	// SetBackupMode upserts the tenant's setting.
	SetBackupMode(ctx context.Context, tenantID string, req BackupModeRequest) error
	Close() error
}

// InviteStore persists invite tokens when no shared (fleet) invite store is
// wired. Optional — when a MemberStore supplies invites, the service prefers
// it. This standalone store exists per the task spec ("…and invite tokens if no
// existing store").
type InviteStore interface {
	// CreateInvite creates a pending invite carrying an optional display name
	// (empty = no name). The name is persisted so it can be applied to the
	// member when the seat is created.
	CreateInvite(ctx context.Context, tenantID, email, role, name string) (InviteResponse, error)
}

// ProductStatusStore persists the per-org override of a suite product's status
// (PRODUCTS-ADMIN-01). GET /api/products advertises every catalogue product with
// a default status of "available"; a row here overrides that default for one
// (tenant, product) pair. Absence of a row ⇒ the catalogue default. Status is
// always scoped to the tenant (never global). SQLStore is the production
// implementation; MemStore is the reference used by tests / dev mode.
type ProductStatusStore interface {
	// ProductStatuses returns the tenant's product-status overrides as a
	// productID → status map. A tenant with no overrides gets an empty map (never
	// an error) so GET /api/products falls back to the catalogue defaults.
	ProductStatuses(ctx context.Context, tenantID string) (map[string]string, error)
	// SetProductStatus upserts one (tenant, product) override. Idempotent: setting
	// the same status twice is a no-op write. Validation of productID/status is the
	// service's job (the store trusts its caller).
	SetProductStatus(ctx context.Context, tenantID, productID, status string) error
}

// ──────────────────────────────────────────────────────────────────────────
// SQLStore
// ──────────────────────────────────────────────────────────────────────────

// SQLStore is the SQL-backed store. On SQLite: WAL + single writer.
// On Postgres: schema-isolated pool.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex
}

// OpenSQLStore applies migrations to db and returns a ready SQLStore.
// db is obtained from cpdb.Open (Postgres) or cpdb.OpenSQLiteDSN (SQLite).
func OpenSQLStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("orgadmin: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("orgadmin: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database.
func (s *SQLStore) Close() error { return s.db.Close() }

// GetBackupMode implements BackupModeStore.
func (s *SQLStore) GetBackupMode(ctx context.Context, tenantID string) (BackupModeResponse, error) {
	var mode, perLocJSON string
	var lastSync, syncHealth sql.NullString
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT mode, per_location_json, last_sync, sync_health
		   FROM org_backup_mode WHERE tenant_id = ?`), tenantID,
	).Scan(&mode, &perLocJSON, &lastSync, &syncHealth)
	if errors.Is(err, sql.ErrNoRows) {
		// No explicit row → central default.
		return BackupModeResponse{Mode: BackupModeCentral, PerLocation: []PerLocationMode{}}, nil
	}
	if err != nil {
		return BackupModeResponse{}, fmt.Errorf("orgadmin: get backup mode: %w", err)
	}
	var perLoc []PerLocationMode
	if perLocJSON != "" {
		if uerr := json.Unmarshal([]byte(perLocJSON), &perLoc); uerr != nil {
			return BackupModeResponse{}, fmt.Errorf("orgadmin: decode per_location: %w", uerr)
		}
	}
	if perLoc == nil {
		perLoc = []PerLocationMode{}
	}
	return BackupModeResponse{
		Mode:        mode,
		PerLocation: perLoc,
		LastSync:    lastSync.String,
		SyncHealth:  syncHealth.String,
	}, nil
}

// SetBackupMode implements BackupModeStore.
func (s *SQLStore) SetBackupMode(ctx context.Context, tenantID string, req BackupModeRequest) error {
	perLoc := req.PerLocation
	if perLoc == nil {
		perLoc = []PerLocationMode{}
	}
	b, err := json.Marshal(perLoc)
	if err != nil {
		return fmt.Errorf("orgadmin: encode per_location: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO org_backup_mode (tenant_id, mode, per_location_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(tenant_id) DO UPDATE SET
		  mode              = excluded.mode,
		  per_location_json = excluded.per_location_json,
		  updated_at        = excluded.updated_at`),
		tenantID, req.Mode, string(b), nowUTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("orgadmin: set backup mode: %w", err)
	}
	// The effective propagation to storagesel happens in the orgadminBackupModeStore
	// wrapper (cmd/server/wire_orgadmin.go). This inner store is the intent record.
	return nil
}

// CreateInvite implements InviteStore.
func (s *SQLStore) CreateInvite(ctx context.Context, tenantID, email, role, name string) (InviteResponse, error) {
	id, err := newInviteID()
	if err != nil {
		return InviteResponse{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO org_invites (id, tenant_id, email, role, state, created_at, display_name)
		VALUES (?, ?, ?, ?, 'pending', ?, ?)`),
		id, tenantID, email, role, nowUTC().Format(time.RFC3339), name,
	)
	if err != nil {
		return InviteResponse{}, fmt.Errorf("orgadmin: create invite: %w", err)
	}
	return InviteResponse{ID: id, Email: email, Role: role, State: "pending"}, nil
}

// ProductStatuses implements ProductStatusStore. Returns the tenant's overrides
// (productID → status). No rows ⇒ empty map (never an error).
func (s *SQLStore) ProductStatuses(ctx context.Context, tenantID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT product_id, status FROM org_product_status WHERE tenant_id = ?`),
		tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("orgadmin: list product statuses: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, fmt.Errorf("orgadmin: scan product status: %w", err)
		}
		out[id] = status
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orgadmin: iterate product statuses: %w", err)
	}
	return out, nil
}

// SetProductStatus implements ProductStatusStore (idempotent upsert).
func (s *SQLStore) SetProductStatus(ctx context.Context, tenantID, productID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO org_product_status (tenant_id, product_id, status, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(tenant_id, product_id) DO UPDATE SET
		  status     = excluded.status,
		  updated_at = excluded.updated_at`),
		tenantID, productID, status, nowUTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("orgadmin: set product status: %w", err)
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────
// MemStore — reference implementation for tests / dev mode.
// ──────────────────────────────────────────────────────────────────────────

// inviteRecord is the MemStore's stored invite: the wire acknowledgement plus
// the (un-surfaced) display name carried through CreateInvite.
type inviteRecord struct {
	InviteResponse
	name string
}

// MemStore is the in-memory reference BackupModeStore + InviteStore.
type MemStore struct {
	mu      sync.Mutex
	backup  map[string]BackupModeResponse
	invites []inviteRecord
	// products maps tenantID → (productID → status) for the per-org product-status
	// overrides (PRODUCTS-ADMIN-01).
	products map[string]map[string]string
}

// NewMemStore constructs an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		backup:   map[string]BackupModeResponse{},
		products: map[string]map[string]string{},
	}
}

// GetBackupMode implements BackupModeStore.
func (m *MemStore) GetBackupMode(_ context.Context, tenantID string) (BackupModeResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.backup[tenantID]
	if !ok {
		return BackupModeResponse{Mode: BackupModeCentral, PerLocation: []PerLocationMode{}}, nil
	}
	// Defensive copy so callers can't mutate stored slice.
	out := v
	out.PerLocation = append([]PerLocationMode(nil), v.PerLocation...)
	if out.PerLocation == nil {
		out.PerLocation = []PerLocationMode{}
	}
	return out, nil
}

// SetBackupMode implements BackupModeStore.
func (m *MemStore) SetBackupMode(_ context.Context, tenantID string, req BackupModeRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	perLoc := append([]PerLocationMode(nil), req.PerLocation...)
	if perLoc == nil {
		perLoc = []PerLocationMode{}
	}
	prev := m.backup[tenantID]
	m.backup[tenantID] = BackupModeResponse{
		Mode:        req.Mode,
		PerLocation: perLoc,
		LastSync:    prev.LastSync,
		SyncHealth:  prev.SyncHealth,
	}
	return nil
}

// CreateInvite implements InviteStore. The name is recorded on the stored
// invite (InviteResponse does not surface it on the wire, matching the existing
// acknowledgement shape).
func (m *MemStore) CreateInvite(_ context.Context, tenantID, email, role, name string) (InviteResponse, error) {
	id, err := newInviteID()
	if err != nil {
		return InviteResponse{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	inv := InviteResponse{ID: id, Email: email, Role: role, State: "pending"}
	m.invites = append(m.invites, inviteRecord{InviteResponse: inv, name: name})
	return inv, nil
}

// ProductStatuses implements ProductStatusStore. Returns a defensive copy of the
// tenant's overrides (empty map when none).
func (m *MemStore) ProductStatuses(_ context.Context, tenantID string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for id, status := range m.products[tenantID] {
		out[id] = status
	}
	return out, nil
}

// SetProductStatus implements ProductStatusStore (idempotent upsert).
func (m *MemStore) SetProductStatus(_ context.Context, tenantID, productID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.products == nil {
		m.products = map[string]map[string]string{}
	}
	byID := m.products[tenantID]
	if byID == nil {
		byID = map[string]string{}
		m.products[tenantID] = byID
	}
	byID[productID] = status
	return nil
}

// Close implements BackupModeStore.
func (m *MemStore) Close() error { return nil }

// compile-time assertions.
var (
	_ BackupModeStore = (*SQLStore)(nil)
	_ BackupModeStore = (*MemStore)(nil)
	_ InviteStore     = (*SQLStore)(nil)
	_ InviteStore     = (*MemStore)(nil)
)

// ──────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────

func nowUTC() time.Time { return time.Now().UTC() }

func newInviteID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("orgadmin: generate invite id: %w", err)
	}
	return "inv-" + hex.EncodeToString(b), nil
}
