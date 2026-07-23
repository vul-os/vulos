package telephony

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// wsHubLen reads the current connection count under the hub lock.
func wsHubLen(h *wsHub) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// wsWaitLen polls until the hub reaches want connections or the deadline passes.
func wsWaitLen(t *testing.T, h *wsHub, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if wsHubLen(h) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("hub conns = %d, want %d", wsHubLen(h), want)
}

// wsURL turns an httptest http:// URL into a ws:// dial URL.
func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

// wsDial opens a client connection, optionally sending an X-User-ID header.
func wsDial(t *testing.T, serverURL, userID string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	var hdr http.Header
	if userID != "" {
		hdr = http.Header{}
		hdr.Set("X-User-ID", userID)
	}
	return websocket.DefaultDialer.Dial(wsURL(serverURL), hdr)
}

// A non-owner must be rejected with 403 BEFORE the WebSocket handshake completes:
// the connection must never reach 101 Switching Protocols, so a hostile page/account
// on the box can't hold an open socket onto the owner's inbound SMS feed.
func TestServeWS_NonOwnerRejectedBeforeUpgrade(t *testing.T) {
	s := newTeleService("owner-1")
	srv := httptest.NewServer(http.HandlerFunc(s.serveWS))
	defer srv.Close()

	// intruder
	conn, resp, err := wsDial(t, srv.URL, "intruder")
	if err == nil {
		conn.Close()
		t.Fatal("intruder handshake succeeded; must be rejected pre-upgrade")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("intruder: want 403, got resp=%v err=%v", resp, err)
	}

	// unauthenticated (no X-User-ID)
	conn2, resp2, err2 := wsDial(t, srv.URL, "")
	if err2 == nil {
		conn2.Close()
		t.Fatal("unauthenticated handshake succeeded; must be rejected pre-upgrade")
	}
	if resp2 == nil || resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated: want 403, got resp=%v err=%v", resp2, err2)
	}

	// No rejected client may have been added to the fan-out set.
	if n := wsHubLen(s.hub); n != 0 {
		t.Errorf("rejected clients leaked into hub: %d conns", n)
	}
}

// The owner passes the gate and is upgraded (101), then appears in the hub.
func TestServeWS_OwnerUpgradesAndRegisters(t *testing.T) {
	s := newTeleService("owner-1")
	srv := httptest.NewServer(http.HandlerFunc(s.serveWS))
	defer srv.Close()

	conn, resp, err := wsDial(t, srv.URL, "owner-1")
	if err != nil {
		t.Fatalf("owner handshake failed: %v (resp=%v)", err, resp)
	}
	defer conn.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("owner: want 101, got %d", resp.StatusCode)
	}
	wsWaitLen(t, s.hub, 1)
}

// broadcast must fan out to every connected client, and concurrent broadcasts must
// be serialized per connection by the per-conn write mutex (gorilla forbids
// concurrent writes to one conn). Run under -race, a missing/incorrect mutex trips
// the detector; functionally, each client must still receive every frame intact.
func TestWSHub_BroadcastFanoutAndConcurrentWrites(t *testing.T) {
	hub := newWSHub()
	srv := httptest.NewServer(http.HandlerFunc(hub.serveWS))
	defer srv.Close()

	const clients = 3
	const msgs = 25

	conns := make([]*websocket.Conn, clients)
	for i := range conns {
		c, _, err := websocket.DefaultDialer.Dial(wsURL(srv.URL), nil)
		if err != nil {
			t.Fatalf("dial client %d: %v", i, err)
		}
		defer c.Close()
		conns[i] = c
	}
	wsWaitLen(t, hub, clients)

	// Fire many broadcasts concurrently at the single hub.
	var wg sync.WaitGroup
	for i := 0; i < msgs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hub.broadcast(map[string]any{"type": "sms", "body": "ping"})
		}()
	}
	wg.Wait()

	// Every client must receive exactly `msgs` well-formed frames.
	for i, c := range conns {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		for j := 0; j < msgs; j++ {
			var got map[string]any
			if err := c.ReadJSON(&got); err != nil {
				t.Fatalf("client %d frame %d: %v", i, j, err)
			}
			if got["type"] != "sms" || got["body"] != "ping" {
				t.Fatalf("client %d frame %d corrupt: %v", i, j, got)
			}
		}
	}
}

// remove() must drop a connection from the fan-out set. When a client closes, the
// server-side read loop errors and removes it — the hub must shrink so a dead conn
// is never written to again.
func TestWSHub_RemoveOnClientClose(t *testing.T) {
	hub := newWSHub()
	srv := httptest.NewServer(http.HandlerFunc(hub.serveWS))
	defer srv.Close()

	c, _, err := websocket.DefaultDialer.Dial(wsURL(srv.URL), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	wsWaitLen(t, hub, 1)

	c.Close()
	wsWaitLen(t, hub, 0) // remove() ran
}
