package relayscale

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// http_helpers.go — small request helpers for the demand API.

// bearer extracts the caller's shared secret from either the Authorization:
// Bearer header or the X-Relay-Auth header (the same header the relay tunnel
// server already uses for its CP calls), whichever is present.
func bearer(r *http.Request) string {
	if h := strings.TrimSpace(r.Header.Get("X-Relay-Auth")); h != "" {
		return h
	}
	const p = "Bearer "
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, p) {
		return strings.TrimSpace(strings.TrimPrefix(h, p))
	}
	return ""
}

// constEq is a constant-time string compare that fails closed on empty inputs.
func constEq(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
