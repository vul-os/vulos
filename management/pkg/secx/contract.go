// Package secx provides app-visibility ownership and access control for the
// vulos.cloud control-plane.
//
// An AppVisibility record ties a (app_id, ulid) pair to an account and a
// visibility level. The VisibilityStore interface is the frozen contract;
// MemStore is the behavioural reference; SQLStore is the production backend.
package secx

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Visibility is the app-visibility level. Exactly three values are valid.
type Visibility string

const (
	VisPrivate Visibility = "private"
	VisLocal   Visibility = "local"
	VisPublic  Visibility = "public"
)

// ValidVisibility reports whether v is one of the three legal values.
func ValidVisibility(v Visibility) bool {
	return v == VisPrivate || v == VisLocal || v == VisPublic
}

// AppVisibility is a single app-visibility record.
type AppVisibility struct {
	AppID      string     `json:"app_id"`
	ULID       string     `json:"ulid"`
	AccountID  string     `json:"account_id"`
	Visibility Visibility `json:"visibility"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// AuditEntry records a single visibility change.
type AuditEntry struct {
	AppID     string    `json:"app_id"`
	ULID      string    `json:"ulid"`
	AccountID string    `json:"account_id"` // actor
	OldVis    string    `json:"old_vis"`
	NewVis    string    `json:"new_vis"`
	IP        string    `json:"ip"`
	ChangedAt time.Time `json:"changed_at"`
}

// Sentinel errors — handlers map these to HTTP status codes.
var (
	ErrNotOwner          = errors.New("secx: caller is not the owner or org admin")
	ErrFreeTierPublic    = errors.New("secx: public visibility requires a paid tier")
	ErrInvalidVisibility = errors.New("secx: invalid visibility (must be private|local|public)")
	ErrUnknownApp        = errors.New("secx: app not found")
)

// VisibilityStore is the secx persistence contract.
// MemStore and SQLStore both satisfy it; handlers depend ONLY on this interface.
type VisibilityStore interface {
	// Get returns the AppVisibility for (app_id, ulid).
	// Returns ErrUnknownApp if no record exists.
	Get(ctx context.Context, appID, ulid string) (AppVisibility, error)

	// Set creates or updates the visibility for (app_id, ulid).
	// Writes an AuditEntry for every change (even no-ops that keep the same value).
	// Does NOT enforce authz — callers must check ownership before calling Set.
	Set(ctx context.Context, av AppVisibility, audit AuditEntry) error

	// ListByULID returns all AppVisibility records for a ULID, ordered by app_id.
	ListByULID(ctx context.Context, ulid string) ([]AppVisibility, error)

	// AuditLog returns recent audit entries for (app_id, ulid), newest first.
	// If limit <= 0 it defaults to 50.
	AuditLog(ctx context.Context, appID, ulid string, limit int) ([]AuditEntry, error)

	Close() error
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore — in-memory reference implementation
// ─────────────────────────────────────────────────────────────────────────────

// MemStore is the in-memory reference VisibilityStore. Concurrency-safe.
// It DEFINES the semantics every other implementation must match.
type MemStore struct {
	mu      sync.Mutex
	records map[string]AppVisibility // key: appID+"\x00"+ulid
	audit   []AuditEntry
}

// NewMemStore returns an empty reference store.
func NewMemStore() *MemStore {
	return &MemStore{
		records: map[string]AppVisibility{},
	}
}

// compile-time assertion: MemStore satisfies VisibilityStore.
var _ VisibilityStore = (*MemStore)(nil)

func memKey(appID, ulid string) string { return appID + "\x00" + ulid }

func (m *MemStore) Get(_ context.Context, appID, ulid string) (AppVisibility, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	av, ok := m.records[memKey(appID, ulid)]
	if !ok {
		return AppVisibility{}, ErrUnknownApp
	}
	return av, nil
}

func (m *MemStore) Set(_ context.Context, av AppVisibility, audit AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	av.UpdatedAt = time.Now().UTC()
	m.records[memKey(av.AppID, av.ULID)] = av
	audit.ChangedAt = av.UpdatedAt
	m.audit = append(m.audit, audit)
	return nil
}

func (m *MemStore) ListByULID(_ context.Context, ulid string) ([]AppVisibility, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []AppVisibility
	for _, av := range m.records {
		if av.ULID == ulid {
			out = append(out, av)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AppID < out[j].AppID })
	return out, nil
}

func (m *MemStore) AuditLog(_ context.Context, appID, ulid string, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []AuditEntry
	for i := len(m.audit) - 1; i >= 0; i-- {
		e := m.audit[i]
		if e.AppID == appID && e.ULID == ulid {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *MemStore) Close() error { return nil }
