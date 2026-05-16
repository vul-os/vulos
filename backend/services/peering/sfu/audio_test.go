package sfu

import (
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

// newTestRoom creates a minimal Room suitable for unit tests (no real
// PeerConnections; participants are injected directly).
func newTestRoom() *Room {
	r := NewRoom(DefaultLastN)
	return r
}

// addTestParticipant inserts a bare Participant (no PeerConnection) into r.
func addTestParticipant(r *Room, id string) *Participant {
	p := &Participant{
		ID:             id,
		room:           r,
		videoTracks:    make(map[string]*webrtc.TrackRemote),
		outputVideo:    make(map[string]*webrtc.TrackLocalStaticRTP),
		lastAudioLevel: 127, // silent
	}
	r.mu.Lock()
	r.participants[id] = p
	r.mu.Unlock()
	return p
}

// fakeRTPPacket creates a minimal valid RTP packet with the given payload.
func fakeRTPPacket(payload []byte) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111, // Opus
			SequenceNumber: 1,
			Timestamp:      0,
			SSRC:           0x1234,
		},
		Payload: payload,
	}
}

// --------------------------------------------------------------------------
// AudioMixer lifecycle
// --------------------------------------------------------------------------

func TestAudioStartMixer_NonNil(t *testing.T) {
	r := newTestRoom()
	defer r.Close()

	m := AudioStartMixer(r)
	if m == nil {
		t.Fatal("AudioStartMixer must not return nil")
	}
	m.Stop()
}

func TestAudioMixer_StopIdempotent(t *testing.T) {
	r := newTestRoom()
	defer r.Close()

	m := AudioStartMixer(r)
	m.Stop()
	m.Stop() // must not panic
}

// --------------------------------------------------------------------------
// VAD threshold
// --------------------------------------------------------------------------

func TestAudioSetVADThreshold(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	m.AudioSetVADThreshold(60)
	m.mu.Lock()
	got := m.vadThreshold
	m.mu.Unlock()
	if got != 60 {
		t.Fatalf("expected vadThreshold=60, got %d", got)
	}
}

// --------------------------------------------------------------------------
// AudioProcessPacket — level tracking
// --------------------------------------------------------------------------

func TestAudioProcessPacket_UpdatesLevel(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	p := addTestParticipant(r, "p1")
	pkt := fakeRTPPacket(make([]byte, 100))

	m.AudioProcessPacket(p, pkt, 30) // loud

	level := AudioParticipantLevel(m, "p1")
	if level != 30 {
		t.Fatalf("expected level=30, got %d", level)
	}
}

func TestAudioProcessPacket_NeverHearsSelf(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	// Two participants, no real tracks — just verify that audioActiveSpeakers
	// always excludes the receiver.
	addTestParticipant(r, "alice")
	addTestParticipant(r, "bob")

	// Mark both as speaking.
	m.mu.Lock()
	m.levels["alice"] = &audioLevelEntry{participantID: "alice", level: 10, speaking: true, lastActive: time.Now()}
	m.levels["bob"] = &audioLevelEntry{participantID: "bob", level: 20, speaking: true, lastActive: time.Now()}
	m.mu.Unlock()

	// When querying top speakers for "alice", "alice" must not appear.
	topForAlice := m.audioActiveSpeakers("alice")
	for _, id := range topForAlice {
		if id == "alice" {
			t.Fatal("participant must not appear in their own audio mix")
		}
	}

	// When querying top speakers for "bob", "bob" must not appear.
	topForBob := m.audioActiveSpeakers("bob")
	for _, id := range topForBob {
		if id == "bob" {
			t.Fatal("participant must not appear in their own audio mix")
		}
	}
}

// --------------------------------------------------------------------------
// Top-N constraint
// --------------------------------------------------------------------------

func TestAudioActiveSpeakers_AtMostTopN(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	// Insert 10 speaking participants.
	for i := range 10 {
		id := string(rune('a' + i))
		addTestParticipant(r, id)
		m.mu.Lock()
		m.levels[id] = &audioLevelEntry{
			participantID: id,
			level:         uint8(i * 5), // ascending levels
			speaking:      true,
			lastActive:    time.Now(),
		}
		m.mu.Unlock()
	}

	// Receiver "r" is not a participant in the level map.
	top := m.audioActiveSpeakers("r")
	if len(top) > audioMixTopN {
		t.Fatalf("audioActiveSpeakers must return ≤%d entries, got %d", audioMixTopN, len(top))
	}
}

func TestAudioActiveSpeakers_ExcludesReceiver(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	ids := []string{"x", "y", "z"}
	for _, id := range ids {
		addTestParticipant(r, id)
		m.mu.Lock()
		m.levels[id] = &audioLevelEntry{
			participantID: id,
			level:         10,
			speaking:      true,
			lastActive:    time.Now(),
		}
		m.mu.Unlock()
	}

	for _, recv := range ids {
		top := m.audioActiveSpeakers(recv)
		for _, id := range top {
			if id == recv {
				t.Fatalf("receiver %q must not be in their own speaker list", recv)
			}
		}
	}
}

func TestAudioActiveSpeakers_OnlyReturnsSpoken(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	addTestParticipant(r, "loud")
	addTestParticipant(r, "silent")

	m.mu.Lock()
	m.levels["loud"] = &audioLevelEntry{participantID: "loud", level: 20, speaking: true, lastActive: time.Now()}
	m.levels["silent"] = &audioLevelEntry{participantID: "silent", level: 120, speaking: false}
	m.mu.Unlock()

	top := m.audioActiveSpeakers("irrelevant-receiver")
	for _, id := range top {
		if id == "silent" {
			t.Fatal("silent participant must not appear in top speakers")
		}
	}
	found := false
	for _, id := range top {
		if id == "loud" {
			found = true
		}
	}
	if !found {
		t.Fatal("loud participant must appear in top speakers")
	}
}

// --------------------------------------------------------------------------
// Dominant speaker election
// --------------------------------------------------------------------------

func TestAudioDominantSpeakerID_InitiallyEmpty(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	if id := AudioDominantSpeakerID(m); id != "" {
		t.Fatalf("expected empty dominant, got %q", id)
	}
}

func TestAudioDominantSpeakerID_NilMixer(t *testing.T) {
	if id := AudioDominantSpeakerID(nil); id != "" {
		t.Fatalf("nil mixer must return empty string, got %q", id)
	}
}

func TestElectDominantSpeaker_PicksLoudest(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	addTestParticipant(r, "quiet")
	addTestParticipant(r, "loud")

	m.mu.Lock()
	m.levels["quiet"] = &audioLevelEntry{participantID: "quiet", level: 70, speaking: true, lastActive: time.Now()}
	m.levels["loud"] = &audioLevelEntry{participantID: "loud", level: 10, speaking: true, lastActive: time.Now()}
	m.mu.Unlock()

	m.electDominantSpeaker()

	got := AudioDominantSpeakerID(m)
	if got != "loud" {
		t.Fatalf("expected dominant=loud, got %q", got)
	}
}

func TestElectDominantSpeaker_UpdatesRoomField(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	addTestParticipant(r, "speaker1")

	m.mu.Lock()
	m.levels["speaker1"] = &audioLevelEntry{
		participantID: "speaker1", level: 5, speaking: true, lastActive: time.Now(),
	}
	m.mu.Unlock()

	m.electDominantSpeaker()

	r.mu.RLock()
	ds := r.dominantSpeaker
	r.mu.RUnlock()

	if ds != "speaker1" {
		t.Fatalf("room.dominantSpeaker should be speaker1, got %q", ds)
	}
}

func TestElectDominantSpeaker_HoldTime(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	addTestParticipant(r, "a")
	addTestParticipant(r, "b")

	m.mu.Lock()
	m.levels["a"] = &audioLevelEntry{participantID: "a", level: 5, speaking: true, lastActive: time.Now()}
	m.levels["b"] = &audioLevelEntry{participantID: "b", level: 3, speaking: true, lastActive: time.Now()}
	// Set "a" as dominant, just elected.
	m.dominant = "a"
	m.dominantSeen = time.Now()
	m.mu.Unlock()

	// "b" is louder but hold time hasn't elapsed — "a" should remain.
	m.electDominantSpeaker()

	got := AudioDominantSpeakerID(m)
	if got != "a" {
		t.Fatalf("dominant should be held as 'a' during hold period, got %q", got)
	}
}

func TestElectDominantSpeaker_ClearWhenSilent(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	addTestParticipant(r, "p")

	// Set "p" as current dominant but silence has elapsed.
	m.mu.Lock()
	m.dominant = "p"
	m.dominantSeen = time.Now().Add(-2 * audioDominantHoldTime)
	m.levels["p"] = &audioLevelEntry{participantID: "p", level: 120, speaking: false}
	m.mu.Unlock()

	m.electDominantSpeaker()

	got := AudioDominantSpeakerID(m)
	if got != "" {
		t.Fatalf("dominant should be cleared when everyone is silent, got %q", got)
	}
}

// --------------------------------------------------------------------------
// AudioDominantLayer — simulcast layer selection
// --------------------------------------------------------------------------

func TestAudioDominantLayer_NilMixer(t *testing.T) {
	layer := AudioDominantLayer(nil, "anyone")
	if layer != "l" {
		t.Fatalf("nil mixer must return 'l', got %q", layer)
	}
}

func TestAudioDominantLayer_DominantGetsHigh(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	addTestParticipant(r, "speaker")

	m.mu.Lock()
	m.dominant = "speaker"
	m.mu.Unlock()

	layer := AudioDominantLayer(m, "speaker")
	if layer != "h" {
		t.Fatalf("dominant speaker must get high layer, got %q", layer)
	}
}

func TestAudioDominantLayer_NonDominantGetsLow(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	addTestParticipant(r, "speaker")
	addTestParticipant(r, "other")

	m.mu.Lock()
	m.dominant = "speaker"
	m.mu.Unlock()

	layer := AudioDominantLayer(m, "other")
	if layer != "l" {
		t.Fatalf("non-dominant participant must get low layer, got %q", layer)
	}
}

func TestAudioDominantLayer_EmptyDominantGivesLow(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	// No dominant set.
	layer := AudioDominantLayer(m, "p1")
	if layer != "l" {
		t.Fatalf("no dominant set must give low layer, got %q", layer)
	}
}

// --------------------------------------------------------------------------
// AudioParticipantLevel
// --------------------------------------------------------------------------

func TestAudioParticipantLevel_UnknownReturns127(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	lvl := AudioParticipantLevel(m, "nobody")
	if lvl != 127 {
		t.Fatalf("unknown participant must return 127, got %d", lvl)
	}
}

func TestAudioParticipantLevel_NilMixer(t *testing.T) {
	lvl := AudioParticipantLevel(nil, "p")
	if lvl != 127 {
		t.Fatalf("nil mixer must return 127, got %d", lvl)
	}
}

// --------------------------------------------------------------------------
// AudioRemoveParticipant
// --------------------------------------------------------------------------

func TestAudioRemoveParticipant_ClearsState(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	addTestParticipant(r, "leaver")

	m.mu.Lock()
	m.levels["leaver"] = &audioLevelEntry{participantID: "leaver", level: 10, speaking: true, lastActive: time.Now()}
	m.mu.Unlock()

	AudioRemoveParticipant(m, "leaver")

	lvl := AudioParticipantLevel(m, "leaver")
	if lvl != 127 {
		t.Fatalf("removed participant must return 127 level, got %d", lvl)
	}
}

func TestAudioRemoveParticipant_ClearsDominant(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	addTestParticipant(r, "ds")

	m.mu.Lock()
	m.dominant = "ds"
	m.levels["ds"] = &audioLevelEntry{participantID: "ds", level: 5, speaking: true, lastActive: time.Now()}
	m.mu.Unlock()

	AudioRemoveParticipant(m, "ds")

	if got := AudioDominantSpeakerID(m); got != "" {
		t.Fatalf("dominant should be cleared after removal, got %q", got)
	}

	r.mu.RLock()
	ds := r.dominantSpeaker
	r.mu.RUnlock()
	if ds != "" {
		t.Fatalf("room.dominantSpeaker should be cleared, got %q", ds)
	}
}

func TestAudioRemoveParticipant_NilSafe(t *testing.T) {
	AudioRemoveParticipant(nil, "p") // must not panic
}

// --------------------------------------------------------------------------
// AudioProcessPacket — RFC 6464 absent level (128) falls back gracefully
// --------------------------------------------------------------------------

func TestAudioProcessPacket_NoRFC6464Extension(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	p := addTestParticipant(r, "noext")
	// Payload > 80 bytes → treated as moderately loud (level=40).
	pkt := fakeRTPPacket(make([]byte, 100))
	m.AudioProcessPacket(p, pkt, 128)

	lvl := AudioParticipantLevel(m, "noext")
	if lvl != 40 {
		t.Fatalf("large payload without RFC6464 should give level=40, got %d", lvl)
	}
}

func TestAudioProcessPacket_SmallPayloadNoExt(t *testing.T) {
	r := newTestRoom()
	defer r.Close()
	m := AudioStartMixer(r)
	defer m.Stop()

	p := addTestParticipant(r, "smallpkt")
	// Payload ≤ 80 bytes → treated as quiet (level=100).
	pkt := fakeRTPPacket(make([]byte, 20))
	m.AudioProcessPacket(p, pkt, 128)

	lvl := AudioParticipantLevel(m, "smallpkt")
	if lvl != 100 {
		t.Fatalf("small payload without RFC6464 should give level=100, got %d", lvl)
	}
}
