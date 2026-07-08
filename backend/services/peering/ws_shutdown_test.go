package peering

// ws_shutdown_test.go — regression for the hijacked-WS drain on shutdown.
//
// Hijacked WebSocket conns are NOT tracked by http.Server.Shutdown (universal Go
// behavior), so the peering Hub's readPump goroutines (blocked in ReadMessage)
// would linger past shutdown. Hub.Shutdown() force-closes every registered conn
// so those goroutines observe a read error, unregister, and return.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func dialHub(t *testing.T, srvURL, userID string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srvURL, "http")
	h := http.Header{}
	h.Set("X-User-ID", userID)
	c, _, err := websocket.DefaultDialer.Dial(u+"/api/peering/stream", h)
	if err != nil {
		t.Fatalf("dial as %q: %v", userID, err)
	}
	return c
}

func hubConnCount(h *Hub) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, set := range h.users {
		n += len(set)
	}
	return n
}

// TestHubShutdownClosesLiveWebSockets verifies Hub.Shutdown() force-drains every
// registered connection: the readPump goroutines exit (registry empties) and each
// client observes the connection close.
func TestHubShutdownClosesLiveWebSockets(t *testing.T) {
	hub := NewHub()
	mux := http.NewServeMux()
	hub.RegisterHandlers(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	const nClients = 4
	conns := make([]*websocket.Conn, nClients)
	for i := range conns {
		conns[i] = dialHub(t, srv.URL, "user-shutdown")
		// Drain the welcome frame so the send buffer stays clear.
		conns[i].SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := conns[i].ReadMessage(); err != nil {
			t.Fatalf("client %d: reading welcome frame: %v", i, err)
		}
	}

	// Wait until all connections are registered.
	deadline := time.Now().Add(2 * time.Second)
	for hubConnCount(hub) < nClients {
		if time.Now().After(deadline) {
			t.Fatalf("only %d/%d connections registered", hubConnCount(hub), nClients)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Force-drain.
	hub.Shutdown()

	// Every readPump goroutine must exit, emptying the registry (unregister runs
	// on read error).
	deadline = time.Now().Add(2 * time.Second)
	for hubConnCount(hub) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("after Shutdown, %d connections still registered (leaked goroutines)", hubConnCount(hub))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Each client's read must now fail (server closed the conn).
	for i, c := range conns {
		readDeadline := time.Now().Add(2 * time.Second)
		c.SetReadDeadline(readDeadline)
		closed := false
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				closed = true
				break
			}
			if time.Now().After(readDeadline) {
				break
			}
		}
		if !closed {
			t.Fatalf("client %d never observed connection close after Shutdown", i)
		}
		c.Close()
	}
}

// TestHubShutdownIdempotent verifies Shutdown() is safe with no connections and
// when called more than once.
func TestHubShutdownIdempotent(t *testing.T) {
	hub := NewHub()
	hub.Shutdown()
	hub.Shutdown()

	mux := http.NewServeMux()
	hub.RegisterHandlers(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := dialHub(t, srv.URL, "u1")
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = c.ReadMessage() // welcome frame

	deadline := time.Now().Add(2 * time.Second)
	for hubConnCount(hub) < 1 {
		if time.Now().After(deadline) {
			t.Fatal("connection never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	hub.Shutdown()
	hub.Shutdown() // second call must be a no-op (registry already drained)

	deadline = time.Now().Add(2 * time.Second)
	for hubConnCount(hub) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("connections remaining after double Shutdown: %d", hubConnCount(hub))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
