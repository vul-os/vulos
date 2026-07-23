package fleet_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/fleet"
)

// acceptFactories runs the accept-seam tests against both stores.
var acceptFactories = []struct {
	name    string
	newFunc func(t *testing.T) fleet.Store
}{
	{"MemStore", func(t *testing.T) fleet.Store { return fleet.NewMemStore() }},
	{"SQLStore", func(t *testing.T) fleet.Store {
		dsn := fmt.Sprintf("file:fleet_accept_%d?mode=memory&cache=shared", sqlSeq.Add(1))
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

// TestMarkInviteAccepted verifies the pending→accepted transition and its
// guards on both stores.
func TestMarkInviteAccepted(t *testing.T) {
	for _, sf := range acceptFactories {
		sf := sf
		t.Run(sf.name, func(t *testing.T) {
			ctx := context.Background()
			st := sf.newFunc(t)
			acc, ok := st.(fleet.InviteAcceptor)
			if !ok {
				t.Fatalf("%s does not implement InviteAcceptor", sf.name)
			}

			org, err := st.CreateOrg(ctx, "Acme", "owner-1")
			if err != nil {
				t.Fatalf("create org: %v", err)
			}
			inv, err := st.CreateInvite(ctx, org.ID, "ada@x.io", "member")
			if err != nil {
				t.Fatalf("create invite: %v", err)
			}

			// Unknown id → ErrInviteNotFound.
			if err := acc.MarkInviteAccepted(ctx, "no-such-id"); !errors.Is(err, fleet.ErrInviteNotFound) {
				t.Fatalf("unknown id: want ErrInviteNotFound, got %v", err)
			}

			// First accept succeeds; the invite is now "accepted".
			if err := acc.MarkInviteAccepted(ctx, inv.ID); err != nil {
				t.Fatalf("first accept: %v", err)
			}
			got, err := st.GetInvite(ctx, inv.ID)
			if err != nil {
				t.Fatalf("get invite: %v", err)
			}
			if got.State != "accepted" {
				t.Fatalf("state = %q, want accepted", got.State)
			}

			// Second accept → ErrInviteNotPending (idempotent guard).
			if err := acc.MarkInviteAccepted(ctx, inv.ID); !errors.Is(err, fleet.ErrInviteNotPending) {
				t.Fatalf("double accept: want ErrInviteNotPending, got %v", err)
			}

			// A revoked invite also rejects accept.
			rev, err := st.CreateInvite(ctx, org.ID, "bob@x.io", "member")
			if err != nil {
				t.Fatalf("create invite 2: %v", err)
			}
			if err := st.RevokeInvite(ctx, rev.ID); err != nil {
				t.Fatalf("revoke: %v", err)
			}
			if err := acc.MarkInviteAccepted(ctx, rev.ID); !errors.Is(err, fleet.ErrInviteNotPending) {
				t.Fatalf("accept revoked: want ErrInviteNotPending, got %v", err)
			}
		})
	}
}

// TestIsInviteExpired verifies the TTL policy used by the accept handler.
func TestIsInviteExpired(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)

	// Zero CreatedAt → never expired (defensive).
	if fleet.IsInviteExpired(fleet.Invite{}, now) {
		t.Fatal("zero CreatedAt should not be expired")
	}
	// Fresh invite (just created) → not expired.
	fresh := fleet.Invite{CreatedAt: now.Add(-1 * time.Hour)}
	if fleet.IsInviteExpired(fresh, now) {
		t.Fatal("1h-old invite should not be expired with 14d TTL")
	}
	// Just inside the window.
	inside := fleet.Invite{CreatedAt: now.Add(-fleet.InviteExpiry + time.Minute)}
	if fleet.IsInviteExpired(inside, now) {
		t.Fatal("invite just inside TTL should not be expired")
	}
	// Past the window → expired.
	stale := fleet.Invite{CreatedAt: now.Add(-fleet.InviteExpiry - time.Minute)}
	if !fleet.IsInviteExpired(stale, now) {
		t.Fatal("invite past TTL should be expired")
	}
}
