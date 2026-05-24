package airouter

import (
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubWhisperServer returns a server that emits a fixed transcript and lets
// the caller inspect the multipart body of the last request.
func stubWhisperServer(t *testing.T, transcript string) (*httptest.Server, *struct {
	model       string
	contentType string
	fileBytes   []byte
}) {
	t.Helper()
	inspect := &struct {
		model       string
		contentType string
		fileBytes   []byte
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.Error(w, "not found", 404)
			return
		}
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			switch part.FormName() {
			case "file":
				inspect.contentType = part.Header.Get("Content-Type")
				b, _ := io.ReadAll(part)
				inspect.fileBytes = b
			case "model":
				b, _ := io.ReadAll(part)
				inspect.model = string(b)
			}
		}
		// Whisper-style response.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": transcript, "language": "en"})
	}))
	t.Cleanup(srv.Close)
	return srv, inspect
}

func TestWhisperProvider_Transcribe_HappyPath(t *testing.T) {
	srv, inspect := stubWhisperServer(t, "hello world")
	p := &WhisperProvider{BaseURL: srv.URL, APIKey: "test", DefaultModel: "whisper-1"}
	audio := []byte{0x00, 0x01, 0x02, 0x03}
	frags, err := p.Transcribe(context.Background(), audio, "audio/wav", "")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if len(frags) != 1 {
		t.Fatalf("frags: got %d, want 1", len(frags))
	}
	if frags[0].Text != "hello world" {
		t.Errorf("text: got %q, want %q", frags[0].Text, "hello world")
	}
	if !frags[0].Final {
		t.Errorf("expected Final=true")
	}
	if frags[0].Language != "en" {
		t.Errorf("language: got %q, want en", frags[0].Language)
	}
	if inspect.model != "whisper-1" {
		t.Errorf("model uploaded: got %q, want whisper-1", inspect.model)
	}
	if !strings.HasPrefix(inspect.contentType, "audio/wav") {
		t.Errorf("file content-type: got %q, want audio/wav…", inspect.contentType)
	}
	if string(inspect.fileBytes) != string(audio) {
		t.Errorf("file bytes round-trip mismatch")
	}
}

func TestWhisperProvider_Transcribe_CustomModel(t *testing.T) {
	srv, inspect := stubWhisperServer(t, "x")
	p := &WhisperProvider{BaseURL: srv.URL}
	_, err := p.Transcribe(context.Background(), []byte{0x01}, "audio/ogg;codecs=opus", "base.en")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if inspect.model != "base.en" {
		t.Errorf("model: got %q, want base.en", inspect.model)
	}
}

func TestWhisperProvider_Transcribe_EmptyAudio(t *testing.T) {
	srv, _ := stubWhisperServer(t, "should-not-be-returned")
	p := &WhisperProvider{BaseURL: srv.URL}
	frags, err := p.Transcribe(context.Background(), nil, "audio/wav", "")
	if err != nil {
		t.Fatalf("Transcribe empty: %v", err)
	}
	if len(frags) != 0 {
		t.Errorf("empty audio: got %d frags, want 0", len(frags))
	}
}

func TestWhisperProvider_Transcribe_ProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate-limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := &WhisperProvider{BaseURL: srv.URL}
	_, err := p.Transcribe(context.Background(), []byte{0x01}, "audio/wav", "")
	if err == nil {
		t.Fatalf("expected error on 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("expected 429 in error, got %v", err)
	}
}

func TestWhisperProvider_Nil_ReturnsUnavailable(t *testing.T) {
	var p *WhisperProvider
	_, err := p.Transcribe(context.Background(), []byte{0x01}, "audio/wav", "")
	if err != ErrTranscriberUnavailable {
		t.Errorf("got %v, want ErrTranscriberUnavailable", err)
	}
}

func TestNewWhisperProviderFromEnv_NoURL(t *testing.T) {
	t.Setenv("VULOS_WHISPER_URL", "")
	_, err := NewWhisperProviderFromEnv()
	if err != ErrTranscriberUnavailable {
		t.Errorf("expected ErrTranscriberUnavailable, got %v", err)
	}
}

func TestNewWhisperProviderFromEnv_HostedShape(t *testing.T) {
	t.Setenv("VULOS_WHISPER_URL", "https://api.openai.com")
	t.Setenv("VULOS_WHISPER_KEY", "sk-foo")
	t.Setenv("VULOS_WHISPER_MODEL", "whisper-1")
	p, err := NewWhisperProviderFromEnv()
	if err != nil {
		t.Fatalf("NewWhisperProviderFromEnv: %v", err)
	}
	if p.Name() != "openai-whisper" {
		t.Errorf("name: got %q, want openai-whisper", p.Name())
	}
}

func TestNewWhisperProviderFromEnv_SelfHostShape(t *testing.T) {
	t.Setenv("VULOS_WHISPER_URL", "http://localhost:8080")
	p, err := NewWhisperProviderFromEnv()
	if err != nil {
		t.Fatalf("NewWhisperProviderFromEnv: %v", err)
	}
	if p.Name() != "whisper-cpp" {
		t.Errorf("name: got %q, want whisper-cpp", p.Name())
	}
}

func TestFilenameFromCT(t *testing.T) {
	tests := map[string]string{
		"audio/wav":             "chunk.wav",
		"audio/ogg;codecs=opus": "chunk.ogg",
		"audio/webm":            "chunk.webm",
		"audio/mp4":             "chunk.m4a",
		"audio/pcm":             "chunk.pcm",
		"audio/mpeg":            "chunk.wav",
		"audio/mp3":             "chunk.mp3",
		"":                      "chunk.wav",
	}
	for ct, want := range tests {
		if got := filenameFromCT(ct); got != want {
			t.Errorf("filenameFromCT(%q): got %q, want %q", ct, got, want)
		}
	}
}

// Sanity: ensure the provider really does set multipart/form-data.
func TestWhisperProvider_SetsMultipartContentType(t *testing.T) {
	var capturedCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "ok"})
	}))
	defer srv.Close()
	p := &WhisperProvider{BaseURL: srv.URL}
	_, err := p.Transcribe(context.Background(), []byte{0x42}, "audio/wav", "")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if !strings.HasPrefix(capturedCT, "multipart/form-data") {
		t.Errorf("Content-Type: got %q, want multipart/form-data…", capturedCT)
	}
	// Sanity: the boundary marker must be present so a recipient can parse it.
	if _, params, ok := strings.Cut(capturedCT, "boundary="); !ok || params == "" {
		t.Errorf("Content-Type missing boundary: %q", capturedCT)
	}
	// Touch the multipart import so it stays explicitly required.
	_ = multipart.NewReader
}
