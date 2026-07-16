// Package compliance implements a minimal, legally-honest request-intake for
// POPIA / GDPR data-subject rights (right of access / data export, and right to
// erasure / account deletion).
//
// SCOPE: this is intentionally a *request recorder*, not an automated export or
// deletion engine. A data-subject's request is persisted (account_id, kind,
// timestamp) so the operator can fulfil it within the statutory window and so
// the request is auditable. It deliberately does NOT fabricate a download URL or
// silently "complete" — the dashboard surfaces an honest "request received"
// acknowledgement with a reference ID.
//
// Storage: SQLite (WAL, no-CGO modernc driver), mirroring internal/residency.
package compliance

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrInvalidKind is returned when the request kind is not a known data-rights
	// action (export | erasure).
	ErrInvalidKind = errors.New("compliance: invalid request kind")
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// Kinds of data-rights request.
const (
	KindExport  = "export"  // GDPR Art.20 / POPIA s23 — right of access / portability
	KindErasure = "erasure" // GDPR Art.17 / POPIA s24-25 — right to erasure
)

// StatusReceived is the only status set by this intake — fulfilment is manual.
const StatusReceived = "received"

// ValidKind reports whether k is a recognised data-rights request kind.
func ValidKind(k string) bool { return k == KindExport || k == KindErasure }

// Request is a recorded data-subject request.
type Request struct {
	ID        string    `json:"id"`
	AccountID string    `json:"account_id"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// Store persists data-rights requests.
// MemStore is the behavioural reference; SQLStore must match it.
type Store interface {
	// Record persists a new request for accountID. Returns ErrInvalidKind if
	// kind is not export|erasure.
	Record(ctx context.Context, accountID, kind, note string) (Request, error)
	// ListByAccount returns the caller's requests, newest first.
	ListByAccount(ctx context.Context, accountID string) ([]Request, error)
	// Close closes the underlying store.
	Close() error
}

// ---------------------------------------------------------------------------
// MemStore — in-memory reference (test double / fallback)
// ---------------------------------------------------------------------------

// MemStore is the concurrency-safe, in-memory reference Store.
type MemStore struct {
	mu   sync.Mutex
	rows []Request
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore { return &MemStore{} }

func (m *MemStore) Record(_ context.Context, accountID, kind, note string) (Request, error) {
	if !ValidKind(kind) {
		return Request{}, ErrInvalidKind
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := Request{
		ID:        newRequestID(),
		AccountID: accountID,
		Kind:      kind,
		Status:    StatusReceived,
		Note:      note,
		CreatedAt: time.Now().UTC(),
	}
	m.rows = append(m.rows, r)
	return r, nil
}

func (m *MemStore) ListByAccount(_ context.Context, accountID string) ([]Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Request{}
	// Newest first.
	for i := len(m.rows) - 1; i >= 0; i-- {
		if m.rows[i].AccountID == accountID {
			out = append(out, m.rows[i])
		}
	}
	return out, nil
}

func (m *MemStore) Close() error { return nil }

// compile-time assertion.
var _ Store = (*MemStore)(nil)
