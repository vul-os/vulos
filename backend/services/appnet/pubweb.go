package appnet

// pubweb.go — shared constants + helpers for routing the public-web edge
// (Caddy / nginx) THROUGH the :8080 auth gateway's anonymous public entrypoint
// instead of straight at an app's network namespace (Finding 1, HIGH).
//
// The edge no longer proxies to 127.0.0.1:{hostPort} (which bypassed the gateway
// and its X-Vulos-* stripping + visibility enforcement). It proxies to the
// gateway on loopback and rewrites the request path to
// PubwebPathPrefix + {appID} + {original-uri}. The gateway's PublicHandler then
// enforces the public opt-in, strips all seam headers, injects no identity, and
// proxies to the namespace.

import "os"

// PubwebPathPrefix is the gateway path that serves an app anonymously to the
// public web. The edge rewrites "https://{fqdn}/{path}" to
// "http://{gateway}{PubwebPathPrefix}{appID}/{path}".
const PubwebPathPrefix = "/__pubweb__/"

// defaultGatewayLoopback is where the OS HTTP server (and its auth gateway)
// listens by default (config PORT default is 8080). Overridable for tests / odd
// deployments via VULOS_PUBWEB_UPSTREAM.
const defaultGatewayLoopback = "127.0.0.1:8080"

// GatewayLoopbackAddr returns the host:port the public-web edge should proxy to
// so traffic passes through the auth gateway. Override with VULOS_PUBWEB_UPSTREAM.
func GatewayLoopbackAddr() string {
	if v := os.Getenv("VULOS_PUBWEB_UPSTREAM"); v != "" {
		return v
	}
	return defaultGatewayLoopback
}

// PubwebUpstreamPath returns the gateway path prefix (PubwebPathPrefix+appID)
// that the edge prepends to the original request URI.
func PubwebUpstreamPath(appID string) string {
	return PubwebPathPrefix + appID
}
