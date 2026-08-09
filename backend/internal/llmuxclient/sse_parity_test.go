package llmuxclient

// sse_parity_test.go — the embedded backend's SSE output must be byte-identical
// to what the sidecar proxy produced.
//
// Before embedding, /api/ai/chat set three headers and copied llmux's bytes
// through. Now the OS frames the events itself from a chunk callback, so the
// wire format is the OS's responsibility and a divergence would only surface in
// a client half way through a stream.
//
// The check is a DIFFERENTIAL, not a golden file: the same request is served
// twice against the SAME fake upstream — once by the in-process gateway, once
// by llmux's real HTTP shell (core/server) with the remote backend proxying to
// it, which is exactly the deployment being replaced — and the two response
// bodies are compared byte for byte. A golden file would only prove the
// embedded path matches whatever it happened to emit the day it was written;
// this proves it matches llmux.

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/vul-os/llmux/core/config"
	"github.com/vul-os/llmux/core/server"
)

// sidecarFor starts llmux's REAL HTTP shell over a gateway configured exactly
// like the embedded one, so the only difference between the two paths under
// test is who frames the SSE.
func sidecarFor(t *testing.T, upstream string) *httptest.Server {
	t.Helper()
	cfg, err := config.Load(writeLLMuxConfig(t, upstream))
	if err != nil {
		t.Fatalf("sidecar config: %v", err)
	}
	srv, err := server.New(cfg)
	if err != nil {
		t.Fatalf("sidecar server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestSSEParity_EmbeddedMatchesProxiedSidecar is the byte-compatibility proof.
func TestSSEParity_EmbeddedMatchesProxiedSidecar(t *testing.T) {
	upstream := fakeProvider(t)
	isolateEnv(t)

	const body = `{"model":"fake-1","stream":true,"messages":[{"role":"user","content":"hi"}]}`

	// Path A: OS handler -> in-process gateway -> fake upstream.
	embedded, err := NewEmbedded(context.Background(), EmbeddedOptions{
		ConfigPath: writeLLMuxConfig(t, upstream.URL),
	})
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = embedded.Close() })
	gotEmbedded := postJSON(t, muxFor(embedded), "/api/ai/chat", body)

	// Path B: OS handler -> HTTP -> llmux's own shell -> same fake upstream.
	sidecar := sidecarFor(t, upstream.URL)
	gotRemote := postJSON(t, muxFor(RemoteBackend(New(Config{BaseURL: sidecar.URL}))), "/api/ai/chat", body)

	if gotEmbedded.Code != 200 || gotRemote.Code != 200 {
		t.Fatalf("statuses embedded=%d remote=%d, want 200/200 (embedded body=%s, remote body=%s)",
			gotEmbedded.Code, gotRemote.Code, gotEmbedded.Body.String(), gotRemote.Body.String())
	}
	if gotEmbedded.Body.String() != gotRemote.Body.String() {
		t.Fatalf("SSE bytes differ between backends:\nembedded %q\n  remote %q",
			gotEmbedded.Body.String(), gotRemote.Body.String())
	}
	// A shared empty body would satisfy the comparison above without either
	// backend having streamed anything.
	if got := gotEmbedded.Body.String(); got != wantEmbeddedSSE() {
		t.Fatalf("both backends agree but on the wrong bytes:\n got %q\nwant %q", got, wantEmbeddedSSE())
	}

	// The response headers a client sees must match too — the proxy path never
	// forwarded llmux's headers, so both backends write the OS's own set.
	for _, h := range []string{"Content-Type", "Cache-Control", "X-Accel-Buffering"} {
		if a, b := gotEmbedded.Header().Get(h), gotRemote.Header().Get(h); a != b {
			t.Fatalf("%s differs: embedded %q, remote %q", h, a, b)
		}
	}
}

// TestSSEParity_NonStreamingBodyMatches covers the other half of the route.
// A non-streaming /api/ai/chat used to return llmux's JSON body copied through
// under the streaming headers; the embedded path must produce the same bytes
// rather than quietly changing shape.
func TestSSEParity_NonStreamingBodyMatches(t *testing.T) {
	upstream := fakeProvider(t)
	isolateEnv(t)

	const body = `{"model":"fake-1","messages":[{"role":"user","content":"hi"}]}`

	embedded, err := NewEmbedded(context.Background(), EmbeddedOptions{
		ConfigPath: writeLLMuxConfig(t, upstream.URL),
	})
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = embedded.Close() })

	gotEmbedded := postJSON(t, muxFor(embedded), "/api/ai/chat", body)
	sidecar := sidecarFor(t, upstream.URL)
	gotRemote := postJSON(t, muxFor(RemoteBackend(New(Config{BaseURL: sidecar.URL}))), "/api/ai/chat", body)

	if gotEmbedded.Body.String() != gotRemote.Body.String() {
		t.Fatalf("non-streaming bytes differ between backends:\nembedded %q\n  remote %q",
			gotEmbedded.Body.String(), gotRemote.Body.String())
	}
	if gotEmbedded.Body.Len() == 0 {
		t.Fatalf("both backends returned an empty body")
	}
}

// TestSSEParity_TerminalSentinelSurvivesAnEmptyStream pins the edge llmux's
// shell handles explicitly: an upstream that yields no chunks at all still ends
// in a valid terminal stream rather than a zero-length body a client would
// wait on forever.
func TestSSEParity_TerminalSentinelSurvivesAnEmptyStream(t *testing.T) {
	upstream := emptyStreamProvider(t)
	isolateEnv(t)

	const body = `{"model":"fake-1","stream":true,"messages":[]}`

	embedded, err := NewEmbedded(context.Background(), EmbeddedOptions{
		ConfigPath: writeLLMuxConfig(t, upstream.URL),
	})
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = embedded.Close() })

	gotEmbedded := postJSON(t, muxFor(embedded), "/api/ai/chat", body)
	sidecar := sidecarFor(t, upstream.URL)
	gotRemote := postJSON(t, muxFor(RemoteBackend(New(Config{BaseURL: sidecar.URL}))), "/api/ai/chat", body)

	if got := gotEmbedded.Body.String(); got != "data: [DONE]\n\n" {
		t.Fatalf("embedded empty stream = %q, want the terminal sentinel", got)
	}
	if gotEmbedded.Body.String() != gotRemote.Body.String() {
		t.Fatalf("empty-stream bytes differ:\nembedded %q\n  remote %q",
			gotEmbedded.Body.String(), gotRemote.Body.String())
	}
}
