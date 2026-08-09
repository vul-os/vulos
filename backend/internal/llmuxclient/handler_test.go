package llmuxclient

// handler_test.go — the /api/ai/* contract, exercised through a real
// http.ServeMux, once per backend state.
//
// These are the tests the Backend seam has to keep passing: the remote path is
// a byte-transparent proxy, and an unconfigured box answers 503 rather than
// pretending to have a gateway.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeSidecarSSE is the byte stream a real llmux emits for a streaming chat:
// one data event per chunk, then the terminal sentinel.
const fakeSidecarSSE = "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\"," +
	"\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}," +
	"\"finish_reason\":null}]}\n\n" +
	"data: [DONE]\n\n"

// newSidecar starts a stand-in for a remote llmux sidecar.
func newSidecar(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(fakeSidecarSSE))
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m","object":"model"}]}`))
		case "/v1/embeddings":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","model":"e","data":[{"object":"embedding","index":0,"embedding":[0.5,-0.25]}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// muxFor mounts the AI routes on a fresh ServeMux backed by b.
func muxFor(b Backend) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterHandlers(mux, b, nil)
	return mux
}

func postJSON(t *testing.T, mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	return rec
}

// TestChat_RemoteProxiesBytesVerbatim pins the property the whole SSE-parity
// argument rests on: on the remote path the OS adds nothing to and removes
// nothing from the gateway's stream.
func TestChat_RemoteProxiesBytesVerbatim(t *testing.T) {
	sidecar := newSidecar(t)
	mux := muxFor(RemoteBackend(New(Config{BaseURL: sidecar.URL})))

	rec := postJSON(t, mux, "/api/ai/chat", `{"model":"m","stream":true,"messages":[]}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != fakeSidecarSSE {
		t.Fatalf("proxied body is not verbatim:\n got %q\nwant %q", got, fakeSidecarSSE)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("X-Accel-Buffering header lost — proxies will buffer the stream")
	}
}

// TestChat_UnconfiguredIs503 keeps the pre-embedding behaviour of a box with no
// gateway: a 503 whose code says the gateway is unconfigured, not a 502 that
// reads like the gateway broke.
func TestChat_UnconfiguredIs503(t *testing.T) {
	mux := muxFor(Unconfigured())
	rec := postJSON(t, mux, "/api/ai/chat", `{"model":"m","messages":[]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body=%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(body.Error, "gateway_unconfigured:") {
		t.Fatalf("error = %q, want a gateway_unconfigured: prefix", body.Error)
	}
}

// TestChat_RejectsBadRequestsBeforeTheGateway checks the handler's own
// pre-flight (which the backends never see) still fires.
func TestChat_RejectsBadRequestsBeforeTheGateway(t *testing.T) {
	sidecar := newSidecar(t)
	mux := muxFor(RemoteBackend(New(Config{BaseURL: sidecar.URL})))

	if rec := postJSON(t, mux, "/api/ai/chat", `not json`); rec.Code != 400 {
		t.Fatalf("invalid JSON: status = %d, want 400", rec.Code)
	}
	if rec := postJSON(t, mux, "/api/ai/chat", `{"messages":[]}`); rec.Code != 422 {
		t.Fatalf("missing model: status = %d, want 422", rec.Code)
	}
}

// TestStatus_ReportsMode asserts GET /api/ai/status names which of the three
// states the box is in — the one place an operator can tell an embedded
// gateway from a remote one from none at all.
func TestStatus_ReportsMode(t *testing.T) {
	sidecar := newSidecar(t)
	for _, tc := range []struct {
		name       string
		backend    Backend
		wantStatus string
		wantMode   Mode
	}{
		{"remote", RemoteBackend(New(Config{BaseURL: sidecar.URL})), "ok", ModeRemote},
		{"unconfigured", Unconfigured(), "unconfigured", ModeUnconfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			muxFor(tc.backend).ServeHTTP(rec, httptest.NewRequest("GET", "/api/ai/status", nil))
			if rec.Code != 200 {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			var body struct {
				Status string `json:"status"`
				Mode   string `json:"mode"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Status != tc.wantStatus || body.Mode != string(tc.wantMode) {
				t.Fatalf("status=%q mode=%q, want %q/%q", body.Status, body.Mode, tc.wantStatus, tc.wantMode)
			}
		})
	}
}

// TestRemoteBackend_CollapsesToUnconfigured guards the one way a caller could
// accidentally get a live-looking backend with nowhere to send requests.
func TestRemoteBackend_CollapsesToUnconfigured(t *testing.T) {
	if got := RemoteBackend(nil).Mode(); got != ModeUnconfigured {
		t.Fatalf("RemoteBackend(nil).Mode() = %q, want unconfigured", got)
	}
	if got := RemoteBackend(New(Config{})).Mode(); got != ModeUnconfigured {
		t.Fatalf("RemoteBackend(no base URL).Mode() = %q, want unconfigured", got)
	}
}

// TestEmbedAndModels_Remote covers the two non-chat gateway calls end to end.
func TestEmbedAndModels_Remote(t *testing.T) {
	sidecar := newSidecar(t)
	mux := muxFor(RemoteBackend(New(Config{BaseURL: sidecar.URL})))

	rec := postJSON(t, mux, "/api/ai/embed", `{"input":"hello"}`)
	if rec.Code != 200 {
		t.Fatalf("embed status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var er EmbedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &er); err != nil {
		t.Fatalf("decode embed: %v", err)
	}
	if len(er.Embedding) != 2 || er.Embedding[0] != 0.5 {
		t.Fatalf("embedding = %v, want [0.5 -0.25]", er.Embedding)
	}

	rec = postJSON(t, mux, "/api/ai/models", ``)
	if rec.Code != 200 {
		t.Fatalf("models status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":"m"`) {
		t.Fatalf("models body = %s, want the sidecar's listing", rec.Body.String())
	}
}
