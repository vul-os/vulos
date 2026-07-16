package compliance

import (
	"context"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func TestSQLStore_RecordAndList(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := Open(db)
	if err != nil {
		db.Close() //nolint:errcheck
		t.Fatalf("Open: %v", err)
	}
	defer st.Close() //nolint:errcheck
	ctx := context.Background()

	r1, err := st.Record(ctx, "acct-1", KindExport, "")
	if err != nil {
		t.Fatalf("Record export: %v", err)
	}
	if r1.ID == "" || r1.Status != StatusReceived || r1.Kind != KindExport {
		t.Fatalf("unexpected request: %+v", r1)
	}
	if _, err := st.Record(ctx, "acct-1", KindErasure, "please delete"); err != nil {
		t.Fatalf("Record erasure: %v", err)
	}
	// Different account — must not leak across accounts.
	if _, err := st.Record(ctx, "acct-2", KindExport, ""); err != nil {
		t.Fatalf("Record other account: %v", err)
	}

	list, err := st.ListByAccount(ctx, "acct-1")
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 requests for acct-1, got %d", len(list))
	}

	other, err := st.ListByAccount(ctx, "acct-2")
	if err != nil {
		t.Fatalf("ListByAccount acct-2: %v", err)
	}
	if len(other) != 1 {
		t.Fatalf("want 1 request for acct-2, got %d", len(other))
	}
}

func TestRecord_RejectsUnknownKind(t *testing.T) {
	st := NewMemStore()
	if _, err := st.Record(context.Background(), "acct-1", "obliterate", ""); err != ErrInvalidKind {
		t.Fatalf("want ErrInvalidKind, got %v", err)
	}
}
