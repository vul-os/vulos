package airouter

// llmux.go — route OpenAI-compatible calls through the suite's llmux gateway.
//
// llmux is the Vulos suite's OpenAI-compatible LLM multiplexer.  When the
// operator sets LLMUX_URL (e.g. http://llmux:4000/v1) the airouter points its
// OpenAI-compatible client(s) — chat/completions and embeddings — at that base
// URL + bearer key instead of calling providers directly.  Because llmux is
// OpenAI-compatible this is a pure (base_url, key) swap, not a protocol change.
//
// Standalone fallback: when LLMUX_URL is unset, llmuxEndpoint reports
// ok == false and every caller keeps its existing behaviour (BYO provider rows
// / cloud proxy / direct Whisper).  This keeps vulos able to run independently.
//
// Scope:
//   - chat/completions  — overridden (see router.go routeBYO).
//   - embeddings        — overridden (see embed.go callEmbedProvider).
//   - transcription     — NOT routed through llmux: llmux is a text/chat +
//     embeddings multiplexer and does not expose the multipart
//     /v1/audio/transcriptions Whisper shape, so whisper.go is left as-is and
//     continues to use VULOS_WHISPER_URL.  If/when llmux gains an
//     OpenAI-compatible audio endpoint this is the place to add it.

import (
	"net/http"
	"os"
	"strings"
)

// llmuxConfig is the resolved llmux gateway target.
type llmuxConfig struct {
	// BaseURL is the gateway base URL WITHOUT a trailing "/v1" or "/" — callers
	// append the OpenAI sub-path (e.g. "/v1/chat/completions") themselves, the
	// same way they do for BYO provider base URLs.  A configured value of
	// "http://llmux:4000/v1" is normalised to "http://llmux:4000".
	BaseURL string
	// Key is the bearer token sent as "Authorization: Bearer <key>".  May be
	// empty for an unauthenticated local gateway.
	Key string
}

// llmuxEndpoint returns the configured llmux gateway target.
//
// It reports ok == true only when LLMUX_URL is set (non-empty after trimming).
// When ok == false the returned config is zero and callers MUST fall back to
// their current direct/cloud behaviour so the OS stays standalone-capable.
func llmuxEndpoint() (cfg llmuxConfig, ok bool) {
	raw := strings.TrimSpace(os.Getenv("LLMUX_URL"))
	if raw == "" {
		return llmuxConfig{}, false
	}
	// Normalise: strip trailing slash, then a trailing "/v1" segment so the
	// existing callers (which append "/v1/...") build the right URL whether the
	// operator supplied ".../v1" or just the host.
	base := strings.TrimRight(raw, "/")
	base = strings.TrimSuffix(base, "/v1")
	base = strings.TrimRight(base, "/")
	return llmuxConfig{
		BaseURL: base,
		Key:     strings.TrimSpace(os.Getenv("LLMUX_KEY")),
	}, true
}

// llmuxHTTPClient returns the HTTP client used for llmux requests.  It reuses
// the caller's existing client (preserving its timeout) so behaviour and
// testability are unchanged; the override is purely (base_url, key).
func llmuxHTTPClient(existing *http.Client) *http.Client {
	if existing != nil {
		return existing
	}
	return http.DefaultClient
}
