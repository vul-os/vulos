package compliance_test

import (
	"context"
	"testing"

	"vulos/backend/services/compliance"
)

func TestRecordAndListByAccount(t *testing.T) {
	store, err := compliance.OpenStore("")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	if _, err := store.Record(ctx, "u1", "bogus", ""); err != compliance.ErrInvalidKind {
		t.Fatalf("Record(bogus kind): got err=%v, want ErrInvalidKind", err)
	}

	exp, err := store.Record(ctx, "u1", compliance.KindExport, "please export my mail")
	if err != nil {
		t.Fatalf("Record(export): %v", err)
	}
	if exp.Status != compliance.StatusReceived {
		t.Fatalf("Status = %q, want %q", exp.Status, compliance.StatusReceived)
	}
	if exp.ID == "" {
		t.Fatal("expected non-empty request ID")
	}

	if _, err := store.Record(ctx, "u1", compliance.KindErasure, ""); err != nil {
		t.Fatalf("Record(erasure): %v", err)
	}
	// A second user's requests must never leak into u1's list.
	if _, err := store.Record(ctx, "u2", compliance.KindExport, ""); err != nil {
		t.Fatalf("Record(u2): %v", err)
	}

	got, err := store.ListByAccount(ctx, "u1")
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByAccount(u1) len = %d, want 2", len(got))
	}
	// Newest first.
	if got[0].Kind != compliance.KindErasure || got[1].Kind != compliance.KindExport {
		t.Fatalf("unexpected order: %+v", got)
	}
	for _, r := range got {
		if r.AccountID != "u1" {
			t.Fatalf("leaked row for account %q into u1's list", r.AccountID)
		}
	}
}
