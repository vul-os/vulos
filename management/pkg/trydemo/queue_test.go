package trydemo

import (
	"context"
	"testing"
	"time"
)

// newTestQueue returns a queue with the given max viewers, no cost cap.
func newTestQueue(t *testing.T, maxViewers int) (Queue, Store) {
	t.Helper()
	s := openTestStore(t)
	cg := newCostGuard(s, 1e9) // effectively infinite cap
	cfg := Config{
		MaxViewers:              maxViewers,
		DriverSessionSecs:       600,
		DriverWarnSecs:          120,
		DriverIdleSecs:          60,
		PerIPConcurrent:         1,
		PerIPDriverCooldownSecs: 1800,
	}
	q := newQueue(cfg, s, cg)
	return q, s
}

func TestQueue_FirstJoinerBecomesDriver(t *testing.T) {
	q, _ := newTestQueue(t, 8)
	ctx := context.Background()

	token, role, pos, err := q.Join(ctx, "1.1.1.1")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if role != RoleDriver {
		t.Errorf("first joiner: want RoleDriver, got %v", role)
	}
	if pos != 0 {
		t.Errorf("first joiner: want pos=0, got %d", pos)
	}
	if token == "" {
		t.Error("token should not be empty")
	}
}

func TestQueue_Fill8_9thQueued(t *testing.T) {
	q, _ := newTestQueue(t, 8)
	ctx := context.Background()

	// 8 viewers: first becomes driver, next 7 become spectators.
	var tokens []string
	var roles []Role
	for i := 0; i < 8; i++ {
		// Different IPs for each.
		ip := "10.0.0." + string(rune('1'+i))
		tok, role, _, err := q.Join(ctx, ip)
		if err != nil {
			t.Fatalf("Join %d: %v", i, err)
		}
		tokens = append(tokens, tok)
		roles = append(roles, role)
	}

	if roles[0] != RoleDriver {
		t.Errorf("viewer 0: want driver, got %v", roles[0])
	}
	for i := 1; i < 8; i++ {
		if roles[i] != RoleSpectator {
			t.Errorf("viewer %d: want spectator, got %v", i, roles[i])
		}
	}

	// 9th viewer goes to queue.
	tok9, role9, pos9, err := q.Join(ctx, "10.0.1.9")
	if err != nil {
		t.Fatalf("Join 9: %v", err)
	}
	if role9 != RoleQueued {
		t.Errorf("9th viewer: want queued, got %v", role9)
	}
	if pos9 != 1 {
		t.Errorf("9th viewer queue pos: want 1, got %d", pos9)
	}
	_ = tok9
	_ = tokens
}

func TestQueue_DriverLeaves_SpectatorNotAutoPromoted(t *testing.T) {
	// Queue order is preserved: spectator does not auto-become driver.
	// The tickerPromote does that asynchronously; this tests queue state only.
	q, _ := newTestQueue(t, 8)
	ctx := context.Background()

	driverTok, _, _, _ := q.Join(ctx, "1.1.1.1")
	specTok, role, _, _ := q.Join(ctx, "2.2.2.2")

	if role != RoleSpectator {
		t.Skipf("second joiner not spectator (%v), skip", role)
	}

	// Driver leaves.
	if err := q.Leave(ctx, driverTok); err != nil {
		t.Fatalf("Leave driver: %v", err)
	}

	// Spectator role unchanged (no auto-promote without Promote() call).
	if q.RoleOf(specTok) != RoleSpectator {
		t.Errorf("spectator should remain spectator after driver leaves (before Promote)")
	}

	// Snapshot shows no driver.
	snap := q.Snapshot()
	if snap.DriverActive {
		t.Error("DriverActive should be false after driver leaves")
	}
}

func TestQueue_Promote_AdvancesQueueHead(t *testing.T) {
	q, _ := newTestQueue(t, 2) // 1 driver + 1 spectator
	ctx := context.Background()

	driverTok, _, _, _ := q.Join(ctx, "1.1.1.1")
	specTok, _, _, _ := q.Join(ctx, "2.2.2.2")   // fills spectator seat
	queuedTok, _, _, _ := q.Join(ctx, "3.3.3.3") // goes to waitQueue

	_ = specTok

	// Driver leaves.
	_ = q.Leave(ctx, driverTok)

	// Promote should pick up the queued token.
	promoted, err := q.Promote(ctx)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if promoted != queuedTok {
		t.Errorf("promoted token = %q, want %q", promoted, queuedTok)
	}
	if q.RoleOf(queuedTok) != RoleDriver {
		t.Errorf("promoted token role = %v, want RoleDriver", q.RoleOf(queuedTok))
	}
}

func TestQueue_PerIPCooldown(t *testing.T) {
	s := openTestStore(t)
	cg := newCostGuard(s, 1e9)
	cfg := Config{
		MaxViewers:              8,
		DriverSessionSecs:       600,
		DriverWarnSecs:          120,
		DriverIdleSecs:          60,
		PerIPConcurrent:         2, // allow 2 concurrent to test cooldown separately
		PerIPDriverCooldownSecs: 1800,
	}
	q := newQueue(cfg, s, cg)
	ctx := context.Background()
	ip := "5.5.5.5"

	// First join: becomes driver, records claim.
	tok1, role1, _, err := q.Join(ctx, ip)
	if err != nil {
		t.Fatalf("first join: %v", err)
	}
	if role1 != RoleDriver {
		t.Fatalf("first join should be driver, got %v", role1)
	}
	_ = q.Leave(ctx, tok1)

	// Cooldown: second join within 30 min from same IP should NOT be driver.
	_, role2, _, err := q.Join(ctx, ip)
	if err != nil {
		t.Fatalf("second join: %v", err)
	}
	if role2 == RoleDriver {
		t.Error("second driver claim within cooldown should be denied (spectator or queued)")
	}
}

func TestQueue_PerIPConcurrentLimit(t *testing.T) {
	q, _ := newTestQueue(t, 8)
	ctx := context.Background()
	ip := "6.6.6.6"

	_, _, _, err := q.Join(ctx, ip)
	if err != nil {
		t.Fatalf("first join: %v", err)
	}
	_, _, _, err = q.Join(ctx, ip)
	if err != ErrPerIPLimitReached {
		t.Errorf("second join from same IP: want ErrPerIPLimitReached, got %v", err)
	}
}

func TestQueue_IdleKillDetection(t *testing.T) {
	s := openTestStore(t)
	cg := newCostGuard(s, 1e9)
	cfg := Config{
		MaxViewers:              8,
		DriverSessionSecs:       600,
		DriverWarnSecs:          120,
		DriverIdleSecs:          1, // 1 second for test speed
		PerIPConcurrent:         1,
		PerIPDriverCooldownSecs: 0, // no cooldown for this test
	}
	q := newQueue(cfg, s, cg)
	ctx := context.Background()

	tok, role, _, err := q.Join(ctx, "7.7.7.7")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if role != RoleDriver {
		t.Fatalf("expected driver role, got %v", role)
	}

	// Wait for idle window to expire.
	time.Sleep(1100 * time.Millisecond)

	iq := q.(*inMemQueue)
	iq.mu.Lock()
	idleTok, idle := iq.driverIdle()
	iq.mu.Unlock()

	if !idle {
		t.Error("expected driver to be detected as idle after 1s")
	}
	if idleTok != tok {
		t.Errorf("idle token = %q, want %q", idleTok, tok)
	}
}

func TestQueue_SessionTimeoutDetection(t *testing.T) {
	s := openTestStore(t)
	cg := newCostGuard(s, 1e9)
	cfg := Config{
		MaxViewers:              8,
		DriverSessionSecs:       1, // 1 second for test speed
		DriverWarnSecs:          0,
		DriverIdleSecs:          60,
		PerIPConcurrent:         1,
		PerIPDriverCooldownSecs: 0,
	}
	q := newQueue(cfg, s, cg)
	ctx := context.Background()

	tok, role, _, err := q.Join(ctx, "8.8.8.8")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if role != RoleDriver {
		t.Fatalf("expected driver, got %v", role)
	}

	// Keep sending input to prevent idle-kill; only session timeout should fire.
	_ = q.RecordInput(ctx, tok)
	time.Sleep(1100 * time.Millisecond)

	iq := q.(*inMemQueue)
	iq.mu.Lock()
	expTok, expired := iq.driverExpired()
	iq.mu.Unlock()

	if !expired {
		t.Error("expected driver session to be detected as expired after 1s")
	}
	if expTok != tok {
		t.Errorf("expired token = %q, want %q", expTok, tok)
	}
}

func TestQueue_Leave_Idempotent(t *testing.T) {
	q, _ := newTestQueue(t, 8)
	ctx := context.Background()

	tok, _, _, _ := q.Join(ctx, "9.9.9.9")
	if err := q.Leave(ctx, tok); err != nil {
		t.Fatalf("Leave (first): %v", err)
	}
	if err := q.Leave(ctx, tok); err != nil {
		t.Errorf("Leave (second, idempotent): %v", err)
	}
}

func TestQueue_HeartbeatUnknownToken(t *testing.T) {
	q, _ := newTestQueue(t, 8)
	ctx := context.Background()

	if err := q.Heartbeat(ctx, "nonexistent"); err != ErrUnknownToken {
		t.Errorf("Heartbeat with unknown token: want ErrUnknownToken, got %v", err)
	}
}

func TestQueue_Snapshot(t *testing.T) {
	q, _ := newTestQueue(t, 8)
	ctx := context.Background()

	snap := q.Snapshot()
	if snap.DriverActive || snap.SpectatorCount != 0 || snap.QueueLength != 0 {
		t.Errorf("empty queue snapshot wrong: %+v", snap)
	}

	q.Join(ctx, "1.2.3.4")
	snap = q.Snapshot()
	if !snap.DriverActive {
		t.Error("expected DriverActive after one join")
	}
	if snap.SpectatorCount != 0 {
		t.Errorf("spectator count: want 0, got %d", snap.SpectatorCount)
	}
}
