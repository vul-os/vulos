package cluster

import (
	"fmt"
	"log"
	"net"
	"strings"

	vulenv "vulos/backend/services/env"
)

// errPlaintextRemoteInProd is returned by the client constructors when the box
// is running in production (--env=prod or VULOS_ENV=prod, whichever main()
// resolved) and the cluster S3 endpoint is off-box without TLS.
//
// # Why this is worth refusing rather than warning about
//
// This subsystem encrypts objects with SSE-C — server-side encryption with a
// customer-provided key. That means the key is not used on this box: it travels
// to the endpoint in a request header on every single PUT and GET, and the
// endpoint does the encrypting and decrypting.
//
// Over TLS to a MinIO you run, that is a reasonable arrangement. Over plain
// HTTP to anything off-box it is not: the SSE-C key and the object body are both
// readable by whatever sits on the path. "Encrypted at rest" survives, and is
// worth precisely nothing to an attacker who watched the key go past.
//
// VULOS_S3_USE_SSL defaults to "false", which is the right default for the
// default endpoint (localhost:9000) and the wrong one the moment an operator
// points VULOS_S3_ENDPOINT at another host and does not think about TLS. Nothing
// in the code noticed that combination before this.
//
// The shape follows services/vault: refuse in prod, warn loudly otherwise, and
// check at every constructor rather than once.
var errPlaintextRemoteInProd = fmt.Errorf(
	"cluster: VULOS_S3_ENDPOINT is not on this machine and VULOS_S3_USE_SSL is false — " +
		"refusing to send SSE-C keys and backup contents over plain HTTP in a production " +
		"environment (set VULOS_S3_USE_SSL=true)")

// isLoopbackEndpoint reports whether an S3 endpoint addresses this machine.
//
// A bare host, a host:port, and an IPv6 literal in brackets all have to resolve
// the same way, so the port is split off first — "[::1]:9000" is loopback and
// naively string-matching on ":" would mangle it.
func isLoopbackEndpoint(endpoint string) bool {
	h := strings.TrimSpace(endpoint)
	if h == "" {
		return false
	}
	// Tolerate a scheme if one was configured, since minio accepts host[:port]
	// but operators paste URLs.
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	h = strings.TrimSuffix(h, "/")
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// guardTransport refuses, or warns about, a cluster endpoint that would carry
// SSE-C keys in the clear. Returns an error only in production.
func guardTransport(cfg S3Config) error {
	if cfg.UseSSL || isLoopbackEndpoint(cfg.Endpoint) {
		return nil
	}
	if vulenv.IsProdActive() {
		return errPlaintextRemoteInProd
	}
	log.Printf("[cluster] WARNING: %s is off-box and VULOS_S3_USE_SSL is false — the SSE-C "+
		"encryption key and every object body cross the network in the clear "+
		"(NEVER use in production; start with --env=prod to enforce)", cfg.Endpoint)
	return nil
}
