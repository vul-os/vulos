package tunnel

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	yamux "github.com/libp2p/go-yamux/v5"
)

// wsconn.go — adapting a WebSocket to the net.Conn that yamux needs, and a
// yamux session to the net.Listener that http.Server needs.
//
// These two adapters are the only glue between four off-the-shelf layers
// (WebSocket, yamux, stdlib HTTP server, stdlib reverse proxy). Keeping them
// small and boring is the point: every line here is a line that is not
// covered by someone else's test suite.

// wsConn presents a gorilla WebSocket as a net.Conn byte stream.
//
// # Why an adapter instead of hijacking the underlying TCP conn
//
// gorilla exposes UnderlyingConn(), and using it directly would avoid
// per-message framing overhead — but it would also bypass WebSocket framing
// entirely, which is exactly what lets this traffic pass through the proxies,
// captive portals, and CDNs that a tunnel exists to traverse. The framing is
// the feature. The overhead (2-14 bytes per yamux frame, further amortised by
// yamux's own write coalescing) is the price, and it is the right trade.
//
// # Concurrency contract
//
// gorilla permits ONE concurrent reader and ONE concurrent writer. yamux
// drives exactly one of each (a recv loop and a send loop), so the mutexes
// below are belt-and-braces rather than load-bearing — but they are cheap,
// and they make the type safe to use from any caller rather than safe only
// under yamux's specific access pattern.
type wsConn struct {
	ws *websocket.Conn

	readMu sync.Mutex
	// r is the reader for the message currently being consumed. A single
	// net.Conn Read may span several WebSocket messages and a single
	// WebSocket message may satisfy several Reads, so this cursor persists
	// across calls.
	r io.Reader

	writeMu sync.Mutex

	closeOnce sync.Once
}

// newWSConn wraps ws. The caller hands over ownership: closing the returned
// net.Conn closes the WebSocket.
func newWSConn(ws *websocket.Conn) *wsConn {
	return &wsConn{ws: ws}
}

// Read fills p from the WebSocket message stream, advancing to the next
// message when the current one is exhausted.
func (c *wsConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	for {
		if c.r == nil {
			typ, r, err := c.ws.NextReader()
			if err != nil {
				return 0, translateWSError(err)
			}
			// Only binary carries tunnel bytes. A text frame at this stage is
			// a protocol violation (the handshake is over) — skipping it
			// rather than erroring would let a peer desynchronise the stream.
			if typ != websocket.BinaryMessage {
				return 0, errors.New("tunnel: peer sent a non-binary frame after the handshake")
			}
			c.r = r
		}
		n, err := c.r.Read(p)
		if errors.Is(err, io.EOF) {
			// End of THIS message, not of the stream: drop the cursor and, if
			// this read produced nothing, pull the next message.
			c.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			return n, translateWSError(err)
		}
		return n, nil
	}
}

// Write sends p as one binary WebSocket message.
func (c *wsConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, translateWSError(err)
	}
	return len(p), nil
}

// Close sends a WebSocket close frame (best effort, bounded) and then closes
// the connection. The close frame matters: without it the peer's read blocks
// until a TCP timeout, which on a mobile or NAT'd path can be minutes.
func (c *wsConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.writeMu.Lock()
		_ = c.ws.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(2*time.Second),
		)
		c.writeMu.Unlock()
		err = c.ws.Close()
	})
	return err
}

func (c *wsConn) LocalAddr() net.Addr  { return c.ws.LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr { return c.ws.RemoteAddr() }

func (c *wsConn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}

func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }

// translateWSError maps a normal WebSocket close into io.EOF.
//
// yamux treats io.EOF as an orderly shutdown and anything else as a fault it
// logs loudly. Without this mapping, every clean tunnel teardown — a box
// restarting, a relay draining — would surface as a scary error in the
// operator's log, training them to ignore the log.
func translateWSError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return io.EOF
	}
	if websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseNoStatusReceived,
	) {
		return io.EOF
	}
	return err
}

// yamuxConfig is the shared session tuning for both ends of a tunnel.
//
// maxIncomingStreams is the concurrency cap on in-flight requests through one
// tunnel. It is a real backpressure limit, not a formality: each stream costs
// a goroutine and a window's worth of buffer on the box, and the box is
// typically the smallest machine in the path.
func yamuxConfig(maxIncomingStreams uint32, logOutput io.Writer) *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = KeepAliveInterval
	cfg.ConnectionWriteTimeout = WriteTimeout
	if maxIncomingStreams > 0 {
		cfg.MaxIncomingStreams = maxIncomingStreams
	}
	if logOutput != nil {
		cfg.LogOutput = logOutput
	}
	return cfg
}

// sessionListener adapts a yamux session to net.Listener so the agent can
// drive it with a stock http.Server.
//
// yamux.Session already has Accept and Close with the right signatures; only
// Addr is missing. That one missing method is the entire reason this type
// exists, and it is why the agent gets the full stdlib HTTP server — timeouts,
// HTTP/1.1 keep-alive, protocol upgrades, panic recovery — for free rather
// than reimplementing request parsing over raw streams.
type sessionListener struct {
	sess *yamux.Session
	addr net.Addr
}

func (l *sessionListener) Accept() (net.Conn, error) { return l.sess.Accept() }
func (l *sessionListener) Close() error              { return l.sess.Close() }
func (l *sessionListener) Addr() net.Addr            { return l.addr }

// tunnelAddr is the net.Addr reported for tunnelled connections. It is
// deliberately NOT a plausible-looking IP: anything that reads like a real
// address here would end up in logs and rate-limit keys as if it were the
// client's, which it is not. The real client IP arrives in HeaderClientIP and
// is applied to RemoteAddr explicitly by the agent.
type tunnelAddr struct{ relay string }

func (a tunnelAddr) Network() string { return "vulos-reach-tunnel" }
func (a tunnelAddr) String() string {
	if a.relay == "" {
		return "tunnel"
	}
	return "tunnel:" + a.relay
}
