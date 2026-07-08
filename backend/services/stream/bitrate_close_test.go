package stream

import (
	"sync"
	"testing"
)

// TestBitrateControllerCloseIdempotent verifies that Close() is a no-op on the
// second and subsequent calls rather than panicking with "close of closed
// channel". The pre-hardening select-on-channel form was not concurrency-safe.
func TestBitrateControllerCloseIdempotent(t *testing.T) {
	bc := &bitrateController{stop: make(chan struct{})}

	// Serial double close must not panic.
	bc.Close()
	bc.Close()
	bc.Close()

	// The stop channel must be closed (run loop would observe it).
	select {
	case <-bc.stop:
	default:
		t.Fatal("stop channel not closed after Close()")
	}
}

// TestBitrateControllerConcurrentClose hammers Close() from many goroutines at
// once. Before the sync.Once fix, two goroutines could both fall through the
// select's default branch and race into close(bc.stop), panicking the process.
// This test must complete without panic and with the channel closed exactly once.
func TestBitrateControllerConcurrentClose(t *testing.T) {
	const goroutines = 64

	bc := &bitrateController{stop: make(chan struct{})}

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer done.Done()
			start.Wait() // line all goroutines up before the race
			bc.Close()
		}()
	}

	start.Done() // fire
	done.Wait()  // a panic in any goroutine would crash the test binary

	select {
	case <-bc.stop:
	default:
		t.Fatal("stop channel not closed after concurrent Close()")
	}
}
