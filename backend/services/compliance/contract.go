// Package compliance implements a minimal, legally-honest request-intake for
// POPIA / GDPR data-subject rights (right of access / data export, and right to
// erasure / account deletion).
//
// SCOPE: this is intentionally a *request recorder*, not an automated export or
// deletion engine. A data-subject's request is persisted (account_id, kind,
// timestamp) so the box owner can fulfil it (POPIA s23 / GDPR Art.15/Art.20 for
// access-export, POPIA s24-25 / GDPR Art.17 for erasure) and so the request is
// auditable. It deliberately does NOT fabricate a download URL or silently
// "complete" a request — the panel surfaces an honest "request received"
// acknowledgement with a reference ID.
//
// This is COMPLEMENTARY to services/files' data export used by
// GET /api/export/data (Settings → Export My Data): that endpoint already lets a
// user self-serve an immediate download. This package instead creates a formal,
// timestamped RECORD of a data-subject request — including erasure, which has no
// self-serve mechanics — so the box owner has an auditable trail to act on within
// the statutory window.
//
// Storage: SQLite (WAL, no-CGO modernc driver), mirroring
// services/integrations/selfhost.
package compliance

import (
	"context"
	"errors"
	"time"
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

// ErrInvalidKind is returned when the request kind is not a known data-rights
// action (export | erasure).
var ErrInvalidKind = errors.New("compliance: invalid request kind")

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// Kinds of data-rights request.
const (
	KindExport  = "export"  // GDPR Art.15/20 / POPIA s23 — right of access / portability
	KindErasure = "erasure" // GDPR Art.17 / POPIA s24-25 — right to erasure
)

// StatusReceived is the only status set by this intake — fulfilment is manual,
// performed by the box owner outside this package.
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

// Recorder persists data-subject requests. *SQLStore is the production
// implementation (store.go).
type Recorder interface {
	// Record persists a new request for accountID. Returns ErrInvalidKind if
	// kind is not export|erasure.
	Record(ctx context.Context, accountID, kind, note string) (Request, error)
	// ListByAccount returns the caller's own requests, newest first.
	ListByAccount(ctx context.Context, accountID string) ([]Request, error)
	// Close closes the underlying store.
	Close() error
}

// compile-time assertion (in store.go, alongside the type it checks).
