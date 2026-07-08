package notify

// notify_shutdown_test.go — regression for the hijacked-WS drain on shutdown.
//
// Hijacked WebSocket conns are NOT tracked by http.Server.Shutdown (universal Go
// behavior), so their reader goroutines (blocked in ReadMessage) would linger
// past shutdown. Service.Shutdown() force-closes every live WS so those handler
// goroutines observe a read error, unregister, and return.

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestShutdownClosesLiveWebSockets verifies Shutdown() force-drains every
// registered WS connection: the server-side handler goroutines exit (client set
// empties) and the client observes the connection close.
func TestShutdownClosesLiveWebSockets(t *testing.T) {
	svc := New()
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	const nClients = 4
	conns := make([]*websocket.Conn, nClients)
	for i := range conns {
		conns[i] = dialAs(t, srv.URL, "")
	}
	waitClients(t, svc, nClients)

	// Force-drain.
	svc.Shutdown()

	// Every server-side handler goroutine must exit, emptying the client set.
	deadline := time.Now().Add(2 * time.Second)
	for {
		svc.mu.RLock()
		n := len(svc.clients)
		svc.mu.RUnlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after Shutdown, %d client handler goroutines still registered (leaked)", n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Each client's read must now fail (connection was closed by the server).
	for i, c := range conns {
		readDeadline := time.Now().Add(2 * time.Second)
		c.SetReadDeadline(readDeadline)
		closed := false
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				closed = true // expected: server sent a close frame / closed the conn
				break
			}
			// A queued notification frame may arrive first; keep reading until the
			// close is observed or the deadline trips.
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

// TestShutdownIdempotent verifies Shutdown() is safe to call multiple times and
// with no connections present.
func TestShutdownIdempotent(t *testing.T) {
	svc := New()
	// No connections.
	svc.Shutdown()
	svc.Shutdown()

	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	c := dialAs(t, srv.URL, "")
	defer c.Close()
	waitClients(t, svc, 1)

	svc.Shutdown()
	svc.Shutdown() // second call: client set already emptied, must be a no-op

	svc.mu.RLock()
	n := len(svc.clients)
	svc.mu.RUnlock()
	if n != 0 {
		t.Fatalf("clients remaining after double Shutdown: %d", n)
	}
}
