package llmuxclient

// sse.go — the /api/ai/chat wire format.
//
// Until llmux was embedded, the OS never framed an SSE event: it set three
// headers and copied the sidecar's bytes through. The embedded gateway hands
// back a chunk CALLBACK instead of a byte stream, so the OS frames the events
// itself now — and it must frame them exactly as llmux's own HTTP shell does
// (core/server/sse.go), or every client that parses the stream breaks on the
// day an operator switches from the sidecar to the in-process gateway.
//
// The format is OpenAI's: one "data: {json}\n\n" per chunk, a terminal
// "data: [DONE]\n\n", and a mid-stream failure relayed as one more data event
// carrying an OpenAI error object. sse_parity_test.go runs the SAME request
// through both backends against the same upstream and asserts the two response
// bodies are byte-identical, so this comment is checked, not just written.

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// writeStreamHeaders commits the response with the headers /api/ai/chat has
// always used. It deliberately does NOT copy llmux's own "Connection:
// keep-alive": the proxy path never forwarded the gateway's headers, so these
// three are exactly what a client saw before, on both backends.
func writeStreamHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

// writeSSEErrorText emits the OS's own mid-stream failure event. This is the
// shape the proxy path produced when the copy from the sidecar broke, kept
// verbatim so that failure mode reads the same on both backends.
func writeSSEErrorText(w http.ResponseWriter, msg string) {
	fmt.Fprintf(w, "data: {\"error\":%q}\n\n", msg) //nolint:errcheck
}

// sseWriter frames Server-Sent Events byte-identically to llmux's HTTP shell.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// newSSEWriter commits the response and returns a writer, or false when the
// ResponseWriter cannot flush — in which case no streaming is possible and
// nothing has been written, so the caller can still answer with a JSON error.
func newSSEWriter(w http.ResponseWriter) (*sseWriter, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	writeStreamHeaders(w)
	flusher.Flush()
	return &sseWriter{w: w, flusher: flusher}, true
}

// event marshals v and writes it as one SSE data event.
func (s *sseWriter) event(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.raw(data)
}

// raw writes a pre-encoded JSON payload as one SSE data event.
func (s *sseWriter) raw(data []byte) error {
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// done writes the terminal sentinel that ends every OpenAI stream.
func (s *sseWriter) done() {
	fmt.Fprint(s.w, "data: [DONE]\n\n") //nolint:errcheck
	s.flusher.Flush()
}
