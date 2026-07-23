// invite_accept.go — fleet-local invite→member acceptance primitive.
//
// The frozen Store contract (contract.go) ships CreateInvite/GetInvite/
// RevokeInvite/ListInvites but no "mark this pending invite accepted" method —
// and there is no invite→member promotion path anywhere in the codebase (see
// the note in invite_name.go). This file closes that gap WITHOUT touching the
// frozen interface, mirroring the optional-seam pattern of remove_member.go
// (MemberRemover), member_name.go (MemberNamer) and invite_name.go
// (MemberInviter).
//
// The seam adds two pieces:
//
//   - InviteExpiry / IsInviteExpired — a pure expiry policy on top of the
//     existing Invite.CreatedAt (the fleet_invites table has no expires_at
//     column, so expiry is derived from age; a 14-day default mirrors typical
//     SaaS invite TTLs and is a package var so callers/tests can override).
//   - InviteAcceptor.MarkInviteAccepted — flips a *pending* invite's state to
//     "accepted" atomically, returning ErrInviteNotPending if it was already
//     accepted/revoked. This is the single state-transition the accept handler
//     needs after it has created the membership row + applied the display name.
//
// The accept HTTP handler (cmd/server/routes_invite_accept.go) orchestrates:
//
//	GetInvite → validate (token + email + not-expired + state==pending) →
//	AddMember(role) → MemberNamer.SetDisplayName(invite.DisplayName) →
//	MarkInviteAccepted → audit "org.member.accept".
package fleet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Sentinel errors for the accept flow. Handlers map these to HTTP status codes:
//   - ErrInviteNotPending → 409 Conflict (already accepted or revoked)
//   - ErrInviteExpired     → 410 Gone (past its TTL)
var (
	ErrInviteNotPending = errors.New("fleet: invite is not pending (already accepted or revoked)")
	ErrInviteExpired    = errors.New("fleet: invite has expired")
)

// InviteExpiry is the time-to-live applied to a pending invite, measured from
// Invite.CreatedAt. A package var (not a const) so tests can shrink it and an
// operator can lengthen it without a schema change. 14 days mirrors typical
// SaaS onboarding-invite windows.
var InviteExpiry = 14 * 24 * time.Hour

// IsInviteExpired reports whether inv is past its TTL relative to now. A zero
// CreatedAt (never set) is treated as NOT expired so a malformed row does not
// silently lock a user out — the state + token + email checks still gate it.
func IsInviteExpired(inv Invite, now time.Time) bool {
	if inv.CreatedAt.IsZero() {
		return false
	}
	return now.After(inv.CreatedAt.Add(InviteExpiry))
}

// InviteAcceptor is the optional accept seam. SQLStore and MemStore satisfy it.
// Callers that hold a fleet.Store may type-assert to InviteAcceptor to flip a
// pending invite to accepted without the frozen Store interface carrying the
// method.
type InviteAcceptor interface {
	// MarkInviteAccepted transitions the invite identified by id from "pending"
	// to "accepted". Returns ErrInviteNotFound when the id is unknown and
	// ErrInviteNotPending when the invite's current state is not "pending"
	// (already accepted, or revoked). The transition is atomic: a concurrent
	// double-accept results in exactly one success and one ErrInviteNotPending.
	MarkInviteAccepted(ctx context.Context, id string) error
}

// compile-time assertions: both concrete stores satisfy InviteAcceptor.
var (
	_ InviteAcceptor = (*SQLStore)(nil)
	_ InviteAcceptor = (*MemStore)(nil)
)

// MarkInviteAccepted implements InviteAcceptor for the SQL-backed store. The
// state guard is in the UPDATE's WHERE clause (state='pending') so SQLite's
// single-writer serialisation makes the pending→accepted flip atomic: the row
// is matched once; a racing second call affects 0 rows → ErrInviteNotPending.
func (s *SQLStore) MarkInviteAccepted(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Existence check (distinguishes 404 from 409).
	var state string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT state FROM fleet_invites WHERE id = ?`), id,
	).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInviteNotFound
	}
	if err != nil {
		return fmt.Errorf("fleet: mark invite accepted lookup: %w", err)
	}

	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE fleet_invites SET state = 'accepted' WHERE id = ? AND state = 'pending'`),
		id,
	)
	if err != nil {
		return fmt.Errorf("fleet: mark invite accepted: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("fleet: mark invite accepted rows: %w", err)
	}
	if n == 0 {
		return ErrInviteNotPending
	}
	return nil
}

// MarkInviteAccepted implements InviteAcceptor for the in-memory reference store.
func (m *MemStore) MarkInviteAccepted(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invites[id]
	if !ok {
		return ErrInviteNotFound
	}
	if inv.State != "pending" {
		return ErrInviteNotPending
	}
	inv.State = "accepted"
	m.invites[id] = inv
	return nil
}
