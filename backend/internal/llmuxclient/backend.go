package llmuxclient

// backend.go — the seam between the /api/ai/* handlers and whatever actually
// serves an LLM request.
//
// There are exactly three states, and every one of them is a Backend:
//
//	embedded     — llmux runs IN THIS PROCESS as a Go library. No sidecar, no
//	               LLMUX_URL, no socket between the OS and the gateway.
//	remote       — llmux runs somewhere else and is reached over HTTP
//	               (LLMUX_URL). This is the original behaviour and stays
//	               supported: an operator running one shared central llmux for
//	               several boxes is a real deployment.
//	unconfigured — no gateway at all. Every /api/ai/* route answers 503, which
//	               is what an OS with no LLMUX_URL did before embedding existed.
//
// The handlers know only this interface, so neither of the two real backends
// can grow a behaviour the other silently lacks: adding a method forces both to
// answer for it.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Mode names one of the three backend states.
type Mode string

const (
	// ModeEmbedded is llmux linked into this process as a library.
	ModeEmbedded Mode = "embedded"
	// ModeRemote is llmux reached over HTTP at LLMUX_URL.
	ModeRemote Mode = "remote"
	// ModeUnconfigured is no gateway at all.
	ModeUnconfigured Mode = "unconfigured"
)

// ErrUnconfigured is returned by every Backend method when no AI gateway is
// configured. The handlers map it to 503 (never 502): nothing is broken, the
// operator simply has not pointed the OS at a gateway.
var ErrUnconfigured = errors.New("llmuxclient: no AI gateway configured")

// Backend serves the OS's LLM requests. It is implemented by the in-process
// gateway (Embedded), the HTTP client (Client, via RemoteBackend) and the
// do-nothing Unconfigured backend.
type Backend interface {
	// Mode reports which of the three states this backend is in.
	Mode() Mode

	// Status is the JSON body GET /api/ai/status returns.
	Status() map[string]any

	// Chat runs one chat completion and writes the whole response to w itself,
	// because the two backends frame it differently: the remote one copies the
	// gateway's bytes through, the embedded one owns the SSE framing.
	//
	// committed reports whether anything was written to w. When it is false the
	// caller still owns the response and may write a JSON error; when it is true
	// the status line is long gone and err (if any) is informational only.
	Chat(ctx context.Context, w http.ResponseWriter, body []byte) (committed bool, err error)

	// Embed returns an embedding vector and the model that actually produced it.
	Embed(ctx context.Context, model, input string) ([]float32, string, error)

	// Models returns the OpenAI-shaped model listing as raw JSON.
	Models(ctx context.Context) (json.RawMessage, error)

	// Close releases whatever the backend holds. Safe on every backend, and safe
	// to call more than once.
	Close() error
}

// ---------------------------------------------------------------------------
// unconfigured
// ---------------------------------------------------------------------------

type unconfiguredBackend struct{}

// Unconfigured returns the backend used when no AI gateway is configured.
func Unconfigured() Backend { return unconfiguredBackend{} }

func (unconfiguredBackend) Mode() Mode { return ModeUnconfigured }

func (unconfiguredBackend) Status() map[string]any {
	return map[string]any{"status": "unconfigured", "mode": string(ModeUnconfigured)}
}

func (unconfiguredBackend) Chat(context.Context, http.ResponseWriter, []byte) (bool, error) {
	return false, ErrUnconfigured
}

func (unconfiguredBackend) Embed(context.Context, string, string) ([]float32, string, error) {
	return nil, "", ErrUnconfigured
}

func (unconfiguredBackend) Models(context.Context) (json.RawMessage, error) {
	return nil, ErrUnconfigured
}

func (unconfiguredBackend) Close() error { return nil }

// ---------------------------------------------------------------------------
// remote (HTTP to LLMUX_URL) — the original behaviour
// ---------------------------------------------------------------------------

type remoteBackend struct{ c *Client }

// RemoteBackend adapts the HTTP Client to the Backend seam. A nil client, or
// one with no base URL, is the unconfigured backend — the same collapse the
// handlers used to do inline by testing client.cfg.BaseURL == "".
func RemoteBackend(c *Client) Backend {
	if c == nil || c.cfg.BaseURL == "" {
		return Unconfigured()
	}
	return &remoteBackend{c: c}
}

func (r *remoteBackend) Mode() Mode { return ModeRemote }

func (r *remoteBackend) Status() map[string]any {
	return map[string]any{
		"status":  "ok",
		"mode":    string(ModeRemote),
		"gateway": r.c.cfg.BaseURL,
	}
}

// Chat forwards the raw request body to the remote gateway and copies the
// response bytes back unchanged. The OS is a transparent proxy on this path:
// whatever llmux frames (SSE for a streaming request, JSON otherwise) is what
// the client sees, under the OS's own streaming headers.
func (r *remoteBackend) Chat(ctx context.Context, w http.ResponseWriter, body []byte) (bool, error) {
	stream, err := r.c.Chat(ctx, body)
	if err != nil {
		return false, err
	}
	defer stream.Close() //nolint:errcheck

	writeStreamHeaders(w)
	flusher, canFlush := w.(http.Flusher)
	if canFlush {
		flusher.Flush()
	}

	buf := make([]byte, 4096)
	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return true, writeErr
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				// The stream broke after the response was committed; the only
				// channel left is the stream itself.
				writeSSEErrorText(w, readErr.Error())
				if canFlush {
					flusher.Flush()
				}
				return true, readErr
			}
			return true, nil
		}
	}
}

func (r *remoteBackend) Embed(ctx context.Context, model, input string) ([]float32, string, error) {
	return r.c.Embed(ctx, model, input)
}

func (r *remoteBackend) Models(ctx context.Context) (json.RawMessage, error) {
	return r.c.Models(ctx)
}

func (r *remoteBackend) Close() error { return nil }
