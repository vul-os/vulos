package main

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// metricsAuthorized decides whether a /metrics request may proceed. It is the
// single, testable gate for the Prometheus scrape endpoint.
//
// Two owner-scoped ways in:
//  1. A scrape token: when scrapeToken is non-empty and the request presents it
//     (Authorization: Bearer <token> or ?token=<token>), matched in constant
//     time. This lets a co-located Prometheus poll without an OS session.
//  2. The box OWNER's session: isOwnerFn(X-User-ID) is true. X-User-ID is set
//     ONLY by the auth middleware, which first strips any client-supplied copy —
//     so this reflects a real authenticated identity, not a spoofable header.
//
// Anything else is denied. No secret is ever emitted in the metrics body, so a
// successful scrape leaks only counts/gauges (see internal/obs).
func metricsAuthorized(r *http.Request, scrapeToken string, isOwnerFn func(userID string) bool) bool {
	if scrapeToken != "" {
		presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if presented == "" {
			presented = r.URL.Query().Get("token")
		}
		if presented != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(scrapeToken)) == 1 {
			return true
		}
	}
	return isOwnerFn(r.Header.Get("X-User-ID"))
}
