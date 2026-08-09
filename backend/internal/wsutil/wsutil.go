// Package wsutil provides a shared gorilla/websocket upgrader with
// permessage-deflate compression enabled.
package wsutil

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// Upgrader is the shared WebSocket upgrader with compression enabled.
// All WebSocket endpoints should use this to get permessage-deflate.
var Upgrader = websocket.Upgrader{
	CheckOrigin:       checkOrigin,
	EnableCompression: true,
}

// allowPrivateOrigins gates the developer-only relaxation that accepts
// WebSocket handshakes from loopback / private-IP origins that are NOT the
// request's own origin (a Vite dev server on localhost:5173 talking to the
// backend on localhost:8080).
//
// It defaults to FALSE — fail closed. A CORS preflight does not protect a
// WebSocket upgrade, so a permissive origin check is a direct cross-site
// WebSocket hijack of the caller's session cookie. Only cmd/server, after it
// has resolved the runtime environment, may relax it; every other binary,
// every test, and any future consumer that forgets to wire it gets the strict
// behaviour.
//
// It is deliberately NOT read from os.Getenv here: the canonical environment
// is resolved by services/env from the --env flag with VULOS_ENV only as a
// fallback, and the documented dev and prod invocations both leave VULOS_ENV
// unset. Reading the variable directly made the strict branch unreachable.
var allowPrivateOrigins atomic.Bool

// SetAllowPrivateOrigins enables or disables the developer-only relaxation
// described on allowPrivateOrigins. Call it once at startup, before serving,
// with the result of !env.IsProd(). Not calling it at all leaves the strict
// (production) behaviour in place.
func SetAllowPrivateOrigins(allow bool) { allowPrivateOrigins.Store(allow) }

// AllowPrivateOrigins reports the current setting. Exported for startup logs
// and tests.
func AllowPrivateOrigins() bool { return allowPrivateOrigins.Load() }

// checkOrigin validates the WebSocket handshake Origin header.
//
// Accepts:
//   - a missing Origin (non-browser clients; browsers always send one on a
//     WebSocket handshake), and
//   - an Origin whose host:port equals the request's own Host (same-origin).
//
// Everything else is rejected, unless the developer relaxation is on, in which
// case a loopback / private-IP LITERAL origin is also accepted. The literal
// requirement matters: matching by string prefix let "localhost.attacker.com",
// "10.attacker.com" and "192.168.attacker.com" — ordinary hostnames any
// attacker can register — pass as "private".
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // same-origin requests don't send Origin
	}

	host := r.Host
	if host == "" {
		host = r.Header.Get("Host")
	}

	// Parse rather than string-trim so that a non-origin value ("null" from a
	// sandboxed iframe, a bare hostname, a javascript: URL) cannot be coerced
	// into a match.
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}

	// Same-origin: the Origin's host:port matches the request's Host.
	if host != "" && strings.EqualFold(u.Host, host) {
		return true
	}

	if !allowPrivateOrigins.Load() {
		return false
	}
	return isLocalHostPort(u.Host)
}

// isLocalHostPort reports whether hostport names the local machine or a
// private-network address. Only the literal name "localhost" and real IP
// literals qualify; any other DNS name returns false, because a name is
// attacker-registrable and tells us nothing about where the page came from.
func isLocalHostPort(hostport string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")

	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast()
}
