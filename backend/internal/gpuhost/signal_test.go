// signal_test.go — P2P-preferred / relay-fallback decision tests.

package gpuhost

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPathChooser_NoProbeReturnsTURN(t *testing.T) {
	// With no probe installed, the chooser MUST return TURN — we never claim
	// a P2P path we haven't proven.
	c := NewPathChooser(0)
	got := c.Choose(context.Background(), "sess-1")
	if got != PathTURN {
		t.Errorf("Choose with no probe = %q, want %q", got, PathTURN)
	}
	p2p, turn := c.Decisions()
	if p2p != 0 || turn != 1 {
		t.Errorf("decisions = (p2p=%d, turn=%d), want (0,1)", p2p, turn)
	}
}

func TestPathChooser_ProbeSuccessChoosesP2P(t *testing.T) {
	c := NewPathChooser(100 * time.Millisecond)
	c.SetProbe(ProbeFunc(func(ctx context.Context, sess string) error {
		return nil // P2P viable
	}))
	for i := 0; i < 5; i++ {
		if got := c.Choose(context.Background(), "sess"); got != PathP2P {
			t.Fatalf("iter %d: want PathP2P, got %q", i, got)
		}
	}
	p2p, turn := c.Decisions()
	if p2p != 5 || turn != 0 {
		t.Errorf("decisions = (p2p=%d, turn=%d), want (5,0)", p2p, turn)
	}
}

func TestPathChooser_ProbeFailureFallsBackToTURN(t *testing.T) {
	c := NewPathChooser(100 * time.Millisecond)
	c.SetProbe(ProbeFunc(func(ctx context.Context, sess string) error {
		return ErrP2PFailed
	}))
	if got := c.Choose(context.Background(), "sess"); got != PathTURN {
		t.Errorf("Choose with failing probe = %q, want %q", got, PathTURN)
	}
}

func TestPathChooser_ProbeTimeoutFallsBackToTURN(t *testing.T) {
	c := NewPathChooser(20 * time.Millisecond)
	c.SetProbe(ProbeFunc(func(ctx context.Context, sess string) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
			return nil
		}
	}))
	start := time.Now()
	got := c.Choose(context.Background(), "sess")
	if got != PathTURN {
		t.Errorf("Choose with slow probe = %q, want %q", got, PathTURN)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("Choose took %s, expected ≲100ms (deadline-bounded)", elapsed)
	}
}

func TestPathChooser_MixedDecisionsCounted(t *testing.T) {
	c := NewPathChooser(50 * time.Millisecond)
	var calls int
	c.SetProbe(ProbeFunc(func(ctx context.Context, sess string) error {
		calls++
		if calls%2 == 0 {
			return errors.New("flap")
		}
		return nil
	}))
	for i := 0; i < 4; i++ {
		_ = c.Choose(context.Background(), "s")
	}
	p2p, turn := c.Decisions()
	if p2p != 2 || turn != 2 {
		t.Errorf("decisions = (p2p=%d, turn=%d), want (2,2)", p2p, turn)
	}
}
