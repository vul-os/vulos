package storagesel_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vul-os/vulos-management/pkg/storagesel"
)

// TestSyncModeDefaultsCentral: an account with no row defaults to central.
func TestSyncModeDefaultsCentral(t *testing.T) {
	sel := openTest(t)
	mode, err := sel.GetSyncMode(context.Background(), "acct-new")
	if err != nil {
		t.Fatalf("GetSyncMode: %v", err)
	}
	if mode != storagesel.SyncModeCentral {
		t.Fatalf("default sync mode = %q, want %q", mode, storagesel.SyncModeCentral)
	}
}

// TestSetSyncModeRoundtrip: Set→Get round-trip for both modes.
func TestSetSyncModeRoundtrip(t *testing.T) {
	ctx := context.Background()
	sel := openTest(t)

	if err := sel.SetSyncMode(ctx, "acct-1", storagesel.SyncModeLocal); err != nil {
		t.Fatalf("SetSyncMode(local): %v", err)
	}
	if got, _ := sel.GetSyncMode(ctx, "acct-1"); got != storagesel.SyncModeLocal {
		t.Fatalf("after set local, got %q", got)
	}
	if err := sel.SetSyncMode(ctx, "acct-1", storagesel.SyncModeCentral); err != nil {
		t.Fatalf("SetSyncMode(central): %v", err)
	}
	if got, _ := sel.GetSyncMode(ctx, "acct-1"); got != storagesel.SyncModeCentral {
		t.Fatalf("after set central, got %q", got)
	}
}

// TestSetSyncModeRejectsInvalid: a bad mode is refused.
func TestSetSyncModeRejectsInvalid(t *testing.T) {
	sel := openTest(t)
	err := sel.SetSyncMode(context.Background(), "acct-1", storagesel.SyncMode("sideways"))
	if !errors.Is(err, storagesel.ErrInvalidSyncMode) {
		t.Fatalf("want ErrInvalidSyncMode, got %v", err)
	}
}

// TestSetSyncModePreservesBackend: toggling sync mode does NOT clobber an
// existing BYO MinIO endpoint config (and vice-versa).
func TestSetSyncModePreservesBackend(t *testing.T) {
	ctx := context.Background()
	sel := openTest(t)

	want := storagesel.Backend{
		Kind:     storagesel.KindMinIO,
		Endpoint: "https://minio.acme.example",
		Region:   "us-east-1",
		Bucket:   "acme-data",
		CredRef:  "env:ACME_MINIO",
	}
	if err := sel.Set(ctx, "acct-byo", want); err != nil {
		t.Fatalf("Set backend: %v", err)
	}
	// Default sync mode for a freshly-Set row is central.
	if got, _ := sel.GetSyncMode(ctx, "acct-byo"); got != storagesel.SyncModeCentral {
		t.Fatalf("fresh backend sync mode = %q, want central", got)
	}
	// Flip to local — endpoint config must survive.
	if err := sel.SetSyncMode(ctx, "acct-byo", storagesel.SyncModeLocal); err != nil {
		t.Fatalf("SetSyncMode: %v", err)
	}
	got, err := sel.Get(ctx, "acct-byo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Kind != storagesel.KindMinIO || got.Endpoint != want.Endpoint || got.Bucket != want.Bucket {
		t.Fatalf("backend clobbered by SetSyncMode: %+v", got)
	}
	if got.SyncMode != storagesel.SyncModeLocal {
		t.Fatalf("sync mode = %q, want local", got.SyncMode)
	}
}

// TestSetSyncModeCreatesRowForNewAccount: an account with no prior backend row
// gets one (the managed store default) when only the sync mode is set.
func TestSetSyncModeCreatesRowForNewAccount(t *testing.T) {
	ctx := context.Background()
	sel := openTest(t)

	if err := sel.SetSyncMode(ctx, "acct-fresh", storagesel.SyncModeLocal); err != nil {
		t.Fatalf("SetSyncMode: %v", err)
	}
	got, err := sel.Get(ctx, "acct-fresh")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Kind != storagesel.KindManaged {
		t.Fatalf("new row kind = %q, want managed", got.Kind)
	}
	if got.SyncMode != storagesel.SyncModeLocal {
		t.Fatalf("new row sync mode = %q, want local", got.SyncMode)
	}
}
