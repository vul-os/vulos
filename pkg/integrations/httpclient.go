// httpclient.go — bounded-timeout HTTP client for outbound OAuth-provider calls.
//
// HTTPTIMEOUT-01: the integrations package talks to external OAuth providers
// (Google, Microsoft, Dropbox) for token exchange, identity lookup, and token
// revocation. Using http.DefaultClient (no timeout) means a slow/hung provider
// endpoint can pin the calling goroutine — and any DB connection it holds —
// indefinitely. providerHTTPClient bounds every outbound request so a stuck
// provider degrades to a timeout error the caller already handles.
package integrations

import (
	"net/http"
	"time"
)

// providerHTTPClient is the shared bounded-timeout client for all outbound
// OAuth-provider HTTP calls. The per-request context still applies; this adds a
// hard ceiling so a request without a deadline (or with a very long one) cannot
// hang forever.
var providerHTTPClient = &http.Client{
	Timeout: 20 * time.Second,
}
