package main

// MEET-TRANSCRIPT-01 — per-room Whisper transcription endpoints.
//
// Endpoints (all under /api/meet/transcribe/...):
//
//   POST /api/meet/transcribe/start          body: {room_id, opt_in:true}
//                                            Acknowledges the per-meeting
//                                            opt-in toggle.  Without a prior
//                                            /start call the /audio endpoint
//                                            returns 412 (precondition).
//
//   POST /api/meet/transcribe/audio          body: raw audio bytes
//                                            query: ?room_id=…&content_type=…
//                                            Buffers a single audio chunk
//                                            (~5s window) and asynchronously
//                                            transcribes via llmuxclient Whisper.
//                                            Fragments are fanned out on the
//                                            room's SSE stream below.
//
//   GET  /api/meet/transcribe/stream/{room_id}
//                                            SSE: one `data: {Text, Final, …}`
//                                            line per fragment.  Live + late
//                                            subscribers both join; a short
//                                            ring buffer (32 fragments) lets
//                                            late subs catch up.
//
//   POST /api/meet/transcribe/stop           body: {room_id}
//                                            Releases the buffer + ends all
//                                            SSE subscribers for the room.
//
// Opt-in:
//   - Global toggle: VULOS_MEET_TRANSCRIBE_ENABLE.  When unset, the routes
//     are still registered but every endpoint short-circuits to 503.
//   - Per-meeting toggle: the office UI calls /start before the first /audio.
//
// Auth:
//   - All endpoints require X-OS-Session (validated by the same adapter the
//     identity package uses).  If the auth store is unavailable the routes
//     degrade to dev-permissive (X-User-ID header trusted) so local builds
//     work without a logged-in session.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"vulos/backend/internal/llmuxclient"
	svcauth "vulos/backend/services/auth"
)

// maxAudioChunkBytes caps a single /audio upload at ~5 MB (≈ 30s of 16kHz PCM16).
const maxAudioChunkBytes = 5 * 1024 * 1024

// fragmentBufferSize is the per-room ring buffer for late subscribers.
const fragmentBufferSize = 32

// transcriber is the package-level singleton; nil = transcription disabled.
type meetTranscriber struct {
	provider  llmuxclient.TranscriptionProvider
	authStore *svcauth.Store

	mu    sync.RWMutex
	rooms map[string]*meetRoomState
}

type meetRoomState struct {
	mu        sync.Mutex
	optedIn   bool
	buffer    []llmuxclient.TranscriptFragment // ring buffer
	subs      map[chan llmuxclient.TranscriptFragment]struct{}
	createdAt time.Time
}

func newMeetTranscriber(provider llmuxclient.TranscriptionProvider, authStore *svcauth.Store) *meetTranscriber {
	return &meetTranscriber{
		provider:  provider,
		authStore: authStore,
		rooms:     make(map[string]*meetRoomState),
	}
}

// enabled reports whether the global env opt-in + a provider are present.
func (m *meetTranscriber) enabled() bool {
	if m == nil || m.provider == nil {
		return false
	}
	return os.Getenv("VULOS_MEET_TRANSCRIBE_ENABLE") == "1"
}

// requireSession resolves the OS session token from the request.  Returns
// ("", false) when not present / invalid.  Dev-permissive when authStore is
// nil: any non-empty X-User-ID counts.
func (m *meetTranscriber) requireSession(r *http.Request) (string, bool) {
	if m == nil {
		return "", false
	}
	if tok := r.Header.Get("X-OS-Session"); tok != "" && m.authStore != nil {
		if sess, ok := m.authStore.ValidateToken(tok); ok && sess != nil {
			return sess.UserID, true
		}
	}
	// Dev fallback: trust X-User-ID when no auth store is bound.
	if m.authStore == nil {
		if u := r.Header.Get("X-User-ID"); u != "" {
			return u, true
		}
	}
	return "", false
}

// ensureRoom returns the per-room state, creating it lazily.
func (m *meetTranscriber) ensureRoom(roomID string) *meetRoomState {
	m.mu.RLock()
	st, ok := m.rooms[roomID]
	m.mu.RUnlock()
	if ok {
		return st
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok = m.rooms[roomID]; ok {
		return st
	}
	st = &meetRoomState{
		subs:      make(map[chan llmuxclient.TranscriptFragment]struct{}),
		createdAt: time.Now().UTC(),
	}
	m.rooms[roomID] = st
	return st
}

// publish appends a fragment to the room buffer and fans out to subscribers.
func (s *meetRoomState) publish(fr llmuxclient.TranscriptFragment) {
	s.mu.Lock()
	if len(s.buffer) >= fragmentBufferSize {
		s.buffer = append(s.buffer[1:], fr)
	} else {
		s.buffer = append(s.buffer, fr)
	}
	subs := make([]chan llmuxclient.TranscriptFragment, 0, len(s.subs))
	for ch := range s.subs {
		subs = append(subs, ch)
	}
	s.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- fr:
		default:
			// Drop on slow consumer rather than block.
		}
	}
}

// subscribe registers a new SSE subscriber and replays the ring buffer.
func (s *meetRoomState) subscribe() (chan llmuxclient.TranscriptFragment, []llmuxclient.TranscriptFragment) {
	ch := make(chan llmuxclient.TranscriptFragment, 16)
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := append([]llmuxclient.TranscriptFragment(nil), s.buffer...)
	s.subs[ch] = struct{}{}
	return ch, snapshot
}

func (s *meetRoomState) unsubscribe(ch chan llmuxclient.TranscriptFragment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[ch]; ok {
		delete(s.subs, ch)
		close(ch)
	}
}

// close drains and closes all subscribers.
func (s *meetRoomState) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		close(ch)
	}
	s.subs = map[chan llmuxclient.TranscriptFragment]struct{}{}
	s.buffer = nil
	s.optedIn = false
}

// registerMeetTranscriptRoutes wires the four endpoints onto mux.  Safe to
// call with provider==nil — the endpoints then return 503 (feature off).
func registerMeetTranscriptRoutes(mux *http.ServeMux, provider llmuxclient.TranscriptionProvider, authStore *svcauth.Store) *meetTranscriber {
	mt := newMeetTranscriber(provider, authStore)

	// POST /api/meet/transcribe/start
	mux.HandleFunc("POST /api/meet/transcribe/start", func(w http.ResponseWriter, r *http.Request) {
		if !mt.enabled() {
			writeMeetErr(w, 503, "transcription disabled (set VULOS_MEET_TRANSCRIBE_ENABLE=1 and configure VULOS_WHISPER_URL)")
			return
		}
		if _, ok := mt.requireSession(r); !ok {
			writeMeetErr(w, 401, "unauthorized")
			return
		}
		var req struct {
			RoomID string `json:"room_id"`
			OptIn  bool   `json:"opt_in"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RoomID == "" {
			writeMeetErr(w, 400, "room_id required")
			return
		}
		if !req.OptIn {
			writeMeetErr(w, 400, "opt_in must be true")
			return
		}
		st := mt.ensureRoom(req.RoomID)
		st.mu.Lock()
		st.optedIn = true
		st.mu.Unlock()
		writeMeetJSON(w, map[string]any{"room_id": req.RoomID, "status": "armed"})
	})

	// POST /api/meet/transcribe/audio
	mux.HandleFunc("POST /api/meet/transcribe/audio", func(w http.ResponseWriter, r *http.Request) {
		if !mt.enabled() {
			writeMeetErr(w, 503, "transcription disabled")
			return
		}
		if _, ok := mt.requireSession(r); !ok {
			writeMeetErr(w, 401, "unauthorized")
			return
		}
		roomID := r.URL.Query().Get("room_id")
		if roomID == "" {
			writeMeetErr(w, 400, "room_id required")
			return
		}
		ct := r.URL.Query().Get("content_type")
		if ct == "" {
			ct = r.Header.Get("Content-Type")
		}
		if ct == "" {
			ct = "audio/wav"
		}

		mt.mu.RLock()
		st := mt.rooms[roomID]
		mt.mu.RUnlock()
		if st == nil {
			writeMeetErr(w, 412, "transcription not started for room (call /start first)")
			return
		}
		st.mu.Lock()
		opted := st.optedIn
		st.mu.Unlock()
		if !opted {
			writeMeetErr(w, 412, "opt-in not set for room")
			return
		}

		audio, err := io.ReadAll(io.LimitReader(r.Body, maxAudioChunkBytes+1))
		if err != nil {
			writeMeetErr(w, 400, "read audio: "+err.Error())
			return
		}
		if len(audio) > maxAudioChunkBytes {
			writeMeetErr(w, 413, "audio chunk too large")
			return
		}
		if len(audio) == 0 {
			writeMeetJSON(w, map[string]any{"room_id": roomID, "buffered": 0})
			return
		}

		// Asynchronously transcribe + publish.  Use the server lifetime context
		// so a slow Whisper call doesn't block the response.
		go func(roomID, ct string, audio []byte, st *meetRoomState) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			frags, err := mt.provider.Transcribe(ctx, audio, ct, "")
			if err != nil {
				return
			}
			for _, fr := range frags {
				if fr.ReceivedAt.IsZero() {
					fr.ReceivedAt = time.Now().UTC()
				}
				st.publish(fr)
			}
		}(roomID, ct, audio, st)

		writeMeetJSON(w, map[string]any{"room_id": roomID, "buffered": len(audio)})
	})

	// GET /api/meet/transcribe/stream/{room_id}
	mux.HandleFunc("GET /api/meet/transcribe/stream/{room_id}", func(w http.ResponseWriter, r *http.Request) {
		if !mt.enabled() {
			writeMeetErr(w, 503, "transcription disabled")
			return
		}
		if _, ok := mt.requireSession(r); !ok {
			writeMeetErr(w, 401, "unauthorized")
			return
		}
		roomID := r.PathValue("room_id")
		if roomID == "" {
			writeMeetErr(w, 400, "room_id required")
			return
		}
		st := mt.ensureRoom(roomID)
		ch, snapshot := st.subscribe()
		defer st.unsubscribe(ch)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		flusher, _ := w.(http.Flusher)
		writeFragment := func(fr llmuxclient.TranscriptFragment) bool {
			b, _ := json.Marshal(fr)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return false
			}
			if flusher != nil {
				flusher.Flush()
			}
			return true
		}
		// Replay any buffered fragments first.
		for _, fr := range snapshot {
			if !writeFragment(fr) {
				return
			}
		}

		ctx := r.Context()
		// Heartbeat every 15s so the connection stays open through proxies.
		hb := time.NewTicker(15 * time.Second)
		defer hb.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case fr, ok := <-ch:
				if !ok {
					return
				}
				if !writeFragment(fr) {
					return
				}
			case <-hb.C:
				if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	})

	// POST /api/meet/transcribe/stop
	mux.HandleFunc("POST /api/meet/transcribe/stop", func(w http.ResponseWriter, r *http.Request) {
		if !mt.enabled() {
			writeMeetErr(w, 503, "transcription disabled")
			return
		}
		if _, ok := mt.requireSession(r); !ok {
			writeMeetErr(w, 401, "unauthorized")
			return
		}
		var req struct {
			RoomID string `json:"room_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RoomID == "" {
			writeMeetErr(w, 400, "room_id required")
			return
		}
		mt.mu.Lock()
		st := mt.rooms[req.RoomID]
		delete(mt.rooms, req.RoomID)
		mt.mu.Unlock()
		if st != nil {
			st.closeAll()
		}
		writeMeetJSON(w, map[string]any{"room_id": req.RoomID, "status": "stopped"})
	})

	return mt
}

func writeMeetJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

func writeMeetErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// meetTranscribeEnvFromTier flips VULOS_MEET_TRANSCRIBE_ENABLE based on a tier
// hint at startup so Pro/cloud users default-on and self-host stays default-off
// unless the operator opts in explicitly.
func meetTranscribeEnvFromTier(tier string) {
	if os.Getenv("VULOS_MEET_TRANSCRIBE_ENABLE") != "" {
		return // explicit operator setting wins.
	}
	switch strings.ToLower(tier) {
	case "pro", "team", "business", "enterprise":
		_ = os.Setenv("VULOS_MEET_TRANSCRIBE_ENABLE", "1")
	}
}
