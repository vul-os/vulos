package streamsession

import (
	"context"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	cdb, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := Open(cdb)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestStore_RoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	in := Session{
		ID:            id,
		AccountID:     "acct-1",
		HostKind:      HostKindCloudGPU,
		HostULID:      "ulid-host-1",
		GPUType:       "a10",
		RelayEndpoint: "https://relay.example/",
	}
	out, err := st.Insert(ctx, in)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if out.State != StateAllocating {
		t.Fatalf("default state = %q, want allocating", out.State)
	}

	got, err := st.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AccountID != "acct-1" || got.HostKind != HostKindCloudGPU ||
		got.HostULID != "ulid-host-1" || got.GPUType != "a10" ||
		got.RelayEndpoint != "https://relay.example/" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	if err := st.SetState(ctx, id, StateActive); err != nil {
		t.Fatalf("SetState active: %v", err)
	}
	got, _ = st.Get(ctx, id)
	if got.State != StateActive {
		t.Fatalf("state after active = %q", got.State)
	}

	if err := st.SetState(ctx, id, StateTornDown); err != nil {
		t.Fatalf("SetState torn_down: %v", err)
	}
	got, _ = st.Get(ctx, id)
	if got.State != StateTornDown {
		t.Fatalf("state after teardown = %q", got.State)
	}
	if got.ClosedAt == nil {
		t.Fatalf("expected ClosedAt set after teardown")
	}
}

// TestStore_GamingRoundTripAndCount verifies the gaming flag round-trips and
// CountActiveGamingByAccount counts only non-terminal gaming sessions.
func TestStore_GamingRoundTripAndCount(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	mk := func(acct string, gaming bool, state SessionState) string {
		id, _ := NewID()
		s := Session{
			ID: id, AccountID: acct, HostKind: HostKindCloudGPU,
			HostULID: "ulid-" + id, GPUType: "l40s", RelayEndpoint: "https://r/",
			Gaming: gaming, State: state,
		}
		if _, err := st.Insert(ctx, s); err != nil {
			t.Fatalf("insert: %v", err)
		}
		return id
	}

	// Gaming flag round-trips.
	id := mk("acct-1", true, StateActive)
	got, err := st.Get(ctx, id)
	if err != nil || !got.Gaming {
		t.Fatalf("gaming flag not persisted: %+v err=%v", got, err)
	}

	// Add: one allocating gaming (counts), one torn-down gaming (excluded),
	// one active NON-gaming (excluded), and another account's gaming (excluded).
	mk("acct-1", true, StateAllocating)
	mk("acct-1", true, StateTornDown)
	mk("acct-1", false, StateActive)
	mk("acct-2", true, StateActive)

	n, err := st.CountActiveGamingByAccount(ctx, "acct-1")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	// active + allocating gaming for acct-1 = 2.
	if n != 2 {
		t.Fatalf("CountActiveGamingByAccount(acct-1)=%d, want 2", n)
	}
	if n2, _ := st.CountActiveGamingByAccount(ctx, "acct-2"); n2 != 1 {
		t.Fatalf("CountActiveGamingByAccount(acct-2)=%d, want 1", n2)
	}
	if n3, _ := st.CountActiveGamingByAccount(ctx, "no-acct"); n3 != 0 {
		t.Fatalf("CountActiveGamingByAccount(no-acct)=%d, want 0", n3)
	}
}

func TestStore_GetNotFound(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.Get(context.Background(), "nope"); err != ErrSessionNotFound {
		t.Fatalf("want ErrSessionNotFound, got %v", err)
	}
}

func TestStore_InsertValidation(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	cases := []Session{
		{ID: "", AccountID: "a", HostULID: "u", HostKind: HostKindCloudGPU},
		{ID: "i", AccountID: "", HostULID: "u", HostKind: HostKindCloudGPU},
		{ID: "i", AccountID: "a", HostULID: "", HostKind: HostKindCloudGPU},
		{ID: "i", AccountID: "a", HostULID: "u", HostKind: HostKind("garbage")},
	}
	for i, c := range cases {
		if _, err := st.Insert(ctx, c); err == nil {
			t.Fatalf("case %d expected error, got nil", i)
		}
	}
}

func TestStore_ListByAccount(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		id, _ := NewID()
		_, err := st.Insert(ctx, Session{
			ID: id, AccountID: "acct-2", HostKind: HostKindBYOGPU,
			HostULID: "box-x", RelayEndpoint: "https://r/",
		})
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	rows, err := st.ListByAccount(ctx, "acct-2", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len=%d, want 3", len(rows))
	}
	rows2, _ := st.ListByAccount(ctx, "no-such-account", 10)
	if len(rows2) != 0 {
		t.Fatalf("foreign account leaked rows: %d", len(rows2))
	}
}
