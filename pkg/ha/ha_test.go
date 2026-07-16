package ha

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ─────────────────────────────────────────────────────────────────────────────
// Store fixtures: run every behavioural test against BOTH MemStore + SQLStore.
// ─────────────────────────────────────────────────────────────────────────────

type storeFixture struct {
	name  string
	open  func(t *testing.T) Store
	clock func(store Store, fn func() time.Time)
}

func storeFixtures(t *testing.T) []storeFixture {
	t.Helper()
	return []storeFixture{
		{
			name: "MemStore",
			open: func(t *testing.T) Store { return NewMemStore() },
			clock: func(s Store, fn func() time.Time) {
				if m, ok := s.(*MemStore); ok {
					m.SetClock(fn)
				}
			},
		},
		{
			name: "SQLStore",
			open: func(t *testing.T) Store {
				dsn := "file:" + filepath.Join(t.TempDir(), "ha.db")
				db, err := cpdb.OpenSQLiteDSN(dsn)
				if err != nil {
					t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
				}
				s, err := Open(db)
				if err != nil {
					_ = db.Close()
					t.Fatalf("Open: %v", err)
				}
				t.Cleanup(func() { _ = s.Close() })
				return s
			},
			clock: func(s Store, fn func() time.Time) {
				if sq, ok := s.(*SQLStore); ok {
					sq.SetClock(fn)
				}
			},
		},
	}
}

// fakeClock implements a settable wall clock for deterministic tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start.UTC()} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d).UTC()
}

// ─────────────────────────────────────────────────────────────────────────────
// 1. Single peer acquires and renews indefinitely.
// ─────────────────────────────────────────────────────────────────────────────

func TestSinglePeerAcquireAndKeep(t *testing.T) {
	for _, fx := range storeFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			s := fx.open(t)
			ctx := context.Background()
			ttl := 10 * time.Second

			l, err := s.TryAcquire(ctx, DefaultLeaseKey, "peer-a", ttl)
			if err != nil {
				t.Fatalf("initial acquire: %v", err)
			}
			if l.HolderID != "peer-a" {
				t.Fatalf("holder = %s, want peer-a", l.HolderID)
			}
			if l.Generation != 1 {
				t.Fatalf("first lease gen = %d, want 1", l.Generation)
			}

			// Renew several times — generation must NOT change.
			lastGen := l.Generation
			for i := 0; i < 5; i++ {
				renewed, err := s.RenewLease(ctx, DefaultLeaseKey, "peer-a", ttl)
				if err != nil {
					t.Fatalf("renew %d: %v", i, err)
				}
				if renewed.Generation != lastGen {
					t.Fatalf("renew bumped generation %d → %d", lastGen, renewed.Generation)
				}
			}

			// Re-acquire by the same peer also must NOT bump generation.
			again, err := s.TryAcquire(ctx, DefaultLeaseKey, "peer-a", ttl)
			if err != nil {
				t.Fatalf("re-acquire same peer: %v", err)
			}
			if again.Generation != lastGen {
				t.Fatalf("same-peer re-acquire bumped gen %d → %d", lastGen, again.Generation)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. Passive peer acquires the lease after the active one stops heartbeating.
//    Verified via a controllable clock advanced past TTL.
// ─────────────────────────────────────────────────────────────────────────────

func TestPassivePromotesOnLeaderTimeout(t *testing.T) {
	for _, fx := range storeFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			s := fx.open(t)
			ctx := context.Background()
			ttl := 5 * time.Second
			clk := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			fx.clock(s, clk.Now)

			// peer-a acquires.
			lA, err := s.TryAcquire(ctx, DefaultLeaseKey, "peer-a", ttl)
			if err != nil {
				t.Fatalf("peer-a acquire: %v", err)
			}
			if lA.Generation != 1 {
				t.Fatalf("gen=%d, want 1", lA.Generation)
			}

			// peer-b tries while peer-a is still live — must fail.
			if _, err := s.TryAcquire(ctx, DefaultLeaseKey, "peer-b", ttl); !errors.Is(err, ErrLeaseHeld) {
				t.Fatalf("peer-b acquire while peer-a live: err=%v, want ErrLeaseHeld", err)
			}

			// peer-a "stops heartbeating": advance clock past TTL.
			clk.Advance(ttl + time.Second)

			// peer-a tries to RENEW — fails (already expired).
			if _, err := s.RenewLease(ctx, DefaultLeaseKey, "peer-a", ttl); !errors.Is(err, ErrNotLeader) {
				t.Fatalf("expired self-renew: err=%v, want ErrNotLeader", err)
			}

			// peer-b takes over.
			lB, err := s.TryAcquire(ctx, DefaultLeaseKey, "peer-b", ttl)
			if err != nil {
				t.Fatalf("peer-b acquire after timeout: %v", err)
			}
			if lB.HolderID != "peer-b" {
				t.Fatalf("holder=%s, want peer-b", lB.HolderID)
			}
			if lB.Generation != 2 {
				t.Fatalf("generation=%d, want 2 (bump on handoff)", lB.Generation)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. Mutual exclusion under concurrency: only one peer can hold at a time.
// ─────────────────────────────────────────────────────────────────────────────

func TestMutualExclusionConcurrent(t *testing.T) {
	for _, fx := range storeFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			s := fx.open(t)
			ctx := context.Background()
			ttl := 30 * time.Second

			const peers = 8
			const rounds = 50

			var (
				wg          sync.WaitGroup
				acquireOK   atomic.Int32
				acquireHeld atomic.Int32
				err         atomic.Pointer[error]
			)

			for i := 0; i < peers; i++ {
				wg.Add(1)
				peerID := fmt.Sprintf("peer-%d", i)
				go func() {
					defer wg.Done()
					for r := 0; r < rounds; r++ {
						_, e := s.TryAcquire(ctx, DefaultLeaseKey, peerID, ttl)
						switch {
						case e == nil:
							acquireOK.Add(1)
						case errors.Is(e, ErrLeaseHeld):
							acquireHeld.Add(1)
						default:
							err.Store(&e)
							return
						}
					}
				}()
			}
			wg.Wait()

			if e := err.Load(); e != nil {
				t.Fatalf("unexpected error: %v", *e)
			}

			// Whoever ends up holding the lease is fine, but exactly ONE row
			// should be present with one holder.
			l, gerr := s.GetLease(ctx, DefaultLeaseKey)
			if gerr != nil {
				t.Fatalf("GetLease: %v", gerr)
			}
			if l.HolderID == "" {
				t.Fatalf("lease has empty holder")
			}
			if l.Generation < 1 {
				t.Fatalf("generation=%d, want ≥1", l.Generation)
			}

			// We expect a healthy mix: at least some successes (the first
			// acquirer + same-peer re-acquires) and at least some held-by-other
			// rejections (rivals losing the race).
			if acquireOK.Load() == 0 {
				t.Fatalf("no successful acquires")
			}
			if acquireHeld.Load() == 0 {
				t.Fatalf("no ErrLeaseHeld rejections — mutual exclusion not exercised")
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. Generation counter is monotonic across multiple promotions.
// ─────────────────────────────────────────────────────────────────────────────

func TestGenerationMonotonic(t *testing.T) {
	for _, fx := range storeFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			s := fx.open(t)
			ctx := context.Background()
			ttl := 2 * time.Second
			clk := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			fx.clock(s, clk.Now)

			peers := []string{"a", "b", "c", "d", "e"}
			var lastGen int64

			for i, p := range peers {
				l, err := s.TryAcquire(ctx, DefaultLeaseKey, p, ttl)
				if err != nil {
					t.Fatalf("acquire by %s: %v", p, err)
				}
				if l.Generation <= lastGen {
					t.Fatalf("non-monotonic at step %d: gen=%d, prev=%d", i, l.Generation, lastGen)
				}
				lastGen = l.Generation
				// Force the next peer to take over by advancing past TTL.
				clk.Advance(ttl + time.Second)
			}

			if int(lastGen) != len(peers) {
				t.Fatalf("final generation=%d, want %d", lastGen, len(peers))
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. Promotion is fast: a fresh peer can take over inside 2× TTL.
//    Uses the Manager, real-time clock, and a small TTL.
// ─────────────────────────────────────────────────────────────────────────────

func TestPromotionUnderTwoTTL(t *testing.T) {
	for _, fx := range storeFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			s := fx.open(t)
			ttl := 300 * time.Millisecond

			mgrA := NewManager(s, Config{
				PeerID:            "peer-a",
				LeaseKey:          DefaultLeaseKey,
				LeaseTTL:          ttl,
				HeartbeatInterval: 60 * time.Millisecond,
				AcquireInterval:   80 * time.Millisecond,
			})
			ctxA, cancelA := context.WithCancel(context.Background())
			doneA := make(chan struct{})
			go func() { defer close(doneA); _ = mgrA.Run(ctxA) }()

			// Wait for peer-a to become leader.
			waitFor(t, time.Second, func() bool { return mgrA.IsLeader() })

			// Simulate peer-a hard-stop WITHOUT graceful release: cancel and
			// then bypass release by... actually the manager releases on
			// ctx.Done. To simulate a true crash, we skip the manager and
			// just stop ticking — easiest way: cancel + don't wait for the
			// release to land (manager.Run shutdown is best-effort). But to
			// really simulate a crash we directly poison-pill: write an
			// expired lease back. Simpler: spin a second manager and rely
			// on TTL expiry. Drop peer-a's renew thread by cancelling.
			//
			// To keep the test crisp we DO want crash semantics, so instead
			// of using cancel (which triggers release), we replace the
			// store's lease with a long-expired row would require store-
			// specific access. We instead use a small helper: stop peer-a's
			// loop by cancel, then *re-acquire* by peer-a as a no-op? No.
			//
			// The cleanest crash-simulation: don't run Manager.Run. Instead
			// drive Tick manually here for peer-a once, then "stop" by no
			// longer calling Tick.

			cancelA() // trigger graceful release path; this releases the lease.
			<-doneA

			// Now spin peer-b and time the promotion.
			mgrB := NewManager(s, Config{
				PeerID:            "peer-b",
				LeaseKey:          DefaultLeaseKey,
				LeaseTTL:          ttl,
				HeartbeatInterval: 60 * time.Millisecond,
				AcquireInterval:   80 * time.Millisecond,
			})
			ctxB, cancelB := context.WithCancel(context.Background())
			defer cancelB()
			start := time.Now()
			go func() { _ = mgrB.Run(ctxB) }()

			deadline := 2 * ttl
			ok := waitFor(t, deadline, func() bool { return mgrB.IsLeader() })
			elapsed := time.Since(start)
			if !ok {
				t.Fatalf("peer-b did not promote within %s (elapsed=%s)", deadline, elapsed)
			}
			if elapsed > deadline {
				t.Fatalf("promotion too slow: %s > 2×TTL=%s", elapsed, deadline)
			}
			if mgrB.Generation() < 2 {
				t.Fatalf("generation after promotion = %d, want ≥2", mgrB.Generation())
			}
		})
	}
}

// TestPromotionAfterTrueCrash exercises the path where the active peer dies
// WITHOUT a graceful release — the passive peer must take over after TTL.
func TestPromotionAfterTrueCrash(t *testing.T) {
	for _, fx := range storeFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			s := fx.open(t)
			ctx := context.Background()
			ttl := 250 * time.Millisecond

			// peer-a acquires via direct store call — no manager, no release.
			lA, err := s.TryAcquire(ctx, DefaultLeaseKey, "peer-a", ttl)
			if err != nil {
				t.Fatalf("peer-a direct acquire: %v", err)
			}
			if lA.Generation != 1 {
				t.Fatalf("gen=%d, want 1", lA.Generation)
			}

			// peer-b spins up; should NOT be able to acquire until TTL passes.
			mgrB := NewManager(s, Config{
				PeerID:            "peer-b",
				LeaseKey:          DefaultLeaseKey,
				LeaseTTL:          ttl,
				HeartbeatInterval: 50 * time.Millisecond,
				AcquireInterval:   50 * time.Millisecond,
			})
			ctxB, cancelB := context.WithCancel(context.Background())
			defer cancelB()
			start := time.Now()
			go func() { _ = mgrB.Run(ctxB) }()

			// Must not promote before TTL.
			deadline := 2 * ttl
			ok := waitFor(t, deadline, func() bool { return mgrB.IsLeader() })
			elapsed := time.Since(start)
			if !ok {
				t.Fatalf("peer-b did not promote within %s (elapsed=%s)", deadline, elapsed)
			}
			if elapsed < ttl {
				t.Fatalf("peer-b promoted before TTL elapsed: %s < %s", elapsed, ttl)
			}
			if mgrB.Generation() != 2 {
				t.Fatalf("generation after crash failover = %d, want 2", mgrB.Generation())
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. Markers written by the old leader survive failover (no data loss for
//    the lease state). Verifies durability across promotion.
// ─────────────────────────────────────────────────────────────────────────────

func TestMarkersSurviveFailover(t *testing.T) {
	for _, fx := range storeFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			s := fx.open(t)
			ctx := context.Background()
			ttl := 5 * time.Second
			clk := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			fx.clock(s, clk.Now)

			// peer-a is leader; write some markers.
			lA, err := s.TryAcquire(ctx, DefaultLeaseKey, "peer-a", ttl)
			if err != nil {
				t.Fatalf("peer-a acquire: %v", err)
			}
			for i := 0; i < 3; i++ {
				if _, err := s.WriteMarker(ctx, LeaderMarker{
					LeaseKey:   DefaultLeaseKey,
					Generation: lA.Generation,
					HolderID:   "peer-a",
					Marker:     fmt.Sprintf("a-%d", i),
				}); err != nil {
					t.Fatalf("write marker: %v", err)
				}
			}

			// peer-a "crashes": clock past TTL.
			clk.Advance(ttl + time.Second)

			// peer-b takes over and reads the markers.
			lB, err := s.TryAcquire(ctx, DefaultLeaseKey, "peer-b", ttl)
			if err != nil {
				t.Fatalf("peer-b acquire: %v", err)
			}
			if lB.Generation != lA.Generation+1 {
				t.Fatalf("gen=%d, want %d", lB.Generation, lA.Generation+1)
			}

			// All of peer-a's markers must be visible to peer-b.
			markers, err := s.ListMarkers(ctx, DefaultLeaseKey)
			if err != nil {
				t.Fatalf("list markers: %v", err)
			}
			if len(markers) != 3 {
				t.Fatalf("got %d markers, want 3", len(markers))
			}
			for i, m := range markers {
				if m.HolderID != "peer-a" {
					t.Fatalf("marker %d holder=%s, want peer-a", i, m.HolderID)
				}
				if m.Generation != lA.Generation {
					t.Fatalf("marker %d generation=%d, want %d", i, m.Generation, lA.Generation)
				}
				want := fmt.Sprintf("a-%d", i)
				if m.Marker != want {
					t.Fatalf("marker %d = %s, want %s", i, m.Marker, want)
				}
			}

			// peer-b writes its own marker under the new generation.
			if _, err := s.WriteMarker(ctx, LeaderMarker{
				LeaseKey:   DefaultLeaseKey,
				Generation: lB.Generation,
				HolderID:   "peer-b",
				Marker:     "b-after-failover",
			}); err != nil {
				t.Fatalf("post-failover marker: %v", err)
			}

			markers, _ = s.ListMarkers(ctx, DefaultLeaseKey)
			if len(markers) != 4 {
				t.Fatalf("got %d markers after b write, want 4", len(markers))
			}
			last := markers[len(markers)-1]
			if last.HolderID != "peer-b" || last.Generation != lB.Generation {
				t.Fatalf("last marker = %+v, want peer-b gen %d", last, lB.Generation)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 7. Release is idempotent + only the holder can release.
// ─────────────────────────────────────────────────────────────────────────────

func TestReleaseOnlyByHolder(t *testing.T) {
	for _, fx := range storeFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			s := fx.open(t)
			ctx := context.Background()
			ttl := 5 * time.Second

			if _, err := s.TryAcquire(ctx, DefaultLeaseKey, "peer-a", ttl); err != nil {
				t.Fatalf("acquire: %v", err)
			}
			// peer-b release is a no-op while peer-a holds.
			if err := s.ReleaseLease(ctx, DefaultLeaseKey, "peer-b"); err != nil {
				t.Fatalf("peer-b release: %v", err)
			}
			if _, err := s.GetLease(ctx, DefaultLeaseKey); err != nil {
				t.Fatalf("lease should still exist after non-holder release: %v", err)
			}
			// peer-a releases successfully. The row is preserved (so the
			// generation counter survives a graceful step-down), but the
			// lease is marked expired so another peer can immediately steal.
			if err := s.ReleaseLease(ctx, DefaultLeaseKey, "peer-a"); err != nil {
				t.Fatalf("peer-a release: %v", err)
			}
			afterRelease, err := s.GetLease(ctx, DefaultLeaseKey)
			if err != nil {
				t.Fatalf("after release: unexpected err=%v", err)
			}
			if afterRelease.IsLive(time.Now().UTC().Add(1 * time.Millisecond)) {
				t.Fatalf("lease still live after release: %+v", afterRelease)
			}
			// Another peer can take over with a bumped generation.
			lB, err := s.TryAcquire(ctx, DefaultLeaseKey, "peer-b", ttl)
			if err != nil {
				t.Fatalf("peer-b takeover after release: %v", err)
			}
			if lB.Generation != afterRelease.Generation+1 {
				t.Fatalf("gen=%d, want %d", lB.Generation, afterRelease.Generation+1)
			}
			// Second release by a non-holder is a no-op.
			if err := s.ReleaseLease(ctx, DefaultLeaseKey, "peer-a"); err != nil {
				t.Fatalf("non-holder release: %v", err)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 8. Peer heartbeats round-trip with role + monotonic generation_seen.
// ─────────────────────────────────────────────────────────────────────────────

func TestPeerHeartbeatRoundTrip(t *testing.T) {
	for _, fx := range storeFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			s := fx.open(t)
			ctx := context.Background()

			now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
			hb := PeerHeartbeat{
				PeerID:         "peer-a",
				Address:        "https://cp-a.example:8081",
				LastHeartbeat:  now,
				Role:           RoleLeader,
				GenerationSeen: 7,
			}
			if err := s.PutPeerHeartbeat(ctx, hb); err != nil {
				t.Fatalf("put: %v", err)
			}
			got, err := s.GetPeer(ctx, "peer-a")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Role != RoleLeader || got.GenerationSeen != 7 {
				t.Fatalf("got %+v", got)
			}

			// Putting a heartbeat with a LOWER generation_seen must not regress.
			hb2 := hb
			hb2.GenerationSeen = 3
			hb2.LastHeartbeat = now.Add(1 * time.Second)
			hb2.Role = RoleStandby
			if err := s.PutPeerHeartbeat(ctx, hb2); err != nil {
				t.Fatalf("put 2: %v", err)
			}
			got2, _ := s.GetPeer(ctx, "peer-a")
			if got2.GenerationSeen != 7 {
				t.Fatalf("generation_seen regressed: %d", got2.GenerationSeen)
			}
			if got2.Role != RoleStandby {
				t.Fatalf("role = %s, want %s", got2.Role, RoleStandby)
			}

			// List returns both peers ordered.
			_ = s.PutPeerHeartbeat(ctx, PeerHeartbeat{
				PeerID:         "peer-b",
				LastHeartbeat:  now,
				Role:           RoleStandby,
				GenerationSeen: 5,
			})
			peers, err := s.ListPeers(ctx)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(peers) != 2 {
				t.Fatalf("got %d peers, want 2", len(peers))
			}
			if peers[0].PeerID != "peer-a" || peers[1].PeerID != "peer-b" {
				t.Fatalf("order = %v %v", peers[0].PeerID, peers[1].PeerID)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 9. Manager: single peer with no rivals becomes leader on first Tick.
// ─────────────────────────────────────────────────────────────────────────────

func TestManagerSinglePeerBecomesLeader(t *testing.T) {
	s := NewMemStore()
	var promoted atomic.Int32
	mgr := NewManager(s, Config{
		PeerID:            "peer-only",
		LeaseTTL:          5 * time.Second,
		HeartbeatInterval: time.Second,
	})
	mgr.OnPromote = func(gen int64) { promoted.Add(1) }

	if err := mgr.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if !mgr.IsLeader() {
		t.Fatalf("manager did not become leader")
	}
	if mgr.Generation() != 1 {
		t.Fatalf("gen=%d, want 1", mgr.Generation())
	}
	if promoted.Load() != 1 {
		t.Fatalf("OnPromote fired %d times, want 1", promoted.Load())
	}

	// Second tick is a renew; OnPromote must NOT fire again.
	if err := mgr.Tick(context.Background()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if promoted.Load() != 1 {
		t.Fatalf("OnPromote re-fired on renew (count=%d)", promoted.Load())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 10. Manager: standby publishes the right state when leader is healthy.
// ─────────────────────────────────────────────────────────────────────────────

func TestManagerStandbyObservesLeader(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()

	// peer-a acquires via direct store call.
	_, err := s.TryAcquire(ctx, DefaultLeaseKey, "peer-a", 30*time.Second)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	mgrB := NewManager(s, Config{
		PeerID:   "peer-b",
		LeaseTTL: 30 * time.Second,
	})
	if err := mgrB.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	snap := mgrB.Snapshot()
	if snap.IsLeader {
		t.Fatalf("standby thinks it is leader")
	}
	if snap.HolderID != "peer-a" {
		t.Fatalf("standby sees holder=%s, want peer-a", snap.HolderID)
	}
	if snap.Generation != 1 {
		t.Fatalf("standby sees gen=%d, want 1", snap.Generation)
	}

	// peer-b's own heartbeat must be recorded as standby.
	hb, err := s.GetPeer(ctx, "peer-b")
	if err != nil {
		t.Fatalf("get peer-b: %v", err)
	}
	if hb.Role != RoleStandby {
		t.Fatalf("peer-b role=%s, want standby", hb.Role)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// waitFor polls cond every 10ms until it returns true or deadline elapses.
// Returns true if cond ever became true. Used in tests that depend on the
// Manager's background loop without resorting to fixed sleeps.
func waitFor(t *testing.T, deadline time.Duration, cond func() bool) bool {
	t.Helper()
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
