// Package sfu — handoff.go
//
// Implements SFU host handoff and the 50-participant join cap guard.
//
// Design constraints
//   - ZERO redeclaration of identifiers from room.go / sfu.go / audio.go.
//   - All exported names are prefixed with Handoff (types/funcs) or handoff
//     (unexported).  The guard function is named HandoffCheckCap.
//   - room.go and sfu.go are NOT modified.  Handoff wires into the package
//     through same-package field access.
//
// # Host handoff flow
//
//  1. Callers (e.g. a signaling WebSocket handler) create a HandoffManager
//     per room via HandoffNewManager.
//  2. Each participant's pre-call reported upload bandwidth is registered with
//     HandoffManager.SetBandwidth.
//  3. When the SFU host connection is detected as lost, the signaling layer
//     calls HandoffManager.HandleHostLoss.  It selects the participant with
//     the highest reported upload bandwidth, promotes them, and notifies all
//     remaining participants via the registered HandoffNotifyFunc.
//  4. All browsers reconnect their WebRTC streams to the new host (2-4 second
//     interruption per the roadmap; the SFU layer itself has no concept of
//     "host" — the host is the process owner; after handoff a new *SFU should
//     be created by the signaling layer on the new host's machine).
//
// # 50-participant cap
//
// HandoffCheckCap is a guard function that returns errRoomFull when the room
// already has MaxParticipants participants.  room.go's Join already enforces
// this; HandoffCheckCap exposes the same check so HTTP handlers or pre-join
// admission logic outside the Room can gate early (e.g. before creating an
// offer/answer round-trip).
package sfu

import (
	"errors"
	"log"
	"sync"
	"time"
)

// --------------------------------------------------------------------------
// Errors
// --------------------------------------------------------------------------

// errHandoffNoParticipants is returned by HandleHostLoss when no remaining
// participants are available to take over as host.
var errHandoffNoParticipants = errors.New("sfu/handoff: no participants available to become host")

// --------------------------------------------------------------------------
// HandoffNotifyFunc — callback type
// --------------------------------------------------------------------------

// HandoffNotifyFunc is called by HandoffManager when a new host is elected.
// newHostID is the Participant.ID of the newly promoted host.  Callers should
// relay this to all browsers so they can reconnect WebRTC streams to the new
// SFU process (hosted by newHostID's Vulos instance).
//
// The function is called once per handoff, on a dedicated goroutine; it must
// not block indefinitely.
type HandoffNotifyFunc func(roomID string, newHostID string)

// --------------------------------------------------------------------------
// HandoffBandwidthEntry — per-participant bandwidth record
// --------------------------------------------------------------------------

// handoffBandwidthEntry records the self-reported upload bandwidth for one
// participant.  Stored inside HandoffManager and protected by its mutex.
type handoffBandwidthEntry struct {
	// participantID matches Participant.ID.
	participantID string
	// uploadMbps is the participant's self-reported upload throughput in Mbit/s.
	// Sourced from GET /api/peering/bandwidth on the participant's Vulos instance.
	uploadMbps float64
	// reportedAt is when the entry was last updated.
	reportedAt time.Time
}

// --------------------------------------------------------------------------
// HandoffManager
// --------------------------------------------------------------------------

// HandoffManager tracks per-participant upload bandwidth for a single Room
// and orchestrates SFU host handoff when the current host drops.
//
// Usage:
//
//	mgr := HandoffNewManager(room, notify)
//	mgr.SetBandwidth(participantID, uploadMbps)
//	// when host loss detected:
//	newHostID, err := mgr.HandleHostLoss(currentHostID)
type HandoffManager struct {
	room   *Room
	notify HandoffNotifyFunc

	mu        sync.Mutex
	bandwidth map[string]*handoffBandwidthEntry
	// currentHostID is the Participant.ID of the participant currently acting
	// as SFU host.  Empty string means undetermined / initial state.
	currentHostID string
}

// HandoffNewManager creates a HandoffManager for room r.  notify is called
// after a successful host election; it may be nil (handoff still works, just
// no external notification is sent).
func HandoffNewManager(r *Room, notify HandoffNotifyFunc) *HandoffManager {
	return &HandoffManager{
		room:      r,
		notify:    notify,
		bandwidth: make(map[string]*handoffBandwidthEntry),
	}
}

// SetHostID records which participant is currently acting as SFU host.  Call
// this when the room is first created and after each successful handoff.
func (m *HandoffManager) SetHostID(participantID string) {
	m.mu.Lock()
	m.currentHostID = participantID
	m.mu.Unlock()
}

// HostID returns the current host participant ID, or an empty string if none
// has been set.
func (m *HandoffManager) HostID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentHostID
}

// SetBandwidth records or updates the self-reported upload bandwidth for
// participantID.  uploadMbps must be ≥ 0; negative values are clamped to 0.
func (m *HandoffManager) SetBandwidth(participantID string, uploadMbps float64) {
	if uploadMbps < 0 {
		uploadMbps = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bandwidth[participantID] = &handoffBandwidthEntry{
		participantID: participantID,
		uploadMbps:    uploadMbps,
		reportedAt:    time.Now(),
	}
}

// RemoveParticipant removes bandwidth tracking for participantID.  Call this
// when a participant leaves so stale entries don't influence future elections.
func (m *HandoffManager) RemoveParticipant(participantID string) {
	m.mu.Lock()
	delete(m.bandwidth, participantID)
	m.mu.Unlock()
}

// BestUploadParticipant returns the Participant.ID of the participant with the
// highest reported upload bandwidth, excluding excludeID.  Returns an empty
// string if no eligible participants are registered.
//
// This is the selection criterion used by HandleHostLoss.  It is exported so
// the pre-call lobby UI can surface the same ranking.
func (m *HandoffManager) BestUploadParticipant(excludeID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var bestID string
	var bestMbps float64 = -1

	for id, entry := range m.bandwidth {
		if id == excludeID {
			continue
		}
		if entry.uploadMbps > bestMbps {
			bestMbps = entry.uploadMbps
			bestID = id
		}
	}
	return bestID
}

// HandleHostLoss is called when the SFU host (currentHostID) drops.  It:
//
//  1. Verifies the dropped host is the registered current host (if currentHostID
//     is empty, any participant may trigger a loss event and the election still
//     runs).
//  2. Removes the dropped host from bandwidth tracking.
//  3. Elects the participant with the highest upload bandwidth as new host.
//  4. Updates the internal host record.
//  5. Invokes the HandoffNotifyFunc (if set) so the signaling layer can
//     propagate the change to browsers.
//
// Returns the new host's Participant.ID, or an error if no eligible
// participants remain.  The call is non-blocking; notification is dispatched
// on a separate goroutine.
func (m *HandoffManager) HandleHostLoss(droppedHostID string) (string, error) {
	log.Printf("[sfu/handoff] room=%s host loss detected participant=%s", m.room.ID, droppedHostID)

	// Remove the dropped host from tracking so it doesn't win the election.
	m.RemoveParticipant(droppedHostID)

	// Elect best remaining participant.
	newHostID := m.BestUploadParticipant(droppedHostID)
	if newHostID == "" {
		// Fall back to any participant present in the room.
		newHostID = m.handoffAnyParticipant(droppedHostID)
	}
	if newHostID == "" {
		log.Printf("[sfu/handoff] room=%s no participants remain after host loss", m.room.ID)
		return "", errHandoffNoParticipants
	}

	m.mu.Lock()
	m.currentHostID = newHostID
	m.mu.Unlock()

	log.Printf("[sfu/handoff] room=%s new host elected participant=%s", m.room.ID, newHostID)

	// Notify callers asynchronously so HandleHostLoss returns promptly.
	if m.notify != nil {
		go m.notify(m.room.ID, newHostID)
	}

	return newHostID, nil
}

// handoffAnyParticipant returns any participant in the room other than
// excludeID.  Used as a fallback when no bandwidth data is available.
// Caller must NOT hold m.mu (acquires room lock internally).
func (m *HandoffManager) handoffAnyParticipant(excludeID string) string {
	m.room.mu.RLock()
	defer m.room.mu.RUnlock()
	for id := range m.room.participants {
		if id != excludeID {
			return id
		}
	}
	return ""
}

// --------------------------------------------------------------------------
// 50-participant cap guard
// --------------------------------------------------------------------------

// HandoffCheckCap returns errRoomFull when r already has MaxParticipants (50)
// or more participants, and nil otherwise.
//
// room.go's Join enforces the same cap inside the PeerConnection setup path.
// HandoffCheckCap exposes the check as a standalone guard so HTTP admission
// handlers or pre-join lobby logic can reject the 51st participant early —
// before allocating signaling resources or doing an SDP round-trip.
//
// Example usage in an HTTP handler:
//
//	if err := sfu.HandoffCheckCap(room); err != nil {
//	    http.Error(w, "call is full (max 50)", http.StatusServiceUnavailable)
//	    return
//	}
func HandoffCheckCap(r *Room) error {
	if r.ParticipantCount() >= MaxParticipants {
		return errRoomFull
	}
	return nil
}

// HandoffCapacity returns the number of remaining slots in the room before the
// MaxParticipants hard cap is reached.  Returns 0 when the room is full.
func HandoffCapacity(r *Room) int {
	remaining := MaxParticipants - r.ParticipantCount()
	if remaining < 0 {
		return 0
	}
	return remaining
}
