package stream

import (
	"testing"

	"vulos/backend/services/gpu"
)

// TestGamingBitrateTier_SelectsGamingPath verifies the gaming bitrate path
// selection: hardware encode → gaming-max (10000 kbps), software → gaming
// (6000 kbps). The gaming path only steps bitrate, never resolution.
func TestGamingBitrateTier_SelectsGamingPath(t *testing.T) {
	cases := []struct {
		tier     gpu.Tier
		wantQ    Quality
		wantKbps int
	}{
		{gpu.TierNVENC, QualityGamingMax, 10000},
		{gpu.TierVAAPI, QualityGamingMax, 10000},
		{gpu.TierSoftware, QualityGaming, 6000},
	}
	for _, c := range cases {
		q := GamingBitrateTier(gpu.Info{Tier: c.tier})
		if q != c.wantQ {
			t.Errorf("tier=%v: quality=%v want %v", c.tier, q, c.wantQ)
		}
		if q.Bitrate() != c.wantKbps {
			t.Errorf("tier=%v: kbps=%d want %d", c.tier, q.Bitrate(), c.wantKbps)
		}
	}
}

// TestDetectGamingCapability_NVENCFlag verifies the NVENC/hardware-encode
// capability is reported correctly with a clean software fallback.
func TestDetectGamingCapability_NVENCFlag(t *testing.T) {
	nv := DetectGamingCapability(gpu.Info{
		Tier: gpu.TierNVENC, TierName: "nvenc", Encoder: "nvh264enc",
		Vendor: gpu.VendorNVIDIA, Device: "L40S", Codec: "video/H264",
	})
	if !nv.NVENC || !nv.HardwareEncode {
		t.Fatalf("NVENC not reported: %+v", nv)
	}
	if nv.InitialQuality != "gaming-max" || nv.InitialKbps != 10000 {
		t.Fatalf("NVENC initial tier wrong: %+v", nv)
	}

	// Software: no hardware encode, cleanly falls back to base gaming tier.
	sw := DetectGamingCapability(gpu.Info{Tier: gpu.TierSoftware, TierName: "software", Encoder: "vp8enc"})
	if sw.NVENC || sw.HardwareEncode {
		t.Fatalf("software should not report hardware encode: %+v", sw)
	}
	if sw.InitialQuality != "gaming" || sw.InitialKbps != 6000 {
		t.Fatalf("software initial tier wrong: %+v", sw)
	}

	// VA-API: hardware encode true, but NVENC false.
	va := DetectGamingCapability(gpu.Info{Tier: gpu.TierVAAPI, TierName: "vaapi", Encoder: "vaapih264enc"})
	if va.NVENC || !va.HardwareEncode {
		t.Fatalf("VA-API capability wrong: %+v", va)
	}
}

// TestGamingManager_StartRequiresOwner rejects an unauthenticated gaming start.
func TestGamingManager_StartRequiresOwner(t *testing.T) {
	m := NewGamingManager(NewPool())
	_, _, err := m.Start(GamingLaunchOpts{Command: "/usr/bin/game"})
	if err != ErrGamingOwnerRequired {
		t.Fatalf("want ErrGamingOwnerRequired, got %v", err)
	}
}

// TestGamingManager_OnePerUser verifies the one-active-gaming-session-per-user
// policy: a second Start for the same owner (whose session is live in the pool)
// is refused with ErrGamingSessionActive WITHOUT launching. We seed the pool +
// manager maps directly so no real process is spawned.
func TestGamingManager_OnePerUser(t *testing.T) {
	pool := NewPool()
	m := NewGamingManager(pool)

	// Seed an existing, live gaming session for "user-a".
	sess := &Session{ID: "g1", OwnerID: "user-a", Running: true, gaming: true}
	pool.mu.Lock()
	pool.sessions["g1"] = sess
	pool.mu.Unlock()
	m.mu.Lock()
	m.byOwner["user-a"] = "g1"
	m.bySessID["g1"] = "user-a"
	m.mu.Unlock()

	if got := m.ActiveFor("user-a"); got != "g1" {
		t.Fatalf("ActiveFor=%q want g1", got)
	}

	// Second start for the same owner must be refused (before any launch).
	_, _, err := m.Start(GamingLaunchOpts{UserID: "user-a", Command: "/usr/bin/game2"})
	if err != ErrGamingSessionActive {
		t.Fatalf("want ErrGamingSessionActive, got %v", err)
	}

	// A DIFFERENT user is not blocked by user-a's session (they'd proceed to
	// launch; we only assert they don't hit the one-per-user gate here by
	// checking ActiveFor is empty for them).
	if got := m.ActiveFor("user-b"); got != "" {
		t.Fatalf("user-b should have no active session, got %q", got)
	}
}

// TestGamingManager_StopFreesSlot verifies teardown drops the owner slot so the
// user can start a new session, and enforces ownership (foreign user cannot stop).
func TestGamingManager_StopFreesSlot(t *testing.T) {
	pool := NewPool()
	m := NewGamingManager(pool)
	sess := &Session{ID: "g1", OwnerID: "user-a", Running: true, gaming: true, cancel: func() {}}
	pool.mu.Lock()
	pool.sessions["g1"] = sess
	pool.mu.Unlock()
	m.mu.Lock()
	m.byOwner["user-a"] = "g1"
	m.bySessID["g1"] = "user-a"
	m.mu.Unlock()

	// Foreign user cannot stop user-a's session.
	if err := m.Stop("g1", "user-b"); err == nil {
		t.Fatal("foreign user was allowed to stop another user's gaming session")
	}
	// The slot is still held after the failed foreign stop.
	if got := m.ActiveFor("user-a"); got != "g1" {
		t.Fatalf("slot lost after foreign stop: %q", got)
	}

	// Owner stops → slot freed, pool entry removed.
	if err := m.Stop("g1", "user-a"); err != nil {
		t.Fatalf("owner Stop: %v", err)
	}
	if got := m.ActiveFor("user-a"); got != "" {
		t.Fatalf("slot not freed after owner stop: %q", got)
	}
	if pool.Get("g1") != nil {
		t.Fatal("pool session not removed after gaming stop")
	}
}

// TestGamingManager_ReapReclaimsSlot verifies an out-of-band session end
// reclaims the owner slot so a subsequent start is not falsely blocked.
func TestGamingManager_ReapReclaimsSlot(t *testing.T) {
	m := NewGamingManager(NewPool())
	m.mu.Lock()
	m.byOwner["user-a"] = "g1"
	m.bySessID["g1"] = "user-a"
	m.mu.Unlock()

	m.reap("g1")
	if got := m.ActiveFor("user-a"); got != "" {
		t.Fatalf("reap did not reclaim slot: %q", got)
	}
}
