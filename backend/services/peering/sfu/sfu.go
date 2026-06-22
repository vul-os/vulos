package sfu

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"

	"github.com/pion/webrtc/v4"

	"vulos/backend/internal/cpbilling"
)

// sentinel errors
var (
	errRoomFull            = errors.New("sfu: room is full")
	errParticipantNotFound = errors.New("sfu: participant not found")
	errRoomNotFound        = errors.New("sfu: room not found")
)

// SFU is the top-level Selective Forwarding Unit manager. It owns a registry
// of active Rooms and exposes HTTP handlers for browser ↔ SFU signaling.
//
// Usage:
//
//	s := sfu.New()
//	sfu.RegisterSFUHandlers(mux, s)
// roomEntry holds a Room together with the user ID of its creator.
type roomEntry struct {
	room    *Room
	ownerID string // X-User-ID of the caller who created this room; empty = any
}

type SFU struct {
	mu    sync.RWMutex
	rooms map[string]*roomEntry

	// billing GATES room creation (meet_enabled, suspended, meet_minutes_budget,
	// meet_max_rooms) and METERS rooms against cp. Nil or disabled (CP_URL
	// unset) = standalone OS: Meet is ungated/unmetered. See WithBilling for
	// enforcement-status notes.
	billing *cpbilling.Client
}

// New creates a new SFU instance with an empty room registry.
func New() *SFU {
	return &SFU{rooms: make(map[string]*roomEntry)}
}

// WithBilling wires a cp billing client so room creation is gated on
// meet_enabled, suspended, meet_minutes_budget, and the meet_max_rooms
// concurrent-room cap. The room count is read in-process from RoomCount()
// (authoritative). A nil/disabled client is a no-op (standalone OS). Returns
// s for chaining.
//
// ENFORCEMENT STATUS:
//   - meet_enabled + suspended: ENFORCED (cp authoritative)
//   - meet_max_rooms concurrent cap: ENFORCED (in-process RoomCount)
//   - meet_minutes_budget=0: ENFORCED (hard-zero = no budget at all)
//   - meet_minutes_budget > 0 (remaining minutes): NOT precisely enforced —
//     cp returns the cap not the remaining balance; cp must flip meet_enabled
//     or suspended when the monthly budget is exhausted.
func (s *SFU) WithBilling(b *cpbilling.Client) *SFU {
	s.billing = b
	return s
}

// CreateRoom creates a new room with the given Last-N value and registers it
// under ownerID.  ownerID is the authenticated user (X-User-ID); pass ""
// for system/unowned rooms.
// Valid lastN values are 4, 6, and 9; other values fall back to DefaultLastN.
func (s *SFU) CreateRoom(lastN int, ownerID string) *Room {
	r := NewRoom(lastN)
	s.mu.Lock()
	s.rooms[r.ID] = &roomEntry{room: r, ownerID: ownerID}
	s.mu.Unlock()
	log.Printf("[sfu] room=%s created lastN=%d owner=%q", r.ID, r.LastN, ownerID)
	return r
}

// Room returns the room with the given ID, or nil if not found.
func (s *SFU) Room(id string) (*Room, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.rooms[id]
	if !ok {
		return nil, false
	}
	return entry.room, true
}

// IsOwner reports whether userID is allowed to manage room id.
// Unowned rooms (ownerID == "") are managed by anyone; otherwise only the
// creator may close the room.
func (s *SFU) IsOwner(id, userID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.rooms[id]
	if !ok {
		return false
	}
	return entry.ownerID == "" || entry.ownerID == userID
}

// CloseRoom closes and removes the room with the given ID.
func (s *SFU) CloseRoom(id string) {
	s.mu.Lock()
	entry, ok := s.rooms[id]
	if ok {
		delete(s.rooms, id)
	}
	s.mu.Unlock()
	if ok {
		entry.room.Close()
	}
}

// RoomCount returns the number of active rooms.
func (s *SFU) RoomCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.rooms)
}

// RegisterSFUHandlers registers SFU HTTP handlers on mux under /api/sfu/.
//
// Routes:
//
//	POST /api/sfu/rooms              — create a room, returns {room_id, last_n}
//	DELETE /api/sfu/rooms/{room_id}  — close a room
//	POST /api/sfu/rooms/{room_id}/join   — join (offer→answer SDP exchange)
//	POST /api/sfu/rooms/{room_id}/ice    — trickle ICE candidate
func RegisterSFUHandlers(mux *http.ServeMux, s *SFU) {
	mux.HandleFunc("POST /api/sfu/rooms", s.handleCreateRoom)
	mux.HandleFunc("DELETE /api/sfu/rooms/{room_id}", s.handleCloseRoom)
	mux.HandleFunc("POST /api/sfu/rooms/{room_id}/join", s.handleJoin)
	mux.HandleFunc("POST /api/sfu/rooms/{room_id}/ice", s.handleICE)
}

// --------------------------------------------------------------------------
// HTTP handlers
// --------------------------------------------------------------------------

// handleCreateRoom handles POST /api/sfu/rooms.
//
// Request body (JSON, optional):
//
//	{ "last_n": 6 }
//
// Response (JSON):
//
//	{ "room_id": "...", "last_n": 6 }
func (s *SFU) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LastN int `json:"last_n"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
	}
	if req.LastN == 0 {
		req.LastN = DefaultLastN
	}

	ownerID := r.Header.Get("X-User-ID")

	// BILLING GATE (surface 4: Meet). Enforces meet_enabled, suspended,
	// meet_minutes_budget, and the meet_max_rooms concurrent-room cap
	// (in-process RoomCount is authoritative). Fail-open/degraded on a cold cp
	// outage. No-op when billing is disabled (standalone OS).
	if s.billing.Enabled() {
		if d := s.billing.GateMeet(r.Context(), ownerID, s.RoomCount()); !d.Allowed {
			http.Error(w, "account not entitled: "+d.Reason, http.StatusPaymentRequired)
			return
		}
	}

	room := s.CreateRoom(req.LastN, ownerID)

	// METER (surface 4: Meet). One room created. No-op when disabled.
	s.billing.MeterAsync(cpbilling.UsageEvent{
		Product:   cpbilling.ProductMeet,
		AccountID: ownerID,
		Kind:      cpbilling.KindMeetRoom,
		Count:     1,
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"room_id": room.ID,
		"last_n":  room.LastN,
	})
}

// handleCloseRoom handles DELETE /api/sfu/rooms/{room_id}.
// Only the room creator (or any user for unowned rooms) may close a room.
func (s *SFU) handleCloseRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("room_id")
	callerID := r.Header.Get("X-User-ID")

	s.mu.RLock()
	entry, ok := s.rooms[roomID]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, errRoomNotFound.Error(), http.StatusNotFound)
		return
	}
	// Ownership check: only the creator may delete; unowned rooms are open.
	if entry.ownerID != "" && entry.ownerID != callerID {
		http.Error(w, "forbidden: only the room creator may close this room", http.StatusForbidden)
		return
	}
	s.CloseRoom(roomID)
	w.WriteHeader(http.StatusNoContent)
}

// handleJoin handles POST /api/sfu/rooms/{room_id}/join.
//
// Request body (JSON):
//
//	{ "sdp": "<SDP offer>", "type": "offer" }
//
// Response (JSON):
//
//	{ "participant_id": "...", "sdp": "<SDP answer>", "type": "answer" }
func (s *SFU) handleJoin(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("room_id")
	room, ok := s.Room(roomID)
	if !ok {
		http.Error(w, errRoomNotFound.Error(), http.StatusNotFound)
		return
	}

	var req struct {
		SDP  string `json:"sdp"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  req.SDP,
	}

	p, answer, err := room.Join(offer)
	if err != nil {
		if errors.Is(err, errRoomFull) {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		log.Printf("[sfu] join room=%s: %v", roomID, err)
		http.Error(w, "join failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"participant_id": p.ID,
		"sdp":            answer.SDP,
		"type":           "answer",
	})
}

// handleICE handles POST /api/sfu/rooms/{room_id}/ice.
//
// Request body (JSON):
//
//	{
//	  "participant_id": "...",
//	  "candidate": "...",
//	  "sdp_mid": "...",
//	  "sdp_mline_index": 0
//	}
func (s *SFU) handleICE(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("room_id")
	room, ok := s.Room(roomID)
	if !ok {
		http.Error(w, errRoomNotFound.Error(), http.StatusNotFound)
		return
	}

	var req struct {
		ParticipantID string `json:"participant_id"`
		Candidate     string `json:"candidate"`
		SDPMid        string `json:"sdp_mid"`
		SDPMLineIndex uint16 `json:"sdp_mline_index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	init := webrtc.ICECandidateInit{
		Candidate:     req.Candidate,
		SDPMid:        &req.SDPMid,
		SDPMLineIndex: &req.SDPMLineIndex,
	}

	if err := room.AddICECandidate(req.ParticipantID, init); err != nil {
		if errors.Is(err, errParticipantNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, "ice failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[sfu] json encode: %v", err)
	}
}
