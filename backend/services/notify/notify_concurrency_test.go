package notify

// notify_concurrency_test.go — regression for the concurrent-writer crash.
//
// gorilla/websocket permits only ONE concurrent writer per connection and PANICS
// ("concurrent write to websocket connection") if two goroutines write to the
// same conn at once. The notify hub broadcasts to shared client connections from
// many call sites (every HTTP handler that emits a notification, plus the
// reminder scheduler), so two overlapping broadcasts to the same client used to
// crash the whole OS backend. These tests drive concurrent broadcasts and pass
// only if no writer panics.

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestConcurrentBroadcastNoWritePanic(t *testing.T) {
	svc := New()
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	const nClients = 3
	conns := make([]*websocket.Conn, nClients)
	for i := range conns {
		conns[i] = dialAs(t, srv.URL, "") // anonymous ⇒ every untargeted broadcast reaches all
	}
	waitClients(t, svc, nClients)

	// Continuously drain each client so a full send buffer never stalls a writer.
	done := make(chan struct{})
	var readers sync.WaitGroup
	for _, c := range conns {
		readers.Add(1)
		go func(c *websocket.Conn) {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
				_, _, _ = c.ReadMessage() // errors (incl. deadline) are fine; loop checks done
			}
		}(c)
	}

	// Many goroutines broadcast AT ONCE to the same set of connections. Mixes the
	// three broadcast paths (plain, action, with-action) that all share one conn.
	var senders sync.WaitGroup
	for g := 0; g < 8; g++ {
		senders.Add(1)
		go func(g int) {
			defer senders.Done()
			for i := 0; i < 30; i++ {
				switch i % 3 {
				case 0:
					svc.SendNotification(Notification{Title: "t", Body: "msg"})
				case 1:
					SendAction(svc, "a", "b", LevelInfo, "src", []ActionSpec{{Label: "ok"}})
				default:
					svc.SendWithAction("t", "b", LevelInfo, "src", "http://x")
				}
			}
		}(g)
	}
	// If any writer panicked, the test binary would have crashed before this returns.
	senders.Wait()

	close(done)
	for _, c := range conns {
		c.Close()
	}
	readers.Wait()
}
