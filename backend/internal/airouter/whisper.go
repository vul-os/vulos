package airouter

// MEET-TRANSCRIPT-01: Whisper-style speech-to-text provider for the airouter.
//
// Shape:
//
//   The transcription provider is a small interface (`TranscriptionProvider`)
//   that takes a model name + an opaque audio chunk (PCM16 or Opus bytes — the
//   provider decides which content-type to emit) and returns one or more
//   transcript fragments. The SAME interface backs both:
//
//     - Hosted Whisper (OpenAI-compatible `/v1/audio/transcriptions`)
//       - Configured via VULOS_WHISPER_URL + VULOS_WHISPER_KEY.
//     - Self-hosted whisper.cpp (the cpp server exposes the same OpenAI shape
//       at `/inference` or `/v1/audio/transcriptions`).
//       - Same env vars; just point VULOS_WHISPER_URL at the local box.
//
//   The hosted-Whisper path is non-streaming over the wire (it returns one
//   JSON {text:"…"} per chunk). To make the per-room transcript feel like a
//   stream, the OS server slices incoming audio into ~5-second windows
//   (configurable) and emits one fragment per window. This keeps the provider
//   trivially swappable and avoids requiring a websocket-capable backend.
//
//   The provider is wired so the existing airouter Router can reuse the same
//   Store / cert / cloud-proxy plumbing later if/when we proxy through the
//   cloud control plane; for now we hit Whisper directly.
//
// Privacy: cloud users default-on for Pro (server flips
// `VULOS_MEET_TRANSCRIBE_ENABLE=1` from the tier hint at startup); self-host
// default-off. The per-meeting opt-in toggle in the office Spaces UI is the
// final gate — the OS server refuses to transcribe a room that hasn't called
// `POST /api/meet/transcribe/start` first (see routes_meet_transcript.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"strings"
	"time"
)

// TranscriptFragment is a single piece of recognised speech.
type TranscriptFragment struct {
	Text       string    `json:"text"`
	StartMS    int64     `json:"start_ms,omitempty"`
	EndMS      int64     `json:"end_ms,omitempty"`
	Final      bool      `json:"final"`
	Language   string    `json:"language,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
}

// TranscriptionProvider is the airouter abstraction for speech-to-text
// backends.  Implementations are stateless from the OS server's point of
// view; the OS server is responsible for windowing/queueing audio chunks.
type TranscriptionProvider interface {
	// Transcribe sends a single audio chunk and returns zero or more
	// transcript fragments.  The chunk's content-type (audio/pcm,
	// audio/ogg;opus, audio/webm, etc.) is supplied so the provider can
	// label the multipart upload appropriately.
	//
	// audio MUST be a complete decodable unit — e.g. a 5-second PCM16 LE
	// slice at 16kHz, or a complete Opus packet stream.  The Whisper API
	// rejects partial bytes.
	//
	// model is optional; the empty string asks the provider for its
	// default (e.g. "whisper-1" for hosted, "base.en" for whisper.cpp).
	Transcribe(ctx context.Context, audio []byte, contentType, model string) ([]TranscriptFragment, error)

	// Name is the human-readable provider name (for logs / status).
	Name() string
}

// ErrTranscriberUnavailable signals that the provider has no backing URL
// configured.  Callers should surface 503 / "off" rather than 5xx.
var ErrTranscriberUnavailable = errors.New("airouter: transcription provider not configured")

// WhisperProvider is the default airouter Whisper provider.  It speaks the
// OpenAI-compatible /v1/audio/transcriptions shape, which is also what
// whisper.cpp's server emits — so a single struct covers hosted + self-host.
type WhisperProvider struct {
	// BaseURL is the provider endpoint.  Typical values:
	//   - https://api.openai.com (hosted)
	//   - http://localhost:8080  (whisper.cpp server)
	// The provider appends "/v1/audio/transcriptions" itself.
	BaseURL string

	// APIKey is the Bearer token sent in the Authorization header.  Empty
	// is allowed for unauthenticated self-host deployments.
	APIKey string

	// DefaultModel is used when the caller passes "".  Defaults to
	// "whisper-1" if unset.
	DefaultModel string

	// HTTPClient is overridable for testing.  Defaults to a 30s-timeout client.
	HTTPClient *http.Client
}

// NewWhisperProviderFromEnv builds a provider from VULOS_WHISPER_URL,
// VULOS_WHISPER_KEY, and VULOS_WHISPER_MODEL.  Returns
// ErrTranscriberUnavailable when no URL is set so the caller can degrade
// gracefully (transcription becomes a no-op).
func NewWhisperProviderFromEnv() (*WhisperProvider, error) {
	url := os.Getenv("VULOS_WHISPER_URL")
	if url == "" {
		return nil, ErrTranscriberUnavailable
	}
	return &WhisperProvider{
		BaseURL:      url,
		APIKey:       os.Getenv("VULOS_WHISPER_KEY"),
		DefaultModel: getenv("VULOS_WHISPER_MODEL", "whisper-1"),
		HTTPClient:   &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Name implements TranscriptionProvider.
func (w *WhisperProvider) Name() string {
	if strings.Contains(w.BaseURL, "openai.com") {
		return "openai-whisper"
	}
	return "whisper-cpp"
}

// Transcribe implements TranscriptionProvider.  Builds a multipart/form-data
// upload with file=<audio>, model=<model>, response_format=json.
func (w *WhisperProvider) Transcribe(ctx context.Context, audio []byte, contentType, model string) ([]TranscriptFragment, error) {
	if w == nil || w.BaseURL == "" {
		return nil, ErrTranscriberUnavailable
	}
	if len(audio) == 0 {
		return nil, nil
	}

	model = strings.TrimSpace(model)
	if model == "" {
		model = w.DefaultModel
		if model == "" {
			model = "whisper-1"
		}
	}
	if contentType == "" {
		contentType = "audio/wav"
	}

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)

	// file part — use a filename hint so whisper.cpp picks up the extension.
	filename := filenameFromCT(contentType)
	partHeader := textproto.MIMEHeader{}
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	partHeader.Set("Content-Type", contentType)
	part, err := mw.CreatePart(partHeader)
	if err != nil {
		return nil, fmt.Errorf("whisper: create part: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return nil, fmt.Errorf("whisper: write part: %w", err)
	}

	// model + response_format
	_ = mw.WriteField("model", model)
	_ = mw.WriteField("response_format", "json")
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("whisper: close multipart: %w", err)
	}

	endpoint := strings.TrimRight(w.BaseURL, "/") + "/v1/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if w.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+w.APIKey)
	}

	client := w.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whisper: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("whisper: provider %d: %s", resp.StatusCode, string(b))
	}

	var out struct {
		Text     string `json:"text"`
		Language string `json:"language,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("whisper: decode response: %w", err)
	}

	text := strings.TrimSpace(out.Text)
	if text == "" {
		return nil, nil
	}
	return []TranscriptFragment{{
		Text:       text,
		Final:      true,
		Language:   out.Language,
		ReceivedAt: time.Now().UTC(),
	}}, nil
}

// filenameFromCT picks a sensible filename so whisper.cpp's multipart parser
// can dispatch by extension.
func filenameFromCT(ct string) string {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "ogg"):
		return "chunk.ogg"
	case strings.Contains(ct, "webm"):
		return "chunk.webm"
	case strings.Contains(ct, "mp4"), strings.Contains(ct, "m4a"):
		return "chunk.m4a"
	case strings.Contains(ct, "pcm"):
		return "chunk.pcm"
	case strings.Contains(ct, "mp3"):
		return "chunk.mp3"
	default:
		return "chunk.wav"
	}
}
