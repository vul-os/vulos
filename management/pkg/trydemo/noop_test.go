package trydemo

import (
	"context"
	"testing"
)

func TestNoopDemoMachines(t *testing.T) {
	dm := NewNoopDemoMachines()
	ctx := context.Background()

	// Initially stopped.
	st, err := dm.Status(ctx)
	if err != nil {
		t.Fatalf("Status (initial): %v", err)
	}
	if st.Started {
		t.Error("expected instance to be stopped initially")
	}

	// Start.
	if err := dm.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st, err = dm.Status(ctx)
	if err != nil {
		t.Fatalf("Status (after start): %v", err)
	}
	if !st.Started {
		t.Error("expected instance to be started after Start()")
	}

	// Stop.
	if err := dm.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	st, err = dm.Status(ctx)
	if err != nil {
		t.Fatalf("Status (after stop): %v", err)
	}
	if st.Started {
		t.Error("expected instance to be stopped after Stop()")
	}

	// Restart brings it back up.
	if err := dm.Restart(ctx); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	st, err = dm.Status(ctx)
	if err != nil {
		t.Fatalf("Status (after restart): %v", err)
	}
	if !st.Started {
		t.Error("expected instance to be started after Restart()")
	}

	// Start is idempotent.
	if err := dm.Start(ctx); err != nil {
		t.Fatalf("Start (idempotent): %v", err)
	}
	st, err = dm.Status(ctx)
	if err != nil {
		t.Fatalf("Status (idempotent start): %v", err)
	}
	if !st.Started {
		t.Error("instance should still be started")
	}
}
