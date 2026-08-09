package telemetry_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"vulos/backend/internal/wsutil"
	"vulos/backend/services/telemetry"
)

// TestHandler_RejectsSpoofedPrivateOrigin drives a REAL shipped WebSocket
// endpoint — telemetry.Handler(), mounted at /api/telemetry in cmd/server —
// through a real gorilla handshake, rather than only calling CheckOrigin.
//
// Attack modelled: a logged-in user visits a page served from a host the
// attacker registered as "localhost.attacker.example". The browser sends the
// SameSite=None session cookie with the WebSocket upgrade (CORS does not apply
// to upgrades). If the box accepts the handshake the attacker's page gets a
// live authenticated stream.
func TestHandler_RejectsSpoofedPrivateOrigin(t *testing.T) {
	// Worst case for us: the developer relaxation is switched ON.
	prev := wsutil.AllowPrivateOrigins()
	wsutil.SetAllowPrivateOrigins(true)
	t.Cleanup(func() { wsutil.SetAllowPrivateOrigins(prev) })

	srv := httptest.NewServer(telemetry.Handler())
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	hdr := http.Header{}
	hdr.Set("Origin", "https://localhost.attacker.example")

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err == nil {
		conn.Close()
		t.Fatal("handshake ACCEPTED from https://localhost.attacker.example — cross-site WebSocket hijack")
	}
	if resp == nil {
		t.Fatalf("dial failed without an HTTP response (transport error, not an origin rejection): %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("got status %d, want 403 from the origin check", resp.StatusCode)
	}
}

// TestHandler_AcceptsSameOrigin proves the rejection above is the origin check
// discriminating, not the handler being broken for everyone.
func TestHandler_AcceptsSameOrigin(t *testing.T) {
	prev := wsutil.AllowPrivateOrigins()
	wsutil.SetAllowPrivateOrigins(false)
	t.Cleanup(func() { wsutil.SetAllowPrivateOrigins(prev) })

	srv := httptest.NewServer(telemetry.Handler())
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	hdr := http.Header{}
	hdr.Set("Origin", srv.URL) // http://127.0.0.1:PORT — same origin as Host

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("same-origin handshake rejected: %v", err)
	}
	conn.Close()
}
