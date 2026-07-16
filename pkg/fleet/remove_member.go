// remove_member.go — tenant-scoped member removal primitive.
//
// The frozen Store contract (contract.go) deliberately has no member-removal
// method; adding it to the interface would force every existing implementation
// + test fake to grow a method. Instead this file adds RemoveMember to the two
// concrete stores (SQLStore + MemStore) and exposes a narrow optional
// MemberRemover interface that callers (e.g. the org-admin adapter) type-assert
// against. This keeps the frozen interface untouched while giving real removal.
//
// Invariants:
//   - Tenant-scoped: a removal only affects (orgID, accountID); a wrong orgID
//     never deletes another org's row.
//   - Last-owner guard: the final remaining "owner" of an org cannot be removed
//     (ErrLastOwner) — an org must always retain at least one owner.
//   - Unknown member / org → ErrNotMember (mirrors SetRole's not-found shape).
package fleet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrLastOwner is returned by RemoveMember when removing the target would leave
// the org with no owner. Callers map this to a 4xx (the action is refused, not
// a server fault).
var ErrLastOwner = errors.New("fleet: cannot remove the last owner of an org")

// MemberRemover is the optional removal seam. SQLStore and MemStore satisfy it.
// Callers that hold a fleet.Store may type-assert to MemberRemover to perform a
// removal without the frozen Store interface having to carry the method.
type MemberRemover interface {
	// RemoveMember deletes (orgID, accountID) from the org roster. Returns
	// ErrNotMember when the pair does not exist, ErrLastOwner when the target is
	// the org's only remaining owner.
	RemoveMember(ctx context.Context, orgID, accountID string) error
}

// compile-time assertions: both concrete stores satisfy MemberRemover.
var (
	_ MemberRemover = (*SQLStore)(nil)
	_ MemberRemover = (*MemStore)(nil)
)

// RemoveMember implements MemberRemover for the SQL-backed store. The
// last-owner check + delete run inside one transaction so a concurrent removal
// can't race two owners out simultaneously.
func (s *SQLStore) RemoveMember(ctx context.Context, orgID, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fleet: remove member begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Resolve the target's role (also acts as the existence check).
	var role string
	err = tx.QueryRowContext(ctx,
		s.db.Rebind(`SELECT role FROM fleet_org_members WHERE org_id = ? AND account_id = ?`),
		orgID, accountID,
	).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotMember
	}
	if err != nil {
		return fmt.Errorf("fleet: remove member lookup: %w", err)
	}

	// Last-owner guard: if the target is an owner and is the only owner left,
	// refuse the removal.
	if role == "owner" {
		var owners int
		if err := tx.QueryRowContext(ctx,
			s.db.Rebind(`SELECT COUNT(*) FROM fleet_org_members WHERE org_id = ? AND role = 'owner'`),
			orgID,
		).Scan(&owners); err != nil {
			return fmt.Errorf("fleet: remove member owner count: %w", err)
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}

	if _, err := tx.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM fleet_org_members WHERE org_id = ? AND account_id = ?`),
		orgID, accountID,
	); err != nil {
		return fmt.Errorf("fleet: remove member delete: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fleet: remove member commit: %w", err)
	}
	return nil
}

// RemoveMember implements MemberRemover for the in-memory reference store.
func (m *MemStore) RemoveMember(_ context.Context, orgID, accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	roster, ok := m.members[orgID]
	if !ok {
		return ErrNotMember
	}
	target, ok := roster[accountID]
	if !ok {
		return ErrNotMember
	}
	if target.Role == "owner" {
		owners := 0
		for _, mem := range roster {
			if mem.Role == "owner" {
				owners++
			}
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}
	delete(roster, accountID)
	return nil
}
