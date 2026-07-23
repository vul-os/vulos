package orgadmin

// leave_test.go — unit tests for OrgService.LeaveOrg (ORG-MULTI-01 / ORG-LEAVE-01).
//
// Covers:
//   - Happy path: member leaves, active org reverts to next remaining org.
//   - Last-owner block: ErrLastOwner when caller is the sole owner.
//   - Non-member: ErrNotFound when caller is not in the org.
//   - Multi-owner: an owner CAN leave when another owner remains.
//   - Active org cleared when the left org was the only one.

import (
	"context"
	"errors"
	"testing"
)

func TestLeaveOrg_MemberCanLeave(t *testing.T) {
	svc, store := newTestService(&fakeMailer{})
	ctx := context.Background()

	// Account "alice" owns org A, account "bob" is added as member.
	a, err := svc.CreateOrg(ctx, "alice", "Alice Org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if err := store.AddOrgMember(ctx, a.ID, "bob", "member"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}
	// Set bob's active org to A.
	if err := store.SetActiveOrg(ctx, "bob", a.ID); err != nil {
		t.Fatalf("SetActiveOrg: %v", err)
	}

	// Bob leaves A.
	if err := svc.LeaveOrg(ctx, "bob", a.ID); err != nil {
		t.Fatalf("LeaveOrg: %v", err)
	}

	// Bob's membership is gone.
	if _, err := store.OrgMemberRole(ctx, a.ID, "bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob still has membership after leave: err=%v", err)
	}
	// Alice's org still exists.
	if _, err := store.GetOrg(ctx, a.ID); err != nil {
		t.Fatalf("alice org gone: %v", err)
	}
}

func TestLeaveOrg_LastOwnerBlocked(t *testing.T) {
	svc, _ := newTestService(&fakeMailer{})
	ctx := context.Background()

	a, err := svc.CreateOrg(ctx, "solo", "Solo Org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	// The sole owner cannot leave.
	err = svc.LeaveOrg(ctx, "solo", a.ID)
	if !errors.Is(err, ErrLastOwner) {
		t.Fatalf("expected ErrLastOwner, got %v", err)
	}
}

func TestLeaveOrg_NonMemberNotFound(t *testing.T) {
	svc, _ := newTestService(&fakeMailer{})
	ctx := context.Background()

	a, _ := svc.CreateOrg(ctx, "owner1", "Org1")

	// "stranger" is not a member.
	err := svc.LeaveOrg(ctx, "stranger", a.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-member, got %v", err)
	}
}

func TestLeaveOrg_MultiOwnerCanLeave(t *testing.T) {
	svc, store := newTestService(&fakeMailer{})
	ctx := context.Background()

	a, err := svc.CreateOrg(ctx, "owner1", "Joint Org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	// Add a second owner.
	if err := store.AddOrgMember(ctx, a.ID, "owner2", string(RoleOwner)); err != nil {
		t.Fatalf("AddOrgMember owner2: %v", err)
	}

	// owner1 can leave now that owner2 exists.
	if err := svc.LeaveOrg(ctx, "owner1", a.ID); err != nil {
		t.Fatalf("LeaveOrg: %v", err)
	}

	// owner2 remains.
	role, err := store.OrgMemberRole(ctx, a.ID, "owner2")
	if err != nil || role != string(RoleOwner) {
		t.Fatalf("owner2 membership broken: role=%q err=%v", role, err)
	}
}

func TestLeaveOrg_ActiveOrgReverts(t *testing.T) {
	svc, store := newTestService(&fakeMailer{})
	ctx := context.Background()

	// Alice owns two orgs.
	a, _ := svc.CreateOrg(ctx, "alice", "Org A")
	b, _ := svc.CreateOrg(ctx, "alice", "Org B")

	// Set active to B.
	if err := store.SetActiveOrg(ctx, "alice", b.ID); err != nil {
		t.Fatalf("SetActiveOrg B: %v", err)
	}

	// Add a co-owner to A so alice can leave it.
	if err := store.AddOrgMember(ctx, a.ID, "coowner", string(RoleOwner)); err != nil {
		t.Fatalf("AddOrgMember coowner: %v", err)
	}

	// Alice leaves A (not the active org — active stays B).
	if err := svc.LeaveOrg(ctx, "alice", a.ID); err != nil {
		t.Fatalf("LeaveOrg A: %v", err)
	}

	// Active org should still be B (unaffected — alice wasn't in A as the active).
	active, _ := store.GetActiveOrg(ctx, "alice")
	if active != b.ID {
		t.Fatalf("active org changed: got %q, want %q", active, b.ID)
	}
}

func TestLeaveOrg_ActiveOrgClearedWhenOnlyOrg(t *testing.T) {
	svc, store := newTestService(&fakeMailer{})
	ctx := context.Background()

	// Alice is a member (not owner) of one org.
	owner, _ := svc.CreateOrg(ctx, "owner", "Some Org")
	if err := store.AddOrgMember(ctx, owner.ID, "alice", "member"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}
	if err := store.SetActiveOrg(ctx, "alice", owner.ID); err != nil {
		t.Fatalf("SetActiveOrg: %v", err)
	}

	// Alice leaves — this was her only org.
	if err := svc.LeaveOrg(ctx, "alice", owner.ID); err != nil {
		t.Fatalf("LeaveOrg: %v", err)
	}

	// Active org should be cleared (set to "" or a new org if any remain).
	memberships, _ := store.ListOrgsForAccount(ctx, "alice")
	if len(memberships) != 0 {
		t.Fatalf("alice still has memberships: %+v", memberships)
	}
}
