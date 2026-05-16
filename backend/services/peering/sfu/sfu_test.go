package sfu

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
)

// --------------------------------------------------------------------------
// SFU / Room unit tests
// --------------------------------------------------------------------------

func TestNewRoom_DefaultLastN(t *testing.T) {
	r := NewRoom(0)
	if r.LastN != DefaultLastN {
		t.Fatalf("expected LastN=%d, got %d", DefaultLastN, r.LastN)
	}
	if r.ID == "" {
		t.Fatal("room ID must not be empty")
	}
	r.Close()
}

func TestNewRoom_ValidLastN(t *testing.T) {
	for _, n := range []int{4, 6, 9} {
		r := NewRoom(n)
		if r.LastN != n {
			t.Fatalf("lastN=%d: expected %d, got %d", n, n, r.LastN)
		}
		r.Close()
	}
}

func TestNewRoom_InvalidLastN_FallsBackToDefault(t *testing.T) {
	for _, n := range []int{-1, 0, 3, 5, 7, 10, 100} {
		r := NewRoom(n)
		if r.LastN != DefaultLastN {
			t.Fatalf("lastN=%d should fall back to %d, got %d", n, DefaultLastN, r.LastN)
		}
		r.Close()
	}
}

func TestRoom_CloseIdempotent(t *testing.T) {
	r := NewRoom(6)
	r.Close()
	r.Close() // must not panic
}

func TestRoom_ParticipantCount_Empty(t *testing.T) {
	r := NewRoom(6)
	defer r.Close()
	if c := r.ParticipantCount(); c != 0 {
		t.Fatalf("empty room should have 0 participants, got %d", c)
	}
}

func TestRoom_Leave_UnknownID_NoOp(t *testing.T) {
	r := NewRoom(6)
	defer r.Close()
	r.Leave("nonexistent-id") // must not panic
}

func TestRoom_AddICECandidate_UnknownParticipant(t *testing.T) {
	r := NewRoom(6)
	defer r.Close()
	err := r.AddICECandidate("no-such-participant", webrtcICECandidate())
	if err == nil {
		t.Fatal("expected error for unknown participant")
	}
}

// --------------------------------------------------------------------------
// SFU manager tests
// --------------------------------------------------------------------------

func TestSFU_CreateAndClose(t *testing.T) {
	s := New()
	if s.RoomCount() != 0 {
		t.Fatal("new SFU should have 0 rooms")
	}

	r := s.CreateRoom(6)
	if s.RoomCount() != 1 {
		t.Fatalf("expected 1 room, got %d", s.RoomCount())
	}

	_, ok := s.Room(r.ID)
	if !ok {
		t.Fatal("Room() should find a created room")
	}

	s.CloseRoom(r.ID)
	if s.RoomCount() != 0 {
		t.Fatalf("expected 0 rooms after close, got %d", s.RoomCount())
	}

	_, ok = s.Room(r.ID)
	if ok {
		t.Fatal("Room() should not find a closed room")
	}
}

func TestSFU_CloseRoom_NotFound_NoOp(t *testing.T) {
	s := New()
	s.CloseRoom("nonexistent") // must not panic
}

func TestSFU_MultipleRooms(t *testing.T) {
	s := New()
	r1 := s.CreateRoom(4)
	r2 := s.CreateRoom(6)
	r3 := s.CreateRoom(9)

	if s.RoomCount() != 3 {
		t.Fatalf("expected 3 rooms, got %d", s.RoomCount())
	}
	if r1.ID == r2.ID || r2.ID == r3.ID || r1.ID == r3.ID {
		t.Fatal("room IDs must be unique")
	}

	s.CloseRoom(r1.ID)
	s.CloseRoom(r2.ID)
	s.CloseRoom(r3.ID)
}

// --------------------------------------------------------------------------
// HTTP handler tests (no actual WebRTC — just the HTTP layer)
// --------------------------------------------------------------------------

func TestHandleCreateRoom_DefaultLastN(t *testing.T) {
	s := New()
	mux := http.NewServeMux()
	RegisterSFUHandlers(mux, s)

	req := httptest.NewRequest(http.MethodPost, "/api/sfu/rooms", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "room_id") {
		t.Fatalf("response missing room_id: %s", body)
	}
	if !strings.Contains(body, `"last_n"`) {
		t.Fatalf("response missing last_n: %s", body)
	}
}

func TestHandleCreateRoom_CustomLastN(t *testing.T) {
	s := New()
	mux := http.NewServeMux()
	RegisterSFUHandlers(mux, s)

	req := httptest.NewRequest(http.MethodPost, "/api/sfu/rooms", strings.NewReader(`{"last_n":9}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"last_n":9`) {
		t.Fatalf("expected last_n=9 in response: %s", w.Body.String())
	}
	// Clean up
	if s.RoomCount() != 1 {
		t.Fatalf("expected 1 room, got %d", s.RoomCount())
	}
}

func TestHandleCreateRoom_EmptyBody(t *testing.T) {
	s := New()
	mux := http.NewServeMux()
	RegisterSFUHandlers(mux, s)

	req := httptest.NewRequest(http.MethodPost, "/api/sfu/rooms", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 with empty body, got %d", w.Code)
	}
}

func TestHandleCloseRoom_NotFound(t *testing.T) {
	s := New()
	mux := http.NewServeMux()
	RegisterSFUHandlers(mux, s)

	req := httptest.NewRequest(http.MethodDelete, "/api/sfu/rooms/no-such-room", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleCloseRoom_Exists(t *testing.T) {
	s := New()
	mux := http.NewServeMux()
	RegisterSFUHandlers(mux, s)

	r := s.CreateRoom(6)

	req := httptest.NewRequest(http.MethodDelete, "/api/sfu/rooms/"+r.ID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if s.RoomCount() != 0 {
		t.Fatalf("expected 0 rooms after delete, got %d", s.RoomCount())
	}
}

func TestHandleJoin_RoomNotFound(t *testing.T) {
	s := New()
	mux := http.NewServeMux()
	RegisterSFUHandlers(mux, s)

	body := `{"sdp":"v=0\r\n","type":"offer"}`
	req := httptest.NewRequest(http.MethodPost, "/api/sfu/rooms/no-room/join", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleICE_RoomNotFound(t *testing.T) {
	s := New()
	mux := http.NewServeMux()
	RegisterSFUHandlers(mux, s)

	body := `{"participant_id":"x","candidate":"c","sdp_mid":"0","sdp_mline_index":0}`
	req := httptest.NewRequest(http.MethodPost, "/api/sfu/rooms/no-room/ice", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleICE_ParticipantNotFound(t *testing.T) {
	s := New()
	mux := http.NewServeMux()
	RegisterSFUHandlers(mux, s)

	r := s.CreateRoom(6)
	defer s.CloseRoom(r.ID)

	body := `{"participant_id":"nobody","candidate":"c","sdp_mid":"0","sdp_mline_index":0}`
	req := httptest.NewRequest(http.MethodPost, "/api/sfu/rooms/"+r.ID+"/ice", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown participant, got %d", w.Code)
	}
}

// --------------------------------------------------------------------------
// Last-N logic tests (unit, no Pion PeerConnection)
// --------------------------------------------------------------------------

func TestLastN_DominantSpeakerFirst(t *testing.T) {
	r := NewRoom(4)
	defer r.Close()

	// Inject fake participants directly to test last-N logic without WebRTC.
	r.mu.Lock()
	for _, id := range []string{"p1", "p2", "p3", "p4", "p5"} {
		r.participants[id] = &Participant{ID: id, outputVideo: make(map[string]*webrtc.TrackLocalStaticRTP)}
	}
	r.dominantSpeaker = "p1"
	r.mu.Unlock()

	// Receiver is p5; dominant speaker p1 should appear first.
	sources := r.lastNSourceIDs("p5")
	if len(sources) == 0 {
		t.Fatal("expected at least 1 source")
	}
	if sources[0] != "p1" {
		t.Fatalf("expected dominant speaker p1 first, got %s", sources[0])
	}
	if len(sources) > 4 {
		t.Fatalf("last-N=4 should cap sources at 4, got %d", len(sources))
	}
}

func TestLastN_CappedAtN(t *testing.T) {
	for _, n := range []int{4, 6, 9} {
		r := NewRoom(n)
		r.mu.Lock()
		// Add 20 participants.
		for i := range 20 {
			id := "p" + string(rune('a'+i))
			r.participants[id] = &Participant{ID: id, outputVideo: make(map[string]*webrtc.TrackLocalStaticRTP)}
		}
		r.mu.Unlock()

		sources := r.lastNSourceIDs("pa")
		if len(sources) > n {
			t.Fatalf("lastN=%d: got %d sources (expected ≤%d)", n, len(sources), n)
		}
		r.Close()
	}
}

func TestLastN_ExcludesReceiver(t *testing.T) {
	r := NewRoom(6)
	defer r.Close()

	r.mu.Lock()
	for _, id := range []string{"alice", "bob", "carol"} {
		r.participants[id] = &Participant{ID: id, outputVideo: make(map[string]*webrtc.TrackLocalStaticRTP)}
	}
	r.mu.Unlock()

	for _, src := range r.lastNSourceIDs("alice") {
		if src == "alice" {
			t.Fatal("receiver alice must not appear in its own last-N source list")
		}
	}
}

func TestPreferredVideoLayer_DominantSpeaker(t *testing.T) {
	r := NewRoom(6)
	defer r.Close()
	r.mu.Lock()
	r.dominantSpeaker = "speaker1"
	r.mu.Unlock()

	if layer := r.preferredVideoLayer("speaker1"); layer != "h" {
		t.Fatalf("dominant speaker should get high layer, got %q", layer)
	}
	if layer := r.preferredVideoLayer("other"); layer != "l" {
		t.Fatalf("non-dominant speaker should get low layer, got %q", layer)
	}
}

// --------------------------------------------------------------------------
// SimulcastLayer constants
// --------------------------------------------------------------------------

func TestSimulcastLayerConstants(t *testing.T) {
	if LayerHigh == LayerLow {
		t.Fatal("LayerHigh and LayerLow must be distinct")
	}
	if LayerHigh <= LayerLow {
		t.Fatal("LayerHigh must be > LayerLow")
	}
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// webrtcICECandidate returns a no-op ICECandidateInit for tests that
// need one without live Pion networking.
func webrtcICECandidate() webrtc.ICECandidateInit {
	mid := "0"
	idx := uint16(0)
	return webrtc.ICECandidateInit{
		Candidate:     "candidate:0 1 UDP 2122252543 192.0.2.1 50000 typ host",
		SDPMid:        &mid,
		SDPMLineIndex: &idx,
	}
}
