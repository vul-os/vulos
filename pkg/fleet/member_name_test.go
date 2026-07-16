package fleet_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/fleet"
)

// namerFactories runs the display-name tests against both MemStore and SQLStore
// with identical assertions (mirrors removerFactories).
var namerFactories = []struct {
	name    string
	newFunc func(t *testing.T) fleet.Store
}{
	{"MemStore", func(t *testing.T) fleet.Store { return fleet.NewMemStore() }},
	{"SQLStore", func(t *testing.T) fleet.Store {
		dsn := fmt.Sprintf("file:fleet_name_%d?mode=memory&cache=shared", sqlSeq.Add(1))
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

// TestMemberDisplayName verifies the fleet-local display_name is persisted via
// the MemberNamer seam and returned by Members().
func TestMemberDisplayName(t *testing.T) {
	for _, sf := range namerFactories {
		sf := sf
		t.Run(sf.name, func(t *testing.T) {
			ctx := context.Background()
			st := sf.newFunc(t)
			namer, ok := st.(fleet.MemberNamer)
			if !ok {
				t.Fatalf("%s does not implement MemberNamer", sf.name)
			}

			org, err := st.CreateOrg(ctx, "Acme", "owner-1")
			if err != nil {
				t.Fatalf("create org: %v", err)
			}
			if err := st.AddMember(ctx, org.ID, "member-1", "member"); err != nil {
				t.Fatalf("add member: %v", err)
			}

			// Default: no name set yet → empty (adapter falls back to email).
			members, err := st.Members(ctx, org.ID)
			if err != nil {
				t.Fatalf("members: %v", err)
			}
			if got := findMember(members, "member-1").DisplayName; got != "" {
				t.Fatalf("expected empty display name before set, got %q", got)
			}

			// Set a display name and confirm it persists + is returned.
			if err := namer.SetDisplayName(ctx, org.ID, "member-1", "Ada Lovelace"); err != nil {
				t.Fatalf("set display name: %v", err)
			}
			members, err = st.Members(ctx, org.ID)
			if err != nil {
				t.Fatalf("members after set: %v", err)
			}
			if got := findMember(members, "member-1").DisplayName; got != "Ada Lovelace" {
				t.Fatalf("expected display name persisted, got %q", got)
			}

			// The owner (added at CreateOrg) has no name → empty, independent of
			// member-1's name.
			if got := findMember(members, "owner-1").DisplayName; got != "" {
				t.Fatalf("owner display name should be empty, got %q", got)
			}

			// Clearing the name (empty string) is allowed and returns empty.
			if err := namer.SetDisplayName(ctx, org.ID, "member-1", ""); err != nil {
				t.Fatalf("clear display name: %v", err)
			}
			members, err = st.Members(ctx, org.ID)
			if err != nil {
				t.Fatalf("members after clear: %v", err)
			}
			if got := findMember(members, "member-1").DisplayName; got != "" {
				t.Fatalf("expected display name cleared, got %q", got)
			}

			// Setting a name on a non-member → ErrNotMember.
			if err := namer.SetDisplayName(ctx, org.ID, "ghost", "Nobody"); !errors.Is(err, fleet.ErrNotMember) {
				t.Fatalf("expected ErrNotMember for unknown member, got %v", err)
			}
		})
	}
}

func findMember(members []fleet.Member, accountID string) fleet.Member {
	for _, m := range members {
		if m.AccountID == accountID {
			return m
		}
	}
	return fleet.Member{}
}

// TestNamedInvite verifies the MemberInviter seam: an invite created with a
// display name persists that name (returned by GetInvite + ListInvites), while
// the frozen 3-arg CreateInvite leaves it empty.
func TestNamedInvite(t *testing.T) {
	for _, sf := range namerFactories {
		sf := sf
		t.Run(sf.name, func(t *testing.T) {
			ctx := context.Background()
			st := sf.newFunc(t)
			inviter, ok := st.(fleet.MemberInviter)
			if !ok {
				t.Fatalf("%s does not implement MemberInviter", sf.name)
			}

			org, err := st.CreateOrg(ctx, "Acme", "owner-1")
			if err != nil {
				t.Fatalf("create org: %v", err)
			}

			// Named invite persists the name.
			inv, err := inviter.CreateInviteWithName(ctx, org.ID, "ada@x.io", "member", "Ada Lovelace")
			if err != nil {
				t.Fatalf("create named invite: %v", err)
			}
			if inv.DisplayName != "Ada Lovelace" || inv.State != "pending" {
				t.Fatalf("named invite wrong: %#v", inv)
			}
			got, err := st.GetInvite(ctx, inv.ID)
			if err != nil {
				t.Fatalf("get invite: %v", err)
			}
			if got.DisplayName != "Ada Lovelace" {
				t.Fatalf("GetInvite name = %q, want %q", got.DisplayName, "Ada Lovelace")
			}
			list, err := st.ListInvites(ctx, org.ID)
			if err != nil {
				t.Fatalf("list invites: %v", err)
			}
			if len(list) != 1 || list[0].DisplayName != "Ada Lovelace" {
				t.Fatalf("ListInvites name wrong: %#v", list)
			}

			// Frozen 3-arg CreateInvite leaves the name empty.
			plain, err := st.CreateInvite(ctx, org.ID, "bob@x.io", "member")
			if err != nil {
				t.Fatalf("create plain invite: %v", err)
			}
			if plain.DisplayName != "" {
				t.Fatalf("plain invite name = %q, want empty", plain.DisplayName)
			}
		})
	}
}

// TestNamedInviteApplyToMember verifies the full contract end to end: a named
// invite's stored name, applied via SetDisplayName when the member is created,
// surfaces on the member row (the name the org-admin Members tab shows). An
// empty-named invite leaves the member name empty (adapter falls back to email).
func TestNamedInviteApplyToMember(t *testing.T) {
	for _, sf := range namerFactories {
		sf := sf
		t.Run(sf.name, func(t *testing.T) {
			ctx := context.Background()
			st := sf.newFunc(t)
			inviter := st.(fleet.MemberInviter)
			namer := st.(fleet.MemberNamer)

			org, err := st.CreateOrg(ctx, "Acme", "owner-1")
			if err != nil {
				t.Fatalf("create org: %v", err)
			}

			// Named invite → member created → apply the invite's name.
			inv, err := inviter.CreateInviteWithName(ctx, org.ID, "ada@x.io", "member", "Ada Lovelace")
			if err != nil {
				t.Fatalf("named invite: %v", err)
			}
			if err := st.AddMember(ctx, org.ID, "member-ada", "member"); err != nil {
				t.Fatalf("add member: %v", err)
			}
			if err := namer.SetDisplayName(ctx, org.ID, "member-ada", inv.DisplayName); err != nil {
				t.Fatalf("apply invite name: %v", err)
			}
			members, err := st.Members(ctx, org.ID)
			if err != nil {
				t.Fatalf("members: %v", err)
			}
			if got := findMember(members, "member-ada").DisplayName; got != "Ada Lovelace" {
				t.Fatalf("member name from invite = %q, want %q", got, "Ada Lovelace")
			}

			// Empty-named invite → member name stays empty (email fallback).
			plain, err := inviter.CreateInviteWithName(ctx, org.ID, "bob@x.io", "member", "")
			if err != nil {
				t.Fatalf("plain invite: %v", err)
			}
			if err := st.AddMember(ctx, org.ID, "member-bob", "member"); err != nil {
				t.Fatalf("add member: %v", err)
			}
			if err := namer.SetDisplayName(ctx, org.ID, "member-bob", plain.DisplayName); err != nil {
				t.Fatalf("apply empty invite name: %v", err)
			}
			members, _ = st.Members(ctx, org.ID)
			if got := findMember(members, "member-bob").DisplayName; got != "" {
				t.Fatalf("member name from empty invite = %q, want empty", got)
			}
		})
	}
}
