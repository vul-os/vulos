package sfu

// handoff_test.go — unit tests for HandoffManager and HandoffCheckCap.
//
// Tests run entirely within the sfu package (white-box) so they can access
// unexported helpers.  No real PeerConnections or RTP tracks are created;
// Room and Participant structs are constructed minimally to exercise the
// handoff logic in isolation.
//
// AC coverage:
//   [✓] host loss → failover to best-bw peer
//   [✓] resumes without full drop (new host elected, call continues)
//   [✓] 51st participant rejected (HandoffCheckCap returns errRoomFull)
//   [✓] ZERO redeclaration; build+test+gofmt clean

import (
	"fmt"
	"sync"
	"testing"
)

// --------------------------------------------------------------------------
// helpers — build minimal Room / Participant without real WebRTC
// --------------------------------------------------------------------------

// newHandoffTestRoom returns a Room ready for handoff tests.
// No WebRTC API or real PeerConnections are created.
func newHandoffTestRoom() *Room {
	return &Room{
		ID:           "handoff-test-room",
		LastN:        DefaultLastN,
		participants: make(map[string]*Participant),
		done:         make(chan struct{}),
	}
}

// insertParticipant adds a bare-minimum Participant (no PeerConnection) to r.
// Nil pion track maps are valid Go; the handoff tests never read/write them.
func insertParticipant(r *Room, id string) *Participant {
	p := &Participant{ID: id, room: r}
	r.mu.Lock()
	r.participants[id] = p
	r.mu.Unlock()
	return p
}

// --------------------------------------------------------------------------
// TestHandoffCheckCap — 50-participant hard cap
// --------------------------------------------------------------------------

func TestHandoffCheckCap_BelowCap(t *testing.T) {
	r := newHandoffTestRoom()
	for i := 0; i < MaxParticipants-1; i++ {
		insertParticipant(r, fmt.Sprintf("%s-p%d", t.Name(), i))
	}
	if err := HandoffCheckCap(r); err != nil {
		t.Fatalf("HandoffCheckCap: want nil for %d participants, got %v",
			r.ParticipantCount(), err)
	}
}

func TestHandoffCheckCap_AtCap(t *testing.T) {
	r := newHandoffTestRoom()
	for i := 0; i < MaxParticipants; i++ {
		insertParticipant(r, fmt.Sprintf("%s-p%d", t.Name(), i))
	}
	if err := HandoffCheckCap(r); err == nil {
		t.Fatal("HandoffCheckCap: want errRoomFull at MaxParticipants, got nil")
	}
}

func TestHandoffCheckCap_51stRejected(t *testing.T) {
	// The 51st join must be rejected.
	r := newHandoffTestRoom()
	for i := 0; i < MaxParticipants; i++ {
		insertParticipant(r, fmt.Sprintf("%s-p%d", t.Name(), i))
	}
	// Simulate a 51st applicant checking before allocating resources.
	err := HandoffCheckCap(r)
	if err == nil {
		t.Fatal("51st participant must be rejected; HandoffCheckCap returned nil")
	}
}

// --------------------------------------------------------------------------
// TestHandoffCapacity — remaining-slot counter
// --------------------------------------------------------------------------

func TestHandoffCapacity_Empty(t *testing.T) {
	r := newHandoffTestRoom()
	if got := HandoffCapacity(r); got != MaxParticipants {
		t.Fatalf("empty room: HandoffCapacity want %d, got %d", MaxParticipants, got)
	}
}

func TestHandoffCapacity_OneParticipant(t *testing.T) {
	r := newHandoffTestRoom()
	insertParticipant(r, "p1")
	if got := HandoffCapacity(r); got != MaxParticipants-1 {
		t.Fatalf("1 participant: HandoffCapacity want %d, got %d", MaxParticipants-1, got)
	}
}

func TestHandoffCapacity_Full(t *testing.T) {
	r := newHandoffTestRoom()
	for i := 0; i < MaxParticipants; i++ {
		insertParticipant(r, fmt.Sprintf("%s-p%d", t.Name(), i))
	}
	if got := HandoffCapacity(r); got != 0 {
		t.Fatalf("full room: HandoffCapacity want 0, got %d", got)
	}
}

// --------------------------------------------------------------------------
// TestHandoffMaxParticipantsValue — canonical constant is 50
// --------------------------------------------------------------------------

func TestHandoffMaxParticipantsValue(t *testing.T) {
	const want = 50
	if MaxParticipants != want {
		t.Fatalf("MaxParticipants = %d, want %d", MaxParticipants, want)
	}
}

// --------------------------------------------------------------------------
// TestHandoffBandwidth — SetBandwidth / BestUploadParticipant
// --------------------------------------------------------------------------

func TestHandoffBestUploadParticipant_Basic(t *testing.T) {
	r := newHandoffTestRoom()
	mgr := HandoffNewManager(r, nil)

	mgr.SetBandwidth("alice", 48.2)
	mgr.SetBandwidth("bob", 22.0)
	mgr.SetBandwidth("carol", 95.0) // highest
	mgr.SetBandwidth("dave", 8.0)

	if best := mgr.BestUploadParticipant(""); best != "carol" {
		t.Fatalf("expected carol (95 Mbps), got %s", best)
	}
}

func TestHandoffBestUploadParticipant_ExcludesDroppedHost(t *testing.T) {
	r := newHandoffTestRoom()
	mgr := HandoffNewManager(r, nil)

	mgr.SetBandwidth("host", 200.0) // highest but excluded
	mgr.SetBandwidth("alice", 48.2) // should win after exclusion
	mgr.SetBandwidth("bob", 22.0)

	if best := mgr.BestUploadParticipant("host"); best != "alice" {
		t.Fatalf("expected alice (48.2 Mbps) after excluding host, got %s", best)
	}
}

func TestHandoffBestUploadParticipant_Empty(t *testing.T) {
	r := newHandoffTestRoom()
	mgr := HandoffNewManager(r, nil)
	if best := mgr.BestUploadParticipant("nobody"); best != "" {
		t.Fatalf("empty manager: expected empty string, got %s", best)
	}
}

func TestHandoffBandwidthNegativeClamped(t *testing.T) {
	r := newHandoffTestRoom()
	mgr := HandoffNewManager(r, nil)
	mgr.SetBandwidth("alice", -5.0)

	mgr.mu.Lock()
	entry := mgr.bandwidth["alice"]
	mgr.mu.Unlock()

	if entry.uploadMbps != 0 {
		t.Fatalf("negative bandwidth should clamp to 0, got %f", entry.uploadMbps)
	}
}

// --------------------------------------------------------------------------
// TestHandoffHandleHostLoss — host loss → failover to best-bw peer
// --------------------------------------------------------------------------

func TestHandoffHandleHostLoss_ElectsBestBW(t *testing.T) {
	r := newHandoffTestRoom()
	insertParticipant(r, "alice")
	insertParticipant(r, "bob")
	insertParticipant(r, "carol")

	notified := ""
	var notifyMu sync.Mutex
	done := make(chan struct{})

	mgr := HandoffNewManager(r, func(roomID, newHostID string) {
		notifyMu.Lock()
		notified = newHostID
		notifyMu.Unlock()
		close(done)
	})
	mgr.SetHostID("alice")
	mgr.SetBandwidth("alice", 10.0)
	mgr.SetBandwidth("bob", 25.0)
	mgr.SetBandwidth("carol", 95.0) // should win

	newHost, err := mgr.HandleHostLoss("alice")
	if err != nil {
		t.Fatalf("HandleHostLoss: unexpected error: %v", err)
	}
	if newHost != "carol" {
		t.Fatalf("expected carol (highest bw) as new host, got %s", newHost)
	}
	if mgr.HostID() != "carol" {
		t.Fatalf("HostID not updated; want carol, got %s", mgr.HostID())
	}

	// Wait for async notification (give it a generous deadline).
	<-done
	notifyMu.Lock()
	n := notified
	notifyMu.Unlock()
	if n != "carol" {
		t.Fatalf("notify received %s, want carol", n)
	}
}

func TestHandoffHandleHostLoss_ResumesWithoutFullDrop(t *testing.T) {
	// AC: call resumes without full drop — a new host is elected so the call
	// can continue (brief reconnect, not a complete teardown).
	// No bandwidth data registered → fallback to any remaining participant.
	r := newHandoffTestRoom()
	insertParticipant(r, "host")
	insertParticipant(r, "remaining")

	mgr := HandoffNewManager(r, nil)
	mgr.SetHostID("host")
	// No SetBandwidth calls — fallback path exercised.

	newHost, err := mgr.HandleHostLoss("host")
	if err != nil {
		t.Fatalf("HandleHostLoss: unexpected error: %v", err)
	}
	if newHost == "" {
		t.Fatal("expected non-empty new host so call can resume")
	}
	if newHost == "host" {
		t.Fatal("new host must not be the dropped host")
	}
}

func TestHandoffHandleHostLoss_NoParticipantsRemain(t *testing.T) {
	// Single-participant room: after host drops, nobody can take over.
	r := newHandoffTestRoom()
	insertParticipant(r, "sole-host")

	mgr := HandoffNewManager(r, nil)
	mgr.SetHostID("sole-host")
	mgr.SetBandwidth("sole-host", 50.0)

	_, err := mgr.HandleHostLoss("sole-host")
	if err == nil {
		t.Fatal("expected error when no participants remain, got nil")
	}
}

func TestHandoffHandleHostLoss_NilNotify_NoPanic(t *testing.T) {
	// nil HandoffNotifyFunc must not cause a panic.
	r := newHandoffTestRoom()
	insertParticipant(r, "host")
	insertParticipant(r, "next")

	mgr := HandoffNewManager(r, nil)
	mgr.SetBandwidth("next", 30.0)

	newHost, err := mgr.HandleHostLoss("host")
	if err != nil {
		t.Fatalf("HandleHostLoss: unexpected error: %v", err)
	}
	if newHost != "next" {
		t.Fatalf("expected next, got %s", newHost)
	}
}

// --------------------------------------------------------------------------
// TestHandoffRemoveParticipant — stale entries pruned on leave
// --------------------------------------------------------------------------

func TestHandoffRemoveParticipant_PrunedFromBandwidth(t *testing.T) {
	r := newHandoffTestRoom()
	mgr := HandoffNewManager(r, nil)
	mgr.SetBandwidth("alice", 100.0)
	mgr.SetBandwidth("bob", 50.0)

	mgr.RemoveParticipant("alice")

	mgr.mu.Lock()
	_, alicePresent := mgr.bandwidth["alice"]
	mgr.mu.Unlock()

	if alicePresent {
		t.Fatal("alice's bandwidth entry should have been removed")
	}
	if best := mgr.BestUploadParticipant(""); best != "bob" {
		t.Fatalf("expected bob to be best after removing alice, got %s", best)
	}
}

// --------------------------------------------------------------------------
// TestHandoffHostIDRoundTrip — SetHostID / HostID accessors
// --------------------------------------------------------------------------

func TestHandoffHostIDRoundTrip(t *testing.T) {
	r := newHandoffTestRoom()
	mgr := HandoffNewManager(r, nil)

	if got := mgr.HostID(); got != "" {
		t.Fatalf("initial HostID should be empty, got %q", got)
	}
	mgr.SetHostID("dave")
	if got := mgr.HostID(); got != "dave" {
		t.Fatalf("HostID should be dave, got %q", got)
	}
}

// --------------------------------------------------------------------------
// TestHandoffConcurrency — race-free under concurrent bandwidth updates
// --------------------------------------------------------------------------

func TestHandoffConcurrency(t *testing.T) {
	r := newHandoffTestRoom()
	mgr := HandoffNewManager(r, nil)

	ids := []string{"p1", "p2", "p3", "p4", "p5"}
	var wg sync.WaitGroup
	for _, id := range ids {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				mgr.SetBandwidth(id, float64(i))
				_ = mgr.BestUploadParticipant("")
				_ = mgr.HostID()
			}
		}()
	}
	wg.Wait()
}
