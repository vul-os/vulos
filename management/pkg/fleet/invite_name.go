// invite_name.go — fleet-local named-invite primitive.
//
// The frozen Store contract (contract.go) keeps CreateInvite's signature at
// (orgID, email, role) — adding a name parameter would break every existing
// caller (e.g. cmd/server/routes_fleet.go createInvite) and the frozen
// interface. Instead this file adds a narrow optional MemberInviter seam
// (mirroring MemberRemover in remove_member.go and MemberNamer in
// member_name.go) so a caller that knows the invited user's name can persist it
// on the invite row without touching the frozen interface.
//
// Flow: orgadmin POST /api/org/invite {name} → CreateInviteWithName stores the
// name on fleet_invites.display_name. When the membership row is later created
// from the invite, the stored name is applied via MemberNamer.SetDisplayName so
// the Members tab shows a real name instead of the email fallback. (There is no
// invite-accept→member flow in the codebase today — member rows are created via
// CreateOrg + the direct AddMember route — so the orgadmin plane ALSO exposes
// PUT /api/org/members/{id}/name to set the name directly; see wire_orgadmin.go
// and internal/orgadmin.)
package fleet

import (
	"context"
	"fmt"
	"time"
)

// MemberInviter is the optional named-invite seam. SQLStore and MemStore satisfy
// it. Callers that hold a fleet.Store may type-assert to MemberInviter to create
// an invite that carries the invited user's display name, without the frozen
// Store interface having to grow CreateInvite's signature.
type MemberInviter interface {
	// CreateInviteWithName creates a pending invite carrying displayName. An
	// empty displayName behaves exactly like the frozen 3-arg CreateInvite.
	// Returns ErrInvalidRole / ErrOrgNotFound on the same conditions.
	CreateInviteWithName(ctx context.Context, orgID, email, role, displayName string) (Invite, error)
}

// compile-time assertions: both concrete stores satisfy MemberInviter.
var (
	_ MemberInviter = (*SQLStore)(nil)
	_ MemberInviter = (*MemStore)(nil)
)

// CreateInviteWithName implements MemberInviter for the SQL-backed store. It is
// the single insert path; the frozen CreateInvite delegates here with an empty
// name so both share one query and stay behaviourally identical.
func (s *SQLStore) CreateInviteWithName(ctx context.Context, orgID, email, role, displayName string) (Invite, error) {
	if !ValidRoles[role] {
		return Invite{}, ErrInvalidRole
	}
	if _, err := s.GetOrg(ctx, orgID); err != nil {
		return Invite{}, err
	}
	id := genULID()
	ts := now()
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO fleet_invites (id, org_id, email, role, state, created_at, display_name)
		 VALUES (?, ?, ?, ?, 'pending', ?, ?)`),
		id, orgID, email, role, ts, displayName,
	)
	s.mu.Unlock()
	if err != nil {
		return Invite{}, fmt.Errorf("fleet: create invite: %w", err)
	}
	return Invite{
		ID:          id,
		OrgID:       orgID,
		Email:       email,
		Role:        role,
		State:       "pending",
		CreatedAt:   parseTime(ts),
		DisplayName: displayName,
	}, nil
}

// CreateInviteWithName implements MemberInviter for the in-memory reference
// store. The frozen CreateInvite delegates here with an empty name.
func (m *MemStore) CreateInviteWithName(_ context.Context, orgID, email, role, displayName string) (Invite, error) {
	if !ValidRoles[role] {
		return Invite{}, ErrInvalidRole
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.orgs[orgID]; !ok {
		return Invite{}, ErrOrgNotFound
	}
	inv := Invite{
		ID:          newOrgULID(),
		OrgID:       orgID,
		Email:       email,
		Role:        role,
		State:       "pending",
		CreatedAt:   time.Now().UTC(),
		DisplayName: displayName,
	}
	m.invites[inv.ID] = inv
	return inv, nil
}
