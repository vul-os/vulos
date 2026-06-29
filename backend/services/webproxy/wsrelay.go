package webproxy

import (
	"context"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"vulos/backend/internal/safedial"
)

// WSRelayHandler proxies WebSocket connections through the server for remote mode.
// Route: /api/proxy/ws/{target_host}/{path}
// Example: /api/proxy/ws/echo.websocket.org/ → connects to wss://echo.websocket.org/
func (s *Service) WSRelayHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/proxy/ws/")
		if path == "" {
			http.Error(w, `{"error":"no target"}`, 400)
			return
		}

		// Build target address
		slashIdx := strings.Index(path, "/")
		var host, rest string
		if slashIdx == -1 {
			host = path
			rest = "/"
		} else {
			host = path[:slashIdx]
			rest = path[slashIdx:]
		}

		// Pre-flight SSRF check: user-friendly 403 for obviously blocked hosts.
		// isPrivate calls safedial.ValidateHost which handles IP literals,
		// obfuscated encodings, and hostname resolution.
		if isPrivate(host) {
			http.Error(w, `{"error":"cannot relay to private addresses"}`, 403)
			return
		}

		// Determine target port
		targetAddr := host
		if !strings.Contains(host, ":") {
			targetAddr = host + ":443"
		}

		// Dial via safedial.New so the Control hook validates the resolved IP at
		// connect(2) time — closing the TOCTOU window between the isPrivate check
		// above and the actual socket connection (DNS-rebinding / redirect defence).
		d := safedial.New(false)
		d.Timeout = 10 * time.Second
		upConn, err := d.DialContext(context.Background(), "tcp", targetAddr)
		if err != nil {
			log.Printf("[wsrelay] dial %s: %v", targetAddr, err)
			http.Error(w, `{"error":"upstream unreachable"}`, 502)
			return
		}

		// Hijack client connection
		hj, ok := w.(http.Hijacker)
		if !ok {
			upConn.Close()
			http.Error(w, "hijack not supported", 500)
			return
		}

		// Forward the original upgrade request to upstream
		reqLine := r.Method + " " + rest + " HTTP/1.1\r\n"
		upConn.Write([]byte(reqLine))
		r.Header.Set("Host", host)
		r.Header.Del("Cookie") // don't leak vulos cookies
		r.Header.Write(upConn)
		upConn.Write([]byte("\r\n"))

		clientConn, _, err := hj.Hijack()
		if err != nil {
			upConn.Close()
			return
		}

		// Bidirectional relay
		go func() {
			io.Copy(upConn, clientConn)
			upConn.Close()
		}()
		go func() {
			io.Copy(clientConn, upConn)
			clientConn.Close()
		}()

		log.Printf("[wsrelay] relaying WebSocket to %s%s", host, rest)
	}
}
