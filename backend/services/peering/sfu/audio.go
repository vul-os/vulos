// Package sfu — audio.go
//
// Implements voice-activity detection (VAD), dominant-speaker selection, and
// per-participant audio mixing (top-3 active speakers, excluding self) for the
// SFU room defined in room.go / sfu.go.
//
// Design constraints
//   - ZERO new top-level identifiers that shadow anything in room.go or sfu.go.
//   - All exported names carry the prefix Audio or Dominant to prevent collision.
//   - room.go and sfu.go are NOT modified; audio.go wires itself in through the
//     package-internal field access that same-package Go code allows.
//
// Integration point
//
//	room.go's forwardAudio already routes every inbound audio packet to all
//	other participants. Call AudioProcessPacket from a forwarder goroutine (or
//	from a thin wrapper launched at join time via AudioStartMixer) so that
//	1. the VAD updates per-participant levels,
//	2. dominant-speaker election updates room.dominantSpeaker, and
//	3. mixed audio (top-3 excl. self) is written to each receiver's outputAudio
//	   track — replacing raw forwarding when the mixer is active.
//
// Simulcast tie-in
//
//	AudioDominantLayer(room, participantID) returns "h" when participantID is
//	the current dominant speaker and "l" otherwise.  room.go's
//	preferredVideoLayer already implements this logic using dominantSpeaker;
//	AudioDominantLayer is the equivalent public helper that room.go could
//	delegate to (or callers outside the package can use directly).
package sfu

import (
	"log"
	"sort"
	"sync"
	"time"

	"github.com/pion/rtp"
)

// --------------------------------------------------------------------------
// Constants
// --------------------------------------------------------------------------

// audioMixTopN is the maximum number of audio streams mixed per participant.
// Each participant receives at most this many concurrent audio streams,
// regardless of call size.  (Roadmap: "mix top 3 active speakers".)
const audioMixTopN = 3

// audioVADThreshold is the RFC 6464 audio level below which a participant is
// considered silent.  RFC 6464 encodes level as 0–127 where 0 is the loudest
// and 127 is silence.  A threshold of 80 filters background noise while
// keeping normal speech; callers may override via AudioSetVADThreshold.
const audioVADThreshold uint8 = 80

// audioSilenceTimeout is how long a participant must be continuously below
// audioVADThreshold before they are no longer counted as "speaking".
const audioSilenceTimeout = 500 * time.Millisecond

// audioDominantHoldTime is how long the current dominant speaker retains that
// status after their audio level drops.  Prevents rapid speaker switching.
const audioDominantHoldTime = 1 * time.Second

// --------------------------------------------------------------------------
// AudioLevelEntry — per-participant voice-activity state
// --------------------------------------------------------------------------

// audioLevelEntry tracks the most recent audio activity for one participant.
// It is stored inside audioMixer and is protected by audioMixer.mu.
type audioLevelEntry struct {
	// participantID is a copy of Participant.ID for map lookups.
	participantID string
	// level is the last observed RFC 6464 audio level (0=loudest, 127=silent).
	level uint8
	// lastActive is the time the participant was last above the VAD threshold.
	lastActive time.Time
	// speaking is true when the participant is currently considered active.
	speaking bool
}

// --------------------------------------------------------------------------
// AudioMixer — VAD + dominant speaker + per-participant audio mix
// --------------------------------------------------------------------------

// AudioMixer tracks voice-activity levels for every participant in a Room,
// elects the dominant speaker, and decides which top-N audio streams to
// forward to each receiver.
//
// One AudioMixer is created per Room by AudioStartMixer.  It is safe for
// concurrent use from multiple goroutines (one per inbound audio track).
type AudioMixer struct {
	room *Room

	mu           sync.Mutex
	levels       map[string]*audioLevelEntry // keyed by participantID
	dominant     string                      // current dominant participantID
	dominantSeen time.Time                   // when dominant was last elected

	vadThreshold uint8 // configurable VAD threshold
	done         chan struct{}
	once         sync.Once
}

// AudioStartMixer creates and returns an AudioMixer attached to r.
// The mixer's background goroutine runs until AudioMixer.Stop is called or
// the room is closed (it watches room.done).
func AudioStartMixer(r *Room) *AudioMixer {
	m := &AudioMixer{
		room:         r,
		levels:       make(map[string]*audioLevelEntry),
		vadThreshold: audioVADThreshold,
		done:         make(chan struct{}),
	}
	go m.dominantSpeakerLoop()
	return m
}

// Stop shuts down the mixer's background goroutine.  Idempotent.
func (m *AudioMixer) Stop() {
	m.once.Do(func() { close(m.done) })
}

// AudioSetVADThreshold overrides the RFC 6464 level threshold used to decide
// whether a participant is speaking.  Lower values are more permissive
// (more participants qualify as speaking); 0 means everyone is always speaking.
// The default is audioVADThreshold (80).
func (m *AudioMixer) AudioSetVADThreshold(t uint8) {
	m.mu.Lock()
	m.vadThreshold = t
	m.mu.Unlock()
}

// --------------------------------------------------------------------------
// Packet processing (called from forwardAudio goroutines)
// --------------------------------------------------------------------------

// AudioProcessPacket observes an inbound audio RTP packet from sender and
// updates that participant's VAD state.  It then:
//
//  1. Updates room.dominantSpeaker when the dominant speaker changes.
//  2. Writes mixed audio (top-3 excl. self) to each eligible receiver's
//     outputAudio track.
//
// It is safe to call from multiple goroutines concurrently.
//
// rfc6464Level is the audio level extracted from the RFC 6464 RTP header
// extension (0=loudest, 127=silent, 128=absent).  If 128 is passed the
// packet's payload size is used as a crude energy proxy.
func (m *AudioMixer) AudioProcessPacket(sender *Participant, pkt *rtp.Packet, rfc6464Level uint8) {
	level := rfc6464Level
	if level == 128 {
		// No RFC 6464 extension — estimate from payload length.
		// Larger payloads generally correspond to louder speech with Opus CBR.
		// This is intentionally coarse; real deployments should use RFC 6464.
		if len(pkt.Payload) > 80 {
			level = 40 // treat as moderately loud
		} else {
			level = 100 // treat as quiet / comfort noise
		}
	}

	m.mu.Lock()
	entry := m.ensureEntry(sender.ID)
	entry.level = level
	thresh := m.vadThreshold

	now := time.Now()
	if level <= thresh {
		// Below threshold means the participant is speaking (0=loud in RFC 6464).
		entry.lastActive = now
		entry.speaking = true
	} else if now.Sub(entry.lastActive) > audioSilenceTimeout {
		entry.speaking = false
	}
	m.mu.Unlock()

	// Update the participant's own fields (same package — direct field access).
	sender.mu.Lock()
	sender.lastAudioLevel = level
	sender.speaking = entry.speaking
	sender.mu.Unlock()

	// Write mixed audio to all eligible receivers.
	m.audioMixAndForward(sender, pkt)
}

// ensureEntry returns the audioLevelEntry for participantID, creating it
// if absent.  Caller must hold m.mu.
func (m *AudioMixer) ensureEntry(participantID string) *audioLevelEntry {
	e, ok := m.levels[participantID]
	if !ok {
		e = &audioLevelEntry{
			participantID: participantID,
			level:         127, // silent until proven otherwise
		}
		m.levels[participantID] = e
	}
	return e
}

// --------------------------------------------------------------------------
// Dominant-speaker election
// --------------------------------------------------------------------------

// dominantSpeakerLoop runs in a background goroutine and periodically
// re-elects the dominant speaker based on current VAD state.
func (m *AudioMixer) dominantSpeakerLoop() {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.electDominantSpeaker()
		case <-m.done:
			return
		case <-m.room.done:
			return
		}
	}
}

// electDominantSpeaker picks the loudest currently-speaking participant and
// writes their ID into room.dominantSpeaker.  It respects audioDominantHoldTime
// so the dominant speaker is not switched away too quickly.
func (m *AudioMixer) electDominantSpeaker() {
	m.mu.Lock()
	now := time.Now()

	// Build a candidate list: participants that are currently speaking.
	type candidate struct {
		id    string
		level uint8
	}
	candidates := make([]candidate, 0, len(m.levels))
	for _, e := range m.levels {
		if e.speaking {
			candidates = append(candidates, candidate{id: e.participantID, level: e.level})
		}
	}

	currentDominant := m.dominant
	dominantSeen := m.dominantSeen
	m.mu.Unlock()

	if len(candidates) == 0 {
		// Nobody is speaking; retain the dominant for the hold period then clear.
		if currentDominant != "" && now.Sub(dominantSeen) > audioDominantHoldTime {
			m.setDominant("")
		}
		return
	}

	// Sort ascending by level (lower RFC 6464 level = louder).
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].level < candidates[j].level
	})
	loudest := candidates[0].id

	if loudest == currentDominant {
		// Still the same speaker — just refresh the hold timer.
		m.mu.Lock()
		m.dominantSeen = now
		m.mu.Unlock()
		return
	}

	// Switch only if hold time has elapsed for the current dominant.
	if currentDominant != "" && now.Sub(dominantSeen) < audioDominantHoldTime {
		return
	}

	m.setDominant(loudest)
}

// setDominant updates both the mixer's local state and room.dominantSpeaker.
func (m *AudioMixer) setDominant(participantID string) {
	m.mu.Lock()
	m.dominant = participantID
	m.dominantSeen = time.Now()
	m.mu.Unlock()

	// Write into the room — same package, so direct field write is fine.
	m.room.mu.Lock()
	m.room.dominantSpeaker = participantID
	m.room.mu.Unlock()

	if participantID != "" {
		log.Printf("[sfu/audio] room=%s dominant speaker -> %s", m.room.ID, participantID)
	}

	// Update Participant.speaking flags to match.
	m.room.mu.RLock()
	participants := make([]*Participant, 0, len(m.room.participants))
	for _, p := range m.room.participants {
		participants = append(participants, p)
	}
	m.room.mu.RUnlock()

	for _, p := range participants {
		p.mu.Lock()
		p.speaking = (p.ID == participantID)
		p.mu.Unlock()
	}
}

// --------------------------------------------------------------------------
// Top-3 audio mixing
// --------------------------------------------------------------------------

// audioActiveSpeakers returns up to audioMixTopN participant IDs that are
// currently speaking (loudest first), excluding excludeID (the receiver).
// Caller must NOT hold m.mu.
func (m *AudioMixer) audioActiveSpeakers(excludeID string) []string {
	m.mu.Lock()
	type sp struct {
		id    string
		level uint8
	}
	speaking := make([]sp, 0, len(m.levels))
	for _, e := range m.levels {
		if e.participantID == excludeID {
			continue
		}
		if e.speaking {
			speaking = append(speaking, sp{id: e.participantID, level: e.level})
		}
	}
	m.mu.Unlock()

	// Sort ascending by level (lower = louder in RFC 6464).
	sort.Slice(speaking, func(i, j int) bool {
		return speaking[i].level < speaking[j].level
	})

	result := make([]string, 0, audioMixTopN)
	for i, s := range speaking {
		if i >= audioMixTopN {
			break
		}
		result = append(result, s.id)
	}
	return result
}

// audioMixAndForward decides which receivers should hear the sender's audio
// (based on top-3 active speakers per receiver, excluding self) and writes
// the raw RTP packet to each eligible receiver's outputAudio track.
//
// AC: each receiver hears at most audioMixTopN (3) streams and never hears
// their own voice.
func (m *AudioMixer) audioMixAndForward(sender *Participant, pkt *rtp.Packet) {
	raw, err := pkt.Marshal()
	if err != nil {
		return
	}

	m.room.mu.RLock()
	receivers := make([]*Participant, 0, len(m.room.participants))
	for _, p := range m.room.participants {
		// AC: never hears self.
		if p.ID != sender.ID {
			receivers = append(receivers, p)
		}
	}
	m.room.mu.RUnlock()

	for _, recv := range receivers {
		// AC: each receiver hears at most audioMixTopN streams.
		topN := m.audioActiveSpeakers(recv.ID)
		eligible := false
		for _, id := range topN {
			if id == sender.ID {
				eligible = true
				break
			}
		}
		if !eligible {
			continue
		}

		recv.mu.Lock()
		track := recv.outputAudio
		recv.mu.Unlock()

		if track != nil {
			if _, err := track.Write(raw); err != nil {
				log.Printf("[sfu/audio] write to %s: %v", recv.ID, err)
			}
		}
	}
}

// --------------------------------------------------------------------------
// Public helper — simulcast layer query
// --------------------------------------------------------------------------

// AudioDominantLayer returns the simulcast RID that should be forwarded for
// sourceParticipantID when delivering video to any receiver.
//
//   - "h" (LayerHigh) when sourceParticipantID is the current dominant speaker.
//   - "l" (LayerLow)  otherwise.
//
// This mirrors room.go's internal preferredVideoLayer logic and is the
// exported surface room.go (or callers) can delegate to.  Using a separate
// AudioMixer instance keeps the audio-level data co-located with the decision.
func AudioDominantLayer(m *AudioMixer, sourceParticipantID string) string {
	if m == nil {
		return "l"
	}
	m.mu.Lock()
	dominant := m.dominant
	m.mu.Unlock()
	if sourceParticipantID == dominant && dominant != "" {
		return "h"
	}
	return "l"
}

// AudioDominantSpeakerID returns the ID of the current dominant speaker in
// the room managed by m, or an empty string if nobody is speaking.
func AudioDominantSpeakerID(m *AudioMixer) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dominant
}

// AudioParticipantLevel returns the most recently observed RFC 6464 audio
// level for the participant with the given ID.  Returns 127 (silence) if the
// participant is unknown.
func AudioParticipantLevel(m *AudioMixer, participantID string) uint8 {
	if m == nil {
		return 127
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.levels[participantID]; ok {
		return e.level
	}
	return 127
}

// AudioRemoveParticipant removes all VAD state for participantID.  Call this
// when a participant leaves so stale levels don't affect speaker election.
func AudioRemoveParticipant(m *AudioMixer, participantID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.levels, participantID)
	wasDominant := m.dominant == participantID
	if wasDominant {
		m.dominant = ""
	}
	m.mu.Unlock()

	if wasDominant {
		m.room.mu.Lock()
		m.room.dominantSpeaker = ""
		m.room.mu.Unlock()
	}
}
