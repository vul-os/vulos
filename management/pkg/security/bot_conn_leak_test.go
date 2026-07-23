package security

import (
	"net"
	"testing"
)

// TestJA3ConnCleansUpOnClose pins the fix for an unbounded fingerprint store.
//
// The JA3Store is keyed by RemoteAddr, which carries the ephemeral client port
// and so is unique for every connection. vulos-cloud wraps its PRODUCTION TLS
// listener with NewJA3Listener, and neither repo ever called Delete or
// CleanHelloStore -- so before this fix the map gained one permanent entry per
// TLS connection, holding data nothing read back (Load runs only from BotScore,
// reachable only through the unmounted BotMiddleware). Over a long-lived process
// that is a straightforward memory leak.
func TestJA3ConnCleansUpOnClose(t *testing.T) {
	store := NewJA3Store()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ja3Ln := NewJA3Listener(ln, store)
	defer ja3Ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ja3Ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		// Trigger the first Read so a fingerprint is actually stored.
		buf := make([]byte, 512)
		_, _ = c.Read(buf)
		accepted <- c
	}()

	client, err := net.Dial("tcp", ja3Ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	// Enough bytes to look like the start of a TLS record and drive the parse.
	if _, err := client.Write([]byte{0x16, 0x03, 0x01, 0x00, 0x05, 0x01, 0x00, 0x00, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}

	srvConn := <-accepted
	if srvConn == nil {
		t.Fatal("accept failed")
	}
	key := srvConn.RemoteAddr().String()

	if _, ok := store.Load(key); !ok {
		t.Fatal("no fingerprint stored; test cannot prove cleanup")
	}

	if err := srvConn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	client.Close()

	if _, ok := store.Load(key); ok {
		t.Fatal("fingerprint survived Close: the store grows by one entry per connection forever")
	}
}
