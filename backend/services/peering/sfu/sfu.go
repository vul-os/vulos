package sfu

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"

	"github.com/pion/webrtc/v4"
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
type SFU struct {
	mu    sync.RWMutex
	rooms map[string]*Room
}

// New creates a new SFU instance with an empty room registry.
func New() *SFU {
	return &SFU{rooms: make(map[string]*Room)}
}

// CreateRoom creates a new room with the given Last-N value and registers it.
// Valid lastN values are 4, 6, and 9; other values fall back to DefaultLastN.
func (s *SFU) CreateRoom(lastN int) *Room {
	r := NewRoom(lastN)
	s.mu.Lock()
	s.rooms[r.ID] = r
	s.mu.Unlock()
	log.Printf("[sfu] room=%s created lastN=%d", r.ID, r.LastN)
	return r
}

// Room returns the room with the given ID, or nil if not found.
func (s *SFU) Room(id string) (*Room, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.rooms[id]
	return r, ok
}

// CloseRoom closes and removes the room with the given ID.
func (s *SFU) CloseRoom(id string) {
	s.mu.Lock()
	r, ok := s.rooms[id]
	if ok {
		delete(s.rooms, id)
	}
	s.mu.Unlock()
	if ok {
		r.Close()
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

	room := s.CreateRoom(req.LastN)
	writeJSON(w, http.StatusCreated, map[string]any{
		"room_id": room.ID,
		"last_n":  room.LastN,
	})
}

// handleCloseRoom handles DELETE /api/sfu/rooms/{room_id}.
func (s *SFU) handleCloseRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("room_id")
	s.mu.RLock()
	_, ok := s.rooms[roomID]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, errRoomNotFound.Error(), http.StatusNotFound)
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
