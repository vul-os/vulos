package sqlcrdt

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A service that keeps its own in-memory view of a bridged table must be told
// when the bridge writes rows underneath it.
//
// This is not a general-purpose event stream; it exists because of one concrete
// failure. Replicated user accounts landed in auth.db correctly, and the auth
// store's Login iterates an in-memory map loaded once at startup — so the
// account was present in SQLite and "not found" to every request, until the box
// was restarted. The engine, the transport and the bridge were all working. The
// account was simply invisible.
//
// The three properties below are what make the callback safe to hang a reload
// off: it fires when a PEER's rows arrive, it stays quiet when only local
// writes happen, and it is called without the bridge's lock held.

func TestOnApplied_FiresWhenAPeersRowsLand(t *testing.T) {
	a := newBox(t, "AAA")
	b := newBox(t, "BBB")
	a.connect(t, b)

	var mu sync.Mutex
	var applied []int
	b.bridge.SetOnApplied(func(n int) {
		mu.Lock()
		applied = append(applied, n)
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.bridge.Run(ctx, 20*time.Millisecond, a.syncer.Nudge)
	go a.syncer.Run(ctx)
	go b.bridge.Run(ctx, 20*time.Millisecond, nil)

	a.insert(t, reminder{id: "rem-applied", userID: "u1", text: "from a peer", remindAt: 9, created: 9})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(applied)
		mu.Unlock()
		if got > 0 {
			// The row must actually be in the live table by the time the
			// callback runs — a reload triggered before the write is visible
			// would read stale state and never be retried.
			if r, ok := b.read(t, "rem-applied"); !ok || r.text != "from a peer" {
				t.Fatalf("callback fired before the row was readable: %+v ok=%v", r, ok)
			}
			mu.Lock()
			n := applied[0]
			mu.Unlock()
			if n <= 0 {
				t.Errorf("callback reported %d rows applied, want > 0", n)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("a peer's row landed in the live table and the applied callback never fired")
}

// The callback must NOT fire for this box's own writes. A reload on every local
// write would be pure overhead on the hot path, and — worse — it would make the
// signal useless for telling "someone else changed this" from "I changed this".
func TestOnApplied_QuietForPurelyLocalWrites(t *testing.T) {
	a := newBox(t, "AAA")

	var mu sync.Mutex
	fired := 0
	a.bridge.SetOnApplied(func(int) {
		mu.Lock()
		fired++
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// No peer, no syncer: nothing can arrive from anywhere else.
	go a.bridge.Run(ctx, 20*time.Millisecond, nil)

	a.insert(t, reminder{id: "rem-local", userID: "u1", text: "mine", remindAt: 1, created: 1})

	// Long enough for many cycles at a 20ms cadence.
	time.Sleep(600 * time.Millisecond)

	mu.Lock()
	got := fired
	mu.Unlock()
	if got != 0 {
		t.Errorf("applied callback fired %d times for a purely local write; it must only "+
			"report rows written on a PEER's behalf", got)
	}
}

// The callback reloads another service's state, and that service may well go on
// to write to a bridged table. Holding the bridge's lock across it would make
// that a deadlock, so notification must happen outside the lock.
func TestOnApplied_CallbackRunsWithoutTheBridgeLock(t *testing.T) {
	a := newBox(t, "AAA")
	b := newBox(t, "BBB")
	a.connect(t, b)

	done := make(chan error, 1)
	b.bridge.SetOnApplied(func(int) {
		// Re-entering the bridge from inside the callback is exactly what a
		// reload-then-write service does. It must not hang.
		if _, err := b.bridge.Capture(); err != nil {
			select {
			case done <- err:
			default:
			}
			return
		}
		select {
		case done <- nil:
		default:
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.bridge.Run(ctx, 20*time.Millisecond, a.syncer.Nudge)
	go a.syncer.Run(ctx)
	go b.bridge.Run(ctx, 20*time.Millisecond, nil)

	a.insert(t, reminder{id: "rem-reentrant", userID: "u1", text: "re-enter", remindAt: 2, created: 2})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("re-entering the bridge from the callback failed: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the applied callback deadlocked re-entering the bridge, so it is being " +
			"invoked with the bridge lock held")
	}
}
