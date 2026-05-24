package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"vulos/backend/internal/airouter"
)

// fakeProvider implements airouter.TranscriptionProvider for tests.
type fakeProvider struct {
	mu       sync.Mutex
	calls    int
	respText string
	err      error
}

func (f *fakeProvider) Transcribe(ctx context.Context, audio []byte, ct, model string) ([]airouter.TranscriptFragment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.respText == "" {
		return nil, nil
	}
	return []airouter.TranscriptFragment{{
		Text:       f.respText,
		Final:      true,
		ReceivedAt: time.Now().UTC(),
	}}, nil
}

func (f *fakeProvider) Name() string { return "fake" }

// buildMux registers the meet routes against a fake provider.  authStore is
// nil (dev-permissive) so X-User-ID alone is accepted as session.
func buildMux(t *testing.T, provider airouter.TranscriptionProvider) (*http.ServeMux, *meetTranscriber) {
	t.Helper()
	t.Setenv("VULOS_MEET_TRANSCRIBE_ENABLE", "1")
	mux := http.NewServeMux()
	mt := registerMeetTranscriptRoutes(mux, provider, nil)
	return mux, mt
}

func doJSON(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var b io.Reader
	if body != nil {
		buf := &bytes.Buffer{}
		_ = json.NewEncoder(buf).Encode(body)
		b = buf
	}
	req := httptest.NewRequest(method, path, b)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "u-test")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestMeetTranscribe_DisabledWithoutEnv(t *testing.T) {
	// Don't call t.Setenv; the global is set/unset across tests. Use a brand
	// new env override.
	t.Setenv("VULOS_MEET_TRANSCRIBE_ENABLE", "")
	mux := http.NewServeMux()
	registerMeetTranscriptRoutes(mux, &fakeProvider{respText: "x"}, nil)

	rr := doJSON(t, mux, "POST", "/api/meet/transcribe/start", map[string]any{"room_id": "r1", "opt_in": true})
	if rr.Code != 503 {
		t.Fatalf("expected 503 without env, got %d", rr.Code)
	}
}

func TestMeetTranscribe_NoProviderReturns503(t *testing.T) {
	t.Setenv("VULOS_MEET_TRANSCRIBE_ENABLE", "1")
	mux := http.NewServeMux()
	registerMeetTranscriptRoutes(mux, nil, nil)
	rr := doJSON(t, mux, "POST", "/api/meet/transcribe/start", map[string]any{"room_id": "r1", "opt_in": true})
	if rr.Code != 503 {
		t.Fatalf("nil provider: expected 503, got %d", rr.Code)
	}
}

func TestMeetTranscribe_SessionRequired(t *testing.T) {
	// authStore != nil path; we simulate by skipping X-User-ID with nil store.
	// With nil authStore we use the dev fallback (trust X-User-ID), so build a
	// separate route with a marker that disables that fallback: pass empty
	// header.
	t.Setenv("VULOS_MEET_TRANSCRIBE_ENABLE", "1")
	mux := http.NewServeMux()
	registerMeetTranscriptRoutes(mux, &fakeProvider{respText: "x"}, nil)

	req := httptest.NewRequest("POST", "/api/meet/transcribe/start", strings.NewReader(`{"room_id":"r1","opt_in":true}`))
	req.Header.Set("Content-Type", "application/json")
	// no X-User-ID, no X-OS-Session → 401
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("expected 401 without session, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestMeetTranscribe_StartArmsRoom(t *testing.T) {
	mux, mt := buildMux(t, &fakeProvider{respText: "hi"})
	rr := doJSON(t, mux, "POST", "/api/meet/transcribe/start", map[string]any{"room_id": "r1", "opt_in": true})
	if rr.Code != 200 {
		t.Fatalf("start: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if st := mt.rooms["r1"]; st == nil || !st.optedIn {
		t.Fatalf("room not opted in")
	}
}

func TestMeetTranscribe_StartRequiresOptIn(t *testing.T) {
	mux, _ := buildMux(t, &fakeProvider{respText: "hi"})
	rr := doJSON(t, mux, "POST", "/api/meet/transcribe/start", map[string]any{"room_id": "r1", "opt_in": false})
	if rr.Code != 400 {
		t.Fatalf("expected 400 without opt_in, got %d", rr.Code)
	}
}

func TestMeetTranscribe_AudioRequiresStart(t *testing.T) {
	mux, _ := buildMux(t, &fakeProvider{respText: "hi"})
	req := httptest.NewRequest("POST", "/api/meet/transcribe/audio?room_id=unstarted&content_type=audio/wav", bytes.NewReader([]byte{0x01, 0x02}))
	req.Header.Set("X-User-ID", "u-test")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 412 {
		t.Fatalf("expected 412 without /start, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestMeetTranscribe_AudioBuffersAndStreams(t *testing.T) {
	prov := &fakeProvider{respText: "hello"}
	mux, mt := buildMux(t, prov)

	// 1. Start room.
	rr := doJSON(t, mux, "POST", "/api/meet/transcribe/start", map[string]any{"room_id": "r2", "opt_in": true})
	if rr.Code != 200 {
		t.Fatalf("start: %d", rr.Code)
	}

	// 2. Upload audio chunk.
	req := httptest.NewRequest("POST", "/api/meet/transcribe/audio?room_id=r2", bytes.NewReader([]byte{1, 2, 3}))
	req.Header.Set("X-User-ID", "u-test")
	req.Header.Set("Content-Type", "audio/wav")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("audio: %d body=%s", rr.Code, rr.Body.String())
	}

	// 3. Wait for the async transcription to publish a fragment.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := mt.rooms["r2"]
		st.mu.Lock()
		hasFrag := len(st.buffer) > 0
		st.mu.Unlock()
		if hasFrag {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 4. Late SSE subscriber should receive the replayed fragment.
	streamReq := httptest.NewRequest("GET", "/api/meet/transcribe/stream/r2", nil)
	streamReq.Header.Set("X-User-ID", "u-test")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	streamReq = streamReq.WithContext(ctx)
	streamRR := httptest.NewRecorder()
	mux.ServeHTTP(streamRR, streamReq)

	// Read SSE lines.
	scanner := bufio.NewScanner(strings.NewReader(streamRR.Body.String()))
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") && strings.Contains(line, `"text":"hello"`) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected hello fragment in SSE; got: %s", streamRR.Body.String())
	}

	prov.mu.Lock()
	calls := prov.calls
	prov.mu.Unlock()
	if calls == 0 {
		t.Errorf("expected provider to be called at least once")
	}
}

func TestMeetTranscribe_StopTearsDown(t *testing.T) {
	mux, mt := buildMux(t, &fakeProvider{respText: "x"})
	doJSON(t, mux, "POST", "/api/meet/transcribe/start", map[string]any{"room_id": "r3", "opt_in": true})
	rr := doJSON(t, mux, "POST", "/api/meet/transcribe/stop", map[string]any{"room_id": "r3"})
	if rr.Code != 200 {
		t.Fatalf("stop: %d", rr.Code)
	}
	mt.mu.RLock()
	_, exists := mt.rooms["r3"]
	mt.mu.RUnlock()
	if exists {
		t.Errorf("room should be deleted after /stop")
	}
}

func TestMeetTranscribe_AudioTooLarge(t *testing.T) {
	mux, _ := buildMux(t, &fakeProvider{respText: "x"})
	doJSON(t, mux, "POST", "/api/meet/transcribe/start", map[string]any{"room_id": "rL", "opt_in": true})
	big := make([]byte, maxAudioChunkBytes+10)
	req := httptest.NewRequest("POST", "/api/meet/transcribe/audio?room_id=rL", bytes.NewReader(big))
	req.Header.Set("X-User-ID", "u-test")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 413 {
		t.Fatalf("expected 413, got %d", rr.Code)
	}
}

func TestMeetTranscribeEnvFromTier(t *testing.T) {
	// reset env between table rows
	for _, tt := range []struct {
		name     string
		preset   string
		tier     string
		wantEnv  string
	}{
		{"pro flips on", "", "pro", "1"},
		{"team flips on", "", "team", "1"},
		{"free leaves off", "", "free", ""},
		{"unknown leaves off", "", "starter", ""},
		{"operator wins", "0", "pro", "0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("VULOS_MEET_TRANSCRIBE_ENABLE", tt.preset)
			meetTranscribeEnvFromTier(tt.tier)
			if got := getEnvForTest("VULOS_MEET_TRANSCRIBE_ENABLE"); got != tt.wantEnv {
				t.Errorf("env: got %q, want %q", got, tt.wantEnv)
			}
		})
	}
}

// getEnvForTest is a tiny test-only helper to read env (avoid importing "os"
// in test bodies repeatedly).
func getEnvForTest(k string) string {
	return os.Getenv(k)
}
