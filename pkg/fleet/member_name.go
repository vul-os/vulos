// member_name.go — fleet-local member display-name primitive.
//
// The frozen Store contract (contract.go) deliberately keeps AddMember's
// signature as (orgID, accountID, role) — adding a name parameter would break
// every implementation and call site. Instead this file adds a narrow optional
// MemberNamer seam (mirroring MemberRemover in remove_member.go) so callers can
// set/refresh a member's display name without touching the frozen interface.
//
// Source-of-truth: the name is stored fleet-local on the membership row because
// auth.users is email+password only (LOCKED) and has no canonical display/full
// name to join against. Set it at invite/accept/add time from the invited
// user's name when known; otherwise it stays empty and the org-admin Members
// adapter falls back to the member's email.
package fleet

import (
	"context"
	"fmt"
)

// MemberNamer is the optional display-name seam. SQLStore and MemStore satisfy
// it. Callers that hold a fleet.Store may type-assert to MemberNamer to set a
// member's fleet-local display name without the frozen Store interface having
// to carry the method.
type MemberNamer interface {
	// SetDisplayName sets the fleet-local display name for (orgID, accountID).
	// An empty name clears it (the adapter then falls back to email). Returns
	// ErrNotMember when the (orgID, accountID) pair does not exist.
	SetDisplayName(ctx context.Context, orgID, accountID, displayName string) error
}

// compile-time assertions: both concrete stores satisfy MemberNamer.
var (
	_ MemberNamer = (*SQLStore)(nil)
	_ MemberNamer = (*MemStore)(nil)
)

// SetDisplayName implements MemberNamer for the SQL-backed store.
func (s *SQLStore) SetDisplayName(ctx context.Context, orgID, accountID, displayName string) error {
	s.mu.Lock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE fleet_org_members SET display_name = ? WHERE org_id = ? AND account_id = ?`),
		displayName, orgID, accountID,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("fleet: set display name: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("fleet: set display name rows: %w", err)
	}
	if n == 0 {
		return ErrNotMember
	}
	return nil
}

// SetDisplayName implements MemberNamer for the in-memory reference store.
func (m *MemStore) SetDisplayName(_ context.Context, orgID, accountID, displayName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.members[orgID][accountID]
	if !ok {
		return ErrNotMember
	}
	mem.DisplayName = displayName
	m.members[orgID][accountID] = mem
	return nil
}
