// Package wsutil provides a shared gorilla/websocket upgrader with
// permessage-deflate compression enabled.
package wsutil

import (
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

// Upgrader is the shared WebSocket upgrader with compression enabled.
// All WebSocket endpoints should use this to get permessage-deflate.
var Upgrader = websocket.Upgrader{
	CheckOrigin:       checkOrigin,
	EnableCompression: true,
}

// checkOrigin validates the WebSocket handshake Origin header.
// Allows same-origin requests (no Origin header, or Origin matches Host)
// and localhost/private network origins for dev.
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // same-origin requests don't send Origin
	}

	host := r.Host
	if host == "" {
		host = r.Header.Get("Host")
	}

	// Strip scheme from origin to compare with host
	o := strings.TrimPrefix(origin, "https://")
	o = strings.TrimPrefix(o, "http://")

	// Same-origin: origin host matches request host
	if o == host {
		return true
	}

	// Allow localhost and private IPs for development
	if strings.HasPrefix(o, "localhost") || strings.HasPrefix(o, "127.0.0.1") ||
		strings.HasPrefix(o, "0.0.0.0") || strings.HasPrefix(o, "192.168.") ||
		strings.HasPrefix(o, "10.") || strings.HasPrefix(o, "172.") {
		return true
	}

	return false
}
