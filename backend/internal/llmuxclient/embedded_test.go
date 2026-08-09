package llmuxclient

// embedded_test.go — /api/ai/* served by an in-process llmux, with NO sidecar
// and NO LLMUX_URL.
//
// The point of these tests is the headline claim of embedding: the OS's own
// routes answer a real request end to end when nothing is listening on
// LLMUX_URL. So they drive the actual http.ServeMux the OS mounts, through the
// actual handlers, into a real gateway.Gateway, out to a fake OpenAI-compatible
// provider — and assert on the bytes that come back. A test that merely
// constructed a Gateway would prove none of that.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// upstreamChunks is what the fake provider streams. Fixed ids and timestamps
// keep the bytes deterministic, which is what makes the parity test in
// sse_parity_test.go a byte comparison rather than a shape comparison.
var upstreamChunks = []string{
	`{"id":"chatcmpl-fake","object":"chat.completion.chunk","created":1700000000,"model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`,
	`{"id":"chatcmpl-fake","object":"chat.completion.chunk","created":1700000000,"model":"fake-1","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`,
	`{"id":"chatcmpl-fake","object":"chat.completion.chunk","created":1700000000,"model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
}

const upstreamUnary = `{"id":"chatcmpl-fake","object":"chat.completion","created":1700000000,"model":"fake-1",` +
	`"choices":[{"index":0,"message":{"role":"assistant","content":"Hello world"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

const upstreamEmbedding = `{"object":"list","model":"fake-embed","data":[{"object":"embedding","index":0,` +
	`"embedding":[0.125,-0.5,0.75]}],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`

// fakeProvider is an OpenAI-compatible upstream on loopback. Loopback matters:
// llmux's sovereignty gate classifies it "local", so the request is allowed
// without an allow_egress opt-in — exactly as an on-box Ollama would be.
func fakeProvider(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			var body struct {
				Stream bool `json:"stream"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !body.Stream {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(upstreamUnary))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			flusher, _ := w.(http.Flusher)
			for _, c := range upstreamChunks {
				_, _ = w.Write([]byte("data: " + c + "\n\n"))
				if flusher != nil {
					flusher.Flush()
				}
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		case "/v1/embeddings":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(upstreamEmbedding))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeLLMuxConfig writes a hermetic llmux config file routing everything to
// the fake provider, and returns its path.
//
// "sources": [] is explicit: nil would mean "the file said nothing", leaving
// config.Default()'s two price-feed URLs in place.
func writeLLMuxConfig(t *testing.T, upstream string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "llmux.json")
	cfg := `{
	  "server": {"addr": ":0"},
	  "log_level": "error",
	  "providers": [{"name": "fake", "type": "passthrough", "base_url": "` + upstream + `/v1", "api_key": "test"}],
	  "routes": [{"model": "*", "provider": "fake"}],
	  "pricing": {"sources": []}
	}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// isolateEnv clears every variable llmux's config.Load consults that could make
// a test depend on the developer's shell — provider auto-detection and, most
// importantly, the Postgres DSNs, which gateway.New would connect to.
func isolateEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"LLMUX_URL", "VULOS_LLMUX_URL", "LLMUX_KEY", "VULOS_LLMUX_KEY",
		"VULOS_AI_MODE", "VULOS_LLMUX_CONFIG", "LLMUX_CONFIG",
		"VULOS_AI_EMBEDDED_BACKGROUND", "VULOS_AI_EMBEDDED_POSTGRES",
		"DATABASE_URL", "VULOS_DATABASE_URL", "LLMUX_POSTGRES", "LLMUX_REDIS",
		"LLMUX_CP_URL", "LLMUX_USAGE_LOG", "LLMUX_BYOK_KEK", "LLMUX_BYOK_STORE",
		"OLLAMA_HOST", "LLMUX_LOCAL_BASE_URL", "OPENAI_API_KEY", "ANTHROPIC_API_KEY",
		"GEMINI_API_KEY", "GROQ_API_KEY", "DEEPSEEK_API_KEY", "TOGETHER_API_KEY",
		"XAI_API_KEY", "OPENROUTER_API_KEY", "COHERE_API_KEY", "MISTRAL_API_KEY",
	} {
		t.Setenv(k, "")
	}
}

// newEmbeddedForTest builds the in-process gateway against the fake provider.
func newEmbeddedForTest(t *testing.T, upstream string) *Embedded {
	t.Helper()
	isolateEnv(t)
	e, err := NewEmbedded(context.Background(), EmbeddedOptions{
		ConfigPath: writeLLMuxConfig(t, upstream),
	})
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// wantEmbeddedSSE is the exact body /api/ai/chat must produce for the fake
// provider's stream: one data event per chunk, then the terminal sentinel.
func wantEmbeddedSSE() string {
	var b strings.Builder
	for _, c := range upstreamChunks {
		b.WriteString("data: " + c + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// TestEmbedded_ChatStreamsWithNoSidecar is the headline test: the OS's own
// /api/ai/chat route serves a streaming completion with no llmux process
// anywhere and LLMUX_URL unset.
func TestEmbedded_ChatStreamsWithNoSidecar(t *testing.T) {
	upstream := fakeProvider(t)
	mux := muxFor(newEmbeddedForTest(t, upstream.URL))

	if v := os.Getenv("LLMUX_URL"); v != "" {
		t.Fatalf("LLMUX_URL = %q; this test must prove the route works without it", v)
	}

	rec := postJSON(t, mux, "/api/ai/chat", `{"model":"fake-1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if got, want := rec.Body.String(), wantEmbeddedSSE(); got != want {
		t.Fatalf("SSE body mismatch:\n got %q\nwant %q", got, want)
	}
}

// TestEmbedded_ChatNonStreaming covers the other half of the route: a
// non-streaming request answers with the completion JSON, as the proxy path did.
func TestEmbedded_ChatNonStreaming(t *testing.T) {
	upstream := fakeProvider(t)
	mux := muxFor(newEmbeddedForTest(t, upstream.URL))

	rec := postJSON(t, mux, "/api/ai/chat", `{"model":"fake-1","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if resp.ID != "chatcmpl-fake" || len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "Hello world" {
		t.Fatalf("unexpected completion: %s", rec.Body.String())
	}
}

// TestEmbedded_EmbedAndNotesStore exercises /api/ai/embed and the note
// embedding store against the in-process gateway — the store is the one piece
// of AI state the OS owns, and embedding llmux must not have detached it.
func TestEmbedded_EmbedAndNotesStore(t *testing.T) {
	upstream := fakeProvider(t)
	e := newEmbeddedForTest(t, upstream.URL)

	store, err := NewStore(filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mux := http.NewServeMux()
	RegisterHandlers(mux, e, store)

	rec := postJSON(t, mux, "/api/ai/embed", `{"input":"hello","model":"fake-embed"}`)
	if rec.Code != 200 {
		t.Fatalf("embed status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var er EmbedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &er); err != nil {
		t.Fatalf("decode embed: %v", err)
	}
	if len(er.Embedding) != 3 || er.Embedding[0] != 0.125 {
		t.Fatalf("embedding = %v, want [0.125 -0.5 0.75]", er.Embedding)
	}

	rec = postJSON(t, mux, "/api/ai/notes/index", `{"note_id":"n1","content":"hello","model":"fake-embed"}`)
	if rec.Code != 200 {
		t.Fatalf("notes/index status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	rec = postJSON(t, mux, "/api/ai/notes/search", `{"query":"hello","model":"fake-embed","threshold":0.5}`)
	if rec.Code != 200 {
		t.Fatalf("notes/search status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var search struct {
		Hits []struct {
			NoteID string `json:"note_id"`
		} `json:"hits"`
		Degraded bool `json:"degraded"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &search); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if search.Degraded {
		t.Fatalf("search reported degraded with an embedded gateway available")
	}
	if len(search.Hits) != 1 || search.Hits[0].NoteID != "n1" {
		t.Fatalf("hits = %+v, want the indexed note", search.Hits)
	}
}

// TestEmbedded_StatusAndModels checks an operator can see, from the box, that
// the gateway is in-process and which providers it can reach.
func TestEmbedded_StatusAndModels(t *testing.T) {
	upstream := fakeProvider(t)
	mux := muxFor(newEmbeddedForTest(t, upstream.URL))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/ai/status", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var st struct {
		Status     string   `json:"status"`
		Mode       string   `json:"mode"`
		Providers  []string `json:"providers"`
		Background bool     `json:"background"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Status != "ok" || st.Mode != string(ModeEmbedded) {
		t.Fatalf("status=%q mode=%q, want ok/embedded", st.Status, st.Mode)
	}
	if len(st.Providers) != 1 || st.Providers[0] != "fake" {
		t.Fatalf("providers = %v, want [fake]", st.Providers)
	}
	if st.Background {
		t.Fatalf("background work reported on without VULOS_AI_EMBEDDED_BACKGROUND")
	}

	rec = postJSON(t, mux, "/api/ai/models", ``)
	if rec.Code != 200 {
		t.Fatalf("models status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var list struct {
		Object string `json:"object"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	if list.Object != "list" {
		t.Fatalf("models object = %q, want list", list.Object)
	}
}

// TestEmbedded_StartsNoBackgroundWorkByDefault is the guard against the exact
// surprise this refactor was supposed to remove: an OS process that, merely by
// embedding llmux, starts GETting a price feed on a timer.
//
// It asserts the property structurally — the price-feed sources are gone from
// the loaded config, so there is nothing for a syncer to fetch even if some
// later change starts one — and that Start was not called.
func TestEmbedded_StartsNoBackgroundWorkByDefault(t *testing.T) {
	upstream := fakeProvider(t)
	e := newEmbeddedForTest(t, upstream.URL)

	if got := e.cfg.Pricing.Sources; len(got) != 0 {
		t.Fatalf("pricing sources = %v, want none: an embedded gateway must not ship outbound price feeds", got)
	}
	if e.started {
		t.Fatalf("Gateway.Start was called without VULOS_AI_EMBEDDED_BACKGROUND")
	}
}

// TestEmbedded_DefaultConfigDropsPriceFeeds is the same guard one level up:
// with NO config file at all, llmux's config.Default() supplies two price-feed
// URLs, and those must not survive into an embedded gateway either. Without
// this, a box that embeds llmux without a config file gets the default feeds
// back and only the "we never call Start" promise stands between it and a
// six-hourly GET to openrouter.ai.
func TestEmbedded_DefaultConfigDropsPriceFeeds(t *testing.T) {
	isolateEnv(t)
	e, err := NewEmbedded(context.Background(), EmbeddedOptions{})
	if err != nil {
		t.Fatalf("NewEmbedded with no config file: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	if got := e.cfg.Pricing.Sources; len(got) != 0 {
		t.Fatalf("pricing sources = %v, want none", got)
	}
	if e.cfg.Pricing.Azure {
		t.Fatalf("azure pricing source left enabled")
	}
}

// TestEmbedded_BackgroundIsOptIn is the other half of the same guard: the
// default is a CHOICE, not an inability. With Background set, Start runs and
// the price feeds survive, so the "off" assertions above cannot pass by the
// switch being broken.
func TestEmbedded_BackgroundIsOptIn(t *testing.T) {
	upstream := fakeProvider(t)
	isolateEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A config file that keeps the default feeds out of the picture but names
	// one of its own, so "sources survived" is observable without any real
	// outbound traffic (nothing fetches until the syncer's first tick).
	path := filepath.Join(t.TempDir(), "llmux.json")
	cfg := `{
	  "log_level": "error",
	  "providers": [{"name": "fake", "type": "passthrough", "base_url": "` + upstream.URL + `/v1"}],
	  "routes": [{"model": "*", "provider": "fake"}],
	  "pricing": {"sources": ["` + upstream.URL + `/prices.json"], "sync_interval_minutes": 600}
	}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	e, err := NewEmbedded(ctx, EmbeddedOptions{ConfigPath: path, Background: true})
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	if !e.started {
		t.Fatalf("Background was requested but Gateway.Start was not called")
	}
	if len(e.cfg.Pricing.Sources) != 1 {
		t.Fatalf("pricing sources = %v, want the configured feed kept", e.cfg.Pricing.Sources)
	}
	if st, _ := e.Status()["background"].(bool); !st {
		t.Fatalf("status does not report background work as running")
	}
}

// TestEmbedded_IgnoresInheritedPostgresDSN guards the second construction-time
// hazard: config.Load reads DATABASE_URL / VULOS_DATABASE_URL, and gateway.New
// CONNECTS AND MIGRATES eagerly when cfg.Postgres is set. In a Vulos deployment
// those variables name the OS's own database.
//
// The DSN below is unroutable on purpose: if the guard regressed, gateway.New
// would try to dial it and NewEmbedded would fail rather than return a gateway
// with no Postgres configured.
func TestEmbedded_IgnoresInheritedPostgresDSN(t *testing.T) {
	upstream := fakeProvider(t)
	isolateEnv(t)
	t.Setenv("VULOS_DATABASE_URL", "postgres://vulos:vulos@127.0.0.1:1/vulos?sslmode=disable&connect_timeout=1")

	e, err := NewEmbedded(context.Background(), EmbeddedOptions{
		ConfigPath: writeLLMuxConfig(t, upstream.URL),
	})
	if err != nil {
		t.Fatalf("NewEmbedded picked up the OS's database DSN instead of ignoring it: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	if e.cfg.Postgres != "" {
		t.Fatalf("cfg.Postgres = %q, want empty: an inherited DSN must not reach gateway.New", e.cfg.Postgres)
	}
}

// TestResolveMode covers the selection rules, including the one that matters
// most for existing deployments: an LLMUX_URL box stays remote unless it is
// explicitly told otherwise.
func TestResolveMode(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want Mode
	}{
		{"nothing set", nil, ModeUnconfigured},
		{"LLMUX_URL infers remote", map[string]string{"LLMUX_URL": "http://llmux:4000"}, ModeRemote},
		{"alias infers remote", map[string]string{"VULOS_LLMUX_URL": "http://llmux:4000"}, ModeRemote},
		{"config file infers embedded", map[string]string{"VULOS_LLMUX_CONFIG": "/etc/llmux.json"}, ModeEmbedded},
		{
			"explicit embedded beats an inherited LLMUX_URL",
			map[string]string{"VULOS_AI_MODE": "embedded", "LLMUX_URL": "http://llmux:4000"},
			ModeEmbedded,
		},
		{
			"explicit remote beats a config file",
			map[string]string{"VULOS_AI_MODE": "remote", "VULOS_LLMUX_CONFIG": "/etc/llmux.json", "LLMUX_URL": "http://llmux:4000"},
			ModeRemote,
		},
		{"off disables an otherwise-configured box", map[string]string{"VULOS_AI_MODE": "off", "LLMUX_URL": "http://llmux:4000"}, ModeUnconfigured},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := ResolveMode(); got != tc.want {
				t.Fatalf("ResolveMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOpenFromEnv_EmbeddedFromEnvironment proves the wiring the OS actually
// uses: nothing but environment variables, and /api/ai/chat serves a real
// completion from an in-process gateway.
func TestOpenFromEnv_EmbeddedFromEnvironment(t *testing.T) {
	upstream := fakeProvider(t)
	isolateEnv(t)
	t.Setenv("VULOS_AI_MODE", "embedded")
	t.Setenv("VULOS_LLMUX_CONFIG", writeLLMuxConfig(t, upstream.URL))

	b := OpenFromEnv(context.Background(), nil)
	t.Cleanup(func() { _ = b.Close() })
	if b.Mode() != ModeEmbedded {
		t.Fatalf("mode = %q, want embedded", b.Mode())
	}

	rec := postJSON(t, muxFor(b), "/api/ai/chat", `{"model":"fake-1","stream":true,"messages":[]}`)
	if rec.Code != 200 || rec.Body.String() != wantEmbeddedSSE() {
		t.Fatalf("status=%d body=%q, want a full SSE stream", rec.Code, rec.Body.String())
	}
}

// TestOpenFromEnv_DegradesRatherThanFailing checks a broken embedded config
// leaves the box bootable with 503s, not a nil backend.
func TestOpenFromEnv_DegradesRatherThanFailing(t *testing.T) {
	isolateEnv(t)
	bad := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(bad, []byte(`{ not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VULOS_AI_MODE", "embedded")
	t.Setenv("VULOS_LLMUX_CONFIG", bad)

	b := OpenFromEnv(context.Background(), nil)
	t.Cleanup(func() { _ = b.Close() })
	if b.Mode() != ModeUnconfigured {
		t.Fatalf("mode = %q, want unconfigured after a bad config", b.Mode())
	}
	if rec := postJSON(t, muxFor(b), "/api/ai/chat", `{"model":"m","messages":[]}`); rec.Code != 503 {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// emptyStreamProvider is an upstream that opens a stream and immediately ends
// it with the sentinel, yielding no chunks at all.
func emptyStreamProvider(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	return srv
}
