// store_pg_test.go — Postgres integration tests for streamsession.
// Skipped when DATABASE_URL / VULOS_DATABASE_URL is not set.
package streamsession

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openTestStorePG(t *testing.T) *Store {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" && os.Getenv("VULOS_DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set; skipping Postgres test")
	}
	db, err := cpdb.Open("streamsession_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := Open(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("Open: %v", err)
	}
	// Shared fixed schema + absolute-count assertions: reset the table so a
	// repeated run (go test -count=2) doesn't accumulate rows and double counts.
	if _, err := db.ExecContext(context.Background(), `TRUNCATE stream_sessions`); err != nil {
		_ = db.Close()
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestPG_Insert_And_Get(t *testing.T) {
	st := openTestStorePG(t)
	ctx := context.Background()

	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	in := Session{
		ID:            id,
		AccountID:     "pg-acct-1",
		HostKind:      HostKindCloudGPU,
		HostULID:      "ulid-pg-host-1",
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
	if got.AccountID != "pg-acct-1" || got.HostKind != HostKindCloudGPU {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestPG_SetState_Lifecycle(t *testing.T) {
	st := openTestStorePG(t)
	ctx := context.Background()

	id, _ := NewID()
	_, err := st.Insert(ctx, Session{
		ID: id, AccountID: "pg-acct-2", HostKind: HostKindBYOGPU,
		HostULID: "box-pg-1", RelayEndpoint: "https://r/",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := st.SetState(ctx, id, StateActive); err != nil {
		t.Fatalf("SetState active: %v", err)
	}
	got, _ := st.Get(ctx, id)
	if got.State != StateActive {
		t.Fatalf("state = %q, want active", got.State)
	}

	if err := st.SetState(ctx, id, StateTornDown); err != nil {
		t.Fatalf("SetState torn_down: %v", err)
	}
	got, _ = st.Get(ctx, id)
	if got.State != StateTornDown {
		t.Fatalf("state = %q, want torn_down", got.State)
	}
	if got.ClosedAt == nil {
		t.Fatal("ClosedAt should be set after torn_down")
	}
}

func TestPG_GetNotFound(t *testing.T) {
	st := openTestStorePG(t)
	if _, err := st.Get(context.Background(), "no-such-pg-id"); err != ErrSessionNotFound {
		t.Fatalf("want ErrSessionNotFound, got %v", err)
	}
}

func TestPG_ListByAccount(t *testing.T) {
	st := openTestStorePG(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		id, _ := NewID()
		_, err := st.Insert(ctx, Session{
			ID: id, AccountID: "pg-list-acct", HostKind: HostKindBYOGPU,
			HostULID: "box-pg-2", RelayEndpoint: "https://r/",
		})
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	rows, err := st.ListByAccount(ctx, "pg-list-acct", 10)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len=%d, want 3", len(rows))
	}
}
