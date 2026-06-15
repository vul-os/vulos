// Package wsutil provides a shared gorilla/websocket upgrader with
// permessage-deflate compression enabled.
package wsutil

import (
	"net/http"
	"os"
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
// Allows same-origin requests (no Origin header, or Origin matches Host).
// Localhost and private-IP origins are only allowed in dev/local mode;
// in prod (VULOS_ENV=prod) they are rejected to prevent cross-origin
// WebSocket attacks from pages served on the same host.
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

	// Allow localhost and private IPs only in dev/local mode.
	// In prod, the real Vulos UI connects from the production domain so
	// the same-origin check above is sufficient. Allowing private IPs in
	// prod would permit WebSocket connections from any page on the LAN.
	if os.Getenv("VULOS_ENV") != "prod" {
		if strings.HasPrefix(o, "localhost") || strings.HasPrefix(o, "127.0.0.1") ||
			strings.HasPrefix(o, "0.0.0.0") || strings.HasPrefix(o, "192.168.") ||
			strings.HasPrefix(o, "10.") || strings.HasPrefix(o, "172.") {
			return true
		}
	}

	return false
}
