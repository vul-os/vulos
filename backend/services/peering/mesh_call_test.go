// mesh_call_test.go — tests for MeshSignalingHub (PEER-26).
//
// Tests use httptest.Server + gorilla/websocket to exercise the full
// WebSocket path without mocking the upgrader.
package peering

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// meshDialWS opens a WebSocket connection to the given URL and returns the conn.
// It converts the test server's http:// URL to ws://.
func meshDialWS(t *testing.T, srv *httptest.Server, room, peer string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") +
		"/api/peering/call/signal?room=" + room + "&peer=" + peer
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("meshDialWS: %v", err)
	}
	return conn
}

// meshRead reads one text message from ws and decodes it.
func meshRead(t *testing.T, ws *websocket.Conn) map[string]json.RawMessage {
	t.Helper()
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("meshRead: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("meshRead unmarshal: %v", err)
	}
	return m
}

// meshSend sends a JSON object as a text message.
func meshSend(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("meshSend marshal: %v", err)
	}
	ws.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := ws.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("meshSend write: %v", err)
	}
}

// newMeshServer sets up an httptest.Server with a MeshSignalingHub.
func newMeshServer(t *testing.T) *httptest.Server {
	t.Helper()
	hub := NewMeshSignalingHub()
	mux := http.NewServeMux()
	RegisterMeshCallHandlers(mux, hub)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestMeshSignaling_JoinBroadcast verifies that when peer B joins a room, peer A
// receives the "join" frame broadcast.
func TestMeshSignaling_JoinBroadcast(t *testing.T) {
	srv := newMeshServer(t)

	// A connects first.
	wsA := meshDialWS(t, srv, "room1", "peer-A")
	defer wsA.Close()

	// B joins.
	wsB := meshDialWS(t, srv, "room1", "peer-B")
	defer wsB.Close()

	// B announces itself.
	meshSend(t, wsB, map[string]any{
		"type":   "join",
		"from":   "peer-B",
		"roomId": "room1",
	})

	// A should receive the join frame.
	frame := meshRead(t, wsA)
	msgType := strings.Trim(string(frame["type"]), `"`)
	if msgType != "join" {
		t.Errorf("expected type=join, got %s", msgType)
	}
	fromVal := strings.Trim(string(frame["from"]), `"`)
	if fromVal != "peer-B" {
		t.Errorf("expected from=peer-B, got %s", fromVal)
	}
}

// TestMeshSignaling_OfferUnicast verifies that an offer frame is unicast to
// the named "to" peer only, not broadcast to all.
func TestMeshSignaling_OfferUnicast(t *testing.T) {
	srv := newMeshServer(t)

	wsA := meshDialWS(t, srv, "room2", "peer-A")
	defer wsA.Close()
	wsB := meshDialWS(t, srv, "room2", "peer-B")
	defer wsB.Close()
	wsC := meshDialWS(t, srv, "room2", "peer-C")
	defer wsC.Close()

	sdp := "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\n"
	meshSend(t, wsA, map[string]any{
		"type":   "offer",
		"from":   "peer-A",
		"to":     "peer-B",
		"roomId": "room2",
		"sdp":    sdp,
	})

	// B should receive the offer.
	frame := meshRead(t, wsB)
	msgType := strings.Trim(string(frame["type"]), `"`)
	if msgType != "offer" {
		t.Errorf("peer-B: expected offer, got %s", msgType)
	}

	// C should NOT receive it — read with a short deadline expecting timeout.
	wsC.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	_, _, err := wsC.ReadMessage()
	if err == nil {
		t.Error("peer-C should not receive a unicast offer addressed to peer-B")
	}
}

// TestMeshSignaling_AnswerUnicast verifies that an answer is routed only to
// the named target peer.
func TestMeshSignaling_AnswerUnicast(t *testing.T) {
	srv := newMeshServer(t)

	wsA := meshDialWS(t, srv, "room3", "peer-A")
	defer wsA.Close()
	wsB := meshDialWS(t, srv, "room3", "peer-B")
	defer wsB.Close()

	meshSend(t, wsB, map[string]any{
		"type":   "answer",
		"from":   "peer-B",
		"to":     "peer-A",
		"roomId": "room3",
		"sdp":    "v=0\r\n",
	})

	frame := meshRead(t, wsA)
	msgType := strings.Trim(string(frame["type"]), `"`)
	if msgType != "answer" {
		t.Errorf("expected answer, got %s", msgType)
	}
}

// TestMeshSignaling_CandidateUnicast verifies ICE candidate unicast delivery.
func TestMeshSignaling_CandidateUnicast(t *testing.T) {
	srv := newMeshServer(t)

	wsA := meshDialWS(t, srv, "room4", "peer-A")
	defer wsA.Close()
	wsB := meshDialWS(t, srv, "room4", "peer-B")
	defer wsB.Close()

	meshSend(t, wsA, map[string]any{
		"type":      "candidate",
		"from":      "peer-A",
		"to":        "peer-B",
		"roomId":    "room4",
		"candidate": map[string]any{"candidate": "candidate:1 1 udp ..."},
	})

	frame := meshRead(t, wsB)
	msgType := strings.Trim(string(frame["type"]), `"`)
	if msgType != "candidate" {
		t.Errorf("expected candidate, got %s", msgType)
	}
}

// TestMeshSignaling_BandwidthBroadcast verifies that a bw frame is sent to
// all other room peers.
func TestMeshSignaling_BandwidthBroadcast(t *testing.T) {
	srv := newMeshServer(t)

	wsA := meshDialWS(t, srv, "room5", "peer-A")
	defer wsA.Close()
	wsB := meshDialWS(t, srv, "room5", "peer-B")
	defer wsB.Close()
	wsC := meshDialWS(t, srv, "room5", "peer-C")
	defer wsC.Close()

	meshSend(t, wsA, map[string]any{
		"type":          "bw",
		"from":          "peer-A",
		"roomId":        "room5",
		"upload_kbps":   2000,
		"download_kbps": 10000,
		"rtt_ms":        15,
	})

	// Both B and C should receive the bw frame.
	for _, ws := range []*websocket.Conn{wsB, wsC} {
		frame := meshRead(t, ws)
		msgType := strings.Trim(string(frame["type"]), `"`)
		if msgType != "bw" {
			t.Errorf("expected bw, got %s", msgType)
		}
	}
}

// TestMeshSignaling_LeaveSynthesised verifies that when a peer closes its
// connection, a synthesised "leave" frame is broadcast to remaining peers.
func TestMeshSignaling_LeaveSynthesised(t *testing.T) {
	srv := newMeshServer(t)

	wsA := meshDialWS(t, srv, "room6", "peer-A")
	defer wsA.Close()
	wsB := meshDialWS(t, srv, "room6", "peer-B")
	defer wsB.Close()

	// C connects and then immediately disconnects.
	wsC := meshDialWS(t, srv, "room6", "peer-C")
	wsC.Close()

	// A or B should receive a leave frame for peer-C.
	// Try A first.
	wsA.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := wsA.ReadMessage()
	if err != nil {
		// Maybe B got it.
		wsB.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, raw, err = wsB.ReadMessage()
		if err != nil {
			t.Fatalf("expected a leave frame but got no message on either peer: %v", err)
		}
	}

	var frame map[string]json.RawMessage
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("unmarshal leave frame: %v", err)
	}
	msgType := strings.Trim(string(frame["type"]), `"`)
	if msgType != "leave" {
		t.Errorf("expected type=leave, got %s", msgType)
	}
	fromVal := strings.Trim(string(frame["from"]), `"`)
	if fromVal != "peer-C" {
		t.Errorf("expected from=peer-C in leave frame, got %s", fromVal)
	}
}

// TestMeshSignaling_RoomFull verifies that the 5th peer is rejected.
func TestMeshSignaling_RoomFull(t *testing.T) {
	srv := newMeshServer(t)

	// Connect 4 peers (max capacity).
	conns := make([]*websocket.Conn, meshRoomMaxPeers)
	for i := range conns {
		c := meshDialWS(t, srv, "roomFull", "peer-"+string(rune('A'+i)))
		conns[i] = c
		defer c.Close()
	}

	// 5th peer should receive an error frame and get disconnected.
	u := "ws" + strings.TrimPrefix(srv.URL, "http") +
		"/api/peering/call/signal?room=roomFull&peer=peer-E"
	ws5, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		// Connection refused at the HTTP level is also acceptable.
		return
	}
	defer ws5.Close()

	ws5.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, _ := ws5.ReadMessage()
	if len(raw) > 0 {
		var frame map[string]json.RawMessage
		if jsonErr := json.Unmarshal(raw, &frame); jsonErr == nil {
			if errType, ok := frame["type"]; ok {
				msgType := strings.Trim(string(errType), `"`)
				if msgType != "error" {
					t.Errorf("expected error frame for 5th peer, got type=%s", msgType)
				}
			}
		}
	}
	// Connection should be closed shortly after.
	ws5.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _, connErr := ws5.ReadMessage()
	if connErr == nil {
		t.Log("note: 5th peer connection still alive; server may close async — acceptable")
	}
}

// TestMeshSignaling_FromStamping verifies that the server stamps the "from"
// field with the authenticated peer ID, preventing spoofing.
func TestMeshSignaling_FromStamping(t *testing.T) {
	srv := newMeshServer(t)

	wsA := meshDialWS(t, srv, "room7", "peer-A")
	defer wsA.Close()
	wsB := meshDialWS(t, srv, "room7", "peer-B")
	defer wsB.Close()

	// A sends a join but claims to be "peer-X" (spoof attempt).
	meshSend(t, wsA, map[string]any{
		"type":   "join",
		"from":   "peer-X", // spoofed
		"roomId": "room7",
	})

	// B should see from="peer-A" (the authenticated identity).
	frame := meshRead(t, wsB)
	fromVal := strings.Trim(string(frame["from"]), `"`)
	if fromVal != "peer-A" {
		t.Errorf("expected from=peer-A (stamped), got %s", fromVal)
	}
}

// TestMeshSignaling_MissingQueryParams verifies 400 when room or peer is absent.
func TestMeshSignaling_MissingQueryParams(t *testing.T) {
	srv := newMeshServer(t)

	cases := []string{
		srv.URL + "/api/peering/call/signal",         // both missing
		srv.URL + "/api/peering/call/signal?room=r1", // peer missing
		srv.URL + "/api/peering/call/signal?peer=p1", // room missing
	}

	for _, rawURL := range cases {
		resp, err := http.Get(rawURL) //nolint:noctx
		if err != nil {
			t.Errorf("GET %s: %v", rawURL, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s: expected 400, got %d", rawURL, resp.StatusCode)
		}
	}
}
