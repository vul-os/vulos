package fleet_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/fleet"
)

// removerFactories pairs a name with a constructor returning a MemberRemover so
// the removal tests run against both MemStore and SQLStore with identical
// assertions.
var removerFactories = []struct {
	name    string
	newFunc func(t *testing.T) fleet.Store
}{
	{"MemStore", func(t *testing.T) fleet.Store { return fleet.NewMemStore() }},
	{"SQLStore", func(t *testing.T) fleet.Store {
		dsn := fmt.Sprintf("file:fleet_remove_%d?mode=memory&cache=shared", sqlSeq.Add(1))
		db, err := cpdb.OpenSQLiteDSN(dsn)
		if err != nil {
			t.Fatalf("open cpdb: %v", err)
		}
		s, err := fleet.Open(db)
		if err != nil {
			t.Fatalf("open fleet store: %v", err)
		}
		return s
	}},
}

func TestRemoveMember(t *testing.T) {
	for _, sf := range removerFactories {
		sf := sf
		t.Run(sf.name, func(t *testing.T) {
			ctx := context.Background()
			st := sf.newFunc(t)
			remover, ok := st.(fleet.MemberRemover)
			if !ok {
				t.Fatalf("%s does not implement MemberRemover", sf.name)
			}

			org, err := st.CreateOrg(ctx, "Acme", "owner-1") // owner-1 auto-owner
			if err != nil {
				t.Fatalf("create org: %v", err)
			}
			if err := st.AddMember(ctx, org.ID, "admin-1", "admin"); err != nil {
				t.Fatalf("add admin: %v", err)
			}
			if err := st.AddMember(ctx, org.ID, "member-1", "member"); err != nil {
				t.Fatalf("add member: %v", err)
			}

			// Remove a regular member → succeeds, roster shrinks.
			if err := remover.RemoveMember(ctx, org.ID, "member-1"); err != nil {
				t.Fatalf("remove member-1: %v", err)
			}
			if _, err := st.MemberRole(ctx, org.ID, "member-1"); !errors.Is(err, fleet.ErrNotMember) {
				t.Fatalf("member-1 should be gone, got err=%v", err)
			}

			// Removing an unknown member → ErrNotMember.
			if err := remover.RemoveMember(ctx, org.ID, "nope"); !errors.Is(err, fleet.ErrNotMember) {
				t.Fatalf("remove unknown: want ErrNotMember, got %v", err)
			}

			// Last-owner guard: owner-1 is the only owner → refused.
			if err := remover.RemoveMember(ctx, org.ID, "owner-1"); !errors.Is(err, fleet.ErrLastOwner) {
				t.Fatalf("remove last owner: want ErrLastOwner, got %v", err)
			}
			// owner-1 must still be present after the refusal.
			if role, err := st.MemberRole(ctx, org.ID, "owner-1"); err != nil || role != "owner" {
				t.Fatalf("owner-1 should remain owner, role=%q err=%v", role, err)
			}

			// Promote admin-1 to owner → now two owners → removing one is allowed.
			if err := st.SetRole(ctx, org.ID, "admin-1", "owner"); err != nil {
				t.Fatalf("promote admin-1: %v", err)
			}
			if err := remover.RemoveMember(ctx, org.ID, "owner-1"); err != nil {
				t.Fatalf("remove owner-1 with a co-owner present: %v", err)
			}
			if role, err := st.MemberRole(ctx, org.ID, "admin-1"); err != nil || role != "owner" {
				t.Fatalf("admin-1 should remain the surviving owner, role=%q err=%v", role, err)
			}
		})
	}
}

// TestRemoveMember_TenantScoped verifies a removal scoped to one org never
// affects another org's identically-keyed member.
func TestRemoveMember_TenantScoped(t *testing.T) {
	for _, sf := range removerFactories {
		sf := sf
		t.Run(sf.name, func(t *testing.T) {
			ctx := context.Background()
			st := sf.newFunc(t)
			remover := st.(fleet.MemberRemover)

			orgA, _ := st.CreateOrg(ctx, "A", "shared-acct")
			orgB, _ := st.CreateOrg(ctx, "B", "shared-acct")
			// Add a second owner to each so the shared-acct owner is removable.
			_ = st.AddMember(ctx, orgA.ID, "owner-a2", "owner")
			_ = st.AddMember(ctx, orgB.ID, "owner-b2", "owner")

			// Remove shared-acct from org A only.
			if err := remover.RemoveMember(ctx, orgA.ID, "shared-acct"); err != nil {
				t.Fatalf("remove from org A: %v", err)
			}
			if _, err := st.MemberRole(ctx, orgA.ID, "shared-acct"); !errors.Is(err, fleet.ErrNotMember) {
				t.Fatalf("shared-acct should be gone from org A, err=%v", err)
			}
			// org B's shared-acct membership is untouched.
			if role, err := st.MemberRole(ctx, orgB.ID, "shared-acct"); err != nil || role != "owner" {
				t.Fatalf("shared-acct should remain in org B, role=%q err=%v", role, err)
			}
		})
	}
}
